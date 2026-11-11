package config

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// DefaultTimeout is the timeout of a hook entry or conflict rule that sets
// none (design section 10.4).
const DefaultTimeout = 10 * time.Minute

// Hook operations and points (design section 12.4).
var (
	operations = []string{"rebase", "land"}
	points     = []string{"before", "after_rebase", "before_move", "after"}
)

// Hook is one hook entry.
type Hook struct {
	Run           []string          `json:"run"`
	Timeout       time.Duration     `json:"timeout"`
	SkipExitCodes []int             `json:"skip_exit_codes"`
	Cwd           string            `json:"cwd"` // "worktree" or "repo"
	Env           map[string]string `json:"env"`
}

// Rule is one conflict rule (or the conflict_default rule, whose Glob is
// empty).
type Rule struct {
	// ID is <layer>:<index>:<glob>, the index being 1-based within the layer.
	ID      string        `json:"id"`
	Layer   string        `json:"layer"`
	Glob    string        `json:"glob"`
	RunPer  string        `json:"run_per"` // "file" or "dir"
	Resolve []string      `json:"resolve"`
	Verify  []string      `json:"verify"`
	Timeout time.Duration `json:"timeout"`
}

// Layer names, used in rule ids and in error locations.
const (
	LayerXDGRepo     = "xdg-repo"
	LayerRepoFile    = "repo-file"
	LayerXDGDefaults = "xdg-defaults"
)

// vals holds what one layer sets; nil or empty means "not set here".
type vals struct {
	primary                       *string
	leaseTTL, wait                *time.Duration
	runGitHooks                   *bool
	maxRace, maxConflict, autofix *int
	hooks                         map[string]map[string][]Hook
	rules                         []Rule
	conflictDefault               *Rule

	// XDG entry only.
	root          string
	trust, ignore *bool
}

// namesCommands reports whether the layer names any command (a hook entry,
// a conflict rule or conflict_default).
func (v *vals) namesCommands() bool {
	if v.conflictDefault != nil || len(v.rules) > 0 {
		return true
	}
	for _, pts := range v.hooks {
		for _, hs := range pts {
			if len(hs) > 0 {
				return true
			}
		}
	}
	return false
}

type issue struct {
	key  string
	line int
	msg  string
}

// parser validates one decoded document.
type parser struct {
	layer   string
	lines   map[string]int
	errs    []issue
	unknown []issue
}

func (p *parser) fail(key, format string, a ...any) {
	p.errs = append(p.errs, issue{key: key, line: lineFor(p.lines, key), msg: fmt.Sprintf(format, a...)})
}

func (p *parser) checkKeys(tbl map[string]any, base string, allowed ...string) {
	for k := range tbl {
		ok := false
		for _, a := range allowed {
			if a == k {
				ok = true
				break
			}
		}
		if !ok {
			key := join(base, k)
			p.unknown = append(p.unknown, issue{key: key, line: lineFor(p.lines, key), msg: "unknown key"})
		}
	}
}

func join(base, k string) string {
	if base == "" {
		return k
	}
	return base + "." + k
}

func (p *parser) table(v any, key string) (map[string]any, bool) {
	t, ok := v.(map[string]any)
	if !ok {
		p.fail(key, "must be a table")
	}
	return t, ok
}

func (p *parser) boolean(tbl map[string]any, base, k string) *bool {
	raw, ok := tbl[k]
	if !ok {
		return nil
	}
	b, ok := raw.(bool)
	if !ok {
		p.fail(join(base, k), "must be a boolean")
		return nil
	}
	return &b
}

func (p *parser) integer(tbl map[string]any, base, k string, min int) *int {
	raw, ok := tbl[k]
	if !ok {
		return nil
	}
	n, ok := raw.(int64)
	if !ok || int(n) < min {
		p.fail(join(base, k), "must be an integer >= %d", min)
		return nil
	}
	i := int(n)
	return &i
}

func (p *parser) str(tbl map[string]any, base, k string) (string, bool) {
	raw, ok := tbl[k]
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	if !ok || s == "" {
		p.fail(join(base, k), "must be a non-empty string")
		return "", false
	}
	return s, true
}

func (p *parser) duration(tbl map[string]any, base, k string) *time.Duration {
	s, ok := p.str(tbl, base, k)
	if !ok {
		return nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		p.fail(join(base, k), "must be a positive duration such as \"30s\" or \"10m\"")
		return nil
	}
	return &d
}

