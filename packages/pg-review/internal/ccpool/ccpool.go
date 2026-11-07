// Package ccpool is the seam between pg-review and the ccpool CLI. The runner
// talks to the Client interface; CLI is the real implementation that runs the
// ccpool command, and tests substitute a fake.
//
// Facts the implementation relies on: a session is created with `new` (which
// takes no prompt), the prompt is a separate `reply --no-wait` that prints a
// turn id, `result <turn-id>` exits 0 (done), 2 (pending) or 1 (unknown or
// unreadable, which can be a transient daemon outage and so is not terminal),
// and only `new` and `reply` ever exit 8 (usage limit refused). There is no
// wait or kill: a session is stopped with `close` and a turn is interrupted
// with `cancel`.
package ccpool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Exit codes of the ccpool CLI that the runner distinguishes.
const (
	ExitUsageLimited = 8
	ExitBusy         = 5
)

// PoolEnv is the environment variable that selects a ccpool pool.
const PoolEnv = "CCPOOL_POOL"

// Session states reported by `state --json`.
const (
	StateIdle            = "idle"
	StateWorking         = "working"
	StateWaitingForHuman = "waiting-for-human"
	StateError           = "error"
	StateNotLive         = "not-live"
)

// ExitError is a ccpool command that exited non-zero.
type ExitError struct {
	Cmd    string
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("ccpool %s exited %d: %s", e.Cmd, e.Code, strings.TrimSpace(e.Stderr))
}

// ExitCode returns the exit code of err when it is (or wraps) an ExitError.
func ExitCode(err error) (int, bool) {
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code, true
	}
	return 0, false
}

// IsUsageLimited reports whether err is the usage-limit refusal (exit 8).
func IsUsageLimited(err error) bool { c, ok := ExitCode(err); return ok && c == ExitUsageLimited }

// IsBusy reports whether err is a busy-turn refusal (exit 5).
func IsBusy(err error) bool { c, ok := ExitCode(err); return ok && c == ExitBusy }

// UsageLimit is a hit account usage window.
type UsageLimit struct {
	Window   string    `json:"window"`
	UsedPct  float64   `json:"used_pct"`
	ResetsAt time.Time `json:"resets_at"`
}

// Capacity is the pool's occupancy. While UsageLimit is set Free is 0.
type Capacity struct {
	MaxSessions int         `json:"max_sessions"`
	Free        int         `json:"free"`
	UsageLimit  *UsageLimit `json:"usage_limit,omitempty"`
}

// StartOpts are the inputs of a new session.
type StartOpts struct {
	ExternalID     string
	Cwd            string
	Model          string
	Effort         string
	PermissionMode string
	AllowedTools   []string
	Env            []string // KEY=VAL pairs injected into the session
}

// ResultStatus is the outcome of one poll of a turn.
type ResultStatus int

const (
	// ResultPending: the turn has not completed (exit 2).
	ResultPending ResultStatus = iota
	// ResultDone: the turn completed and Text holds its last message (exit 0).
	ResultDone
	// ResultUnknown: unknown turn or unreadable transcript (exit 1). Not
	// terminal on its own: it can be a transient daemon outage.
	ResultUnknown
)

// Client is the ccpool seam. Capacity never exits 8; Start and Reply can.
type Client interface {
	Capacity(ctx context.Context) (Capacity, error)
	Start(ctx context.Context, o StartOpts) error
	// Reply delivers a prompt without waiting and returns the turn id.
	Reply(ctx context.Context, externalID, prompt string) (turnID string, err error)
	Result(ctx context.Context, turnID string) (text string, st ResultStatus, err error)
	// State returns the session's reconciled state (one of the State words).
	State(ctx context.Context, externalID string) (string, error)
	Cancel(ctx context.Context, externalID string) error
	Close(ctx context.Context, externalID, reason string) error
}

// CLI drives the ccpool command. PoolDir, when set, is passed as CCPOOL_POOL
// to every command; when empty CCPOOL_POOL is removed from the child
// environment, which selects ccpool's default pool.
type CLI struct {
	Binary  string // default "ccpool"
	PoolDir string
}

// Environ returns the environment of a ccpool child process.
func (c CLI) Environ() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, PoolEnv+"=") {
			env = append(env, kv)
		}
	}
	if c.PoolDir != "" {
		env = append(env, PoolEnv+"="+c.PoolDir)
	}
	return env
}

func (c CLI) run(ctx context.Context, name string, args ...string) (string, error) {
	bin := c.Binary
	if bin == "" {
		bin = "ccpool"
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{name}, args...)...)
	cmd.Env = c.Environ()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err == nil {
		return out.String(), nil
	}
	var xe *exec.ExitError
	if errors.As(err, &xe) {
		return out.String(), &ExitError{Cmd: name, Code: xe.ExitCode(), Stderr: errb.String()}
	}
	return out.String(), fmt.Errorf("ccpool %s: %w", name, err)
}

// Capacity implements Client.
func (c CLI) Capacity(ctx context.Context) (Capacity, error) {
	out, err := c.run(ctx, "capacity", "--json")
	if err != nil {
		return Capacity{}, err
	}
	var cp Capacity
	if err := json.Unmarshal([]byte(out), &cp); err != nil {
		return Capacity{}, fmt.Errorf("ccpool capacity: decode: %w", err)
	}
	return cp, nil
}

// Start implements Client.
func (c CLI) Start(ctx context.Context, o StartOpts) error {
	args := []string{o.ExternalID}
	if o.Cwd != "" {
		args = append(args, "--cwd", o.Cwd)
	}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	if o.Effort != "" {
		args = append(args, "--effort", o.Effort)
	}
	if o.PermissionMode != "" {
		args = append(args, "--permission-mode", o.PermissionMode)
	}
	if len(o.AllowedTools) > 0 {
		args = append(args, "--allowed-tools", strings.Join(o.AllowedTools, ","))
	}
	for _, e := range o.Env {
		args = append(args, "--env", e)
	}
	_, err := c.run(ctx, "new", args...)
	return err
}

// Reply implements Client.
func (c CLI) Reply(ctx context.Context, externalID, prompt string) (string, error) {
	out, err := c.run(ctx, "reply", externalID, prompt, "--no-wait")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", errors.New("ccpool reply --no-wait printed no turn id")
	}
	return id, nil
}

// Result implements Client.
func (c CLI) Result(ctx context.Context, turnID string) (string, ResultStatus, error) {
	out, err := c.run(ctx, "result", turnID)
	if err == nil {
		return strings.TrimRight(out, "\n"), ResultDone, nil
	}
	if code, ok := ExitCode(err); ok {
		switch code {
		case 2:
			return "", ResultPending, nil
		case 1:
			return "", ResultUnknown, nil
		}
	}
	return "", ResultUnknown, err
}

// State implements Client.
func (c CLI) State(ctx context.Context, externalID string) (string, error) {
	out, err := c.run(ctx, "state", externalID, "--json")
	if err != nil {
		return "", err
	}
	var st struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return "", fmt.Errorf("ccpool state: decode: %w", err)
	}
	return st.State, nil
}

// Cancel implements Client.
func (c CLI) Cancel(ctx context.Context, externalID string) error {
	_, err := c.run(ctx, "cancel", externalID)
	return err
}

// Close implements Client.
func (c CLI) Close(ctx context.Context, externalID, reason string) error {
	args := []string{externalID}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	_, err := c.run(ctx, "close", args...)
	return err
}
