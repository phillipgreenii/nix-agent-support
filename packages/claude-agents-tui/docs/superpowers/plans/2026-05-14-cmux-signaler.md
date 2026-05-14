# Cmux Signaler (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the `CmuxSignaler` stub in `internal/signal/cmux.go` with a working signaler so `claude-agents-tui` can nudge idle Claude sessions back to work when run inside cmux, matching the existing tmux behavior. Outside cmux the signaler is silently inert.

**Architecture:** CLI-driven signaler that mirrors `TmuxSignaler` in shape, dependency injection (`RunCmd`), and error handling. Adds one new injection seam (`LookupEnv`) so unit tests can toggle `CMUX_WORKSPACE_ID` without mutating the test process env. Sending performs three steps: one shot `cmux --json top --processes` to enumerate every surface in the cmux instance (each entry carries `workspace_ref`, `pane_ref`, `surface_ref`, and `tty_process_pids` — already inclusive of the controlling shell and all its descendants); direct match of the agent pid into `tty_process_pids`; then `cmux send` + `cmux send-key enter` with both `--workspace` and `--surface` pinned. No `cmux list-*` calls and no `ps` ancestry walk are required — the `tty_process_pids` field already covers descendants, simplifying both the implementation and the failure surface.

**Tech Stack:** Go 1.x (standard library only — `os/exec`, `context`, `os`), Bubble Tea (already wired upstream), `cmux` CLI (already on PATH inside cmux), `ps` (already used by `TmuxSignaler`). No new Go dependencies.

**Working directory:** `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui` (the package containing `go.mod`). All paths below are relative to that directory unless prefixed with `/`.

**Reference spec:** `docs/superpowers/specs/2026-05-14-cmux-signaler-design.md`.

---

## Task 1: Beads issue + commit spec/plan

**Files:**
- No code changes. Spike already complete: `cmux --json top --processes` output captured at `/Users/phillipg/phillipg_mbp/top-processes.json` (sibling files `list-workspaces.json`, `list-pane-surfaces.json`, `tree-all.json` also captured but unused — see "Confirmed format" below). This file is a throwaway artifact and is NOT committed.

**Confirmed format (`cmux --json top --processes`):**

JSON object with top-level keys `active`, `caller`, `sample`, `totals`, `windows`. Surface entries are reachable via:

```text
.windows[].workspaces[].panes[].surfaces[]
```

Each terminal surface entry has the relevant fields:

```json
{
  "ref": "surface:19",
  "pane_ref": "pane:17",
  "type": "terminal",
  "tty": "ttys007",
  "tty_process_pids": [26230, 26502, 66123, 66144, 84353]
}
```

`tty_process_pids` is the union of every pid attached to the surface's controlling tty — the surface shell, its children, and grandchildren. `workspace_ref` is the enclosing `.windows[].workspaces[].ref`. All 19 surfaces in the captured spike had non-null `tty` and non-empty `tty_process_pids`; the implementation skips any surface where either is missing/empty (defensive — should not occur in practice).

This obviates the per-workspace `list-pane-surfaces` loop and the `ps`-based ancestry walk that Phase 1 originally proposed. Matching is a single direct lookup: pid in `tty_process_pids`.

- [ ] **Step 1: Create the beads tracking issue**

The repo rule (root `CLAUDE.md`) requires a `bd` issue before any code change. Run:

```bash
bd create \
  --title="Implement CmuxSignaler (Phase 1)" \
  --description="Replace internal/signal/cmux.go stub with a working CLI-driven signaler so auto-resume works inside cmux. Out-of-cmux runs stay silent. Spec: docs/superpowers/specs/2026-05-14-cmux-signaler-design.md" \
  --type=feature --priority=2
```

Note the returned issue id (e.g. `beads-NNN`) — referenced as `BD_ID` below. Claim it:

```bash
bd update BD_ID --claim
```

- [ ] **Step 2: Commit the spec and plan to git (not yet committed)**

