package labels

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DecoratorConfig configures a shell-out label decorator. Loaded from
// nix-rendered config.toml.
//
// Command is a shell-words string: the first word is the binary (validated to
// live under /nix/store/), and any remaining words are passed as argv so a
// generic decorator can carry flags (e.g. "…/bin/decorator -rule scope"). Env
// is a set of extra environment variables merged onto the runner's minimal base
// env, so a generic decorator receives its configuration (e.g.
// PA_MONITOR_SCOPE_RULES) without needing a bespoke writeShellScriptBin wrapper.
type DecoratorConfig struct {
	Name      string
	Command   string
	Env       map[string]string
	TimeoutMS int
}

// Decorator runs an external program to derive labels for a session.
// Decorators are intended for consumer-specific labelling (e.g. ZR-internal
// scope and project values) that should not live in the agent-support repo.
type Decorator struct {
	name string
	// argv is the split command: argv[0] is the (validated) binary, argv[1:]
	// are its arguments. Never empty for a constructed Decorator.
	argv []string
	// env is the fully-resolved environment passed to the child: the minimal
	// base env with any DecoratorConfig.Env merged over it, deterministically
	// sorted by key.
	env       []string
	timeoutMS int
}

// NewDecorator rejects any command whose binary path is not absolute under
// /nix/store/. This is the security boundary spec'd in the design doc —
// decorators must come from reproducible nix-managed builds, not arbitrary user
// paths. The Command string is shell-split first; the /nix/store/ check applies
// to the split-out binary (argv[0]), so flags cannot smuggle a non-store path
// past validation.
func NewDecorator(cfg DecoratorConfig) (*Decorator, error) {
	argv, err := splitArgs(cfg.Command)
	if err != nil {
		return nil, fmt.Errorf("decorator %q: %w", cfg.Name, err)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("decorator %q: command is empty", cfg.Name)
	}
	// Canonicalise to defeat traversal tricks like `/nix/store/../etc/passwd`.
	// filepath.Clean collapses `..` segments and removes trailing slashes.
	clean := filepath.Clean(argv[0])
	if !filepath.IsAbs(clean) || !strings.HasPrefix(clean, "/nix/store/") {
		return nil, fmt.Errorf("decorator %q: command must be an absolute path under /nix/store/", cfg.Name)
	}
	argv[0] = clean
	tm := cfg.TimeoutMS
	if tm <= 0 {
		tm = 2000
	}
	return &Decorator{name: cfg.Name, argv: argv, env: mergeDecoratorEnv(cfg.Env), timeoutMS: tm}, nil
}

// newDecoratorRaw is the unsafe constructor used by tests. It bypasses the
// /nix/store/ enforcement (and shell-splitting) so a fake single-binary
// decorator in $TMPDIR can be wired up. Production callers MUST use NewDecorator.
func newDecoratorRaw(name, cmd string, timeoutMS int) *Decorator {
	return newDecoratorRawArgv(name, []string{cmd}, nil, timeoutMS)
}

// newDecoratorRawArgv is newDecoratorRaw with an explicit argv and extra env,
// for tests that exercise argument-passing and env-forwarding end-to-end.
func newDecoratorRawArgv(name string, argv []string, env map[string]string, timeoutMS int) *Decorator {
	if timeoutMS <= 0 {
		timeoutMS = 2000
	}
	return &Decorator{name: name, argv: argv, env: mergeDecoratorEnv(env), timeoutMS: timeoutMS}
}

// mergeDecoratorEnv builds the child environment: the minimal base env
// (PA_MONITOR_DECORATE=1, a fixed PATH) with any config-provided entries merged
// over it (config wins on key collision). The base keys are emitted first in a
// fixed order — a config entry for one of them overrides its value in place
// rather than duplicating the key — so with no extra env the result is
// byte-identical to the pre-existing stripped env. Additional (non-base) config
// keys follow, sorted, so the result is deterministic.
func mergeDecoratorEnv(extra map[string]string) []string {
	base := []struct{ k, v string }{
		{"PA_MONITOR_DECORATE", "1"},
		{"PATH", "/usr/bin:/bin"},
	}
	env := make([]string, 0, len(base)+len(extra))
	isBase := make(map[string]bool, len(base))
	for _, b := range base {
		isBase[b.k] = true
		v := b.v
		if override, ok := extra[b.k]; ok {
			v = override
		}
		env = append(env, b.k+"="+v)
	}
	extraKeys := make([]string, 0, len(extra))
	for k := range extra {
		if !isBase[k] {
			extraKeys = append(extraKeys, k)
		}
	}
	sort.Strings(extraKeys)
	for _, k := range extraKeys {
		env = append(env, k+"="+extra[k])
	}
	return env
}

