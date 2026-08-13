package ccpool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

// ErrCancelUnconfirmed is returned by Cancel when `ccpool cancel` exits 6
// (the interrupt could not be confirmed — the turn may still be running).
var ErrCancelUnconfirmed = errors.New("ccpool cancel unconfirmed")

// exitCoder is satisfied by *exec.ExitError (and test fakes).
type exitCoder interface{ ExitCode() int }

// Per-call timeouts backstop ctx cancellation: even if the orchestrator never
// cancels, a wedged ccpool must not hang the pool forever (pg2-yy42).
const (
	// quickCallTimeout bounds the fast calls (list/reply/cancel/close).
	quickCallTimeout = 60 * time.Second
	// ensureTimeout bounds `ccpool new`, which blocks until the session reaches
	// ready. ccpool's own wait default is 10m, so this sits comfortably above it
	// to avoid killing a legitimately-slow launch while still bounding a wedge.
	ensureTimeout = 12 * time.Minute
)

// CLIRunner is the Phase-1 Runner: it shells out to the `ccpool` binary on PATH.
// run is injectable for tests (zero real processes), exactly like ccpool's
// tmux.Client. The launch-flag fields come from config and are emitted on
// `ccpool new` per the agreed contract (ccpool N2 — see pg2-7mnq.3).
type CLIRunner struct {
	Effort         string
	Model          string
	PermissionMode string // claude --permission-mode; emitted on `ccpool new` when non-empty
	AllowedTools   string // claude --allowed-tools allowlist; emitted on `ccpool new` when non-empty
	// ConfirmIngest is the post-delivery ingestion-guard window forwarded as
	// `ccpool reply --confirm-ingest` on the worker's initial fire-and-forget nudge
	// (ModeNoWait). >0 makes ccpool exit 7 when the model never starts a turn (a
	// dropped nudge); 0 keeps the old fire-and-forget behavior (pg2-yukh #1).
	ConfirmIngest time.Duration
	Autonomous    bool   // emits --autonomous on `ccpool new` when true (block AskUserQuestion)
	bin           string // ccpool binary name/path (resolved on PATH by execCmd)
	// run executes `bin args...` under ctx and returns stdout and stderr in
	// SEPARATE buffers (so stderr noise can never corrupt `list --json` —
	// pg2-x6ef) plus the run error.
	run func(ctx context.Context, args []string) (stdout, stderr []byte, err error)
}

func NewCLIRunner(cfg config.Config) *CLIRunner {
	c := &CLIRunner{Effort: cfg.Effort, Model: cfg.Model, PermissionMode: cfg.PermissionMode, AllowedTools: cfg.AllowedTools, ConfirmIngest: cfg.ConfirmIngest, Autonomous: cfg.Autonomous, bin: config.CCPoolCommand}
	c.run = func(ctx context.Context, args []string) ([]byte, []byte, error) {
		return execCmd(ctx, c.bin, args)
	}
	return c
}

// execCmd runs `bin args...`, capturing stdout and stderr into separate buffers
// (pg2-x6ef) via exec.CommandContext so a cancelled or expired ctx actually
// kills the child rather than hanging the orchestrator/watchdog (pg2-yy42).
func execCmd(ctx context.Context, bin string, args []string) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	return so.Bytes(), se.Bytes(), err
}

// ccpool runs a ccpool subcommand under a per-call timeout and returns stdout
// only; on failure the wrapped error carries the (trimmed) stderr for context.
func (c *CLIRunner) ccpool(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr, err := c.run(tctx, args)
	if err == nil {
		return stdout, nil
	}
	// A wedged ccpool that blew the per-call timeout is the common operator
	// failure; name the timeout rather than leak a bare "context deadline
	// exceeded". tctx (not the parent ctx) carries the WithTimeout deadline.
	if errors.Is(tctx.Err(), context.DeadlineExceeded) {
		return stdout, fmt.Errorf("ccpool %s timed out after %s: %w", argSummary(args), timeout, err)
	}
	if se := bytes.TrimSpace(stderr); len(se) > 0 {
		return stdout, fmt.Errorf("ccpool %s: %w (%s)", argSummary(args), err, se)
	}
	return stdout, fmt.Errorf("ccpool %s: %w", argSummary(args), err)
}

