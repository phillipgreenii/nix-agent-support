# Cmux Sidebar Integration (Phase 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface claude-agents-tui state (caffeinate, nudge, working/idle/paused, 5h-block progress) on the cmux sidebar of the TUI's own workspace, plus a one-shot notification when the 5h window resets and agents are nudged. Outside cmux: silent no-op.

**Architecture:** A new `internal/cmuxstatus` package defines a `Reporter` interface with one `Push(Snapshot)` entry point plus `Notify` and `Clear`. `Cmux` implementation shells out to `cmux set-status`, `cmux set-progress`, `cmux notify`, `cmux clear-status`, `cmux clear-progress`. `Noop` implementation has empty methods, used when `CMUX_WORKSPACE_ID` is empty or the config kill switch `cmux_sidebar_enable=false`. The TUI Model calls `reporter.Push(m.buildSidebarSnapshot())` on every caffeinate / auto-resume toggle (synchronously, inside the keybinding handler) and every Nth poll tick (default 5; configurable). `Notify` fires from the `autoResumeFireMsg` handler. `Clear` fires on `tea.Quit`. Headless mode shares the same Reporter for state + progress, no notification path.

**Tech Stack:** Go standard library (`context`, `os`, `os/exec`, `time`, `fmt`, `sync`, `encoding/json` not needed here — no JSON parsing). Bubble Tea already wired upstream. `cmux` CLI already on PATH inside cmux. No new Go dependencies.

**Working directory:** `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui` for `go` commands; parent `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support` for `git`. No git remote — do not push.

**Reference spec:** `docs/superpowers/specs/2026-05-14-cmux-sidebar-design.md`.

**Prerequisite:** Phase 1 (cmux signaler) merged. Verify: `internal/signal/cmux.go` exists and contains `CmuxSignaler` with `RunCmd`/`LookupEnv` fields and `enumerateSurfaces` (post-Phase-1 fixes, with TTL cache).

---

## Task 1: Beads issue + commit spec/plan

- [ ] **Step 1: Create the bd issue**

```bash
bd create \
  --title="Cmux sidebar integration (Phase 2)" \
  --description="Push caffeinate/nudge/state/progress to cmux sidebar; notify on 5h reset+nudge. Spec: docs/superpowers/specs/2026-05-14-cmux-sidebar-design.md" \
  --type=feature --priority=2
```

Note returned id. Claim:

```bash
bd update <id> --claim
```

Referenced as `BD_ID2` below (distinct from Phase 1's `beads_pg2-s1t`).

- [ ] **Step 2: Commit spec + plan**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/docs/superpowers/specs/2026-05-14-cmux-sidebar-design.md
git add packages/claude-agents-tui/docs/superpowers/plans/2026-05-14-cmux-sidebar.md
git commit -m "docs(claude-agents-tui): cmux sidebar phase 2 spec and plan"
```

Capture SHA.

---

## Task 2: cmuxstatus package skeleton — Reporter interface, Snapshot, Noop, constructor

**Files:**
- Create: `internal/cmuxstatus/reporter.go`
- Create: `internal/cmuxstatus/reporter_test.go`

- [ ] **Step 1: Write failing test for `NewReporter` env gating + `Noop` semantics**

Create `internal/cmuxstatus/reporter_test.go`:

```go
package cmuxstatus_test

import (
	"context"
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/cmuxstatus"
)

func TestNewReporterReturnsNoopWhenNotInCmux(t *testing.T) {
	lookup := func(string) (string, bool) { return "", false }
	calls := 0
	run := func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, nil
	}
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    run,
		LookupEnv: lookup,
	})
	r.Push(cmuxstatus.Snapshot{CaffeinateOn: true, HasProgress: true, Progress: 0.5})
	r.Notify("t", "b")
	r.Clear()
	if calls != 0 {
		t.Errorf("Noop should produce 0 subprocess calls; got %d", calls)
	}
}

func TestNewReporterReturnsNoopWhenDisabled(t *testing.T) {
	lookup := func(k string) (string, bool) {
		if k == "CMUX_WORKSPACE_ID" {
			return "workspace:1", true
		}
		return "", false
	}
	calls := 0
	run := func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, nil
	}
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    false,
		RunCmd:    run,
		LookupEnv: lookup,
	})
	r.Push(cmuxstatus.Snapshot{})
	if calls != 0 {
		t.Errorf("disabled reporter should produce 0 subprocess calls; got %d", calls)
	}
}
```

Run, expect compile FAIL (types don't exist yet).

- [ ] **Step 2: Implement the skeleton**

Create `internal/cmuxstatus/reporter.go`:

```go
package cmuxstatus

import (
	"context"
	"os"
	"os/exec"
	"time"
)

// State enumerates the aggregate TUI state surfaced to the cmux sidebar.
type State int

const (
	StateUnknown State = iota
	StateDormant
	StateIdle
	StateWorking
	StatePaused
)

// Snapshot is the full sidebar state. Push always receives one of these.
type Snapshot struct {
	CaffeinateOn  bool
	NudgeOn       bool
	State         State
	PausedResetAt time.Time
	Progress      float64
	ProgressLabel string
	HasProgress   bool
}

// Reporter pushes sidebar updates and notifications to cmux when invoked from
// inside a cmux workspace. The Noop implementation is used outside cmux or when
// the feature is disabled by config.
type Reporter interface {
	Push(s Snapshot)
	Notify(title, body string)
	Clear()
}

