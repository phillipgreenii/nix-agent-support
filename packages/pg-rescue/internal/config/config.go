// Package config loads and validates pg-rescue's TOML config. The whole file
// is validated on load, so a broken file is noticed at its first use, and
// every error names the file, the table and the key.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// DefaultTimeout is a handler's timeout when the config does not set one.
const DefaultTimeout = 5 * time.Minute

// Handler is one configured handler instance.
type Handler struct {
	Name        string
	Command     []string
	Timeout     time.Duration
	Env         map[string]string
	EnvFile     string // absolute path after "~" expansion; "" when unset
	Description string
	Tags        []string
}

// Chain is a named, ordered list of handler instance names.
type Chain struct {
	Name     string
	Handlers []string
}

// Config is a validated config file.
type Config struct {
	Path     string
	Redact   []*regexp.Regexp
	Handlers map[string]*Handler
	Chains   map[string]*Chain
}

// Error is a config problem. Its message is already complete (it names the
// file, the table and the key) and may span several lines when the file has
// several problems.
type Error struct {
	Path     string
	Problems []string
}

func (e *Error) Error() string {
	if len(e.Problems) == 1 {
		return fmt.Sprintf("config %s: %s", e.Path, e.Problems[0])
	}
	var b strings.Builder
	fmt.Fprintf(&b, "config %s: %d problems", e.Path, len(e.Problems))
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		b.WriteString(p)
	}
	return b.String()
}

// Valid key names, for "unknown key" messages.
var (
	topLevelKeys = []string{"chain", "handler", "redact"}
	handlerKeys  = []string{"command", "description", "env", "env_file", "tags", "timeout"}
	chainKeys    = []string{"handlers"}
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type rawHandler struct {
	Command     []string          `toml:"command"`
	Timeout     *string           `toml:"timeout"`
	Env         map[string]string `toml:"env"`
	EnvFile     *string           `toml:"env_file"`
	Description string            `toml:"description"`
	Tags        []string          `toml:"tags"`
}

type rawChain struct {
	Handlers []string `toml:"handlers"`
}

type rawFile struct {
	Redact  []string              `toml:"redact"`
	Handler map[string]rawHandler `toml:"handler"`
	Chain   map[string]rawChain   `toml:"chain"`
}

// Load reads and validates the config file at path. home is used to expand a
// leading "~/" in env_file. It returns *Error for every config problem.
func Load(path, home string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &Error{Path: path, Problems: []string{fmt.Sprintf("cannot read file: %v", err)}}
	}
	return Parse(path, data, home)
}

// Parse validates config text. path is used for messages and the env_file
// permission check only.
func Parse(path string, data []byte, home string) (*Config, error) {
	var raw rawFile
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		var perr toml.ParseError
		if errors.As(err, &perr) {
			where := fmt.Sprintf("line %d", perr.Position.Line)
			if k := perr.LastKey; k != "" {
				where += fmt.Sprintf(", key %q", k)
			}
			return nil, &Error{Path: path, Problems: []string{fmt.Sprintf("invalid TOML (%s): %s", where, perr.Message)}}
		}
		return nil, &Error{Path: path, Problems: []string{fmt.Sprintf("invalid TOML: %v", err)}}
	}

	v := &validator{path: path, home: home}
	v.unknownKeys(md.Undecoded())

	cfg := &Config{Path: path, Handlers: map[string]*Handler{}, Chains: map[string]*Chain{}}

	for i, pat := range raw.Redact {
		re, err := regexp.Compile(pat)
		if err != nil {
			v.addf("top level: key \"redact\"[%d]: invalid regular expression %q: %v", i, pat, err)
			continue
		}
		cfg.Redact = append(cfg.Redact, re)
	}

	for _, name := range sortedKeys(raw.Handler) {
		if h := v.handler(name, raw.Handler[name]); h != nil {
			cfg.Handlers[name] = h
		}
	}
	for _, name := range sortedKeys(raw.Chain) {
		if c := v.chain(name, raw.Chain[name], raw.Handler); c != nil {
			cfg.Chains[name] = c
		}
	}

	if len(v.problems) > 0 {
		return nil, &Error{Path: path, Problems: v.problems}
	}
	return cfg, nil
}

type validator struct {
	path     string
	home     string
	problems []string
}

func (v *validator) addf(format string, args ...any) {
	v.problems = append(v.problems, fmt.Sprintf(format, args...))
}

