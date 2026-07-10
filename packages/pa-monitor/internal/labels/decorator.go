package labels

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DecoratorConfig configures a shell-out label decorator. Loaded from
// nix-rendered config.toml.
type DecoratorConfig struct {
	Name      string
	Command   string
	TimeoutMS int
}

// Decorator runs an external program to derive labels for a session.
// Decorators are intended for consumer-specific labelling (e.g. ZR-internal
// scope and project values) that should not live in the agent-support repo.
type Decorator struct {
	name      string
	cmd       string
	timeoutMS int
}

// NewDecorator rejects any command path that is not absolute under
// /nix/store/. This is the security boundary spec'd in the design doc —
// decorators must come from reproducible nix-managed builds, not
// arbitrary user paths.
func NewDecorator(cfg DecoratorConfig) (*Decorator, error) {
	// Canonicalise to defeat traversal tricks like `/nix/store/../etc/passwd`.
	// filepath.Clean collapses `..` segments and removes trailing slashes.
	clean := filepath.Clean(cfg.Command)
	if !filepath.IsAbs(clean) || !strings.HasPrefix(clean, "/nix/store/") {
		return nil, fmt.Errorf("decorator %q: command must be an absolute path under /nix/store/", cfg.Name)
	}
	tm := cfg.TimeoutMS
	if tm <= 0 {
		tm = 2000
	}
	return &Decorator{name: cfg.Name, cmd: clean, timeoutMS: tm}, nil
}

// newDecoratorRaw is the unsafe constructor used by tests. It bypasses
// the /nix/store/ enforcement so fake decorators in $TMPDIR can be wired
// up. Production callers MUST use NewDecorator.
func newDecoratorRaw(name, cmd string, timeoutMS int) *Decorator {
	if timeoutMS <= 0 {
		timeoutMS = 2000
	}
	return &Decorator{name: name, cmd: cmd, timeoutMS: timeoutMS}
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

	cmd := exec.CommandContext(ctx, d.cmd)
	cmd.Env = []string{
		"PA_MONITOR_DECORATE=1",
		"PATH=/usr/bin:/bin",
	}
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