// Options configures NewReporter. RunCmd and LookupEnv are injectable for tests;
// nil falls back to exec.CommandContext and os.LookupEnv. Logf receives one line
// per cmux subprocess failure (typically wired to the TUI's signal-errors.log).
type Options struct {
	Enable    bool
	RunCmd    func(ctx context.Context, name string, args ...string) ([]byte, error)
	LookupEnv func(key string) (string, bool)
	Logf      func(string)
}

// NewReporter returns a Cmux reporter when claude-agents-tui is itself running
// inside cmux and Enable is true; a Noop otherwise.
func NewReporter(o Options) Reporter {
	if !o.Enable {
		return noop{}
	}
	lookup := o.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if v, _ := lookup("CMUX_WORKSPACE_ID"); v == "" {
		return noop{}
	}
	return &cmuxReporter{
		runCmd: o.RunCmd,
		logf:   o.Logf,
	}
}

type noop struct{}

func (noop) Push(Snapshot)        {}
func (noop) Notify(string, string) {}
func (noop) Clear()               {}

// cmuxReporter speaks to cmux via subprocesses. Implementation lands in Task 3.
type cmuxReporter struct {
	runCmd func(ctx context.Context, name string, args ...string) ([]byte, error)
	logf   func(string)
}

func (c *cmuxReporter) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.runCmd != nil {
		return c.runCmd(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).Output()
}

func (c *cmuxReporter) log(msg string) {
	if c.logf != nil {
		c.logf(msg)
	}
}

// Push, Notify, Clear are filled in by Tasks 3-4.
func (c *cmuxReporter) Push(Snapshot)        {}
func (c *cmuxReporter) Notify(string, string) {}
func (c *cmuxReporter) Clear()               {}
```

- [ ] **Step 3: Run tests**

```bash
go test ./internal/cmuxstatus/... -v
```

Expected: both tests PASS. `cmuxReporter`'s stub methods are empty so even when constructed, they don't make subprocess calls — but the gating tests use either `Enable=false` or no env var, so they get `noop{}`. Confirm by reading the test setup carefully.

If tests fail because `cmuxReporter` *would* be returned in `TestNewReporterReturnsNoopWhenNotInCmux` and its stub methods don't call `RunCmd`, that's fine — `calls` stays 0 either way. The point is: when not in cmux, `noop` is returned. Verify by inspecting the returned type (use `fmt.Sprintf("%T", r)` printing if helpful during debugging; not required in the test).

- [ ] **Step 4: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/cmuxstatus/
git commit -m "feat(claude-agents-tui): cmuxstatus.Reporter scaffold + Noop"
```

---

## Task 3: TDD `Cmux.Push` — emits 3 set-status + conditional set-progress

- [ ] **Step 1: Append failing tests**

Append to `internal/signal/cmuxstatus/reporter_test.go`. First, augment the imports:

```go
import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/cmuxstatus"
)
```

Helper for recording calls:

```go
// recordingRun returns a RunCmd that appends every "cmux <args>" invocation to
// *calls. The "always succeed" variant returns empty bytes. For per-call error
// injection use recordingRunWithError below.
func recordingRun(calls *[]string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "cmux" {
			return nil, fmt.Errorf("unexpected command: %s", name)
		}
		*calls = append(*calls, "cmux "+strings.Join(args, " "))
		return []byte(""), nil
	}
}

// inCmuxEnv produces a LookupEnv stub claiming we are inside cmux.
func inCmuxEnv() func(string) (string, bool) {
	return func(k string) (string, bool) {
		if k == "CMUX_WORKSPACE_ID" {
			return "workspace:1", true
		}
		return "", false
	}
}
```

Tests:

```go
func TestCmuxPushEmitsFourSubprocessCalls(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	r.Push(cmuxstatus.Snapshot{
		CaffeinateOn:  true,
		NudgeOn:       false,
		State:         cmuxstatus.StateWorking,
		Progress:      0.5,
		ProgressLabel: "5h block 50% used",
		HasProgress:   true,
	})
	if len(calls) != 4 {
		t.Fatalf("expected 4 cmux calls (3 set-status + 1 set-progress), got %d: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "set-status caffeinate on") {
		t.Errorf("call[0] = %q, want set-status caffeinate on", calls[0])
	}
	if !strings.Contains(calls[1], "set-status nudge off") {
		t.Errorf("call[1] = %q, want set-status nudge off", calls[1])
	}
	if !strings.Contains(calls[2], "set-status state working") {
		t.Errorf("call[2] = %q, want set-status state working", calls[2])
	}
	if !strings.Contains(calls[3], "set-progress 0.50 --label 5h block 50% used") {
		t.Errorf("call[3] = %q, want set-progress 0.50 --label '5h block 50%% used'", calls[3])
	}
}

func TestCmuxPushSkipsProgressWhenHasProgressFalse(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	r.Push(cmuxstatus.Snapshot{
		State:       cmuxstatus.StateIdle,
		HasProgress: false,
	})
	if len(calls) != 3 {
		t.Fatalf("expected 3 cmux calls (no progress), got %d: %v", len(calls), calls)
	}
	for _, c := range calls {
		if strings.Contains(c, "set-progress") {
			t.Errorf("unexpected set-progress call: %q", c)
		}
	}
}

func TestCmuxPushClampsProgress(t *testing.T) {
	cases := []struct {
		in     float64
		wanted string
	}{
		{-1, "set-progress 0.00"},
		{0.5, "set-progress 0.50"},
		{2.5, "set-progress 1.00"},
	}
	for _, tc := range cases {
		var calls []string
		r := cmuxstatus.NewReporter(cmuxstatus.Options{
			Enable:    true,
			RunCmd:    recordingRun(&calls),
			LookupEnv: inCmuxEnv(),
		})
		r.Push(cmuxstatus.Snapshot{HasProgress: true, Progress: tc.in, ProgressLabel: "x"})
		if len(calls) < 4 {
			t.Fatalf("in=%v: expected 4 calls, got %d: %v", tc.in, len(calls), calls)
		}
		if !strings.Contains(calls[3], tc.wanted) {
			t.Errorf("in=%v: call[3]=%q, want substring %q", tc.in, calls[3], tc.wanted)
		}
	}
}

func TestCmuxPushPausedStateIncludesResetTime(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	resetAt := time.Date(2026, 5, 14, 15, 30, 0, 0, time.UTC)
	r.Push(cmuxstatus.Snapshot{
		State:         cmuxstatus.StatePaused,
		PausedResetAt: resetAt,
	})
	if len(calls) < 3 {
		t.Fatalf("expected ≥ 3 calls, got %d", len(calls))
	}
	if !strings.Contains(calls[2], "paused") {
		t.Errorf("state call = %q, want it to mention paused", calls[2])
	}
	// Wall-clock formatting: just check that the hour-minute renders into the value.
	if !strings.Contains(calls[2], "15:30") {
		t.Errorf("state call = %q, want it to mention the reset time 15:30", calls[2])
	}
}
```

Run:

```bash
go test ./internal/cmuxstatus/... -run TestCmuxPush -v
```

Expected: all four FAIL (current `Push` is empty).

- [ ] **Step 2: Implement `Push`**

In `internal/cmuxstatus/reporter.go`, replace the empty `Push` body with:

```go
// Push issues 3 cmux set-status calls (caffeinate, nudge, state) and, when
// HasProgress is true, one cmux set-progress call. All four share one 5-second
// context. Errors per call route to logf but do not short-circuit subsequent
// calls — partial-success beats all-or-nothing for sidebar UX.
func (c *cmuxReporter) Push(s Snapshot) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	caff := onOff(s.CaffeinateOn)
	c.runStatus(ctx, "caffeinate", caff, "bolt", caffeinateColor(s.CaffeinateOn))

	nudge := onOff(s.NudgeOn)
	c.runStatus(ctx, "nudge", nudge, "bell", nudgeColor(s.NudgeOn))

	stateVal, stateIcon, stateColor := stateAttrs(s.State, s.PausedResetAt)
	c.runStatus(ctx, "state", stateVal, stateIcon, stateColor)

	if s.HasProgress {
		v := s.Progress
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		args := []string{"set-progress", fmt.Sprintf("%.2f", v)}
		if s.ProgressLabel != "" {
			args = append(args, "--label", s.ProgressLabel)
		}
		if _, err := c.run(ctx, "cmux", args...); err != nil {
			c.log(fmt.Sprintf("cmux set-progress: %v", err))
		}
	}
}

// runStatus issues one cmux set-status with icon and color; logs failures.
func (c *cmuxReporter) runStatus(ctx context.Context, key, value, icon, color string) {
	_, err := c.run(ctx, "cmux", "set-status", key, value, "--icon", icon, "--color", color)
	if err != nil {
		c.log(fmt.Sprintf("cmux set-status %s: %v", key, err))
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func caffeinateColor(on bool) string {
	if on {
		return "#ffcc00"
	}
	return "#888888"
}

func nudgeColor(on bool) string {
	if on {
		return "#00aaff"
	}
	return "#888888"
}

func stateAttrs(s State, resetAt time.Time) (value, icon, color string) {
	switch s {
	case StateWorking:
		return "working", "play", "#00cc66"
	case StateIdle:
		return "idle", "pause", "#888888"
	case StatePaused:
		v := "paused"
		if !resetAt.IsZero() {
			v = fmt.Sprintf("paused (resets %s)", resetAt.Format("15:04"))
		}
		return v, "clock", "#ff8800"
	case StateDormant:
		return "dormant", "moon", "#555555"
	default:
		return "unknown", "circle", "#888888"
	}
}
```

You'll need `"fmt"` in the imports (already there for `os.LookupEnv` — no, that was `os`. Add `fmt` to imports if missing).

- [ ] **Step 3: Run tests, expect green**

```bash
go test ./internal/cmuxstatus/... -v
```

Expected: PASS for everything in this package.

- [ ] **Step 4: Commit**

```bash
git add packages/claude-agents-tui/internal/cmuxstatus/
git commit -m "feat(claude-agents-tui): cmuxstatus.Push emits set-status + set-progress"
```

---

## Task 4: TDD `Cmux.Notify`, `Cmux.Clear`, partial-failure-continues

- [ ] **Step 1: Append failing tests**

Append to `internal/cmuxstatus/reporter_test.go`:

