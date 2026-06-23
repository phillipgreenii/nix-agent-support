# Worker did nothing: lost initial nudge + harmful reminder — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Bead:** `pg2-yukh` (P1, bug, labels `ccpool` `pr-pool` `worker-reliability`).

**Goal:** Stop a pr-pool worker from silently burning a budget window doing nothing when its initial nudge is dropped, and stop a context-less worker from mutating an unrelated bead — by (1) confirming in ccpool that the model actually ingested a delivered prompt, (2) running each worker in a fresh per-bead worktree, and (3) gating the budget reminder so it never fires before the first model turn and naming the bead it refers to.

**Architecture:** Three independent root causes across two Go modules; this is ONE bead but two commit/PR streams.
- **ccpool — ingestion guard.** `internal/session/send.go` delivers a prompt (paste + Enter) and, in the fire-and-forget `ModeNoWait`/`ModeQueue` paths, returns immediately with no confirmation the model ever started a turn. We add an OPT-IN, bounded post-delivery confirmation: after delivery, poll the session's transcript for a first-turn advance within a caller-supplied window; if none, surface a DISTINCT error (`ErrPromptNotIngested`) mapped to a DISTINCT `ccpool reply` exit code (7). The detector reuses the `claude-transcript` library (`LastMessageActivity`) through the existing `session.Transcript` port (no new module dependency, no hard dependency on the `pg2-oois.5` registry adoption — see "Blocking unknown" at the end).
- **pr-pool — fresh per-bead worktree.** `internal/executor/ccpool.go:44` launches EVERY session at `Cfg.RepoRoot` (the monorepo on whatever branch it happens to be on). We create/assign a fresh per-bead git worktree at dispatch and launch the session there, threading that path into the prompt's `{{.WorktreeDir}}` and into the watchdog's reset boundary.
- **pr-pool — safe budget reminder.** `ReminderMsg`/`WrapUpMsg` fire on a timer (`internal/watchdog/watchdog.go:72,77`) regardless of whether the model ever took a turn, and say "this bead" (no id). We (a) gate both messages on a first-model-turn having occurred, and (b) make them bead-explicit by templating the bead id in.