The spec file (`docs/superpowers/specs/2026-05-14-cmux-signaler-design.md`) and this plan were written during brainstorming and planning but not committed. Commit them now so this plan has a stable reference:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/docs/superpowers/specs/2026-05-14-cmux-signaler-design.md
git add packages/claude-agents-tui/docs/superpowers/plans/2026-05-14-cmux-signaler.md
git commit -m "docs(claude-agents-tui): cmux signaler phase 1 spec and plan"
```

---

## Task 2: Introduce `LookupEnv` + `RunCmd` fields without breaking existing callers

**Goal:** Lay down the new struct fields and helpers in `internal/signal/cmux.go` while keeping `Detect`/`Send` returning the same values as the current stub. This is a pure scaffolding step so subsequent TDD tasks have a place to write into.

**Files:**
- Modify: `internal/signal/cmux.go`

- [ ] **Step 1: Replace the stub with the scaffolded struct + helpers**

Open `internal/signal/cmux.go` and replace its contents with:

```go
package signal

import (
	"context"
	"os"
	"os/exec"
)

// CmuxSignaler sends keys to the cmux surface hosting a process.
// RunCmd and LookupEnv are injectable for tests; nil values fall back to
// exec.CommandContext and os.LookupEnv respectively.
type CmuxSignaler struct {
	RunCmd    func(ctx context.Context, name string, args ...string) ([]byte, error)
	LookupEnv func(key string) (string, bool)
}

func (c *CmuxSignaler) Name() string { return "cmux" }

func (c *CmuxSignaler) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.RunCmd != nil {
		return c.RunCmd(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).Output()
}

func (c *CmuxSignaler) lookupEnv(key string) (string, bool) {
	if c.LookupEnv != nil {
		return c.LookupEnv(key)
	}
	return os.LookupEnv(key)
}

// Detect returns true when claude-agents-tui is itself running inside cmux.
// Outside cmux the signaler is silently inert.
func (c *CmuxSignaler) Detect(pid int) bool {
	// TODO Task 3: env check.
	_ = pid
	return false
}

// Send injects text followed by Enter into the cmux surface hosting pid.
func (c *CmuxSignaler) Send(pid int, text string) error {
	// TODO Task 4-6: enumerate, match, send.
	_ = pid
	_ = text
	return ErrNotImplemented
}
```

- [ ] **Step 2: Compile and run the existing test suite**

```bash
go test ./internal/signal/...
```

Expected: PASS. Reason: `Detect` still returns false and `Send` still returns `ErrNotImplemented`, matching the assertions in `TestStubSignalersSendNotImplemented` and `TestResolveSignalerReturnsNilWhenNoneMatch`.

- [ ] **Step 3: Commit**

```bash
git add packages/claude-agents-tui/internal/signal/cmux.go
git commit -m "refactor(claude-agents-tui): scaffold CmuxSignaler injection seams"
```

---

## Task 3: TDD `Detect` — env-based in-cmux guard

**Files:**
- Test: `internal/signal/cmux_test.go` (new file)
- Modify: `internal/signal/cmux.go`

- [ ] **Step 1: Create the new test file with two failing tests**

Create `internal/signal/cmux_test.go`:

```go
package signal_test

import (
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/signal"
)