```go
func TestCmuxNotifyEmitsCmuxNotify(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	r.Notify("claude-agents-tui", "5h reset, nudged 3 sessions")
	if len(calls) != 1 {
		t.Fatalf("expected 1 cmux notify call, got %d: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "notify --title claude-agents-tui --body 5h reset, nudged 3 sessions") {
		t.Errorf("call = %q, want cmux notify with title+body", calls[0])
	}
}

func TestCmuxClearIssuesFourCalls(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	r.Clear()
	if len(calls) != 4 {
		t.Fatalf("expected 4 clear calls, got %d: %v", len(calls), calls)
	}
	want := []string{
		"clear-status caffeinate",
		"clear-status nudge",
		"clear-status state",
		"clear-progress",
	}
	for i, w := range want {
		if !strings.Contains(calls[i], w) {
			t.Errorf("call[%d] = %q, want substring %q", i, calls[i], w)
		}
	}
}

func TestCmuxPushPartialFailureContinuesAndLogs(t *testing.T) {
	var calls []string
	var logs []string
	// Fail the SECOND call (nudge); the third and fourth must still attempt.
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, "cmux "+strings.Join(args, " "))
		if len(calls) == 2 {
			return nil, fmt.Errorf("simulated nudge failure")
		}
		return []byte(""), nil
	}
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    run,
		LookupEnv: inCmuxEnv(),
		Logf:      func(s string) { logs = append(logs, s) },
	})
	r.Push(cmuxstatus.Snapshot{HasProgress: true, Progress: 0.1, ProgressLabel: "x"})
	if len(calls) != 4 {
		t.Errorf("expected 4 attempts despite failure, got %d: %v", len(calls), calls)
	}
	if len(logs) != 1 {
		t.Errorf("expected 1 log line for the failed call, got %d: %v", len(logs), logs)
	}
	if len(logs) >= 1 && !strings.Contains(logs[0], "simulated nudge failure") {
		t.Errorf("log[0] = %q, want it to mention the failure", logs[0])
	}
}
```

Run:

```bash
go test ./internal/cmuxstatus/... -run "TestCmuxNotify|TestCmuxClear|TestCmuxPushPartialFailure" -v
```