**Tech Stack:** Go (stdlib `flag`, `testing`, `reflect`-style table tests). Repo: `phillipgreenii-nix-agent-support`, packages `packages/ccpool` and `packages/pr-pool`. Shared lib `github.com/phillipgreenii/claude-transcript` (in-repo sibling; `LastMessageActivity(path) (time.Time, bool)` already exists and is already imported by ccpool via `cmd/ccpool/reply.go`'s `transcriptAdapter`). gomod2nix engine (ADR 0008) — no new deps are added, so no `gomod2nix.toml` change.

**Branch:** `pr-pool-lost-nudge` (off `main`).

**Commit/PR split (one bead, two streams):**
- **Stream A — ccpool ingestion guard** (Tasks A1–A4): self-contained; can merge first.
- **Stream B — pr-pool worktree + reminder + regression** (Tasks B1–B6): depends on Stream A's `ccpool reply --confirm-ingest` flag + exit code 7 being available, but the worktree and reminder halves (B1–B4) are independent of A and could land in either order.
- **Task R** (final): repo-wide checks + bead close.

---

## File Structure

**ccpool (Stream A):**
- `packages/ccpool/internal/session/send.go` — add `ErrPromptNotIngested`; add `confirmIngested` post-delivery poll; call it from `sendLocked` for `ModeNoWait`/`ModeQueue` when a confirm window is set.
- `packages/ccpool/internal/session/session.go` — extend `Transcript` port with `FirstMessageActivity`; add `ConfirmIngestWindow` + `Now`-driven poll dep already present; add `EnsureOpts`-independent `Send` window plumbing via a new `Mode`-adjacent field is NOT used — the window is carried on `Deps` + a per-call argument (see Task A2).
- `packages/ccpool/cmd/ccpool/reply.go` — add `--confirm-ingest <dur>` flag; map `ErrPromptNotIngested` to exit code 7; extend `transcriptAdapter` with `FirstMessageActivity`.
- Tests: `internal/session/send_test.go`, `cmd/ccpool/reply_test.go` (create if absent; mirror existing `cmd/ccpool` table tests).

**pr-pool (Stream B):**
- `packages/pr-pool/internal/worktree/worktree.go` (CREATE) — `Ensure(ctx, git, worktreeDir, repoRoot, beadID) (path string, err error)`: create/reuse a per-bead worktree dir; pure-ish, git injected.
- `packages/pr-pool/internal/executor/ccpool.go` — at dispatch, ensure the per-bead worktree, launch the session in it (not `Cfg.RepoRoot`), thread the worktree path into `renderNudge`'s `WorktreeDir` and the watchdog.
- `packages/pr-pool/internal/executor/executor.go` — add a `Git watchdog.GitRunner` seam to `Deps` (nil ⇒ `watchdog.OSGit{}`) so the worktree creation is testable.
- `packages/pr-pool/internal/watchdog/watchdog.go` — gate `ReminderMsg`/`WrapUpMsg` on a first-model-turn signal; thread the bead id into the rendered message.
- `packages/pr-pool/internal/config/config.go` — make `ReminderMsg`/`WrapUpMsg` bead-explicit templates (`{{.BeadID}}`), remove the "this bead" ambiguity.
- Tests: `internal/worktree/worktree_test.go`, `internal/executor/ccpool_test.go`, `internal/watchdog/watchdog_test.go`, `internal/config/config_test.go` (or wherever default-config assertions live).

---

# Stream A — ccpool ingestion guard

### Task A1: `Transcript.FirstMessageActivity` port + `ErrPromptNotIngested`

**Files:**
- Modify: `packages/ccpool/internal/session/session.go:48-51` (the `Transcript` interface)
- Modify: `packages/ccpool/internal/session/send.go:13-15` (error vars)
- Test: `packages/ccpool/internal/session/send_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/session/send_test.go`. This asserts a brand-new error value exists and is wrapped by a not-ingested confirmation. It also extends the existing `fakeTranscript` with the new port method (so the package still compiles); to keep the diff honest, add the field and method here in the test file's fake:

```go
func TestErrPromptNotIngested_isExported(t *testing.T) {
	// A sentinel the CLI maps to a distinct exit code; must be a stable value
	// callers can errors.Is against.
	if ErrPromptNotIngested == nil {
		t.Fatal("ErrPromptNotIngested must be a non-nil sentinel error")
	}
	if !strings.Contains(ErrPromptNotIngested.Error(), "ingest") {
		t.Errorf("error text = %q, want it to mention ingest", ErrPromptNotIngested.Error())
	}
}
```

Then extend the existing `fakeTranscript` (it currently has `reply`/`awaiting`) so the package compiles against the new interface method — change its definition near the top of `send_test.go`:

```go
type fakeTranscript struct {
	reply    string
	awaiting bool
	// firstAt/firstOK back FirstMessageActivity. Zero firstOK means "no model
	// turn yet" (the dropped-prompt case).
	firstAt time.Time
	firstOK bool
}

func (f fakeTranscript) LastAssistantText(string) (string, error) { return f.reply, nil }
func (f fakeTranscript) IsAwaitingInput(string) (bool, error)     { return f.awaiting, nil }
func (f fakeTranscript) FirstMessageActivity(string) (time.Time, bool) {
	return f.firstAt, f.firstOK
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./internal/session/ -run ErrPromptNotIngested -v`
Expected: FAIL — compile error (`ErrPromptNotIngested` undefined; `Transcript` has no `FirstMessageActivity`, so the fake's method doesn't satisfy the interface yet).

- [ ] **Step 3: Add the error sentinel**

In `internal/session/send.go`, after the `ErrBusy` declaration (line 15):

```go
// ErrPromptNotIngested is returned by Send when an ingestion-confirmed delivery
// (a non-zero confirm window) delivers the prompt but the session never starts a
// turn within the window — i.e. the paste/Enter did not reach the model (a
// dropped initial nudge). Callers map it to the dedicated exit code 7. This is
// DISTINCT from a turn that started but timed out (TimedOut) or refused (ErrBusy).
var ErrPromptNotIngested = errors.New("prompt delivered but model never started a turn (not ingested)")
```

- [ ] **Step 4: Add the port method to `Transcript`**

In `internal/session/session.go`, extend the `Transcript` interface (currently lines 48-51):

```go
// Transcript reads reply text / awaiting-input state from a transcript file.
type Transcript interface {
	LastAssistantText(path string) (string, error)
	IsAwaitingInput(path string) (bool, error)
	// FirstMessageActivity reports the timestamp of the most recent real
	// message event in the transcript and whether one exists. ok=false means the
	// transcript has no user/assistant message yet (the model has not started a
	// turn). Backed by claude-transcript.LastMessageActivity. A missing/half-
	// written transcript yields (zero, false) — tolerated, never an error.
	FirstMessageActivity(path string) (time.Time, bool)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd packages/ccpool && go test ./internal/session/ -run ErrPromptNotIngested -v`
Expected: FAIL still — the REAL adapters (`transcriptAdapter` in `cmd/ccpool/reply.go`, and any other `Transcript` implementer) do not yet implement `FirstMessageActivity`, so `./internal/session/` compiles (its fakes are updated) but a package-wide build will break. Confirm the session package alone passes:

Run: `cd packages/ccpool && go test ./internal/session/ -run ErrPromptNotIngested -v`
Expected: PASS (the session package's own fakes satisfy the interface). The cmd package is fixed in Task A4.

- [ ] **Step 6: Commit**

```bash
git add internal/session/send.go internal/session/session.go internal/session/send_test.go
git commit -m "feat(ccpool): add Transcript.FirstMessageActivity port + ErrPromptNotIngested sentinel"
```

---

### Task A2: `confirmIngested` bounded post-delivery poll

**Files:**
- Modify: `packages/ccpool/internal/session/session.go:102-126` (the `Deps` struct — add the poll knobs)
- Modify: `packages/ccpool/internal/session/send.go` (add `confirmIngested`, wire it into `sendLocked`)
- Test: `packages/ccpool/internal/session/send_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/session/send_test.go`. Two cases: a dropped prompt (no first turn within the window → `ErrPromptNotIngested`), and an ingested prompt (first turn appears → no error). The confirm window is carried per-call via a new `Send` variant; we add `SendWithConfirm`. The store row needs a `TranscriptPath` so the resolver has a path to read.

```go
func TestSend_confirmIngest_dropped_returnsNotIngested(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Ready, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})
	tm := &sendTmux{live: true}
	// firstOK=false ⇒ no model turn ever appears in the window.
	tr := fakeTranscript{firstOK: false}
	s := newSendService(t, st, tm, tr, waitFunc(nil))
	// confirm window 30ms, poll 5ms, injected no-op sleep so the test is instant.
	_, err := s.SendWithConfirm(ctx, "a", "do the task", session.ModeNoWait, 30*time.Millisecond)
	if !errors.Is(err, ErrPromptNotIngested) {
		t.Fatalf("dropped prompt must yield ErrPromptNotIngested, got %v", err)
	}
	// The prompt was still pasted (delivery happened; only confirmation failed).
	if len(tm.pasted) != 1 {
		t.Errorf("prompt should still have been delivered; pasted=%v", tm.pasted)
	}
}

func TestSend_confirmIngest_ingested_ok(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Ready, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})
	tm := &sendTmux{live: true}
	// firstOK=true ⇒ a model turn is present ⇒ ingested.
	tr := fakeTranscript{firstOK: true, firstAt: time.Unix(100, 0)}
	s := newSendService(t, st, tm, tr, waitFunc(nil))
	res, err := s.SendWithConfirm(ctx, "a", "do the task", session.ModeNoWait, 30*time.Millisecond)
	if err != nil {
		t.Fatalf("ingested prompt must succeed, got %v", err)
	}
	if res.State != store.Working {
		t.Errorf("no-wait confirmed result state = %q, want working", res.State)
	}
}

func TestSend_noConfirmWindow_skipsCheck(t *testing.T) {
	// A zero window keeps today's fire-and-forget behavior: no transcript read,
	// no ErrPromptNotIngested even when firstOK=false.
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{ExternalID: "a", ClaudeSessionID: "u", Name: "a", State: store.Ready, TmuxSession: "cc-a", TranscriptPath: "/p/a.jsonl"})
	s := newSendService(t, st, &sendTmux{live: true}, fakeTranscript{firstOK: false}, waitFunc(nil))
	res, err := s.SendWithConfirm(ctx, "a", "do it", session.ModeNoWait, 0)
	if err != nil {
		t.Fatalf("zero window must skip the ingestion check, got %v", err)
	}
	if res.State != store.Working {
		t.Errorf("state = %q, want working", res.State)
	}
}
```

NOTE the test references `session.ModeNoWait` — inside the `session` package's own `_test.go` the mode constants are unqualified (`ModeNoWait`). Use the unqualified form to match the file's existing style (e.g. line 63 uses `ModeRefuseIfBusy`). The three tests above are shown qualified for clarity; when adding them, write `ModeNoWait` without the `session.` prefix.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./internal/session/ -run 'confirmIngest|noConfirmWindow' -v`
Expected: FAIL — `SendWithConfirm` is undefined.

- [ ] **Step 3: Add the poll-sleep dep (reuse the existing injected sleep)**

The `Service` already has an injected `Sleep` (`Deps.Sleep`, line 123, with the `s.sleep(d)` helper at `session.go` ~line ~390). Reuse it for the confirm poll so tests need no new wiring (a nil `Sleep` is a no-op, making the poll loop spin to the bounded iteration count instantly). No `Deps` change is required for the sleep.

Confirm the existing helper exists (read-only check):

Run: `cd packages/ccpool && grep -n 'func (s \*Service) sleep' internal/session/session.go`
Expected: prints the `sleep` helper line. (If absent, add `func (s *Service) sleep(d time.Duration) { if s.d.Sleep != nil { s.d.Sleep(d) } }` — but it already exists per the read above.)

- [ ] **Step 4: Add `confirmIngested` and `SendWithConfirm`**

In `internal/session/send.go`, add the confirm poll and a `Send` variant that carries the window. Place `SendWithConfirm` next to `Send` (after line 27), and `confirmIngested` after `deliverPrompt` (after line 102):

```go
// confirmIngestPoll is the gap between transcript checks while confirming the
// model started a turn. Small relative to the caller window so the bound is the
// window, not the poll granularity.
const confirmIngestPoll = 250 * time.Millisecond

// SendWithConfirm is Send with a post-delivery ingestion guard. When window > 0
// and mode is a fire-and-forget mode (ModeNoWait/ModeQueue), after delivering the
// prompt it polls the session transcript for a first model turn; if none appears
// within window it returns ErrPromptNotIngested (the dropped-nudge case). A zero
// window, or a waiting mode, behaves exactly like Send.
func (s *Service) SendWithConfirm(ctx context.Context, externalID, prompt string, mode Mode, window time.Duration) (Result, error) {
	var res Result
	err := s.withLock(externalID, func() error {
		var e error
		res, e = s.sendLocked(ctx, externalID, prompt, mode)
		if e != nil {
			return e
		}
		if window > 0 && (mode == ModeNoWait || mode == ModeQueue) {
			return s.confirmIngested(ctx, externalID, window)
		}
		return nil
	})
	return res, err
}

// confirmIngested polls the session's transcript until a real message event
// appears (the model started a turn) or window elapses. Returns nil on the first
// observed turn, ErrPromptNotIngested on timeout. A row without a transcript path
// (hook has not stamped one yet) is tolerated — the resolver returns (zero,false)
// and we keep polling until the window closes, then fail (no transcript ⇒ no turn
// observed ⇒ treat as not ingested, which is the safe direction for a worker).
func (s *Service) confirmIngested(ctx context.Context, externalID string, window time.Duration) error {
	deadline := s.d.Now().Add(window)
	for {
		row, ok, err := s.d.Store.GetByExternalID(ctx, externalID)
		if err != nil {
			return fmt.Errorf("confirm ingest: %w", err)
		}
		if ok && row.TranscriptPath != "" {
			if _, started := s.d.Transcript.FirstMessageActivity(row.TranscriptPath); started {
				return nil
			}
		}
		if !s.d.Now().Before(deadline) {
			return ErrPromptNotIngested
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		s.sleep(confirmIngestPoll)
	}
}
```

Add `"time"` to the `send.go` import block (it currently imports `context`, `errors`, `fmt`, `strings`, plus the two internal packages):

```go
import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)
```

NOTE on the test clock: the three A2 tests inject `Now: func() time.Time { return time.Unix(1, 0) }` via `newSendService` (a FIXED clock). With a fixed clock, `s.d.Now().Before(deadline)` is false on the first iteration when window>0 only if `Now == deadline`; since `deadline = Now + window` and `window > 0`, the first check IS before the deadline, so the loop polls once (sleep is a no-op), then on the second iteration `Now` is still `Unix(1,0)` which is STILL before `Unix(1,0)+window` — an infinite loop. To make the bound testable with a fixed clock, the deadline must be evaluated against an ITERATION budget, not wall-clock. Revise `confirmIngested` to use a max-iteration bound derived from the window so a fixed test clock terminates:

```go
func (s *Service) confirmIngested(ctx context.Context, externalID string, window time.Duration) error {
	// Bound by iteration count (window/poll, min 1) so a fixed test clock still
	// terminates; production uses the same bound with a real sleep so the wall
	// time is ~window.
	iters := int(window / confirmIngestPoll)
	if iters < 1 {
		iters = 1
	}
	for i := 0; i < iters; i++ {
		row, ok, err := s.d.Store.GetByExternalID(ctx, externalID)
		if err != nil {
			return fmt.Errorf("confirm ingest: %w", err)
		}
		if ok && row.TranscriptPath != "" {
			if _, started := s.d.Transcript.FirstMessageActivity(row.TranscriptPath); started {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		s.sleep(confirmIngestPoll)
	}
	return ErrPromptNotIngested
}
```

Use the iteration-bounded form. Drop the unused `"time"` import addition for `Now` (the `time` import is still needed for the `confirmIngestPoll` constant and the `window time.Duration` parameter, so keep it). Set `confirmIngestPoll` so the 30ms test window yields exactly 1 iteration when poll is 250ms — that is fine (1 check, then fail). For the ingested test, the single check observes `firstOK=true` and returns nil.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd packages/ccpool && go test ./internal/session/ -run 'confirmIngest|noConfirmWindow|ErrPromptNotIngested' -v`
Expected: PASS — dropped → `ErrPromptNotIngested`; ingested → nil/working; zero window → skipped.

- [ ] **Step 6: Run the full session package (no regressions)**

Run: `cd packages/ccpool && go test ./internal/session/ -v`
Expected: PASS — existing `Send` tests unchanged (`Send` still exists and is unmodified; `SendWithConfirm` is additive).

- [ ] **Step 7: Commit**

```bash
git add internal/session/send.go internal/session/send_test.go
git commit -m "feat(ccpool): SendWithConfirm post-delivery ingestion guard (ErrPromptNotIngested)"
```

---

### Task A3: `ccpool reply --confirm-ingest` flag + exit code 7

**Files:**
- Modify: `packages/ccpool/cmd/ccpool/reply.go:24-104`
- Test: `packages/ccpool/cmd/ccpool/reply_test.go` (CREATE if absent; mirror `cmd/ccpool` table-test style)

- [ ] **Step 1: Write the failing test**

Create/extend `cmd/ccpool/reply_test.go`. The exit-code mapping is a pure function (`replyExitCode`), so test it directly (this avoids launching claude):

```go
package main

import (
	"testing"

	"github.com/phillipgreenii/ccpool/internal/session"
)

func TestReplyExitCode_notIngestedIsSeven(t *testing.T) {
	if got := replyExitCode(session.ErrPromptNotIngested); got != 7 {
		t.Errorf("replyExitCode(ErrPromptNotIngested) = %d, want 7", got)
	}
	// Existing mappings unchanged.
	if got := replyExitCode(session.ErrBusy); got != 5 {
		t.Errorf("replyExitCode(ErrBusy) = %d, want 5", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run ReplyExitCode -v`
Expected: FAIL — `replyExitCode(ErrPromptNotIngested)` returns 1 (the default), not 7.

- [ ] **Step 3: Map the error to exit code 7**

In `cmd/ccpool/reply.go`, extend `replyExitCode` (lines 111-120):

```go
func replyExitCode(err error) int {
	switch {
	case errors.Is(err, session.ErrBusy):
		return 5
	case errors.Is(err, session.ErrCancelUnconfirmed):
		return 6
	case errors.Is(err, session.ErrPromptNotIngested):
		return 7
	default:
		return 1
	}
}
```

- [ ] **Step 4: Add the `--confirm-ingest` flag and call `SendWithConfirm`**

In `cmd/ccpool/reply.go`, add the flag after `interrupt` (line 28):

```go
	confirmIngest := fs.Duration("confirm-ingest", 0, "after a fire-and-forget delivery, confirm the model started a turn within this window; exit 7 if not (0 = no check)")
```

Update the usage string (line 31):

```go
		fmt.Fprintln(os.Stderr, "usage: ccpool reply <external_id> <prompt> [--no-wait] [--queue-message] [--interrupt] [--confirm-ingest dur]")
```

Replace the `svc.Send(...)` call (line 70) with the confirm-aware variant:

```go
	res, err := svc.SendWithConfirm(context.Background(), externalID, prompt, mode, *confirmIngest)
```

- [ ] **Step 5: Extend `transcriptAdapter` with `FirstMessageActivity`**

In `cmd/ccpool/reply.go`, the `transcriptAdapter` (lines 19-22) must satisfy the widened `session.Transcript` interface. Add:

```go
func (transcriptAdapter) FirstMessageActivity(p string) (time.Time, bool) {
	return ct.LastMessageActivity(p)
}
```

Add `"time"` to `reply.go`'s import block (it currently imports `context`, `errors`, `flag`, `fmt`, `os`, the uuid pkg, the internal packages, and `ct`):

```go
import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/session"
	"github.com/phillipgreenii/ccpool/internal/store"
	ct "github.com/phillipgreenii/claude-transcript"
)
```

- [ ] **Step 6: Find and fix every other `session.Transcript` implementer**

The widened interface breaks any other adapter. Find them:

Run: `cd packages/ccpool && grep -rln 'LastAssistantText\|IsAwaitingInput' --include='*.go' | grep -v _test`
Expected: at minimum `cmd/ccpool/reply.go` (fixed above) and possibly `cmd/ccpool/state.go` or a shared `cmd/ccpool` adapter. For EACH implementer that is a `session.Transcript`, add the same `FirstMessageActivity` method delegating to `ct.LastMessageActivity`. If `transcriptAdapter` is the single shared adapter used everywhere (likely — it lives in `reply.go` and `cmd/ccpool` is one package), Step 5 already covers it; this step is the verification that nothing else implements the port.

- [ ] **Step 7: Run tests to verify they pass + package compiles**

Run: `cd packages/ccpool && go build ./... && go test ./cmd/ccpool/ -run ReplyExitCode -v`
Expected: PASS — build is clean (all `Transcript` implementers satisfy the new method); exit-code test passes.

- [ ] **Step 8: Commit**

```bash
git add cmd/ccpool/reply.go cmd/ccpool/reply_test.go
git commit -m "feat(ccpool): 'ccpool reply --confirm-ingest' flag + exit code 7 for dropped prompts"
```

---

### Task A4: ccpool stream verification

- [ ] **Step 1: Full ccpool test + vet**

Run: `cd packages/ccpool && go test ./... && go vet ./...`
Expected: all PASS.

- [ ] **Step 2: Manual flag smoke**

Run: `cd packages/ccpool && go run ./cmd/ccpool reply 2>&1 | grep -- --confirm-ingest`
Expected: the usage line lists `--confirm-ingest`.

- [ ] **Step 3: Commit any vet-fix (if needed)**

```bash
git add -A && git commit -m "chore(ccpool): vet/test clean for ingestion guard" || echo "nothing to commit"
```

---

# Stream B — pr-pool fresh worktree + safe reminder + regression

### Task B1: `worktree.Ensure` — fresh per-bead worktree

**Files:**
- Create: `packages/pr-pool/internal/worktree/worktree.go`
- Test: `packages/pr-pool/internal/worktree/worktree_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/worktree/worktree_test.go`. The git runner is injected (a recorder), so the test asserts the git plumbing without a real repo. Per-bead path = `<worktreeDir>/<beadID>`; a fresh branch `pr-pool/<beadID>` is created off the repo's current HEAD; an existing worktree dir is reused (idempotent).

```go
package worktree

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

type recGit struct {
	calls [][]string
	// existsAt: a path the fake reports as an existing worktree (rev-parse ok).
	existsAt string
}

func (g *recGit) Run(_ context.Context, dir string, args ...string) error {
	g.calls = append(g.calls, append([]string{dir}, args...))
	// Simulate "worktree already present": `git -C <path> rev-parse` succeeds only
	// for existsAt.
	if len(args) > 0 && args[0] == "rev-parse" {
		if dir == g.existsAt {
			return nil
		}
		return errNotARepo
	}
	return nil
}

func TestEnsure_createsFreshPerBeadWorktree(t *testing.T) {
	g := &recGit{}
	wtDir := t.TempDir()
	got, err := Ensure(context.Background(), g, wtDir, "/repo", "zr-6bq.3")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	want := filepath.Join(wtDir, "zr-6bq.3")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	// Must have run `git -C /repo worktree add -B pr-pool/zr-6bq.3 <path>`.
	var added bool
	for _, c := range g.calls {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "/repo worktree add") &&
			strings.Contains(joined, "pr-pool/zr-6bq.3") &&
			strings.Contains(joined, want) {
			added = true
		}
	}
	if !added {
		t.Errorf("expected a `git -C /repo worktree add -B pr-pool/zr-6bq.3 %s`; calls=%v", want, g.calls)
	}
}

func TestEnsure_reusesExistingWorktree(t *testing.T) {
	wtDir := t.TempDir()
	path := filepath.Join(wtDir, "zr-1")
	g := &recGit{existsAt: path}
	got, err := Ensure(context.Background(), g, wtDir, "/repo", "zr-1")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
	// Reuse path must NOT run `worktree add`.
	for _, c := range g.calls {
		if strings.Contains(strings.Join(c, " "), "worktree add") {
			t.Errorf("existing worktree must be reused, not re-added; calls=%v", g.calls)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pr-pool && go test ./internal/worktree/ -v`
Expected: FAIL — package `worktree` does not exist.

- [ ] **Step 3: Write `worktree.Ensure`**

Create `internal/worktree/worktree.go`:

```go
// Package worktree assigns a fresh, isolated per-bead git worktree at dispatch so
// a pr-pool worker never runs on whatever unrelated branch the monorepo happens
// to be on (pg2-yukh root cause #2). The worktree dir is <worktreeDir>/<beadID>
// on a dedicated branch pr-pool/<beadID>; it is idempotent (an existing worktree
// for the bead is reused).
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// errNotARepo is returned by the production Git when a path is not a worktree
// root (rev-parse fails). Tests reuse it via the recGit fake.
var errNotARepo = errors.New("not a git worktree")

// Git runs `git -C <dir> <args...>` (injectable; matches watchdog.GitRunner so
// the executor can share OSGit). A nil error means the command succeeded.
type Git interface {
	Run(ctx context.Context, dir string, args ...string) error
}

// Ensure returns the path to a fresh per-bead worktree, creating it under
// worktreeDir off repoRoot's current HEAD on branch pr-pool/<beadID>. If a
// worktree already exists at that path it is reused (idempotent). The branch name
// and dir derive deterministically from beadID, so a redispatch reuses the same
// isolated workspace rather than the shared monorepo checkout.
func Ensure(ctx context.Context, git Git, worktreeDir, repoRoot, beadID string) (string, error) {
	path := filepath.Join(worktreeDir, beadID)
	// Reuse: if the path is already a worktree root, keep it.
	if err := git.Run(ctx, path, "rev-parse", "--is-inside-work-tree"); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir worktree dir: %w", err)
	}
	branch := "pr-pool/" + beadID
	// -B resets/creates the branch at HEAD; addressing repoRoot so the new worktree
	// branches off the monorepo's current commit, then checks out in isolation.
	if err := git.Run(ctx, repoRoot, "worktree", "add", "-B", branch, path); err != nil {
		return "", fmt.Errorf("worktree add %s: %w", path, err)
	}
	return path, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/pr-pool && go test ./internal/worktree/ -v`
Expected: PASS — create path runs `worktree add -B pr-pool/zr-6bq.3 <path>`; reuse path skips it.

- [ ] **Step 5: Commit**

```bash
git add internal/worktree/worktree.go internal/worktree/worktree_test.go
git commit -m "feat(pr-pool): worktree.Ensure assigns a fresh per-bead worktree"
```

---

### Task B2: Launch the worker session in the per-bead worktree

**Files:**
- Modify: `packages/pr-pool/internal/executor/executor.go:32-42` (add `Git` seam to `Deps`)
- Modify: `packages/pr-pool/internal/executor/ccpool.go:36-79,110-132,168-189`
- Test: `packages/pr-pool/internal/executor/ccpool_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/executor/ccpool_test.go`. The existing `dtest.FakeCC` records `Ensure`'s `cwd`; assert it is the per-bead worktree, not `Cfg.RepoRoot`. Inject a recording git into `Deps`. First confirm what `dtest.FakeCC.Ensure` captures:

Run: `cd packages/pr-pool && grep -n 'func.*FakeCC.*Ensure\|ensureCwd\|EnsureCwd\|cwd' internal/dtest/*.go`
Expected: shows whether `FakeCC` records the cwd. If it does NOT, extend `dtest.FakeCC` to capture the last `Ensure` cwd (add a field `EnsuredCwd string` set in its `Ensure`). Make that edit first if needed, then:

```go
func TestDispatch_launchesInFreshPerBeadWorktree(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	cc := &dtest.FakeCC{ /* list/etc as other Dispatch tests set up; bead closes fast */ }
	bd := dtest.NewScriptBD( /* mirror an existing success scenario so Dispatch returns cleanly */ )
	rg := &recGit{} // the worktree-creating recorder (reuse the worktree_test pattern or a local fake)
	r := &ccpoolRun{deps: Deps{
		CC: cc, BD: bd, Cfg: cfg, ExternalID: "zr-1",
		Now: (&dtest.ManualClock{T: time.Unix(0, 0)}).Now,
		Tick: (&dtest.ManualClock{T: time.Unix(0, 0)}).TickAdvancing(),
		Git: rg,
	}}
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-1"}}
	_, _ = r.run(context.Background(), d)
	want := filepath.Join(cfg.WorktreeDir, "zr-1")
	if cc.EnsuredCwd != want {
		t.Errorf("session launched at %q, want fresh worktree %q", cc.EnsuredCwd, want)
	}
}
```

NOTE: model this test's bead-completion scaffolding on the nearest existing passing `TestDispatch_*` (e.g. a success/handback case) so `run` returns without hanging; the only NEW assertion is `cc.EnsuredCwd == <worktree>`. The `recGit` fake can be a tiny local copy of the worktree_test recorder, or promoted to `dtest`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pr-pool && go test ./internal/executor/ -run FreshPerBeadWorktree -v`
Expected: FAIL — compile error (`Deps` has no `Git`) and/or the session launches at `Cfg.RepoRoot`, not the worktree.

- [ ] **Step 3: Add the `Git` seam to `Deps`**

In `internal/executor/executor.go`, import the watchdog package's runner type and add the field to `Deps` (after `ExternalID`, line 41):

```go
	// Git creates the per-bead worktree at dispatch (nil ⇒ watchdog.OSGit{}). Shared
	// with the watchdog so the worktree it resets is the one the worker ran in.
	Git watchdog.GitRunner
```

Add the import `"github.com/phillipgreenii/pr-pool/internal/watchdog"` to `executor.go`'s import block, and a helper next to the other `Deps` accessors (after `waitPoll`, line 75):

```go
func (d Deps) git() watchdog.GitRunner {
	if d.Git != nil {
		return d.Git
	}
	return watchdog.OSGit{}
}
```

`watchdog.GitRunner` and `worktree.Git` have the identical method set (`Run(ctx, dir, ...string) error`), so `OSGit` satisfies `worktree.Git` structurally — pass `d.git()` straight into `worktree.Ensure`. (If Go complains about the named-interface mismatch at the call site, accept `worktree.Git` as the parameter type — both are satisfied by `OSGit`.)

- [ ] **Step 4: Create the worktree at dispatch and launch there**

In `internal/executor/ccpool.go`, `run` (lines 36-79), BEFORE the `Ensure` call (line 44), assign the worktree and use it as the session cwd. Replace the env/Ensure block:

```go
func (r *ccpoolRun) run(ctx context.Context, d discover.DispatchContext) (report.Result, error) {
	cc := d.Role.CCPool
	display := d.Role.DisplayName(r.deps.Cfg.SessionPrefix, d.Item.ID)

	// Fresh per-bead worktree so the worker never runs on a stale unrelated branch
	// (pg2-yukh root cause #2). On failure to create one, treat it like a launch
	// failure (escalate per ADR 0015) — running in the shared monorepo is exactly
	// the bug we are fixing, so we do NOT silently fall back to RepoRoot.
	wt, werr := worktree.Ensure(ctx, r.deps.git(), r.deps.Cfg.WorktreeDir, r.deps.Cfg.RepoRoot, d.Item.ID)
	if werr != nil {
		var res report.Result
		if r.escalateLaunchFailure(ctx, d.Item.ID) {
			res = failureAction(report.Escalated, d.Item.ID)
		}
		return res, fmt.Errorf("worktree %s: %w", d.Item.ID, werr)
	}

	env := map[string]string{
		"BEADS_ACTOR":    cc.Actor,
		"BEADS_DIR":      r.deps.Cfg.RepoRoot + "/.beads",
		"WORKSPACE_ROOT": wt,
	}
	if err := r.deps.CC.Ensure(ctx, r.deps.ExternalID, display, wt, env); err != nil {
```

(Keep the rest of `run` from `// Could not even create the session.` onward unchanged, EXCEPT the nudge/watchdog now use `wt` — see Steps 5-6.)

Add the import `"github.com/phillipgreenii/pr-pool/internal/worktree"` to `ccpool.go`.

NOTE: `BEADS_DIR`/`.beads` still points at `RepoRoot` so the worker shares the one bead database (worktrees share `.git` but the beads dolt store is repo-rooted); only the working tree is isolated. This is correct — the worker must read/write the SAME bead store, just on its own branch.

- [ ] **Step 5: Thread the worktree into the rendered nudge**

In `internal/executor/ccpool.go`, `run` calls `r.renderNudge(cc, d)` (line 60). Change `renderNudge` to take the worktree path so `{{.WorktreeDir}}` resolves to the per-bead worktree, not the pool-wide default. Update the call site (line 60):

```go
	nudge := r.renderNudge(cc, d, wt)
```

And `renderNudge` (lines 110-132):

```go
func (r *ccpoolRun) renderNudge(cc *roles.CCPoolConfig, d discover.DispatchContext, worktreeDir string) string {
	pctx := prompt.Context{
		Item:        d.Item,
		WorktreeDir: worktreeDir,
		SkillMD:     cc.SkillMD,
		SelfLogin:   r.deps.Cfg.SelfLogin,
		RepoRoot:    r.deps.Cfg.RepoRoot,
	}
```

(Leave the rest of `renderNudge` unchanged.)

- [ ] **Step 6: Point the watchdog's reset boundary at the per-bead worktree**

In `internal/executor/ccpool.go`, `workerWaitWithWatchdog` (lines 168-200) is called from `run` (line 76). Thread `wt` to it so the watchdog's guarded reset (`watchdog.terminal` → `safeToReset`) targets the worktree the worker actually ran in, not the pool-wide `Cfg.WorktreeDir`. Change the call (line 76):

```go
		werr = r.workerWaitWithWatchdog(ctx, d, r.deps.ExternalID, wt)
```

And the signature + the `WorktreeDir` field it sets (lines 168, 182):

```go
func (r *ccpoolRun) workerWaitWithWatchdog(ctx context.Context, d discover.DispatchContext, name, worktreeDir string) error {
	...
	wd := &watchdog.Watchdog{
		...
		RepoRoot:      r.deps.Cfg.RepoRoot,
		WorktreeDir:   worktreeDir,
		...
	}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd packages/pr-pool && go test ./internal/executor/ -v`
Expected: PASS — the new test sees `EnsuredCwd == <worktreeDir>/zr-1`; existing Dispatch tests still pass (they get a recording git via `Deps.Git` nil ⇒ `OSGit`, but those tests must now provide a fake git OR a `WorktreeDir` under a tempdir; update any existing Dispatch test that calls `run`/`Dispatch` to inject a no-op `Git` fake so it does not shell out to real git). Fix existing Dispatch tests by adding `Git: &recGit{}` (or a shared `dtest` no-op git) to their `Deps`.

- [ ] **Step 8: Commit**

```bash
git add internal/executor/executor.go internal/executor/ccpool.go internal/executor/ccpool_test.go internal/dtest/
git commit -m "feat(pr-pool): launch worker in a fresh per-bead worktree (pg2-yukh #2)"
```

---

### Task B3: Bead-explicit reminder/wrap-up messages

**Files:**
- Modify: `packages/pr-pool/internal/config/config.go:89-90` (default messages)
- Modify: `packages/pr-pool/internal/watchdog/watchdog.go:53-97` (render bead id into the message)
- Test: `packages/pr-pool/internal/watchdog/watchdog_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/watchdog/watchdog_test.go`. The reminder the worker receives MUST name the bead so a context-less model cannot guess a different one. Assert the queued reminder contains the bead id and does NOT contain the ambiguous bare phrase "this bead".

```go
func TestRun_reminderIsBeadExplicit(t *testing.T) {
	r := &fakeReader{seq: []usage.Snapshot{{OutputTokens: 700}, {OutputTokens: 730}}}
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	bd := &recBD{}
	wd := newWD(r, cc, bd, tokBudget(1000))
	// Use the bead-explicit template form (set by config in Step 3).
	wd.ReminderMsg = "You are nearing your budget for bead {{.BeadID}} — start wrapping up: record progress with bd comment {{.BeadID}}."
	_ = wd.Run(context.Background(), "s", "zr-6bq.3")
	if len(cc.sent) == 0 {
		t.Fatal("expected a queued reminder")
	}
	got := cc.sent[0] // "queue:<prompt>"
	if !strings.Contains(got, "zr-6bq.3") {
		t.Errorf("reminder must name the bead; got %q", got)
	}
	if strings.Contains(got, "this bead") {
		t.Errorf("reminder must not use the ambiguous 'this bead'; got %q", got)
	}
}
```

Add `"strings"` to the `watchdog_test.go` import block if not present.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pr-pool && go test ./internal/watchdog/ -run BeadExplicit -v`
Expected: FAIL — the watchdog queues the raw template string verbatim (`{{.BeadID}}` is sent literally, so it does not contain `zr-6bq.3`).

- [ ] **Step 3: Render the bead id into the message in the watchdog**

In `internal/watchdog/watchdog.go`, add a tiny renderer and use it where the messages are sent (lines 72, 77). Add near the top of the file (after the imports, before `Run`):

```go
// renderMsg substitutes {{.BeadID}} in a budget message. A message with no
// template action is returned unchanged. A malformed template falls back to the
// raw string (the reminder still fires; it just may carry the literal token) —
// never blocks the hard-stop path.
func renderMsg(tmpl, beadID string) string {
	t, err := template.New("budget").Parse(tmpl)
	if err != nil {
		return tmpl
	}
	var sb strings.Builder
	if err := t.Execute(&sb, struct{ BeadID string }{beadID}); err != nil {
		return tmpl
	}
	return sb.String()
}
```

Add `"strings"` and `"text/template"` to `watchdog.go`'s import block. Then in `Run`, change the two sends (lines 72 and 77):

```go
			case budget.Reminder:
				_ = w.CC.Send(ctx, sessionName, renderMsg(w.ReminderMsg, beadID), ccpool.ModeQueue)
```

```go
			case budget.Cancel:
				_ = w.CC.Cancel(ctx, sessionName)
				_ = w.CC.Send(ctx, sessionName, renderMsg(w.WrapUpMsg, beadID), ccpool.ModeQueue)
```

- [ ] **Step 4: Update the default messages to the bead-explicit template form**

In `internal/config/config.go`, `Default()` (lines 89-90):

```go
		ReminderMsg:    "You are nearing your budget for bead {{.BeadID}} — start wrapping up: record progress with bd comment {{.BeadID}}.",
		WrapUpMsg:      "Budget nearly exhausted for bead {{.BeadID}}. Stop now: commit your notes with bd comment {{.BeadID}}, then finish or hand back. Do not start new work on any other bead.",
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd packages/pr-pool && go test ./internal/watchdog/ ./internal/config/ -v`
Expected: PASS — the reminder contains `zr-6bq.3` and no "this bead". If a config test asserts the old literal `ReminderMsg`, update it to the new template form.

- [ ] **Step 6: Commit**

```bash
git add internal/watchdog/watchdog.go internal/config/config.go internal/watchdog/watchdog_test.go internal/config/config_test.go
git commit -m "feat(pr-pool): bead-explicit budget reminder/wrap-up messages (pg2-yukh #3a)"
```

---

### Task B4: Gate the reminder/wrap-up on a first model turn

**Files:**
- Modify: `packages/pr-pool/internal/watchdog/watchdog.go:24-97`
- Test: `packages/pr-pool/internal/watchdog/watchdog_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/watchdog/watchdog_test.go`. When the model has NOT started a turn (the dropped-nudge case), the watchdog MUST NOT send the reminder or the wrap-up — those messages would be the model's FIRST prompt, which is the exact incident. It may still hard-stop the budget (that path unclaims, it does not nudge the model). The first-turn signal is read from the transcript via an injected predicate.

```go
func TestRun_noReminderBeforeFirstModelTurn(t *testing.T) {
	r := &fakeReader{seq: []usage.Snapshot{{OutputTokens: 700}, {OutputTokens: 730}, {OutputTokens: 920}, {OutputTokens: 1000}}}
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	bd := &recBD{}
	wd := newWD(r, cc, bd, tokBudget(1000))
	// Model never started a turn ⇒ no first-turn signal.
	wd.FirstTurnStarted = func(string) bool { return false }
	err := wd.Run(context.Background(), "s", "zr-1")
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("hard stop must still fire, got %v", err)
	}
	// No reminder/wrap-up was QUEUED to the model (those are the harmful first prompt).
	for _, s := range cc.sent {
		t.Errorf("no model nudge may be sent before the first turn; got %q", s)
	}
}

func TestRun_reminderFiresAfterFirstModelTurn(t *testing.T) {
	r := &fakeReader{seq: []usage.Snapshot{{OutputTokens: 700}, {OutputTokens: 730}}}
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	bd := &recBD{}
	wd := newWD(r, cc, bd, tokBudget(1000))
	wd.FirstTurnStarted = func(string) bool { return true } // a turn happened
	_ = wd.Run(context.Background(), "s", "zr-1")
	if len(cc.sent) == 0 {
		t.Error("reminder must fire once the model has taken a turn")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pr-pool && go test ./internal/watchdog/ -run 'FirstModelTurn' -v`
Expected: FAIL — `Watchdog` has no `FirstTurnStarted` field; the reminder fires unconditionally.

- [ ] **Step 3: Add the gate to the watchdog**

In `internal/watchdog/watchdog.go`, add the field to `Watchdog` (after `Poll`, line 34):

```go
	// FirstTurnStarted reports whether the model has taken at least one turn in
	// the session transcript. The reminder/wrap-up NUDGES are gated on this: a
	// context-less model that never ingested its task must not be prompted, or it
	// guesses a target and mutates an unrelated bead (pg2-yukh). nil ⇒ always true
	// (standalone/tests that do not exercise the gate). The hard STOP is NOT gated
	// — it unclaims the bead, it does not nudge the model.
	FirstTurnStarted func(transcriptPath string) bool
```

Add a helper (next to `now()`/`emit()`):

```go
func (w *Watchdog) firstTurnStarted(sessionName string) bool {
	if w.FirstTurnStarted == nil {
		return true
	}
	return w.FirstTurnStarted(w.transcriptPath(context.TODO(), sessionName))
}
```

(Use the live `ctx` rather than `context.TODO()` if convenient; pass it through to `firstTurnStarted(ctx, sessionName)`.) Then gate the two nudging branches in `Run` (lines 71-79):

```go
			case budget.Reminder:
				if w.firstTurnStarted(sessionName) {
					_ = w.CC.Send(ctx, sessionName, renderMsg(w.ReminderMsg, beadID), ccpool.ModeQueue)
					w.emit("info", "reminder", "budget reminder threshold reached",
						map[string]any{"session": sessionName, "bead": beadID})
				} else {
					w.emit("warn", "reminder-suppressed", "budget reminder suppressed: no model turn yet",
						map[string]any{"session": sessionName, "bead": beadID})
				}
			case budget.Cancel:
				_ = w.CC.Cancel(ctx, sessionName)
				if w.firstTurnStarted(sessionName) {
					_ = w.CC.Send(ctx, sessionName, renderMsg(w.WrapUpMsg, beadID), ccpool.ModeQueue)
				}
				w.emit("warn", "cancel", "budget cancel threshold reached",
					map[string]any{"session": sessionName, "bead": beadID})
```

(The `Cancel` cancel-of-turn stays unconditional; only the wrap-up NUDGE is gated. The `budget.Hard` branch is unchanged — it never nudges.)

- [ ] **Step 4: Wire the real first-turn predicate in the executor**

In `internal/executor/ccpool.go`, `workerWaitWithWatchdog`, set `FirstTurnStarted` on the `Watchdog` literal (the same struct edited in Task B2 Step 6). Use the transcript module's `LastMessageActivity`:

```go
		FirstTurnStarted: func(path string) bool {
			if path == "" {
				return false
			}
			_, ok := ct.LastMessageActivity(path)
			return ok
		},
```

Add the import `ct "github.com/phillipgreenii/claude-transcript"` to `executor/ccpool.go`. (claude-transcript is already an in-repo sibling dep used elsewhere; confirm with `grep -rn claude-transcript packages/pr-pool/go.mod`. If pr-pool does NOT already depend on it, prefer threading the predicate through `Deps` from a layer that does, OR have pr-pool's `usage` reader expose a `FirstTurn` — see the blocking-unknown note. Default assumption: pr-pool's `usage.NewTranscriptReader` already parses the transcript, so add a `usage.FirstTurn(path) bool` helper there instead of importing claude-transcript directly. Pick whichever keeps pr-pool's existing dependency set; the predicate signature `func(string) bool` is unchanged either way.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd packages/pr-pool && go test ./internal/watchdog/ ./internal/executor/ -v`
Expected: PASS — no nudge before the first turn (but hard-stop still fires + unclaims); reminder fires after a turn.

- [ ] **Step 6: Commit**

```bash
git add internal/watchdog/watchdog.go internal/executor/ccpool.go internal/watchdog/watchdog_test.go
git commit -m "feat(pr-pool): gate budget nudges on a first model turn (pg2-yukh #3b)"
```

---

### Task B5: Wire `--confirm-ingest` into the worker dispatch (consume Stream A)

**Files:**
- Modify: `packages/pr-pool/internal/ccpool/cli.go:138-148` (`Send` forwards a confirm window)
- Modify: `packages/pr-pool/internal/ccpool/ccpool.go` (Runner.Send signature or a new SendConfirm)
- Modify: `packages/pr-pool/internal/executor/ccpool.go:61-69` (handle a not-ingested failure)
- Test: `packages/pr-pool/internal/ccpool/cli_test.go`, `packages/pr-pool/internal/executor/ccpool_test.go`

- [ ] **Step 1: Write the failing test (CLI forwards the flag)**

First inspect the CLIRunner test harness to mirror it:

Run: `cd packages/pr-pool && grep -n 'func Test\|fakeExec\|ccpoolPath\|argv\|recorded' internal/ccpool/cli_test.go | head`
Expected: shows how `cli_test.go` captures the argv passed to the `ccpool` binary.

Add a test asserting `Send` (for the worker's initial fire-and-forget nudge) forwards `--confirm-ingest <dur>` when a confirm window is configured. Model it on the existing `Send`/argv-capture test:

```go
func TestSend_forwardsConfirmIngest(t *testing.T) {
	// (mirror the existing argv-capturing harness in this file)
	c := newTestRunner(/* ... */)
	c.ConfirmIngest = 90 * time.Second
	_ = c.Send(context.Background(), "zr-1", "do it", ModeNoWait)
	argv := /* captured argv from the harness */
	if !containsPair(argv, "--confirm-ingest", "1m30s") {
		t.Errorf("Send must forward --confirm-ingest; argv=%v", argv)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pr-pool && go test ./internal/ccpool/ -run ConfirmIngest -v`
Expected: FAIL — `CLIRunner` has no `ConfirmIngest` field; `Send` does not forward the flag.

- [ ] **Step 3: Forward the flag from `CLIRunner.Send`**

In `internal/ccpool/cli.go`, add a `ConfirmIngest time.Duration` field to `CLIRunner` (next to `PermissionMode`/`Effort`/`Model` — find them with `grep -n 'PermissionMode\|Effort\|Model' internal/ccpool/cli.go`), then in `Send` (lines 138-148) append the flag for the fire-and-forget modes:

```go
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
	// confirmation (the model is already mid-turn by then).
	if c.ConfirmIngest > 0 && mode == ModeNoWait {
		args = append(args, "--confirm-ingest", c.ConfirmIngest.String())
	}
	_, err := c.ccpool(ctx, quickCallTimeout, args...)
	return err
}
```

Add `"time"` to `cli.go` imports if not present.

- [ ] **Step 4: Set `ConfirmIngest` where the CLIRunner is constructed**

Find where pr-pool builds the `*ccpool.CLIRunner` and set `ConfirmIngest` from config (default: a bounded window like 90s, well under the 25m budget so a dropped nudge is caught early):

Run: `cd packages/pr-pool && grep -rn 'CLIRunner{' --include='*.go' | grep -v _test`
Expected: the orchestrator/main construction site. Set `ConfirmIngest: cfg.ConfirmIngest` (add a `ConfirmIngest time.Duration` config scalar in `config.go` `Default()` = `90 * time.Second`, with a `PR_POOL_CONFIRM_INGEST` env overlay mirroring the other `envSecs` scalars). Wire it through identically to `Effort`/`Model`.

- [ ] **Step 5: Handle exit-code-7 (not ingested) in the executor as a fail-fast**

The worker's initial `Send` is at `executor/ccpool.go:61`. A not-ingested delivery surfaces as a non-nil error from `CC.Send` (the CLIRunner returns the exit-coded error). Currently the send-fail branch (lines 62-69) applies `OnDispatchFail` (worker = `DispatchLeave` ⇒ no verb, bead stays claimed-or-as-is). For a CONFIRMED dropped nudge we want the bead handed BACK cleanly (unclaimed), not left mid-flight. Detect exit code 7 and unclaim:

```go
	if err := r.deps.CC.Send(ctx, r.deps.ExternalID, nudge, ccpool.ModeNoWait); err != nil {
		var res report.Result
		// A confirmed dropped nudge (exit 7): the model never ingested the task, so
		// hand the bead back unclaimed regardless of on_dispatch_fail — leaving it
		// claimed would let the budget watchdog later nudge a context-less model
		// (the pg2-yukh incident). The session never did anything, so no other bead
		// can have been touched.
		if ccpool.IsNotIngested(err) {
			_ = beads.Unclaim(ctx, r.deps.BD, d.Item.ID)
			res = failureAction(report.Unclaimed, d.Item.ID)
			return res, fmt.Errorf("send %s: prompt not ingested: %w", r.deps.ExternalID, err)
		}
		if cc.OnDispatchFail == roles.DispatchUnclaim {
			_ = beads.Unclaim(ctx, r.deps.BD, d.Item.ID)
			res = failureAction(report.Unclaimed, d.Item.ID)
		}
		return res, fmt.Errorf("send %s: %w", r.deps.ExternalID, err)
	}
```

Add `ccpool.IsNotIngested(err)` to `internal/ccpool/`: it inspects the exit-coded error for code 7 (mirror the `exitCoder`/code-6 pattern already in `cli.go`'s `Cancel`). Add to `ccpool.go` (next to `ErrCancelUnconfirmed`):

```go
// ErrPromptNotIngested mirrors ccpool's exit code 7: a fire-and-forget delivery
// whose model never started a turn within the confirm window (a dropped nudge).
var ErrPromptNotIngested = errors.New("ccpool: prompt not ingested")

// IsNotIngested reports whether err is (or wraps) a ccpool exit-code-7 outcome.
func IsNotIngested(err error) bool {
	if errors.Is(err, ErrPromptNotIngested) {
		return true
	}
	var ec exitCoder
	return errors.As(err, &ec) && ec.ExitCode() == 7
}
```

And in `cli.go`'s `Send`, wrap a code-7 error as `ErrPromptNotIngested` (mirror `Cancel`'s code-6 wrap) so `errors.Is` works through the call chain.

- [ ] **Step 6: Write the executor not-ingested test**

Add to `internal/executor/ccpool_test.go`: a `dtest.FakeCC` whose `Send` returns `ccpool.ErrPromptNotIngested`; assert `Dispatch`/`run` returns an `Unclaimed` action and the bead was unclaimed, with NO other bead command issued. (This is the unit half of AC#4; the full regression is Task B6.)

```go
func TestDispatch_droppedNudge_handsBackNoOtherBeadTouched(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	cc := &dtest.FakeCC{SendErr: ccpool.ErrPromptNotIngested} // add SendErr to FakeCC
	bd := dtest.NewScriptBD( /* expect only: update zr-1 --status=open --assignee="" */ )
	rg := &recGit{}
	r := &ccpoolRun{deps: Deps{CC: cc, BD: bd, Cfg: cfg, ExternalID: "zr-1", Git: rg,
		Now: (&dtest.ManualClock{T: time.Unix(0, 0)}).Now}}
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-1"}}
	res, err := r.run(context.Background(), d)
	if err == nil {
		t.Fatal("dropped nudge must return an error")
	}
	if !hasVerb(res, report.Unclaimed, "zr-1") {
		t.Errorf("dropped nudge must hand the bead back unclaimed; res=%+v", res)
	}
	// The ONLY bead mutation may be on zr-1 (unclaim). No comment/update on any other id.
	for _, c := range bd.Calls() {
		if strings.Contains(c, "comment") || (strings.Contains(c, "update") && !strings.Contains(c, "zr-1")) {
			t.Errorf("no other bead may be touched on a dropped nudge; got %q", c)
		}
	}
}
```

(Extend `dtest.FakeCC` with a `SendErr error` returned from its `Send`; add `hasVerb`/`bd.Calls()` helpers if not present — mirror existing executor-test assertions.)

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd packages/pr-pool && go test ./internal/ccpool/ ./internal/executor/ -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/ccpool/cli.go internal/ccpool/ccpool.go internal/executor/ccpool.go internal/config/config.go internal/ccpool/cli_test.go internal/executor/ccpool_test.go internal/dtest/
git commit -m "feat(pr-pool): fail-fast hand-back on a confirmed dropped worker nudge (pg2-yukh #1)"
```

---

### Task B6: Deterministic dropped-prompt regression (AC#4) + manual-repro note

**Files:**
- Test: `packages/pr-pool/internal/executor/ccpool_test.go` (an end-to-end-ish dispatch regression)
- Doc: append a "MANUAL repro" note to this plan (already below in §"Manual / live verification")

- [ ] **Step 1: Write the regression test (the incident, deterministically)**

This is the hermetic stand-in for the live lost-prompt repro. It reproduces the incident shape: a worker dispatched for `zr-6bq.3` whose initial nudge is dropped (zero model turns) must end with the bead handed back and NO write to any other bead (the incident wrote to `zr-o8el2`). It composes the pieces from B2/B4/B5: a fake CC that (a) creates the session, (b) returns `ErrPromptNotIngested` on the initial `Send`, and a script BD pre-loaded with multiple in-progress beads (so a context-less guess WOULD have a tempting target). Assert: exactly one bead command, an unclaim of `zr-6bq.3`; zero commands referencing any other id.

```go
func TestRegression_droppedNudge_noWriteToOtherBead_pg2yukh(t *testing.T) {
	cfg := fastCfg()
	cfg.WorktreeDir = t.TempDir()
	// The incident's tempting targets — present in the store but must stay untouched.
	bd := dtest.NewScriptBD(
		dtest.Reply("list --status in_progress", "zr-o8el2\nzr-n6uo\nzr-meaz\n"),
		// the ONLY mutation we permit:
		dtest.Reply(`update zr-6bq.3 --status=open --assignee=`, ""),
	)
	cc := &dtest.FakeCC{SendErr: ccpool.ErrPromptNotIngested}
	r := &ccpoolRun{deps: Deps{CC: cc, BD: bd, Cfg: cfg, ExternalID: "zr-6bq.3", Git: &recGit{},
		Now: (&dtest.ManualClock{T: time.Unix(0, 0)}).Now}}
	d := discover.DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-6bq.3"}}
	res, err := r.run(context.Background(), d)
	if err == nil {
		t.Fatal("dropped nudge must fail the dispatch")
	}
	if !hasVerb(res, report.Unclaimed, "zr-6bq.3") {
		t.Errorf("bead must be handed back; res=%+v", res)
	}
	for _, c := range bd.Calls() {
		// No comment to ANY bead, and no update to any id other than zr-6bq.3.
		if strings.Contains(c, "comment") {
			t.Errorf("dropped nudge must write NO comment to any bead; got %q", c)
		}
		for _, other := range []string{"zr-o8el2", "zr-n6uo", "zr-meaz"} {
			if strings.Contains(c, other) {
				t.Errorf("must not touch unrelated bead %s; got %q", other, c)
			}
		}
	}
}
```

(Use whatever `dtest.ScriptBD` constructor/recording API actually exists — `grep -n 'func.*ScriptBD\|func NewScriptBD\|Reply\|Calls' internal/dtest/*.go` — and adapt the `Reply`/`Calls` calls to the real names. The ASSERTIONS are the load-bearing part: handed back + zero writes to any other bead.)

- [ ] **Step 2: Run the regression to verify it passes**

Run: `cd packages/pr-pool && go test ./internal/executor/ -run Regression_droppedNudge -v`
Expected: PASS — the dispatch hands `zr-6bq.3` back unclaimed and issues no command touching another bead.

- [ ] **Step 3: Commit**

```bash
git add internal/executor/ccpool_test.go internal/dtest/
git commit -m "test(pr-pool): regression — dropped nudge hands bead back, no write to other beads (pg2-yukh AC#4)"
```

---

## Manual / live verification (AC#4 non-hermetic path)

The deterministic regression (Task B6) substitutes for the live repro. The LIVE path cannot be exercised by an agent (it needs a real `claude` TUI and a real tmux paste race). Record this runbook for the operator:

> **MANUAL repro (operator, real monorepo):**
> 1. `pr-pool drain` (or a single worker dispatch) against a repo with several in-progress beads.
> 2. To force the dropped-prompt condition, launch the worker's `claude` with a deliberate startup delay (e.g. a wrapper that `sleep`s before the TUI is ready) so the paste lands before the input box exists.
> 3. Expected with the fix: ccpool `reply --confirm-ingest` returns exit 7 within the confirm window (~90s); pr-pool unclaims the assigned bead; the budget watchdog never nudges (no first turn); NO other bead is mutated. Verify with `bd show <assigned-bead>` (status open, unassigned) and `bd comments <each-other-in-progress-bead>` (no new comment).

---

### Task R: Repo-wide checks + bead close

- [ ] **Step 1: Both modules green**

Run:
```bash
cd packages/ccpool && go test ./... && go vet ./...
cd ../pr-pool   && go test ./... && go vet ./...
```
Expected: all PASS.

- [ ] **Step 2: Repo checks required before "complete" (per agent-support CLAUDE.md)**

Run (from repo root `phillipgreenii-nix-agent-support`):
```bash
prek run --all-files || pre-commit run --all-files
nix flake check
```
Expected: both PASS. (No new deps ⇒ no `gomod2nix.toml` change for either package; confirm `git status` shows no untracked `gomod2nix.toml` diff.)

- [ ] **Step 3: Close the bead**

```bash
bd update pg2-yukh --claim    # if not already claimed
bd comment pg2-yukh "Fixed all three root causes (one bead, two PR streams off branch pr-pool-lost-nudge). ccpool: SendWithConfirm post-delivery ingestion guard + 'ccpool reply --confirm-ingest' flag → distinct exit code 7 / ErrPromptNotIngested (Transcript.FirstMessageActivity via claude-transcript.LastMessageActivity). pr-pool: fresh per-bead worktree at dispatch (worktree.Ensure, launched there instead of RepoRoot); budget reminder/wrap-up now bead-explicit ({{.BeadID}}) AND gated on a first model turn; worker dispatch forwards --confirm-ingest and hands the bead back unclaimed on a confirmed dropped nudge. Deterministic regression proves a dropped nudge hands the bead back with NO write to any other bead (the zr-o8el2 incident shape). Live repro documented as a manual runbook (AC#4 non-hermetic)."
bd close pg2-yukh
```

---

## Self-review checklist (run while writing)

**1. Spec coverage (the 4 ACs + 3 root causes):**
- AC#1 (detect a worker that never ingested its nudge, fail fast) → Tasks A1–A3 (ccpool guard + exit 7), B5 (pr-pool consumes it, unclaims). ✓
- AC#2 (reminder never the first prompt + bead-explicit) → B3 (bead-explicit) + B4 (gate on first turn). ✓
- AC#3 (fresh per-bead worktree) → B1 + B2. ✓
- AC#4 (regression: dropped prompt ⇒ bead handed back, no other-bead writes) → B6 (deterministic) + manual runbook. ✓
- Root cause: ingestion (send.go) → A2. Worktree (executor/ccpool.go:44) → B2. Reminder (watchdog.go:72,77 / config.go:57) → B3+B4. ✓

**2. Placeholder scan:** Each code step shows the actual edit. The two places that say "mirror the existing harness" (B5 Step 1 CLI test, B6 ScriptBD API) are because the exact `dtest`/`cli_test` recording API must be read at execution time — flagged with the precise `grep` to run and the load-bearing assertion spelled out. No "TODO/handle errors/add validation" placeholders.

**3. Type consistency:** `ErrPromptNotIngested` (ccpool session sentinel) ↔ exit code 7 ↔ `ccpool.ErrPromptNotIngested`/`IsNotIngested` (pr-pool side). `Transcript.FirstMessageActivity(path) (time.Time, bool)` ↔ `transcriptAdapter.FirstMessageActivity` → `ct.LastMessageActivity` (same signature). `SendWithConfirm(ctx, id, prompt, mode, window)` used identically in `reply.go`. `worktree.Ensure(ctx, git, worktreeDir, repoRoot, beadID) (string, error)` — same arg order at the executor call site. `Watchdog.FirstTurnStarted func(string) bool` ↔ the executor's `ct.LastMessageActivity` predicate. `Deps.Git watchdog.GitRunner` reused for `worktree.Ensure` (structurally identical `Run`).

**Note for the executor (B2/B5):** existing `TestDispatch_*` / `TestWaitDone_*` tests now run `run()` which calls `worktree.Ensure` and shells to git unless `Deps.Git` is a fake — each such test MUST get a no-op recording git and a tempdir `Cfg.WorktreeDir`. This is called out in B2 Step 7; do not skip it or the existing suite shells out to real git.
