package labels

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
	if !strings.HasPrefix(cfg.Command, "/nix/store/") {
		return nil, fmt.Errorf("decorator %q: command must be under /nix/store/", cfg.Name)
	}
	tm := cfg.TimeoutMS
	if tm <= 0 {
		tm = 2000
	}
	return &Decorator{name: cfg.Name, cmd: cfg.Command, timeoutMS: tm}, nil
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
// advisory.
func (d *Decorator) Detect(s Session) Set {
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
		return nil
	}
	var parsed decoratorOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil
	}
	return Set(parsed.Labels)
}