func (p *parser) argv(tbl map[string]any, base, k string) ([]string, bool) {
	raw, ok := tbl[k]
	if !ok {
		return nil, false
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		p.fail(join(base, k), "must be a non-empty list of strings")
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		s, ok := e.(string)
		if !ok || (len(out) == 0 && s == "") {
			p.fail(join(base, k), "must be a non-empty list of strings")
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

var commonKeys = []string{"run_git_hooks", "max_race_retries", "max_conflict_rounds", "autofix", "hooks", "conflict", "conflict_default"}

// common parses the keys every layer may set. withXDG adds primary and lock.
func (p *parser) common(tbl map[string]any, base string, withXDG bool, extra ...string) *vals {
	v := &vals{}
	allowed := append(append([]string{}, commonKeys...), extra...)
	if withXDG {
		allowed = append(allowed, "primary", "lock")
	}
	p.checkKeys(tbl, base, allowed...)

	v.runGitHooks = p.boolean(tbl, base, "run_git_hooks")
	v.maxRace = p.integer(tbl, base, "max_race_retries", 0)
	v.maxConflict = p.integer(tbl, base, "max_conflict_rounds", 0)

	if raw, ok := tbl["autofix"]; ok {
		if t, ok := p.table(raw, join(base, "autofix")); ok {
			p.checkKeys(t, join(base, "autofix"), "attempts")
			v.autofix = p.integer(t, join(base, "autofix"), "attempts", 1)
		}
	}
	if withXDG {
		if s, ok := p.str(tbl, base, "primary"); ok {
			v.primary = &s
		}
		if raw, ok := tbl["lock"]; ok {
			if t, ok := p.table(raw, join(base, "lock")); ok {
				lb := join(base, "lock")
				p.checkKeys(t, lb, "lease_ttl", "wait")
				v.leaseTTL = p.duration(t, lb, "lease_ttl")
				v.wait = p.duration(t, lb, "wait")
			}
		}
	}
	if raw, ok := tbl["hooks"]; ok {
		v.hooks = p.hooks(raw, join(base, "hooks"))
	}
	if raw, ok := tbl["conflict"]; ok {
		list, ok := tableList(raw)
		if !ok {
			p.fail(join(base, "conflict"), "must be an array of tables ([[conflict]])")
		}
		for i, e := range list {
			key := fmt.Sprintf("%s[%d]", join(base, "conflict"), i)
			if r := p.rule(e, key, true); r != nil {
				r.Layer = p.layer
				r.ID = fmt.Sprintf("%s:%d:%s", p.layer, i+1, r.Glob)
				v.rules = append(v.rules, *r)
			}
		}
	}
	if raw, ok := tbl["conflict_default"]; ok {
		key := join(base, "conflict_default")
		if t, ok := p.table(raw, key); ok {
			if r := p.rule(t, key, false); r != nil {
				r.Layer = p.layer
				r.ID = p.layer + ":default"
				v.conflictDefault = r
			}
		}
	}
	return v
}

func (p *parser) hooks(raw any, base string) map[string]map[string][]Hook {
	t, ok := p.table(raw, base)
	if !ok {
		return nil
	}
	p.checkKeys(t, base, operations...)
	out := map[string]map[string][]Hook{}
	for _, op := range operations {
		rawOp, ok := t[op]
		if !ok {
			continue
		}
		ob := join(base, op)
		ot, ok := p.table(rawOp, ob)
		if !ok {
			continue
		}
		p.checkKeys(ot, ob, points...)
		for _, pt := range points {
			rawPt, ok := ot[pt]
			if !ok {
				continue
			}
			key := join(ob, pt)
			list, ok := tableList(rawPt)
			if !ok {
				p.fail(key, "must be a list of hook entries")
				continue
			}
			hooks := []Hook{}
			for i, e := range list {
				if h := p.hook(e, fmt.Sprintf("%s[%d]", key, i), pt); h != nil {
					hooks = append(hooks, *h)
				}
			}
			if out[op] == nil {
				out[op] = map[string][]Hook{}
			}
			out[op][pt] = hooks
		}
	}
	return out
}

func (p *parser) hook(e map[string]any, key, point string) *Hook {
	p.checkKeys(e, key, "run", "timeout", "skip_exit_codes", "cwd", "env")
	h := &Hook{Timeout: DefaultTimeout, Cwd: "worktree", Env: map[string]string{}}
	if point == "before" {
		h.Cwd = "repo"
	}
	n := len(p.errs)
	if _, ok := e["run"]; !ok {
		p.fail(key, "a hook entry needs run")
	} else if run, ok := p.argv(e, key, "run"); ok {
		h.Run = run
	}
	if d := p.duration(e, key, "timeout"); d != nil {
		h.Timeout = *d
	}
	if raw, ok := e["skip_exit_codes"]; ok {
		list, isList := raw.([]any)
		if !isList {
			p.fail(join(key, "skip_exit_codes"), "must be a list of integers")
		}
		for _, c := range list {
			i, ok := c.(int64)
			if !ok {
				p.fail(join(key, "skip_exit_codes"), "must be a list of integers")
				break
			}
			h.SkipExitCodes = append(h.SkipExitCodes, int(i))
		}
	}
	if raw, ok := e["cwd"]; ok {
		s, _ := raw.(string)
		if s != "worktree" && s != "repo" {
			p.fail(join(key, "cwd"), `must be "worktree" or "repo"`)
		} else {
			h.Cwd = s
		}
	}
	if raw, ok := e["env"]; ok {
		t, isTbl := raw.(map[string]any)
		if !isTbl {
			p.fail(join(key, "env"), "must be a table of strings")
		}
		for k, val := range t {
			s, ok := val.(string)
			if !ok {
				p.fail(join(key, "env"), "value of %s must be a string", k)
				continue
			}
			h.Env[k] = s
		}
	}
	if len(p.errs) > n {
		return nil
	}
	return h
}

func (p *parser) rule(e map[string]any, key string, withGlob bool) *Rule {
	allowed := []string{"run_per", "resolve", "verify", "timeout"}
	if withGlob {
		allowed = append(allowed, "glob")
	}
	p.checkKeys(e, key, allowed...)
	r := &Rule{RunPer: "file", Timeout: DefaultTimeout}
	n := len(p.errs)
	if withGlob {
		g, ok := p.str(e, key, "glob")
		switch {
		case !ok && e["glob"] == nil:
			p.fail(key, "a conflict rule needs glob")
		case ok:
			if _, err := path.Match(strings.ReplaceAll(g, "**", "*"), ""); err != nil {
				p.fail(join(key, "glob"), "malformed glob %q", g)
			}
			r.Glob = g
		}
	}
	if _, ok := e["resolve"]; !ok {
		p.fail(key, "a conflict rule needs resolve")
	} else if res, ok := p.argv(e, key, "resolve"); ok {
		r.Resolve = res
	}
	if _, ok := e["verify"]; ok {
		if v, ok := p.argv(e, key, "verify"); ok {
			r.Verify = v
		}
	}
	if raw, ok := e["run_per"]; ok {
		s, _ := raw.(string)
		if s != "file" && s != "dir" {
			p.fail(join(key, "run_per"), `must be "file" or "dir"`)
		} else {
			r.RunPer = s
		}
	}
	if d := p.duration(e, key, "timeout"); d != nil {
		r.Timeout = *d
	}
	if len(p.errs) > n {
		return nil
	}
	return r
}

// xdgDoc is a parsed XDG file.
type xdgDoc struct {
	defaults *vals
	repos    []*vals
}

func decodeTable(src []byte) (map[string]any, *toml.ParseError) {
	var m map[string]any
	if _, err := toml.Decode(string(src), &m); err != nil {
		var pe toml.ParseError
		if e, ok := err.(toml.ParseError); ok {
			pe = e
		} else {
			pe = toml.ParseError{Message: err.Error()}
		}
		return nil, &pe
	}
	return m, nil
}

// parseXDG validates the XDG file. Unknown keys are errors there.
func parseXDG(src []byte) (*xdgDoc, []issue) {
	m, perr := decodeTable(src)
	if perr != nil {
		return nil, []issue{{line: perr.Position.Line, msg: perr.Message}}
	}
	p := &parser{layer: LayerXDGDefaults, lines: scanLines(src)}
	p.checkKeys(m, "", "defaults", "repo")
	doc := &xdgDoc{defaults: &vals{}}
	if raw, ok := m["defaults"]; ok {
		if t, ok := p.table(raw, "defaults"); ok {
			doc.defaults = p.common(t, "defaults", true)
		}
	}
	if raw, ok := m["repo"]; ok {
		list, ok := tableList(raw)
		if !ok {
			p.fail("repo", "must be an array of tables ([[repo]])")
		}
		p.layer = LayerXDGRepo
		for i, t := range list {
			base := fmt.Sprintf("repo[%d]", i)
			v := p.common(t, base, true, "root", "trust_repo_config", "ignore_repo_config")
			v.trust = p.boolean(t, base, "trust_repo_config")
			v.ignore = p.boolean(t, base, "ignore_repo_config")
			if s, ok := p.str(t, base, "root"); ok {
				v.root = s
			} else if t["root"] == nil {
				p.fail(base, "a [[repo]] entry needs root")
			}
			doc.repos = append(doc.repos, v)
		}
	}
	return doc, append(p.errs, p.unknown...)
}

// parseRepoFile validates the repo file. Unknown keys are returned apart:
// they are warnings there.
func parseRepoFile(src []byte) (v *vals, errs, unknown []issue) {
	m, perr := decodeTable(src)
	if perr != nil {
		return nil, []issue{{line: perr.Position.Line, msg: perr.Message}}, nil
	}
	p := &parser{layer: LayerRepoFile, lines: scanLines(src)}
	v = p.common(m, "", false)
	return v, p.errs, p.unknown
}

// tableList accepts both an array of tables and an array of inline tables.
func tableList(raw any) ([]map[string]any, bool) {
	switch l := raw.(type) {
	case []map[string]any:
		return l, true
	case []any:
		out := make([]map[string]any, 0, len(l))
		for _, e := range l {
			m, ok := e.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, m)
		}
		return out, true
	}
	return nil, false
}