func (v *validator) unknownKeys(keys []toml.Key) {
	var msgs []string
	for _, k := range keys {
		parts := []string(k)
		switch {
		case len(parts) == 0:
		case parts[0] == "handler" && len(parts) >= 3:
			msgs = append(msgs, fmt.Sprintf("[handler.%s]: unknown key %q; valid keys: %s",
				parts[1], strings.Join(parts[2:], "."), strings.Join(handlerKeys, ", ")))
		case parts[0] == "chain" && len(parts) >= 3:
			msgs = append(msgs, fmt.Sprintf("[chain.%s]: unknown key %q; valid keys: %s",
				parts[1], strings.Join(parts[2:], "."), strings.Join(chainKeys, ", ")))
		default:
			msgs = append(msgs, fmt.Sprintf("top level: unknown key %q; valid keys: %s",
				strings.Join(parts, "."), strings.Join(topLevelKeys, ", ")))
		}
	}
	sort.Strings(msgs)
	v.problems = append(v.problems, msgs...)
}

func (v *validator) handler(name string, r rawHandler) *Handler {
	table := fmt.Sprintf("[handler.%s]", name)
	ok := true
	fail := func(format string, args ...any) {
		ok = false
		v.addf("%s: %s", table, fmt.Sprintf(format, args...))
	}

	if !namePattern.MatchString(name) {
		fail("invalid handler name %q; use letters, digits, '_' and '-'", name)
	}
	if len(r.Command) == 0 {
		fail("missing required key \"command\" (a non-empty argv list)")
	} else if r.Command[0] == "" {
		fail("key \"command\": command[0] must not be empty")
	}

	timeout := DefaultTimeout
	if r.Timeout != nil {
		d, err := time.ParseDuration(*r.Timeout)
		switch {
		case err != nil:
			fail("key \"timeout\": invalid duration %q (Go syntax, for example \"90s\" or \"2m\"): %v", *r.Timeout, err)
		case d <= 0:
			fail("key \"timeout\": duration %q must be positive", *r.Timeout)
		default:
			timeout = d
		}
	}

	for k := range r.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			fail("key \"env\": invalid variable name %q", k)
		}
	}

	var envFile string
	if r.EnvFile != nil {
		p, err := expandPath(*r.EnvFile, v.home)
		if err != nil {
			fail("key \"env_file\": %v", err)
		} else if err := checkEnvFileMode(p); err != nil {
			fail("key \"env_file\": %v", err)
		} else {
			envFile = p
		}
	}

	if !ok {
		return nil
	}
	return &Handler{
		Name:        name,
		Command:     append([]string(nil), r.Command...),
		Timeout:     timeout,
		Env:         r.Env,
		EnvFile:     envFile,
		Description: r.Description,
		Tags:        append([]string(nil), r.Tags...),
	}
}

func (v *validator) chain(name string, r rawChain, handlers map[string]rawHandler) *Chain {
	table := fmt.Sprintf("[chain.%s]", name)
	ok := true
	if !namePattern.MatchString(name) {
		v.addf("%s: invalid chain name %q; use letters, digits, '_' and '-'", table, name)
		ok = false
	}
	if len(r.Handlers) == 0 {
		v.addf("%s: key \"handlers\": must be a non-empty list of handler names; configured: %s",
			table, joinNames(sortedKeys(handlers)))
		return nil
	}
	for i, h := range r.Handlers {
		if _, known := handlers[h]; !known {
			v.addf("%s: key \"handlers\"[%d]: unknown handler %q; configured: %s",
				table, i, h, joinNames(sortedKeys(handlers)))
			ok = false
		}
	}
	if !ok {
		return nil
	}
	return &Chain{Name: name, Handlers: append([]string(nil), r.Handlers...)}
}

// expandPath expands a leading "~/" and requires the result to be absolute.
func expandPath(p, home string) (string, error) {
	switch {
	case p == "":
		return "", errors.New("path must not be empty")
	case p == "~" || strings.HasPrefix(p, "~/"):
		if home == "" {
			return "", fmt.Errorf("cannot expand %q: no home directory", p)
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("path %q must be absolute or start with \"~/\"", p)
	}
	return filepath.Clean(p), nil
}

// checkEnvFileMode rejects an env_file that is group- or world-accessible.
// A file that does not exist yet is not a config error: it is read when the
// handler is spawned, where a missing file fails that attempt.
func checkEnvFileMode(p string) error {
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot stat %s: %v", p, err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s has unsafe permissions %04o: it is group- or world-accessible; run chmod 600 on it",
			p, fi.Mode().Perm())
	}
	return nil
}

// HandlerNames returns the configured handler names, sorted.
func (c *Config) HandlerNames() []string { return sortedKeys(c.Handlers) }

// ChainNames returns the configured chain names, sorted.
func (c *Config) ChainNames() []string { return sortedKeys(c.Chains) }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func joinNames(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

// UnknownNameError builds the message for an unknown handler or chain name
// given on the command line, listing the valid names.
func UnknownNameError(kind, name, flag string, configured []string) error {
	return fmt.Errorf("unknown %s %q in %s; configured: %s", kind, name, flag, joinNames(configured))
}
