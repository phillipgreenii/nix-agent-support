package pggwmworktree

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// exception is one standing exception from a repository's pg-gwm
// [[repos."<key>".exceptions]] table. Only Path is consulted here; the rest is
// carried so the exception log and a reviewer can see why it exists.
type exception struct {
	Path, Reason, Owner, ReviewBy string
}

// gwmConfig is the part of the pg-gwm configuration this rule needs: which
// repositories pg-gwm manages (keyed by the real path of the main checkout) and
// each one's standing exceptions.
type gwmConfig struct {
	repos map[string][]exception
}

// defaultConfigPath mirrors pg-gwm's config.DefaultPath:
// $XDG_CONFIG_HOME/pg-gwm/config.toml, else ~/.config/pg-gwm/config.toml.
func defaultConfigPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "pg-gwm", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "pg-gwm", "config.toml")
}

var (
	// [repos."/path"] or [repos.'/path'], optionally followed by an unrelated
	// sub-key (never "exceptions") which this reader ignores.
	repoHeaderRE = regexp.MustCompile(`^\[\s*repos\s*\.\s*("(?:[^"\\]|\\.)*"|'[^']*')\s*\]\s*(#.*)?$`)
	// [[repos."/path".exceptions]]
	excHeaderRE = regexp.MustCompile(`^\[\[\s*repos\s*\.\s*("(?:[^"\\]|\\.)*"|'[^']*')\s*\.\s*exceptions\s*\]\]\s*(#.*)?$`)
	keyValRE    = regexp.MustCompile(`^([A-Za-z0-9_-]+)\s*=\s*("(?:[^"\\]|\\.)*"|'[^']*')\s*(#.*)?$`)
)

func unquoteTOML(s string) string {
	if strings.HasPrefix(s, "'") {
		return strings.Trim(s, "'")
	}
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return strings.Trim(s, `"`)
}

// loadConfig reads the pg-gwm configuration at path. A missing file is an empty
// configuration (no repository is managed). The reader understands exactly the
// two shapes it needs ([repos."<key>"] headers and [[repos."<key>".exceptions]]
// tables with string keys); everything else is ignored, so a new pg-gwm key can
// never make this rule misjudge a repository. An unreadable file is an error and
// the caller fails open.
//
// It is hand-rolled rather than a TOML library so this binary gains no new
// module dependency for two header shapes.
func loadConfig(path string) (gwmConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return gwmConfig{repos: map[string][]exception{}}, nil
		}
		return gwmConfig{}, err
	}
	defer func() { _ = f.Close() }()
	cfg := gwmConfig{repos: map[string][]exception{}}
	var curKey string
	var cur *exception
	flush := func() {
		if cur != nil {
			cfg.repos[curKey] = append(cfg.repos[curKey], *cur)
			cur = nil
		}
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			flush()
			if m := excHeaderRE.FindStringSubmatch(line); m != nil {
				curKey = realPath(unquoteTOML(m[1]))
				if _, ok := cfg.repos[curKey]; !ok {
					cfg.repos[curKey] = nil
				}
				cur = &exception{}
				continue
			}
			if m := repoHeaderRE.FindStringSubmatch(line); m != nil {
				curKey = realPath(unquoteTOML(m[1]))
				if _, ok := cfg.repos[curKey]; !ok {
					cfg.repos[curKey] = nil
				}
				continue
			}
			// Any other table: not ours.
			curKey = ""
			continue
		}
		if cur == nil {
			continue
		}
		if m := keyValRE.FindStringSubmatch(line); m != nil {
			v := unquoteTOML(m[2])
			switch m[1] {
			case "path":
				cur.Path = v
			case "reason":
				cur.Reason = v
			case "owner":
				cur.Owner = v
			case "review-by":
				cur.ReviewBy = v
			}
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		return gwmConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	return cfg, nil
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// matches reports whether p is covered by the exception's path or glob, using
// pg-gwm's glob semantics: "*" within one element, "**" across elements, "?" one
// character.
func (e exception) matches(p string) bool {
	if e.Path == "" {
		return false
	}
	var b strings.Builder
	b.WriteString("^")
	g := e.Path
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			if i+1 < len(g) && g[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(p)
}
