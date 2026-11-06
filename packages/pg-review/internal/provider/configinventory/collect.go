package configinventory

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Layer names understood by --layer.
const (
	LayerUser    = "user"
	LayerRules   = "rules"
	LayerProject = "project"
)

// layerOrder is the fixed order layers are read in; it decides which of two
// aliases of one real file names it.
var layerOrder = []string{LayerUser, LayerRules, LayerProject}

// File kinds, also the row types of the per-file rows.
const (
	kindInstruction = "instruction"
	kindMemory      = "memory"
	kindSkill       = "skill"
	kindSettings    = "setting"
)

const externalLayer = "external"

// entry is one real file found in a loaded layer.
type entry struct {
	kind      string
	real      string
	definedIn string // layer of the resolved real path
	rel       string // path relative to that layer's root (never absolute)
	names     []string
	data      []byte
	mtime     time.Time
}

// danglingLink is a skill or instruction entry whose symlink target is gone.
type danglingLink struct {
	kind      string
	definedIn string
	rel       string
}

type root struct {
	path, layer, prefix string
}

type collector struct {
	scopeRoot string // evaluated first scope prefix; empty when none
	userDir   string // evaluated user config directory; empty when unknown
	roots     []root // longest path first
	byReal    map[string]*entry
	entries   []*entry
	dangling  []danglingLink
}

func evalOrClean(p string) string {
	if p == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func newCollector(scopeRootRaw, userDirRaw string) *collector {
	c := &collector{scopeRoot: evalOrClean(scopeRootRaw), userDir: evalOrClean(userDirRaw), byReal: map[string]*entry{}}
	if c.userDir != "" {
		c.roots = append(c.roots,
			root{c.userDir, LayerUser, ""},
			root{filepath.Join(c.userDir, "rules"), LayerRules, "user/"})
		if scopeRootRaw != "" {
			// The directory name is a slug of the scope root's path, so it is
			// never shown; its files appear under a fixed name.
			c.roots = append(c.roots, root{filepath.Join(c.userDir, "projects", projectSlug(scopeRootRaw)), LayerUser, "project-data/"})
		}
	}
	if c.scopeRoot != "" {
		c.roots = append(c.roots,
			root{c.scopeRoot, LayerProject, ""},
			root{filepath.Join(c.scopeRoot, ".claude", "rules"), LayerRules, "project/"})
	}
	sort.SliceStable(c.roots, func(i, j int) bool { return len(c.roots[i].path) > len(c.roots[j].path) })
	return c
}

// define returns the layer a path belongs to and its path relative to that
// layer's root; a path under no known root is external and keeps only its
// base name, so no absolute path ever reaches a row.
func (c *collector) define(path string) (layer, rel string) {
	for _, r := range c.roots {
		if path == r.path {
			return r.layer, r.prefix
		}
		if strings.HasPrefix(path, r.path+string(filepath.Separator)) {
			return r.layer, r.prefix + filepath.ToSlash(strings.TrimPrefix(path, r.path+string(filepath.Separator)))
		}
	}
	return externalLayer, filepath.Base(path)
}

// add records the file at path (a lexical path under an evaluated root). A
// missing file is skipped; a dangling symlink is recorded; any other read
// failure is returned.
func (c *collector) add(kind, path string, names []string) error {
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			l, rel := c.define(path)
			c.dangling = append(c.dangling, danglingLink{kind: kind, definedIn: l, rel: rel})
			return nil
		}
		return err
	}
	if e := c.byReal[real]; e != nil {
		for _, n := range names {
			if !contains(e.names, n) {
				e.names = append(e.names, n)
			}
		}
		return nil
	}
	st, err := os.Stat(real)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return nil
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return err
	}
	l, rel := c.define(real)
	e := &entry{kind: kind, real: real, definedIn: l, rel: rel, names: names, data: data, mtime: st.ModTime().UTC().Truncate(time.Second)}
	c.byReal[real] = e
	c.entries = append(c.entries, e)
	return nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func readDirSorted(dir string) ([]string, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
			return nil, nil
		}
		if errors.Is(err, syscall.ENOTDIR) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(des))
	for _, d := range des {
		out = append(out, d.Name())
	}
	sort.Strings(out)
	return out, nil
}

func (c *collector) addMarkdownDir(kind, dir string) error {
	names, err := readDirSorted(dir)
	if err != nil {
		return err
	}
	for _, n := range names {
		if strings.HasSuffix(n, ".md") {
			if err := c.add(kind, filepath.Join(dir, n), nil); err != nil {
				return err
			}
		}
	}
	return nil
}