Expected: first two FAIL (Notify and Clear are empty stubs); the third already PASSes because Push already keeps going after each failure and logs (verify; if it fails, the implementation is wrong — fix Task 3's Push).

- [ ] **Step 2: Implement `Notify` and `Clear`**

In `internal/cmuxstatus/reporter.go`, replace the empty `Notify` and `Clear` with:

```go
// Notify issues one cmux notify call. Failures log but do not panic.
func (c *cmuxReporter) Notify(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.run(ctx, "cmux", "notify", "--title", title, "--body", body); err != nil {
		c.log(fmt.Sprintf("cmux notify: %v", err))
	}
}

// Clear removes every sidebar entry this reporter owns. Best-effort; partial
// failures are logged and ignored.
func (c *cmuxReporter) Clear() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, key := range []string{"caffeinate", "nudge", "state"} {
		if _, err := c.run(ctx, "cmux", "clear-status", key); err != nil {
			c.log(fmt.Sprintf("cmux clear-status %s: %v", key, err))
		}
	}
	if _, err := c.run(ctx, "cmux", "clear-progress"); err != nil {
		c.log(fmt.Sprintf("cmux clear-progress: %v", err))
	}
}
```

- [ ] **Step 3: Run all package tests**

```bash
go test ./internal/cmuxstatus/...
```

Expected: PASS for everything.

- [ ] **Step 4: Commit**

```bash
git add packages/claude-agents-tui/internal/cmuxstatus/
git commit -m "feat(claude-agents-tui): cmuxstatus.Notify + Clear"
```

---

## Task 5: Config flags + `aggregateState` / `windowProgress` helpers in tui

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/tui/model.go` (add helpers, NOT wiring yet)

- [ ] **Step 1: Write failing config tests**

In `internal/config/config_test.go` add:

```go
func TestConfigDefaultsCmuxSidebar(t *testing.T) {
	cfg, _ := config.Load("")
	if !cfg.CmuxSidebarEnable {
		t.Errorf("CmuxSidebarEnable = %v, want true by default", cfg.CmuxSidebarEnable)
	}
	if cfg.CmuxSidebarIntervalTicks != 5 {
		t.Errorf("CmuxSidebarIntervalTicks = %v, want 5", cfg.CmuxSidebarIntervalTicks)
	}
}
```

Run:

```bash
go test ./internal/config/...
```

Expected: FAIL (fields don't exist).

- [ ] **Step 2: Add the fields**

In `internal/config/config.go`:

1. Add to the `Config` struct (the exported one — match the existing pattern around `AutoResumeDelay`):

```go
CmuxSidebarEnable        bool
CmuxSidebarIntervalTicks int
```

2. Add to the raw TOML struct (the one with `*int`/`*string`/etc. pointer fields):

```go
CmuxSidebarEnable        *bool `toml:"cmux_sidebar_enable"`
CmuxSidebarIntervalTicks *int  `toml:"cmux_sidebar_interval_ticks"`
```

3. Add defaults in the same place that sets `AutoResumeDelay: 45 * time.Second`:

```go
CmuxSidebarEnable:        true,
CmuxSidebarIntervalTicks: 5,
```

4. Add the override-when-set pattern matching how `AutoResumeDelay` does it:

```go
if raw.CmuxSidebarEnable != nil {
    cfg.CmuxSidebarEnable = *raw.CmuxSidebarEnable
}
if raw.CmuxSidebarIntervalTicks != nil {
    cfg.CmuxSidebarIntervalTicks = *raw.CmuxSidebarIntervalTicks
}
```

- [ ] **Step 3: Run config tests, expect green**

```bash
go test ./internal/config/...
```

Expected: PASS.

- [ ] **Step 4: Add helpers to `internal/tui/model.go`**

Append (after existing helpers, before the type/method noise gets out of hand):

```go
// aggregateState collapses the tree into the single state we expose on the
// cmux sidebar. Paused (rate-limit hit) wins over everything; otherwise any
// Working session promotes the aggregate to Working; otherwise Idle if any
// non-dormant session exists; otherwise Dormant.
func aggregateState(tree *aggregate.Tree) (cmuxstatus.State, time.Time) {
	if tree == nil {
		return cmuxstatus.StateUnknown, time.Time{}
	}
	if !tree.WindowResetsAt.IsZero() {
		return cmuxstatus.StatePaused, tree.WindowResetsAt
	}
	anyWorking, anyIdle := false, false
	for _, d := range tree.Dirs {
		for _, sv := range d.Sessions {
			switch sv.Status {
			case session.Working:
				anyWorking = true
			case session.Dormant:
				// ignore
			default:
				anyIdle = true
			}
		}
	}
	switch {
	case anyWorking:
		return cmuxstatus.StateWorking, time.Time{}
	case anyIdle:
		return cmuxstatus.StateIdle, time.Time{}
	default:
		return cmuxstatus.StateDormant, time.Time{}
	}
}

// windowProgress derives (used, label, ok) from the active 5h block. When ok
// is false the caller should leave Snapshot.HasProgress false so the reporter
// skips the cmux set-progress call.
func windowProgress(tree *aggregate.Tree, now time.Time) (float64, string, bool) {
	if tree == nil {
		return 0, "", false
	}
	if !tree.WindowResetsAt.IsZero() {
		return 1.0, "5h block exhausted — waiting for reset", true
	}
	b := tree.ActiveBlock
	if b == nil {
		return 0, "", false
	}
	span := b.EndTime.Sub(b.StartTime)
	if span <= 0 {
		return 0, "", false
	}
	used := float64(now.Sub(b.StartTime)) / float64(span)
	if used < 0 {
		used = 0
	}
	if used > 1 {
		used = 1
	}
	return used, fmt.Sprintf("5h block %.0f%% used", used*100), true
}
```

Add imports at top of `internal/tui/model.go` if not already present:

```go
"fmt"
"time"

"github.com/phillipgreenii/claude-agents-tui/internal/cmuxstatus"
"github.com/phillipgreenii/claude-agents-tui/internal/session"
```

(`session` is already imported elsewhere in the tui package; verify and add only what's missing.)

- [ ] **Step 5: Compile-check**

```bash
go build ./...
```

Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add packages/claude-agents-tui/internal/config/ packages/claude-agents-tui/internal/tui/model.go
git commit -m "feat(claude-agents-tui): sidebar config + aggregateState/windowProgress helpers"
```

---

## Task 6: Wire Reporter into Model — field, snapshot builder, periodic Push, quit Clear

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/update.go`
- Modify: `internal/tui/model_test.go`

- [ ] **Step 1: Add fake reporter + failing tests**

In `internal/tui/model_test.go` add at the top (or near other helpers):

```go
type fakeReporter struct {
	pushes  []cmuxstatus.Snapshot
	notifies [][2]string
	clears  int
}

func (f *fakeReporter) Push(s cmuxstatus.Snapshot)      { f.pushes = append(f.pushes, s) }
func (f *fakeReporter) Notify(title, body string)        { f.notifies = append(f.notifies, [2]string{title, body}) }
func (f *fakeReporter) Clear()                           { f.clears++ }
```

Add imports `"github.com/phillipgreenii/claude-agents-tui/internal/cmuxstatus"` if missing.

Append tests:

```go
func TestModelPushesEveryNTicks(t *testing.T) {
	fr := &fakeReporter{}
	m := tui.NewModel(tui.Options{
		Reporter:             fr,
		SidebarIntervalTicks: 3,
	})
	// Drive 5 poll-result messages; expect Push at tick 3 only (tick count starts at 0).
	for i := 0; i < 5; i++ {
		m.Update(tui.PollResultForTest(&aggregate.Tree{}))
	}
	if len(fr.pushes) != 1 {
		t.Errorf("expected 1 Push after 5 ticks with N=3, got %d", len(fr.pushes))
	}
}

func TestModelClearsSidebarOnQuit(t *testing.T) {
	fr := &fakeReporter{}
	m := tui.NewModel(tui.Options{Reporter: fr, SidebarIntervalTicks: 5})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if fr.clears != 1 {
		t.Errorf("expected 1 Clear on quit, got %d", fr.clears)
	}
}
```

You'll need a small test-only helper exposed via `internal/tui/export_test.go` to construct a `pollResultMsg` from a tree (since the message type may be unexported). If `pollResultMsg` is unexported, add to `export_test.go`:

```go
package tui

import "github.com/phillipgreenii/claude-agents-tui/internal/aggregate"

// PollResultForTest builds an internal poll-result message from a tree.
func PollResultForTest(tree *aggregate.Tree) tea.Msg {
	return pollResultMsg{tree: tree, anyWorking: false}
}
```

(Adjust field names to match the actual `pollResultMsg` struct.)

Run, expect compile errors / FAILs.

- [ ] **Step 2: Wire the Model**

In `internal/tui/model.go`, add to `Options`:

```go
Reporter             cmuxstatus.Reporter // optional; nil → noop behavior is the caller's responsibility
SidebarIntervalTicks int                 // 0 or negative → push every tick
```

Add to `Model` struct:

```go
reporter             cmuxstatus.Reporter
sidebarIntervalTicks int
tickCount            int
```

In `NewModel`, after the existing field assignments, add:

```go
m.reporter = o.Reporter
if m.reporter == nil {
    m.reporter = noopReporter{}
}
m.sidebarIntervalTicks = o.SidebarIntervalTicks
```

Add a small inline noop so the field is never nil (avoids nil-deref in Update):

```go
// noopReporter is the fallback when no Reporter is provided via Options.
type noopReporter struct{}

func (noopReporter) Push(cmuxstatus.Snapshot)  {}
func (noopReporter) Notify(string, string)     {}
func (noopReporter) Clear()                    {}
```

Add a snapshot builder method on `*Model`:

```go
// buildSidebarSnapshot collects current TUI state into a Snapshot for push.
func (m *Model) buildSidebarSnapshot() cmuxstatus.Snapshot {
	state, resetAt := aggregateState(m.tree)
	prog, label, ok := windowProgress(m.tree, time.Now())
	return cmuxstatus.Snapshot{
		CaffeinateOn:  m.caffeinateOn,
		NudgeOn:       m.autoResume,
		State:         state,
		PausedResetAt: resetAt,
		Progress:      prog,
		ProgressLabel: label,
		HasProgress:   ok,
	}
}
```

- [ ] **Step 3: Wire the tick counter in update.go**

In `internal/tui/update.go`, find the `pollResultMsg` branch. After the existing logic (`m.tree = msg.tree`, etc.), add:

```go
m.tickCount++
n := m.sidebarIntervalTicks
if n <= 0 {
    n = 1
}
if m.tickCount % n == 0 {
    m.reporter.Push(m.buildSidebarSnapshot())
}
```

- [ ] **Step 4: Wire quit Clear**

In `internal/tui/update.go`, find the `isQuit(msg)` branch (`return m, tea.Quit`). Replace:

```go
if isQuit(msg) {
    return m, tea.Quit
}
```

with:

```go
if isQuit(msg) {
    m.reporter.Clear()
    return m, tea.Quit
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./...
```

Expected: PASS for everything, including the two new model tests.

- [ ] **Step 6: Commit**

```bash
git add packages/claude-agents-tui/internal/tui/
git commit -m "feat(claude-agents-tui): Model pushes sidebar every N ticks, clears on quit"
```

---

## Task 7: Immediate Push on toggle + Notify on autoResumeFireMsg

**Files:**
- Modify: `internal/tui/keybindings.go`
- Modify: `internal/tui/update.go`
- Modify: `internal/tui/model_test.go`

- [ ] **Step 1: Append failing tests**

```go
func TestModelPushesSidebarOnCaffeinateToggle(t *testing.T) {
	fr := &fakeReporter{}
	m := tui.NewModel(tui.Options{Reporter: fr, SidebarIntervalTicks: 5})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("C")})
	if len(fr.pushes) != 1 {
		t.Errorf("expected 1 Push on C, got %d", len(fr.pushes))
	}
}