// argSummary renders ccpool args for an error message, eliding long positionals
// (e.g. the full reply prompt, which is the entire skill markdown) so the real
// diagnostic isn't buried under thousands of characters.
func argSummary(args []string) string {
	const maxArg = 48
	parts := make([]string, len(args))
	for i, a := range args {
		if len(a) > maxArg {
			parts[i] = fmt.Sprintf("<%d bytes>", len(a))
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

// Ensure: ccpool new <external_id> --cwd <cwd> [--name <name>] --env K=V… --meta K=V…
// --permission-mode <mode> [--allowed-tools <list>] --effort <effort> [--model <model>].
// The session is addressed by external_id; name is an optional display label
// (omitted when empty). env and meta keys are sorted for deterministic argv. meta is
// stamped atomically as part of `ccpool new` (no separate `ccpool meta set` call).
func (c *CLIRunner) Ensure(ctx context.Context, externalID, name, cwd string, env, meta map[string]string) error {
	args := []string{"new", externalID, "--cwd", cwd}
	if name != "" {
		args = append(args, "--name", name)
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+env[k])
	}
	mkeys := make([]string, 0, len(meta))
	for k := range meta {
		mkeys = append(mkeys, k)
	}
	sort.Strings(mkeys)
	for _, k := range mkeys {
		args = append(args, "--meta", k+"="+meta[k])
	}
	if c.PermissionMode != "" {
		args = append(args, "--permission-mode", c.PermissionMode)
	}
	if c.AllowedTools != "" {
		args = append(args, "--allowed-tools", c.AllowedTools)
	}
	if c.Effort != "" {
		args = append(args, "--effort", c.Effort)
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.Autonomous {
		args = append(args, "--autonomous")
	}
	_, err := c.ccpool(ctx, ensureTimeout, args...)
	return err
}

// Send: ccpool reply <external_id> <prompt> <mode-flag> [--confirm-ingest dur].
func (c *CLIRunner) Send(ctx context.Context, externalID, prompt string, mode SendMode) error {
	flag := "--no-wait"
	switch mode {
	case ModeInterrupt:
		flag = "--interrupt"
	case ModeQueue:
		flag = "--queue-message"
	}
	args := []string{"reply", externalID, prompt, flag}
	// Confirm ingestion only for the worker's initial fire-and-forget nudge
	// (no-wait); a queued budget message is intentionally fire-and-forget with no
	// confirmation (the model is already mid-turn by then). (pg2-yukh #1)
	if c.ConfirmIngest > 0 && mode == ModeNoWait {
		args = append(args, "--confirm-ingest", c.ConfirmIngest.String())
	}
	_, err := c.ccpool(ctx, quickCallTimeout, args...)
	if err != nil {
		// Surface a confirmed dropped nudge (exit 7) as ErrPromptNotIngested so the
		// executor can fail-fast and hand the bead back (mirror Cancel's code-6 wrap).
		var ec exitCoder
		if errors.As(err, &ec) && ec.ExitCode() == 7 {
			return fmt.Errorf("%w: %s", ErrPromptNotIngested, externalID)
		}
		return err
	}
	return nil
}

func (c *CLIRunner) Cancel(ctx context.Context, externalID string) error {
	_, err := c.ccpool(ctx, quickCallTimeout, "cancel", externalID)
	if err != nil {
		var ec exitCoder
		if errors.As(err, &ec) && ec.ExitCode() == 6 {
			return fmt.Errorf("%w: %s", ErrCancelUnconfirmed, externalID)
		}
		return err
	}
	return nil
}

// Close: ccpool close <external_id> [--purge]. purge deletes the session row so
// the next dispatch is always brand-new (pr-pool never resumes; ADR 0015).
func (c *CLIRunner) Close(ctx context.Context, externalID string, purge bool) error {
	args := []string{"close", externalID}
	if purge {
		args = append(args, "--purge")
	}
	_, err := c.ccpool(ctx, quickCallTimeout, args...)
	return err
}

// List: ccpool list --all --json.
func (c *CLIRunner) List(ctx context.Context) ([]Session, error) {
	out, err := c.ccpool(ctx, quickCallTimeout, "list", "--all", "--json")
	if err != nil {
		return nil, err
	}
	var sessions []Session
	if err := json.Unmarshal(out, &sessions); err != nil {
		return nil, fmt.Errorf("ccpool list --json decode: %w", err)
	}
	return sessions, nil
}