var frontNameRE = regexp.MustCompile(`(?m)^name:\s*["']?([^"'\r\n]+?)["']?\s*$`)

func frontmatterName(data []byte) string {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return ""
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return ""
	}
	if m := frontNameRE.FindStringSubmatch(s[4 : 4+end]); m != nil {
		return m[1]
	}
	return ""
}

func (c *collector) addSkills(dir string) error {
	names, err := readDirSorted(dir)
	if err != nil {
		return err
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		if _, err := filepath.EvalSymlinks(p); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				l, rel := c.define(p)
				c.dangling = append(c.dangling, danglingLink{kind: kindSkill, definedIn: l, rel: rel})
				continue
			}
			return err
		}
		if err := c.add(kindSkill, filepath.Join(p, "SKILL.md"), []string{n}); err != nil {
			return err
		}
	}
	return nil
}

var slugRE = regexp.MustCompile(`[^A-Za-z0-9]`)

func projectSlug(scopeRootRaw string) string {
	return slugRE.ReplaceAllString(filepath.Clean(scopeRootRaw), "-")
}

// collect reads every loaded layer in the fixed layer order.
func (c *collector) collect(layers []string, scopeRootRaw string) error {
	loaded := map[string]bool{}
	for _, l := range layers {
		loaded[l] = true
	}
	for _, layer := range layerOrder {
		if !loaded[layer] {
			continue
		}
		var err error
		switch layer {
		case LayerUser:
			err = c.collectUser(scopeRootRaw)
		case LayerRules:
			if c.userDir != "" {
				err = c.addMarkdownDir(kindInstruction, filepath.Join(c.userDir, "rules"))
			}
			if err == nil && c.scopeRoot != "" {
				err = c.addMarkdownDir(kindInstruction, filepath.Join(c.scopeRoot, ".claude", "rules"))
			}
		case LayerProject:
			err = c.collectProject()
		}
		if err != nil {
			return err
		}
	}
	// A skill file's frontmatter name is a second name for it.
	for _, e := range c.entries {
		if e.kind == kindSkill {
			if n := frontmatterName(e.data); n != "" && !contains(e.names, n) {
				e.names = append(e.names, n)
			}
		}
	}
	return nil
}

func (c *collector) collectUser(scopeRootRaw string) error {
	if c.userDir == "" {
		return nil
	}
	u := c.userDir
	steps := []func() error{
		func() error { return c.add(kindInstruction, filepath.Join(u, "CLAUDE.md"), nil) },
		func() error { return c.add(kindSettings, filepath.Join(u, "settings.json"), nil) },
		func() error { return c.addSkills(filepath.Join(u, "skills")) },
		func() error { return c.addMarkdownDir(kindMemory, filepath.Join(u, "memory")) },
	}
	if scopeRootRaw != "" {
		steps = append(steps, func() error {
			return c.addMarkdownDir(kindMemory, filepath.Join(u, "projects", projectSlug(scopeRootRaw), "memory"))
		})
	}
	for _, s := range steps {
		if err := s(); err != nil {
			return err
		}
	}
	return nil
}

func (c *collector) collectProject() error {
	if c.scopeRoot == "" {
		return nil
	}
	r := c.scopeRoot
	steps := []func() error{
		func() error { return c.add(kindInstruction, filepath.Join(r, "CLAUDE.md"), nil) },
		func() error { return c.add(kindInstruction, filepath.Join(r, "AGENTS.md"), nil) },
		func() error { return c.add(kindSettings, filepath.Join(r, ".claude", "settings.json"), nil) },
		func() error { return c.add(kindSettings, filepath.Join(r, ".claude", "settings.local.json"), nil) },
		func() error { return c.addSkills(filepath.Join(r, ".claude", "skills")) },
		func() error { return c.addMarkdownDir(kindMemory, filepath.Join(r, ".claude", "memory")) },
	}
	for _, s := range steps {
		if err := s(); err != nil {
			return err
		}
	}
	return nil
}

// settingLeaves flattens the scalar leaves of a settings document into dotted
// keys; arrays are skipped because their layers merge rather than override.
func settingLeaves(data []byte) (map[string]string, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	out := map[string]string{}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, x := range t {
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				walk(p, x)
			}
		case []any:
		default:
			if prefix != "" {
				b, _ := json.Marshal(t)
				out[prefix] = string(b)
			}
		}
	}
	walk("", v)
	return out, nil
}

func describe(e *entry) string { return fmt.Sprintf("%s:%s", e.definedIn, e.rel) }