func TestModelPushesSidebarOnAutoResumeToggle(t *testing.T) {
	fr := &fakeReporter{}
	m := tui.NewModel(tui.Options{Reporter: fr, SidebarIntervalTicks: 5})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	if len(fr.pushes) != 1 {
		t.Errorf("expected 1 Push on R, got %d", len(fr.pushes))
	}
}

func TestModelNotifiesOnAutoResumeFire(t *testing.T) {
	fr := &fakeReporter{}
	m := tui.NewModel(tui.Options{Reporter: fr, SidebarIntervalTicks: 5})
	// Inject a tree with WindowResetsAt set so autoResumeFireMsg's guard passes
	// and at least one non-working session so signalNonWorking iterates.
	tree := &aggregate.Tree{
		WindowResetsAt: time.Now().Add(-1 * time.Second), // already due
		Dirs: []aggregate.Dir{
			{Sessions: []aggregate.SessionView{{PID: 12345, Status: session.Idle}}},
		},
	}
	m.SetTreeAndAutoResumeForTest(tree, true) // enables autoResume
	m.Update(tui.AutoResumeFireForTest())
	if len(fr.notifies) != 1 {
		t.Errorf("expected 1 Notify on auto-resume fire, got %d", len(fr.notifies))
	}
}
```

You'll need a few more test hooks in `internal/tui/export_test.go`:

```go
func (m *Model) SetTreeAndAutoResumeForTest(tree *aggregate.Tree, autoResume bool) {
	m.tree = tree
	m.autoResume = autoResume
}