// splitArgs splits a command string into argv using POSIX-shell-like word
// rules: unquoted whitespace separates words; single quotes take everything
// literally; double quotes group and allow backslash escaping of ", \, $, `
// and newline; a backslash outside quotes escapes the next character. It
// returns an error on an unterminated quote or a trailing backslash. Empty or
// whitespace-only input yields zero words (nil).
func splitArgs(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\'':
			inWord = true
			i++
			for i < len(s) && s[i] != '\'' {
				cur.WriteByte(s[i])
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated single quote in %q", s)
			}
			i++ // consume closing quote
		case c == '"':
			inWord = true
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					switch s[i+1] {
					case '"', '\\', '$', '`', '\n':
						cur.WriteByte(s[i+1])
						i += 2
						continue
					}
				}
				cur.WriteByte(s[i])
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated double quote in %q", s)
			}
			i++ // consume closing quote
		case c == '\\':
			if i+1 >= len(s) {
				return nil, fmt.Errorf("trailing backslash in %q", s)
			}
			inWord = true
			cur.WriteByte(s[i+1])
			i += 2
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inWord {
				args = append(args, cur.String())
				cur.Reset()
				inWord = false
			}
			i++
		default:
			inWord = true
			cur.WriteByte(c)
			i++
		}
	}
	if inWord {
		args = append(args, cur.String())
	}
	return args, nil
}

func (d *Decorator) Name() string { return d.name }

type decoratorOutput struct {
	Labels map[string]string `json:"labels"`
}

// Detect runs the decorator binary with session JSON on stdin and parses
// its stdout JSON. On any failure (timeout, non-zero exit, parse error),
// returns nil — the failure is swallowed because decorator output is
// advisory. Callers that need to distinguish a transient FAILURE from a
// legitimately-empty result MUST use DetectOK instead.
func (d *Decorator) Detect(s Session) Set {
	set, _ := d.DetectOK(s)
	return set
}

// DetectOK is Detect plus an explicit success signal. It returns ok=false
// when this invocation FAILED (timeout, non-zero exit, or parse error) — a
// distinct outcome from a successful run that legitimately produced no labels
// (which returns an empty/nil Set with ok=true).
//
// The daemon's per-session label cache uses this to avoid caching a failed
// decorator result: caching a transient failure would freeze the (wrong)
// empty label set for the session's entire lifetime, so a session that should
// be scoped (e.g. `workspace.scope=ziprecruiter`) would stick at the
// DefaultScope (`personal`) until it restarts (ADR 0024 D5). A successful
// empty result is still safe to cache.
func (d *Decorator) DetectOK(s Session) (Set, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(d.timeoutMS)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.argv[0], d.argv[1:]...)
	cmd.Env = d.env
	input, _ := json.Marshal(s)
	cmd.Stdin = strings.NewReader(string(input))
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var parsed decoratorOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, false
	}
	return Set(parsed.Labels), true
}

// FailableDetector is a Detector whose Detect can fail (e.g. a shell-out
// Decorator that times out or exits non-zero). DetectOK reports that failure
// explicitly (ok=false) so callers that cache label results can decline to
// cache a failed run and retry it on a later tick, instead of freezing a
// transient failure for the session's lifetime. A successful run with no
// labels returns (empty-or-nil Set, true) and IS safe to cache.
type FailableDetector interface {
	Detector
	DetectOK(s Session) (Set, bool)
}

// AsFailable adapts a slice of concrete *Decorator to []FailableDetector so
// the daemon's label path can treat decorators uniformly (and so tests can
// inject fakes implementing the interface without shelling out). A nil input
// yields a nil result.
func AsFailable(decs []*Decorator) []FailableDetector {
	if decs == nil {
		return nil
	}
	out := make([]FailableDetector, len(decs))
	for i, d := range decs {
		out[i] = d
	}
	return out
}