// stubEnv returns a LookupEnv whose values come from m.
func stubEnv(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestCmuxDetectReturnsTrueWhenWorkspaceEnvSet(t *testing.T) {
	sig := &signal.CmuxSignaler{LookupEnv: stubEnv(map[string]string{"CMUX_WORKSPACE_ID": "ws-123"})}
	if !sig.Detect(1234) {
		t.Error("Detect = false, want true when CMUX_WORKSPACE_ID is set")
	}
}

func TestCmuxDetectReturnsFalseWhenWorkspaceEnvUnset(t *testing.T) {
	sig := &signal.CmuxSignaler{LookupEnv: stubEnv(map[string]string{})}
	if sig.Detect(1234) {
		t.Error("Detect = true, want false when CMUX_WORKSPACE_ID is unset")
	}
}
```

- [ ] **Step 2: Run tests and confirm both fail**

```bash
go test ./internal/signal/... -run TestCmuxDetect -v
```

Expected: `TestCmuxDetectReturnsTrueWhenWorkspaceEnvSet` FAILs (currently returns false unconditionally). `TestCmuxDetectReturnsFalseWhenWorkspaceEnvUnset` PASSes (false matches false). The first failure is enough — proceed.

- [ ] **Step 3: Implement `Detect`**

Replace the `Detect` body in `internal/signal/cmux.go`:

```go
// Detect returns true when claude-agents-tui is itself running inside cmux.
// Outside cmux the signaler is silently inert.
func (c *CmuxSignaler) Detect(pid int) bool {
	_ = pid // cmux's socket is instance-global; reachability depends on the caller, not the target.
	v, _ := c.lookupEnv("CMUX_WORKSPACE_ID")
	return v != ""
}
```

- [ ] **Step 4: Run tests and confirm both pass**

```bash
go test ./internal/signal/... -run TestCmuxDetect -v
```

Expected: both PASS.

- [ ] **Step 5: Run the full signal package test suite to catch regressions**

```bash
go test ./internal/signal/...
```

Expected: PASS for everything. `TestStubSignalersSendNotImplemented` will fail because `Detect(1)` is no longer always false. Fix this now before commit:

In `internal/signal/signal_test.go`, locate `TestStubSignalersSendNotImplemented` and remove `&signal.CmuxSignaler{}` from the `stubs` slice. The function becomes:

```go
func TestStubSignalersSendNotImplemented(t *testing.T) {
	stubs := []signal.Signaler{&signal.GhosttySignaler{}, &signal.VSCodeSignaler{}}
	for _, s := range stubs {
		if s.Detect(1) {
			t.Errorf("%s.Detect returned true, want false (stub)", s.Name())
		}
		if err := s.Send(1, "hi"); err != signal.ErrNotImplemented {
			t.Errorf("%s.Send err = %v, want ErrNotImplemented", s.Name(), err)
		}
	}
}
```

Also locate `TestResolveSignalerReturnsNilWhenNoneMatch` and update it so the `CmuxSignaler` has a stub env that returns false (otherwise it will pick up the test runner's actual env, which may or may not have `CMUX_WORKSPACE_ID`):

```go
func TestResolveSignalerReturnsNilWhenNoneMatch(t *testing.T) {
	cmux := &signal.CmuxSignaler{LookupEnv: func(string) (string, bool) { return "", false }}
	got := signal.ResolveSignaler([]signal.Signaler{cmux}, 42)
	if got != nil {
		t.Errorf("ResolveSignaler = %v, want nil", got)
	}
}
```

Re-run:

```bash
go test ./internal/signal/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add packages/claude-agents-tui/internal/signal/cmux.go packages/claude-agents-tui/internal/signal/cmux_test.go packages/claude-agents-tui/internal/signal/signal_test.go
git commit -m "feat(claude-agents-tui): CmuxSignaler.Detect via CMUX_WORKSPACE_ID"
```

---

## Task 4: TDD `Send` — surface enumeration via `cmux --json top --processes`

**Files:**
- Modify: `internal/signal/cmux_test.go`
- Modify: `internal/signal/cmux.go`

**Output shape (confirmed in Task 1):** `cmux --json top --processes` produces a JSON object whose `.windows[].workspaces[].panes[].surfaces[]` path enumerates every surface in the cmux instance. Each surface entry carries `ref`, `pane_ref`, `tty`, `type`, and `tty_process_pids` (all descendants of the surface's controlling tty). The enclosing `.windows[].workspaces[].ref` provides `workspace_ref`. Direct pid match into `tty_process_pids` replaces both the per-workspace listing loop and the `ps` ancestry walk from the original draft.

- [ ] **Step 1: Append a new failing test**

First, replace the import block at the top of `internal/signal/cmux_test.go` (which currently imports only `testing` and the signal package) with:

```go
import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/signal"
)
```

Then append to the same file:

```go
// fakeCmuxRun returns a RunCmd that responds to `cmux --json top --processes`
// with a synthesized JSON envelope built from `surfaces`, and to
// `cmux send --workspace ... --surface ... <text>` /
// `cmux send-key --workspace ... --surface ... enter` by appending the full
// argv (joined by spaces) to *sentCalls.
//
// surfaces is a slice of (workspaceRef, surfaceRef, ttyProcessPIDs) triples; the
// fake nests them inside one window with one pane per surface — that nesting
// shape mirrors real `cmux --json top --processes` output and is sufficient
// because the parser flattens via `.windows[].workspaces[].panes[].surfaces[]`.
type fakeSurface struct {
	workspaceRef string
	surfaceRef   string
	ttyPIDs      []int
}