func AutoResumeFireForTest() tea.Msg { return autoResumeFireMsg{} }
```

Run, expect FAIL.

- [ ] **Step 2: Wire the toggles**

In `internal/tui/keybindings.go`:

`handleToggleCaffeinate`:

```go
func handleToggleCaffeinate(m *Model) tea.Cmd {
    m.caffeinateOn = !m.caffeinateOn
    m.reporter.Push(m.buildSidebarSnapshot())
    return nil
}
```

`handleToggleAutoResume`:

```go
func handleToggleAutoResume(m *Model) tea.Cmd {
    m.autoResume = !m.autoResume
    if m.autoResume && m.tree != nil && !m.tree.WindowResetsAt.IsZero() && !m.autoResumeFired {
        // existing logic — preserve it; just paste in the immediate push call below
        // before returning.
    }
    m.reporter.Push(m.buildSidebarSnapshot())
    return /* existing return value */
}
```

(Inspect the existing function body and preserve all current behavior; only add the `m.reporter.Push(...)` call at the end before the return.)

- [ ] **Step 3: Wire Notify on autoResumeFireMsg**

In `internal/tui/update.go`, find the `autoResumeFireMsg` case. After `m.signalNonWorking("auto-resume")` and `m.autoResumeFired = true`, count how many sessions got nudged. Replace the existing `m.signalNonWorking("auto-resume")` line with a counting variant.

Add this helper to `internal/tui/update.go` (or inline if you prefer):

```go
// signalNonWorkingAndCount calls signalNonWorking and returns the number of
// non-working sessions iterated. Used to populate the notification body.
func (m *Model) signalNonWorkingAndCount(label string) int {
    if m.tree == nil {
        return 0
    }
    count := 0
    for _, d := range m.tree.Dirs {
        for _, sv := range d.Sessions {
            if sv.Status == session.Working {
                continue
            }
            count++
            sig := signal.ResolveSignaler(m.signalers, sv.PID)
            if sig == nil {
                m.signalLog(fmt.Sprintf("%s: no signaler for pid %d", label, sv.PID))
                continue
            }
            if err := sig.Send(sv.PID, m.autoResumeMessage); err != nil {
                m.signalLog(fmt.Sprintf("%s: send failed pid %d: %v", label, sv.PID, err))
            }
        }
    }
    return count
}
```

Then replace `m.signalNonWorking("auto-resume")` in the `autoResumeFireMsg` branch with:

```go
n := m.signalNonWorkingAndCount("auto-resume")
m.reporter.Notify("claude-agents-tui",
    fmt.Sprintf("5h window reset. Nudged %d idle session(s) to continue.", n))
```

The old `signalNonWorking` is still used by the manual-resume keybinding — leave that callsite alone (it still routes through `signalNonWorking` as before).

- [ ] **Step 4: Run tests**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/claude-agents-tui/internal/tui/
git commit -m "feat(claude-agents-tui): immediate sidebar push on toggle, notify on auto-resume fire"
```

---

## Task 8: Wire Reporter construction in main + headless

**Files:**
- Modify: `cmd/claude-agents-tui/main.go`
- Modify: `internal/headless/headless.go`

- [ ] **Step 1: Construct in main.go**

In `cmd/claude-agents-tui/main.go`, after the config is loaded and BEFORE constructing the tui options, add a reporter construction. The reporter needs a `logf` callback wired to the same signal-errors.log that `signalNonWorking` writes to. The cleanest approach: open the log file early and pass a closure that writes to it.

But this is the same write target as the TUI Model's `signalLog`. Easiest: skip per-process file opening here; rely on the existing Model `signalLog` plumbing by passing a closure that calls back into the Model.

That creates a circular dependency between the reporter and the model. Simplest fix: define a small `errorLogger` helper outside both, lazy-open the file in cacheDir, expose `LogString(msg)`, and pass that closure to both the Model AND the cmuxstatus.Options.Logf.

Inspect `internal/tui/model.go`'s `signalLog` to confirm the file path and access mode. Then extract that file-opening logic into a new helper in `internal/tui/`:

```go
// internal/tui/errorlog.go (new file)
package tui

import (
    "fmt"
    "io"
    "os"
    "path/filepath"
    "sync"
)

// ErrorLogger writes append-mode lines to <cacheDir>/signal-errors.log,
// opening the file lazily. Used by both the TUI Model and the cmuxstatus reporter.
type ErrorLogger struct {
    CacheDir string

    mu   sync.Mutex
    file io.WriteCloser
}

func (e *ErrorLogger) LogString(msg string) {
    if e.CacheDir == "" {
        return
    }
    e.mu.Lock()
    defer e.mu.Unlock()
    if e.file == nil {
        if err := os.MkdirAll(e.CacheDir, 0o755); err != nil {
            return
        }
        f, err := os.OpenFile(filepath.Join(e.CacheDir, "signal-errors.log"),
            os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
        if err != nil {
            return
        }
        e.file = f
    }
    fmt.Fprintln(e.file, msg)
}
```

Update `internal/tui/model.go` `signalLog` to delegate to an `*ErrorLogger` field on Model (replace the current inline file-open). Add `ErrorLogger *ErrorLogger` to `Options`.

In `cmd/claude-agents-tui/main.go`:

```go
elog := &tui.ErrorLogger{CacheDir: cacheDir}

reporter := cmuxstatus.NewReporter(cmuxstatus.Options{
    Enable: cfg.CmuxSidebarEnable,
    Logf:   elog.LogString,
})

// ... existing tui.NewModel call, augmented:
model := tui.NewModel(tui.Options{
    // ... existing fields ...
    Reporter:             reporter,
    SidebarIntervalTicks: cfg.CmuxSidebarIntervalTicks,
    ErrorLogger:          elog,
})
```

- [ ] **Step 2: Wire headless**

In `internal/headless/headless.go`, find the poll loop. After each poll completes and the tree is built, add:

```go
// after the existing poll/aggregate step:
snapshot := buildHeadlessSnapshot(tree, caffeinateOn, /* autoResume */ false)
reporter.Push(snapshot)
```

Where `buildHeadlessSnapshot` is defined in `headless.go` and mirrors `Model.buildSidebarSnapshot` but takes raw arguments (since headless has no Model). On exit (the function's `defer` or end), call `reporter.Clear()`.

The reporter is constructed at the top of the headless entry function:

```go
reporter := cmuxstatus.NewReporter(cmuxstatus.Options{
    Enable: cfg.CmuxSidebarEnable,
    Logf:   func(string) {}, // headless doesn't have a TUI to crash; drop log lines for now
})
defer reporter.Clear()
```

No notifications from headless (no auto-resume mechanism in headless).

If headless doesn't currently pass cacheDir into a place where we'd open the log file, that's fine — accept the dropped error lines as a v1 trade-off. Note this in the bd issue as a follow-up if you care.

- [ ] **Step 3: Run all tests**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add packages/claude-agents-tui/cmd/ packages/claude-agents-tui/internal/headless/ packages/claude-agents-tui/internal/tui/
git commit -m "feat(claude-agents-tui): construct sidebar Reporter in main + headless"
```

---

## Task 9: Manual smoke test inside cmux

**Goal:** Validate end-to-end behavior. If anything looks wrong, fix the parser / strings and re-run the relevant Task before continuing.

- [ ] **Step 1: Build**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go build -o /tmp/claude-agents-tui ./cmd/claude-agents-tui
```

- [ ] **Step 2: Run inside cmux**

Open the TUI in a cmux surface. Verify on the cmux sidebar (workspace-scoped, only the workspace running the TUI):

- 3 status entries appear: `caffeinate off`, `nudge off`, `state idle/working/dormant`.
- Progress bar appears at the bottom of the sidebar with `5h block N% used`.

Press `C` → sidebar shows `caffeinate on` immediately.
Press `C` again → `caffeinate off` immediately.
Press `R` → `nudge on` immediately. Press again → `nudge off`.

Let the TUI run for ~10s. Confirm the progress bar % updates roughly every 5 seconds.

- [ ] **Step 3: Trigger an auto-resume fire**

Edit a transcript JSONL to inject a synthetic rate-limit message (see spec `2026-05-07-fix-auto-resume-design.md` for the schema) OR wait for a real 5h reset. When the auto-resume fires, confirm:

- A cmux notification toast pops up titled "claude-agents-tui" with body "5h window reset. Nudged N idle session(s) to continue."
- Sidebar `state` shows `paused (resets HH:MM)` until the reset, then transitions.

- [ ] **Step 4: Quit and verify sidebar clears**

Press `q`. Confirm all 3 status entries vanish from the sidebar, and the progress bar disappears.

- [ ] **Step 5: Run outside cmux to verify silence**

Open a regular terminal (not cmux). Run `/tmp/claude-agents-tui`. Press `C`, `R`, then `q`. Confirm:

- No `cmux:` errors.
- `~/.cache/claude-agents-tui/signal-errors.log` is not created (or has no new lines).
- TUI displays cleanly.

- [ ] **Step 6: Record outcome**

```bash
bd update BD_ID2 --notes "Smoke test passed: sidebar updates immediately on C/R; periodic updates every ~5s; notify fires on auto-resume; sidebar clears on quit; outside cmux silent."
```

If anything fails: do NOT close BD_ID2. Adjust the relevant Task's code and re-run.

---

## Task 10: Final verification + close

- [ ] **Step 1: Full Go test suite**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./...
go vet ./...
```

Expected: PASS / no output.

- [ ] **Step 2: `nix flake check` from repo root**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
nix flake check
```

If the only failures are the pre-existing `darwin/services/beads-web/default.nix` statix lint + treefmt issues that were already broken before Phase 1, ignore them — they are not regressions. If `nix flake check` complains about a vendorHash, run `./update-deps.sh` from the package dir and commit with `chore(claude-agents-tui): refresh vendorHash`.

- [ ] **Step 3: Diff inspection**

```bash
git log --oneline -20
git diff --stat <pre-task-2-SHA>..HEAD -- packages/claude-agents-tui/
```

Confirm only these directories/files changed:

- `internal/cmuxstatus/` (new)
- `internal/tui/{model.go,update.go,keybindings.go,model_test.go,export_test.go,errorlog.go}`
- `internal/config/{config.go,config_test.go}`
- `cmd/claude-agents-tui/main.go`
- `internal/headless/headless.go`
- `docs/superpowers/{specs,plans}/2026-05-14-cmux-sidebar*.md`

No accidental changes to `internal/signal/`, `internal/poller/`, `internal/aggregate/`, `internal/ccusage/`, `go.mod`, `go.sum`, `default.nix` (unless vendorHash refresh).

- [ ] **Step 4: Close bd issue**

```bash
bd close BD_ID2 --reason "Cmux sidebar phase 2 implemented and smoke-tested."
```

- [ ] **Step 5: Save insights**

```bash
bd remember "cmux sidebar Reporter: Push() sends 3 set-status + (optional) 1 set-progress per call; partial failures continue. Workspace scope = caller's CMUX_WORKSPACE_ID; sibling workspaces untouched. Noop fallback when CMUX_WORKSPACE_ID unset or cmux_sidebar_enable=false."
```