func fakeCmuxRun(surfaces []fakeSurface, sentCalls *[]string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "cmux" {
			return nil, fmt.Errorf("unexpected command: %s", name)
		}
		// Match the `cmux --json top --processes` invocation precisely.
		if len(args) >= 3 && args[0] == "--json" && args[1] == "top" && args[2] == "--processes" {
			// Group surfaces by workspace so the JSON has one workspace object per
			// distinct workspaceRef, each containing all that workspace's surfaces.
			byWS := map[string][]fakeSurface{}
			var order []string
			for _, s := range surfaces {
				if _, ok := byWS[s.workspaceRef]; !ok {
					order = append(order, s.workspaceRef)
				}
				byWS[s.workspaceRef] = append(byWS[s.workspaceRef], s)
			}
			var wsObjs []string
			for _, w := range order {
				var paneObjs []string
				for i, s := range byWS[w] {
					pidParts := make([]string, len(s.ttyPIDs))
					for j, p := range s.ttyPIDs {
						pidParts[j] = fmt.Sprintf("%d", p)
					}
					paneObjs = append(paneObjs, fmt.Sprintf(
						`{"ref":"pane:%d-%d","surfaces":[{"ref":%q,"pane_ref":"pane:%d-%d","type":"terminal","tty":"ttysX","tty_process_pids":[%s]}]}`,
						len(wsObjs), i, s.surfaceRef, len(wsObjs), i, strings.Join(pidParts, ","),
					))
				}
				wsObjs = append(wsObjs, fmt.Sprintf(`{"ref":%q,"panes":[%s]}`, w, strings.Join(paneObjs, ",")))
			}
			body := fmt.Sprintf(`{"windows":[{"ref":"window:1","workspaces":[%s]}]}`, strings.Join(wsObjs, ","))
			return []byte(body), nil
		}
		if len(args) >= 1 && (args[0] == "send" || args[0] == "send-key") {
			if sentCalls != nil {
				*sentCalls = append(*sentCalls, "cmux "+strings.Join(args, " "))
			}
			return []byte(""), nil
		}
		return nil, fmt.Errorf("unexpected cmux args: %v", args)
	}
}

func TestCmuxSendFindsSurfaceInOwnWorkspace(t *testing.T) {
	// Agent pid 1000 is one of surface:4's tty processes in workspace:1.
	surfaces := []fakeSurface{
		{"workspace:1", "surface:4", []int{100, 500, 1000}},
		{"workspace:1", "surface:5", []int{200, 600}},
	}
	var sent []string
	sig := &signal.CmuxSignaler{
		RunCmd:    fakeCmuxRun(surfaces, &sent),
		LookupEnv: stubEnv(map[string]string{"CMUX_WORKSPACE_ID": "workspace:1"}),
	}
	if err := sig.Send(1000, "continue"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(sent) != 2 {
		t.Fatalf("expected 2 cmux calls (send + send-key), got %d: %v", len(sent), sent)
	}
	if !strings.Contains(sent[0], "send --workspace workspace:1 --surface surface:4 continue") {
		t.Errorf("call[0] = %q, want cmux send targeting workspace:1 surface:4 with text", sent[0])
	}
	if !strings.Contains(sent[1], "send-key --workspace workspace:1 --surface surface:4 enter") {
		t.Errorf("call[1] = %q, want cmux send-key Enter targeting workspace:1 surface:4", sent[1])
	}
}
```

- [ ] **Step 2: Run the test, expect failure**

```bash
go test ./internal/signal/... -run TestCmuxSendFindsSurfaceInOwnWorkspace -v
```

Expected: FAIL with "signaler not implemented for this terminal" (current stub returns `ErrNotImplemented`).

- [ ] **Step 3: Implement `Send` end-to-end (enumerate via JSON + direct pid match + send)**

Replace the contents of `internal/signal/cmux.go` with:

```go
package signal

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// CmuxSignaler sends keys to the cmux surface hosting a process.
// RunCmd and LookupEnv are injectable for tests; nil values fall back to
// exec.CommandContext and os.LookupEnv respectively.
type CmuxSignaler struct {
	RunCmd    func(ctx context.Context, name string, args ...string) ([]byte, error)
	LookupEnv func(key string) (string, bool)
}

// surfaceLoc identifies a cmux surface by its enclosing workspace and surface ref.
type surfaceLoc struct {
	workspaceRef string
	surfaceRef   string
}

// cmuxTopOutput models the subset of `cmux --json top --processes` fields used
// to map a pid to its hosting surface. Fields not consumed here are deliberately
// omitted; encoding/json ignores unknown keys, so any unrelated schema additions
// in cmux are non-breaking.
type cmuxTopOutput struct {
	Windows []struct {
		Workspaces []struct {
			Ref   string `json:"ref"`
			Panes []struct {
				Surfaces []struct {
					Ref            string `json:"ref"`
					Type           string `json:"type"`
					Tty            string `json:"tty"`
					TtyProcessPids []int  `json:"tty_process_pids"`
				} `json:"surfaces"`
			} `json:"panes"`
		} `json:"workspaces"`
	} `json:"windows"`
}

func (c *CmuxSignaler) Name() string { return "cmux" }

func (c *CmuxSignaler) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.RunCmd != nil {
		return c.RunCmd(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).Output()
}

func (c *CmuxSignaler) lookupEnv(key string) (string, bool) {
	if c.LookupEnv != nil {
		return c.LookupEnv(key)
	}
	return os.LookupEnv(key)
}

// Detect returns true when claude-agents-tui is itself running inside cmux.
// Outside cmux the signaler is silently inert.
func (c *CmuxSignaler) Detect(pid int) bool {
	_ = pid // cmux's socket is instance-global; reachability depends on the caller, not the target.
	v, _ := c.lookupEnv("CMUX_WORKSPACE_ID")
	return v != ""
}

// Send injects text followed by Enter into the cmux surface hosting pid.
//
// Steps:
//  1. One shot `cmux --json top --processes` to enumerate every surface.
//  2. Match pid directly into each surface's tty_process_pids.
//  3. cmux send + cmux send-key enter against the matched workspace+surface.
func (c *CmuxSignaler) Send(pid int, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	locs, err := c.enumerateSurfaces(ctx)
	if err != nil {
		return fmt.Errorf("cmux enumerate: %w", err)
	}
	loc, ok := locs[pid]
	if !ok {
		return fmt.Errorf("signal: no cmux surface found for pid %d", pid)
	}
	if _, err := c.run(ctx, "cmux", "send", "--workspace", loc.workspaceRef, "--surface", loc.surfaceRef, text); err != nil {
		return fmt.Errorf("cmux send: %w", err)
	}
	if _, err := c.run(ctx, "cmux", "send-key", "--workspace", loc.workspaceRef, "--surface", loc.surfaceRef, "enter"); err != nil {
		return fmt.Errorf("cmux send-key: %w", err)
	}
	return nil
}

// enumerateSurfaces returns a flat map keyed by every pid in any surface's
// tty_process_pids, so a target pid resolves directly to the surface that
// hosts it. Non-terminal surfaces and surfaces without a tty are skipped.
func (c *CmuxSignaler) enumerateSurfaces(ctx context.Context) (map[int]surfaceLoc, error) {
	out, err := c.run(ctx, "cmux", "--json", "top", "--processes")
	if err != nil {
		return nil, fmt.Errorf("cmux --json top --processes: %w", err)
	}
	var parsed cmuxTopOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parse cmux top: %w", err)
	}
	result := map[int]surfaceLoc{}
	for _, w := range parsed.Windows {
		for _, ws := range w.Workspaces {
			for _, p := range ws.Panes {
				for _, s := range p.Surfaces {
					if s.Type != "terminal" || s.Tty == "" {
						continue
					}
					for _, pid := range s.TtyProcessPids {
						result[pid] = surfaceLoc{workspaceRef: ws.Ref, surfaceRef: s.Ref}
					}
				}
			}
		}
	}
	return result, nil
}
```

- [ ] **Step 4: Run the test and confirm pass**

```bash
go test ./internal/signal/... -run TestCmuxSendFindsSurfaceInOwnWorkspace -v
```

Expected: PASS.

- [ ] **Step 5: Run the full signal package test suite**

```bash
go test ./internal/signal/...
```

Expected: PASS for everything.

- [ ] **Step 6: Commit**

```bash
git add packages/claude-agents-tui/internal/signal/cmux.go packages/claude-agents-tui/internal/signal/cmux_test.go
git commit -m "feat(claude-agents-tui): CmuxSignaler.Send via cmux --json top --processes"
```

---

## Task 5: TDD — cross-workspace match (regression guard)

**Goal:** Lock in the fix for the `CMUX_WORKSPACE_ID`-as-default footgun. If a future refactor accidentally drops `--workspace` from any `cmux` call, this test fails.

**Files:**
- Modify: `internal/signal/cmux_test.go`

- [ ] **Step 1: Append a new test**

Append to `internal/signal/cmux_test.go`:

```go
func TestCmuxSendCrossesWorkspaces(t *testing.T) {
	// Caller (claude-agents-tui) runs in workspace:1.
	// Agent pid 2000 lives in workspace:2, surface:7.
	surfaces := []fakeSurface{
		{"workspace:1", "surface:1", []int{100, 1000}},
		{"workspace:1", "surface:2", []int{200, 1100}},
		{"workspace:2", "surface:7", []int{300, 2000}},
		{"workspace:2", "surface:8", []int{400, 2100}},
	}
	var sent []string
	sig := &signal.CmuxSignaler{
		RunCmd:    fakeCmuxRun(surfaces, &sent),
		LookupEnv: stubEnv(map[string]string{"CMUX_WORKSPACE_ID": "workspace:1"}),
	}
	if err := sig.Send(2000, "continue"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(sent) != 2 {
		t.Fatalf("expected 2 cmux calls, got %d: %v", len(sent), sent)
	}
	for i, call := range sent {
		if !strings.Contains(call, "--workspace workspace:2") {
			t.Errorf("call[%d] = %q, want --workspace workspace:2 (not caller's workspace:1)", i, call)
		}
		if !strings.Contains(call, "--surface surface:7") {
			t.Errorf("call[%d] = %q, want --surface surface:7", i, call)
		}
	}
}
```

- [ ] **Step 2: Run the test, expect pass (regression guard, not a behavior change)**

```bash
go test ./internal/signal/... -run TestCmuxSendCrossesWorkspaces -v
```

Expected: PASS. The implementation from Task 4 already supports cross-workspace targeting; this test pins the behavior so it cannot regress silently.

If it FAILs, the bug is in Task 4's `Send` — the matched `loc.workspaceRef` is not being passed to both `cmux send` and `cmux send-key`. Fix Task 4 before continuing.

- [ ] **Step 3: Commit**

```bash
git add packages/claude-agents-tui/internal/signal/cmux_test.go
git commit -m "test(claude-agents-tui): CmuxSignaler cross-workspace regression guard"
```

---

## Task 6: TDD — no-match error path

**Files:**
- Modify: `internal/signal/cmux_test.go`

- [ ] **Step 1: Append a new test**

Append to `internal/signal/cmux_test.go`:

```go
func TestCmuxSendErrorsWhenNoSurfaceFound(t *testing.T) {
	// Agent pid 1000 is in no surface's tty_process_pids.
	surfaces := []fakeSurface{
		{"workspace:1", "surface:1", []int{9001, 9002}},
	}
	sig := &signal.CmuxSignaler{
		RunCmd:    fakeCmuxRun(surfaces, nil),
		LookupEnv: stubEnv(map[string]string{"CMUX_WORKSPACE_ID": "workspace:1"}),
	}
	err := sig.Send(1000, "continue")
	if err == nil {
		t.Fatal("Send should return error when no surface matches pid")
	}
	if !strings.Contains(err.Error(), "no cmux surface found for pid 1000") {
		t.Errorf("error = %q, want it to mention pid 1000", err.Error())
	}
}
```

- [ ] **Step 2: Run the test, expect pass**

```bash
go test ./internal/signal/... -run TestCmuxSendErrorsWhenNoSurfaceFound -v
```

Expected: PASS. Behavior already implemented in Task 4 via `findSurfaceForPID` returning `false`.

- [ ] **Step 3: Commit**

```bash
git add packages/claude-agents-tui/internal/signal/cmux_test.go
git commit -m "test(claude-agents-tui): CmuxSignaler no-match error message"
```

---

## Task 7: Manual smoke test inside cmux

**Goal:** Confirm end-to-end behavior in a real cmux session. Unit tests prove the parser and the algorithm; this proves the assumed CLI shape matches reality. If this task fails, return to Task 1's spike notes and adjust the parser in Task 4.

**Files:** none (exploratory).

- [ ] **Step 1: Build the binary**

From inside cmux:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go build -o /tmp/claude-agents-tui ./cmd/claude-agents-tui
```

Expected: no errors.

- [ ] **Step 2: Open two cmux surfaces**

In cmux:

- Surface A: this surface, where you will run `claude-agents-tui` in step 4.
- Surface B: a second surface where you launch `claude` and let it sit idle (any prompt, then wait for Claude to finish responding so the session is in Idle state).

- [ ] **Step 3: Force a fake auto-resume condition**

Auto-resume in production only fires after `WindowResetsAt` becomes non-zero (the 5h block hit). Two options for testing:

- **Option A (preferred):** Use the manual-resume keybinding. Check `internal/tui/keys.go` (or run `/tmp/claude-agents-tui` and press `?` for the help modal) to find the manual-resume key. Pressing it triggers `signalNonWorking` with label `"manual-resume"` against every non-Working session, exercising the exact same code path as the auto path.
- **Option B:** Edit a recent transcript JSONL in `~/.claude/projects/<...>` to inject a synthetic rate-limit `assistant` message (see spec `2026-05-07-fix-auto-resume-design.md`). More invasive — only do this if Option A is not available.

Use Option A unless blocked.

- [ ] **Step 4: Run the binary in Surface A and trigger the resume**

```bash
/tmp/claude-agents-tui
```

Verify Surface B's Claude session is listed and not in Working state. Press the manual-resume key. Watch Surface B.

- [ ] **Step 5: Confirm Surface B received "continue\n"**

Surface B should show the text `continue` entered at Claude's prompt and submitted. Claude should resume processing.

If text appears but Enter was not submitted: `cmux send-key enter` failed — check stderr from `claude-agents-tui` for the second-call error.

If nothing appears: the surface match failed. Common causes:

- `cmux list-pane-surfaces` does not include a shell pid column in the format the Task 4 parser expects. Re-capture stdout (`cmux list-pane-surfaces --workspace "$CMUX_WORKSPACE_ID"`) and update the parser.
- Process-tree walk halts at a wrong ancestor. Compare `ps -o ppid=,comm= -p <claude-pid>` output against `pgrep -P` ancestry; the walk should reach the shell pid that owns the surface.

- [ ] **Step 6: Run the binary OUTSIDE cmux to verify silence**

Open a regular terminal (not cmux — `Terminal.app`, `iTerm`, or `ghostty` without cmux). Confirm `CMUX_WORKSPACE_ID` is unset (`echo $CMUX_WORKSPACE_ID` prints empty). Run:

```bash
/tmp/claude-agents-tui
```

Press the manual-resume key. Confirm:

- No `cmux: command not found` errors.
- No "failed to dial socket" errors.
- The existing "no signaler for pid X" log line is acceptable (tmux users already see it). If it appears for *every* session every cycle, that is pre-existing behavior, not a regression introduced by this task.

- [ ] **Step 7: Record the result in beads**

```bash
bd update BD_ID --notes "Smoke test passed inside cmux: manual-resume successfully sent 'continue' + Enter to a Claude session in a sibling surface. Outside cmux: silent."
```

If smoke test failed: do NOT close BD_ID. Adjust the parser in `internal/signal/cmux.go` based on observation, re-run Tasks 4-6's `go test`, return to Step 1 of this task.

---

## Task 8: Final verification + close

**Files:** none.

- [ ] **Step 1: Run the full Go test suite from the package root**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./...
```

Expected: PASS.

- [ ] **Step 2: Run `go vet`**

```bash
go vet ./...
```

Expected: no output.

- [ ] **Step 3: Run `nix flake check` from the agent-support repo root**

Per root `CLAUDE.md`: "If `flake.nix` exists: `nix flake check` MUST pass." Run from the repo root, not the package dir:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
nix flake check
```

Expected: PASS.

Note: there is no `.pre-commit-config.yaml` in this repo (verified via `ls`); the pre-commit step in the root `CLAUDE.md` does not apply. If `nix flake check` complains about an outdated `vendorHash` in `packages/claude-agents-tui/default.nix`, run the package's `./update-deps.sh` per its `README.md` and commit the new hash with message `chore(claude-agents-tui): refresh vendorHash`.

- [ ] **Step 4: Inspect the diff one last time**

```bash
git log --oneline origin/main..HEAD 2>/dev/null || git log --oneline -10
git diff origin/main...HEAD 2>/dev/null || git diff HEAD~6 HEAD
```

Confirm:

- Files touched are limited to: `internal/signal/cmux.go`, `internal/signal/cmux_test.go`, `internal/signal/signal_test.go`, the spec md, this plan md.
- No accidental changes to `main.go`, `poller.go`, `update.go`, `model.go`, `go.mod`, `go.sum`, `default.nix`.

- [ ] **Step 5: Close the beads issue**

```bash
bd close BD_ID --reason "CmuxSignaler phase 1 implemented and smoke-tested inside cmux. Outside cmux confirmed silent."
```

- [ ] **Step 6: Save an insight to beads memory for future sessions**

```bash
bd remember "cmux signaler maps agent pid to surface via: cmux list-workspaces -> for each, cmux list-pane-surfaces --workspace <ref> -> walk process tree until ancestor pid matches a surface shell pid. ALWAYS pass --workspace + --surface explicitly on send/send-key because CMUX_WORKSPACE_ID is the implicit default and would scope to caller's workspace."
```

---

## Self-review notes (do not implement — record-keeping only)

- **Spec coverage:** Each spec section has at least one task:
  - "Detect" → Task 3.
  - "Send" steps 1-4 → Tasks 4-6.
  - "Testing" cases → covered in Tasks 3 (`TestCmuxDetect*`), 4 (`TestCmuxSendFindsSurfaceInOwnWorkspace`), 5 (`TestCmuxSendCrossesWorkspaces`), 6 (`TestCmuxSendErrorsWhenNoSurfaceFound`), plus updates to existing stub/resolver tests in Task 3 step 5.
  - "Wire-up: zero changes in callers" → Task 8 step 4 diff check enforces it.
  - "Spike" → Task 1.
  - "Failure modes" table → exercised by Task 6 (no-match) and Task 7 step 6 (outside-cmux silence).
- **Placeholder scan:** no TBD/TODO except the two `TODO Task N:` markers in Task 2's intentional scaffold, which are removed in Tasks 3 and 4 respectively.
- **Type consistency:** `surfaceLoc{workspaceRef, surfaceRef}` is defined once in Task 4 and referenced consistently. `RunCmd` and `LookupEnv` signatures are stable across Tasks 2, 3, 4.
