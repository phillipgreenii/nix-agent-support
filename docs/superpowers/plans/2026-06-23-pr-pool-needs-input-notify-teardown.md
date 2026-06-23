# pr-pool needs_input alert + teardown survival — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Bead:** `pg2-th35` (P2, labels `pr-pool`, `reliability`).

**Goal:** When a dispatched ccpool session enters `needs_input`, pr-pool alerts the operator exactly once (distinct eventlog record + log line naming the session and the `ccpool attach` command), and the end-of-pass teardown preserves `needs_input` sessions so a human can still attach.

**Architecture:** Two surgical changes, no new packages, leaning on ccpool's existing desktop notifier (we do NOT rebuild notification mechanics).
1. **Edge-fire-once alert (executor):** `waitDone` already polls per-`PollInterval` and calls `active()` to read session state. `active()` only returns a bool, so it cannot see the *transition* into `needs_input`. We add a tiny `sessionState(ctx, externalID)` lookup that returns the session's `ccpool.SessionState` (and whether it was present), and a local `alerted bool` latch in `waitDone`: on the FIRST poll where the state is `StateNeedsInput`, emit a distinct eventlog record (`kind:"needs_input"`) plus an `slog.Warn` naming the external_id and the `ccpool attach <id>` command, then set the latch so later polls stay silent. **Non-terminal semantics are unchanged**: the loop keeps polling to `MaxWait` exactly as today (`active()` still treats `needs_input` as active).
2. **Teardown survival (orchestrator):** `teardownAll()` closes every `<SessionPrefix>*` session unconditionally. We make it inspect each session's `State` first and **skip** (preserve, leave alive) any session in `StateNeedsInput`, logging a distinct "preserved" line. Strays and all other states are still reaped exactly as today. (Per the AC this is the recommended skip+preserve option; TTL is intentionally out of scope — see "Decisions" below.)

**Tech Stack:** Go (stdlib `log/slog`, `testing`). Repo: `phillipgreenii-nix-agent-support` (package `packages/pr-pool`). Existing seams reused: `ccpool.SessionState` (`internal/ccpool/ccpool.go:13-22`), `eventlog.Writer.Emit` (`internal/eventlog/eventlog.go:50`), the `dtest.FakeCC` fake (`internal/dtest/dtest.go:44-86`), `ManualClock` (`internal/dtest/dtest.go:180-203`).

**Operator-attach fact (verified 2026-06-23):** a human attaches to a paused session with `ccpool attach <external_id>` (`packages/ccpool/cmd/ccpool/attach.go:33` shells `tmux -L <socket> attach -t <TmuxName(prefix, externalID)>`; ccpool's own `reply` path prints the identical hint at `packages/ccpool/cmd/ccpool/reply.go:93`). pr-pool's `externalID` IS the value the operator passes to `ccpool attach`, so the alert names the external_id and that exact command.

**Branch:** `pr-pool-needs-input-notify` (off `main`).

> **NOTE on bead line numbers:** the bead text cites `teardownAll()` at `orchestrator.go:297-313` and `active()` at `orchestrator.go:284-295`. Those are STALE. In the live source `teardownAll` is `internal/orchestrator/orchestrator.go:262-279` and `active()` is `internal/executor/ccpool.go:291-302`. This plan uses the live locations.

---

### Task 0: Branch off main

- [ ] **Step 1: Create the working branch**

Run (from repo root `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`):
```bash
git switch main && git pull --ff-only
git switch -c pr-pool-needs-input-notify
```
Expected: now on a fresh `pr-pool-needs-input-notify` branch off the latest `main`.

---

### Task 1: `sessionState` lookup helper (executor)

`active()` collapses session state to a bool, so the edge-alert logic can't reuse it to see the `needs_input` transition. Add a sibling helper that returns the raw state. We do NOT change `active()`'s behavior.

**Files:**
- Modify: `packages/pr-pool/internal/executor/ccpool.go` (add helper after `active()`, currently ending at line 302)
- Test: `packages/pr-pool/internal/executor/ccpool_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/executor/ccpool_test.go` (mirrors the existing `TestActive_stateMapping` table at `ccpool_test.go:326`):

```go
func TestSessionState_lookup(t *testing.T) {
	cases := []struct {
		name      string
		sess      []ccpool.Session
		wantState ccpool.SessionState
		wantOK    bool
	}{
		{"present-needs-input", []ccpool.Session{{ExternalID: "s", Live: true, State: ccpool.StateNeedsInput}}, ccpool.StateNeedsInput, true},
		{"present-working", []ccpool.Session{{ExternalID: "s", Live: true, State: ccpool.StateWorking}}, ccpool.StateWorking, true},
		{"absent", []ccpool.Session{{ExternalID: "other", Live: true, State: ccpool.StateWorking}}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newExec(&dtest.FakeCC{ListSeq: [][]ccpool.Session{tc.sess}}, &dtest.ScriptBD{}, fastCfg())
			gotState, gotOK := e.sessionState(context.Background(), "s")
			if gotState != tc.wantState || gotOK != tc.wantOK {
				t.Errorf("sessionState(%s) = (%q, %v), want (%q, %v)", tc.name, gotState, gotOK, tc.wantState, tc.wantOK)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pr-pool && go test ./internal/executor/ -run SessionState -v`
Expected: FAIL — `e.sessionState` undefined (compile error).

- [ ] **Step 3: Add the helper**

In `internal/executor/ccpool.go`, immediately AFTER the `active()` method (after its closing brace at line 302), add:

```go
// sessionState returns the current ccpool state of the session addressed by
// externalID and whether it was present in the list. Unlike active() (which
// collapses state to a keep-waiting bool), this preserves the raw state so the
// caller can detect the EDGE into needs_input. A list error returns ("", false)
// — can't tell ⇒ no edge fires this poll (the next poll retries).
func (r *ccpoolRun) sessionState(ctx context.Context, externalID string) (ccpool.SessionState, bool) {
	sessions, err := r.deps.CC.List(ctx)
	if err != nil {
		return "", false
	}
	for _, s := range sessions {
		if s.ExternalID == externalID {
			return s.State, true
		}
	}
	return "", false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/pr-pool && go test ./internal/executor/ -run SessionState -v`
Expected: PASS (all three subcases).

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/executor/ccpool.go packages/pr-pool/internal/executor/ccpool_test.go
git commit -m "feat(pr-pool): add sessionState lookup helper (raw state, for edge detection)"
```

---

### Task 2: Edge-fire-once needs_input alert in `waitDone`

`waitDone` (`internal/executor/ccpool.go:213-275`) is the poll loop. We add an `alerted` latch and, at the top of each iteration (after the `ctx.Err()` guard, before reading bead status), check the raw session state and fire the alert exactly once on the edge into `needs_input`. The alert is a distinct eventlog record + `slog.Warn`. **We touch nothing that decides terminal outcome**: `active()`, the deadline, `DoneSignal`, and the watchdog race are all unchanged, so `needs_input` stays non-terminal and the loop still runs to `MaxWait`.

**Files:**
- Modify: `packages/pr-pool/internal/executor/ccpool.go` (the `waitDone` loop, `:223-274`)
- Test: `packages/pr-pool/internal/executor/ccpool_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/executor/ccpool_test.go`. These need an eventlog `Writer` wired onto `deps.Log` and a clock; build them inline (the existing `newExec` does not set `Log`).

Add this import to the test file's import block if not present: `"github.com/phillipgreenii/pr-pool/internal/eventlog"` and `"bufio"`, `"encoding/json"`, `"os"`, `"path/filepath"`, `"strings"`.

```go
// readEventLog returns the parsed JSONL records written to path.
func readEventLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil // no file ⇒ no records emitted
	}
	defer func() { _ = f.Close() }()
	var recs []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("invalid JSONL line %q: %v", sc.Text(), err)
		}
		recs = append(recs, m)
	}
	return recs
}

// countKind returns how many records carry kind == k.
func countKind(recs []map[string]any, k string) int {
	n := 0
	for _, r := range recs {
		if r["kind"] == k {
			n++
		}
	}
	return n
}

// newExecWithLog is newExec plus an eventlog.Writer at logPath on deps.Log.
func newExecWithLog(cc *dtest.FakeCC, bd *dtest.ScriptBD, cfg config.Config, logPath string) *ccpoolRun {
	clk := &dtest.ManualClock{T: time.Unix(0, 0)}
	lw, err := eventlog.New(logPath)
	if err != nil {
		panic(err)
	}
	lw.Now = clk.Now
	return &ccpoolRun{deps: Deps{
		CC: cc, BD: bd, Cfg: cfg, Log: lw,
		Now: clk.Now, Tick: clk.TickAdvancing(),
	}}
}

// A session that sits in needs_input until MaxWait must emit EXACTLY ONE
// needs_input alert (edge-fire-once), naming the external_id, and must still
// run to MaxWait then time out (non-terminal semantics unchanged).
func TestWaitDone_needsInput_alertsOnceOnEdge(t *testing.T) {
	cfg := fastCfg()
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateNeedsInput}}}}
	e := newExecWithLog(cc, bd, cfg, logPath)
	d := discover.DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	if err := e.waitDone(context.Background(), nil, d, "pr-pool-feedback-zr-c"); err == nil {
		t.Fatal("needs_input that never resolves must still time out (non-terminal)")
	}
	if cc.ListIdx < 10 {
		t.Errorf("needs_input must keep polling to MaxWait; listIdx=%d", cc.ListIdx)
	}
	recs := readEventLog(t, logPath)
	if n := countKind(recs, "needs_input"); n != 1 {
		t.Fatalf("needs_input alert must fire exactly once on the edge; got %d records: %v", n, recs)
	}
	var alert map[string]any
	for _, r := range recs {
		if r["kind"] == "needs_input" {
			alert = r
		}
	}
	if got, _ := alert["session"].(string); got != "pr-pool-feedback-zr-c" {
		t.Errorf("alert must name the external_id session; session=%q rec=%v", got, alert)
	}
	if lvl, _ := alert["level"].(string); lvl != "warn" {
		t.Errorf("needs_input alert level = %q, want warn", lvl)
	}
}

// A session that is working first, THEN goes needs_input, THEN resolves to a
// closed bead must alert once (only on the working→needs_input edge) and end
// successfully.
func TestWaitDone_needsInput_edgeNotEveryPoll(t *testing.T) {
	cfg := fastCfg()
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	// status stays in_progress, then closed on the last read.
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "in_progress", "in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateWorking}},
		{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateNeedsInput}},
		{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateNeedsInput}},
		{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateWorking}},
	}}
	e := newExecWithLog(cc, bd, cfg, logPath)
	d := discover.DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	_ = e.waitDone(context.Background(), nil, d, "pr-pool-feedback-zr-c")
	recs := readEventLog(t, logPath)
	if n := countKind(recs, "needs_input"); n != 1 {
		t.Fatalf("alert must fire once across two consecutive needs_input polls, got %d: %v", n, recs)
	}
}

// A session that NEVER reaches needs_input must emit NO needs_input alert.
func TestWaitDone_noNeedsInput_noAlert(t *testing.T) {
	cfg := fastCfg()
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: "pr-pool-feedback-zr-c", Live: true, State: ccpool.StateWorking}}}}
	e := newExecWithLog(cc, bd, cfg, logPath)
	d := discover.DispatchContext{Role: feedbackRole(cfg), Item: item.Item{ID: "zr-c"}}
	if err := e.waitDone(context.Background(), nil, d, "pr-pool-feedback-zr-c"); err != nil {
		t.Fatalf("working→closed should succeed, got %v", err)
	}
	if n := countKind(readEventLog(t, logPath), "needs_input"); n != 0 {
		t.Errorf("no needs_input ⇒ no alert; got %d", n)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pr-pool && go test ./internal/executor/ -run NeedsInput -v`
Expected: FAIL — `TestWaitDone_needsInput_alertsOnceOnEdge` and `TestWaitDone_needsInput_edgeNotEveryPoll` fail because zero `needs_input` records are emitted (alert not yet implemented). `TestWaitDone_noNeedsInput_noAlert` passes already (it is a guard).

- [ ] **Step 3: Add the edge-alert helper + latch to `waitDone`**

First add the alert emitter. In `internal/executor/ccpool.go`, add this method right after the new `sessionState` helper from Task 1:

```go
// alertNeedsInput fires the one-shot operator alert when a dispatched session
// enters needs_input: a distinct eventlog record (kind "needs_input") plus a
// warn log line that NAMES the session and the attach command. It does NOT
// change completion semantics — needs_input stays non-terminal and waitDone
// keeps polling to MaxWait. ccpool's own desktop notifier still fires
// independently (internal/notify, On=[needs_input,failed]); this surfaces the
// same event into pr-pool's log/eventlog so an operator watching pr-pool sees
// which session to attach to. (pg2-th35)
func (r *ccpoolRun) alertNeedsInput(externalID, beadID string) {
	slog.Warn("session needs input — attach to continue",
		"session", externalID, "bead", beadID, "attach", "ccpool attach "+externalID)
	if r.deps.Log != nil {
		_ = r.deps.Log.Emit("warn", "needs_input",
			"session needs input; operator must attach",
			map[string]any{"session": externalID, "bead": beadID, "attach": "ccpool attach " + externalID})
	}
}
```

`slog` is already imported in `internal/executor/ccpool.go` (line 7). No new imports needed.

Now wire the latch into the loop. In `waitDone` (`internal/executor/ccpool.go:213-275`), add a latch declaration alongside `seenClaimed` (currently line 216):

```go
	seenClaimed := false
	alertedNeedsInput := false // edge latch: fire the needs_input alert at most once
```

Then, INSIDE the `for {` loop, right after the existing `ctx.Err()` guard block (the `if ctx.Err() != nil { return ctx.Err() }` ending at line 229) and BEFORE the `status, _ := beads.Status(...)` line (currently line 231), insert:

```go
		// Edge-detect needs_input and alert the operator exactly once. Pure
		// observation: it does not affect the terminal decision below (active()
		// still treats needs_input as keep-waiting, bounded by MaxWait).
		if !alertedNeedsInput {
			if st, ok := r.sessionState(ctx, name); ok && st == ccpool.StateNeedsInput {
				r.alertNeedsInput(name, d.Item.ID)
				alertedNeedsInput = true
			}
		}
```

`ccpool` is already imported (line 13). Note: this adds one `List` call per poll *until* the latch trips; after it trips, the extra call stops (the `if !alertedNeedsInput` guard short-circuits). `active()`'s own `List` call later in the loop is unchanged. The `cc.ListIdx` assertions in the existing fast-stop tests (`TestWaitDone_workerDoneStopsFast_failure` expects `ListIdx == 1`) are NOT affected because those sessions are `idle`/`errored`, never `needs_input`, so the pre-status `sessionState` call is the FIRST List call and `active()` is the second — see Step 5 note.

- [ ] **Step 4: Run the needs_input tests to verify they pass**

Run: `cd packages/pr-pool && go test ./internal/executor/ -run NeedsInput -v`
Expected: PASS (all three).

- [ ] **Step 5: Fix the now-stale `ListIdx` assertions in fast-stop tests**

The new pre-status `sessionState` call adds one `List` per poll, so the two fast-stop tests that assert `cc.ListIdx == 1` now see `2` (one `sessionState` + one `active`). Update both assertions to `== 2`.

In `internal/executor/ccpool_test.go`, `TestWaitDone_workerDoneStopsFast_failure` (currently `ccpool_test.go:269`):

```go
	// sessionState (edge check) + active() each call List once on the single
	// stopping poll, so 2 List calls proves the loop stopped immediately.
	if cc.ListIdx != 2 {
		t.Errorf("done must stop on first poll (listIdx=2: sessionState+active), got %d (looped to MaxWait?)", cc.ListIdx)
	}
```

In `TestWaitDone_feedbackDoneStopsFast_unclaims` (currently `ccpool_test.go:286`):

```go
	if cc.ListIdx != 2 {
		t.Errorf("done must stop on first poll (listIdx=2), got %d", cc.ListIdx)
	}
```

Leave `TestWaitDone_needsInputWaitsUntilMaxWait` (`ccpool_test.go:309`) and `TestWaitDone_doneStopsFast_successRace` (`:293`) as-is: the former uses `cc.ListIdx < 10` (a floor — still true) and the latter has no `ListIdx` assertion.

- [ ] **Step 6: Run the FULL executor package to confirm no regressions**

Run: `cd packages/pr-pool && go test ./internal/executor/ -v`
Expected: PASS — all existing tests (incl. `TestWaitDone_needsInputWaitsUntilMaxWait`, the watchdog-race tests, and the two updated `ListIdx==2` tests) plus the three new alert tests.

- [ ] **Step 7: Commit**

```bash
git add packages/pr-pool/internal/executor/ccpool.go packages/pr-pool/internal/executor/ccpool_test.go
git commit -m "feat(pr-pool): edge-fire-once operator alert on needs_input (eventlog + log)"
```

---

### Task 3: Teardown preserves needs_input sessions (orchestrator)

`teardownAll()` (`internal/orchestrator/orchestrator.go:262-279`) closes every `<SessionPrefix>*` session unconditionally. We make it SKIP (preserve, leave alive) sessions whose `State == ccpool.StateNeedsInput`, so an operator can still attach after the pass. Everything else (strays, idle, errored, working) is still reaped exactly as today; the returned closed-count still counts only the sessions actually closed.

**Files:**
- Modify: `packages/pr-pool/internal/orchestrator/orchestrator.go:259-279` (the `teardownAll` doc comment + body)
- Test: `packages/pr-pool/internal/orchestrator/orchestrator_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/orchestrator/orchestrator_test.go` (mirrors `TestTeardownAll_purges` at `orchestrator_test.go:312` and `TestTeardownAll_returnsClosedCount` at `:469`):

```go
// TestTeardownAll_preservesNeedsInput: a pr-pool session in needs_input is left
// alive (NOT closed) so the operator can still attach after the pass; other
// pr-pool sessions are still reaped, and the returned count excludes the
// preserved one. (pg2-th35)
func TestTeardownAll_preservesNeedsInput(t *testing.T) {
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{
		{ExternalID: "pr-pool-worker-zr-need", Live: true, State: ccpool.StateNeedsInput},
		{ExternalID: "pr-pool-worker-zr-done", Live: true, State: ccpool.StateIdle},
		{ExternalID: "cc-unrelated", Live: true, State: ccpool.StateWorking},
	}}}
	o := newOrch(cc, &dtest.ScriptBD{}, fastCfg())
	n := o.teardownAll(context.Background())
	if n != 1 {
		t.Errorf("teardownAll closed count = %d, want 1 (needs_input preserved, stray excluded); closed=%v", n, cc.Closed)
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != "pr-pool-worker-zr-done" {
		t.Fatalf("teardown must close the idle pr-pool session only; closed=%v", cc.Closed)
	}
	if dtest.Contains(cc.Closed, "pr-pool-worker-zr-need") {
		t.Errorf("teardown must NOT close a needs_input session; closed=%v", cc.Closed)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pr-pool && go test ./internal/orchestrator/ -run PreservesNeedsInput -v`
Expected: FAIL — `closed count = 2` and `pr-pool-worker-zr-need` IS in `cc.Closed` (current code closes it).

- [ ] **Step 3: Add the skip to `teardownAll`**

In `internal/orchestrator/orchestrator.go`, the current loop body (`:268-276`) is:

```go
	for _, s := range sessions {
		if strings.HasPrefix(s.ExternalID, o.Cfg.SessionPrefix) {
			if err := o.CC.Close(ctx, s.ExternalID, true); err != nil {
				slog.Warn("teardown close failed", "session", s.ExternalID, "err", err)
				continue
			}
			closed++
		}
	}
```

Replace it with:

```go
	for _, s := range sessions {
		if !strings.HasPrefix(s.ExternalID, o.Cfg.SessionPrefix) {
			continue
		}
		// Preserve a needs_input session: it is paused awaiting a human who must
		// still be able to `ccpool attach <external_id>` after the pass ends.
		// Closing it here would kill the session before the operator can attach.
		// (pg2-th35; the alert that points the operator here fires in waitDone.)
		if s.State == ccpool.StateNeedsInput {
			slog.Info("teardown preserving needs_input session for operator attach",
				"session", s.ExternalID, "attach", "ccpool attach "+s.ExternalID)
			continue
		}
		if err := o.CC.Close(ctx, s.ExternalID, true); err != nil {
			slog.Warn("teardown close failed", "session", s.ExternalID, "err", err)
			continue
		}
		closed++
	}
```

Add the `ccpool` import. The current import block (`orchestrator.go:12-31`) already imports `"github.com/phillipgreenii/pr-pool/internal/ccpool"` (line 22), so NO new import is needed — verify it is present.

Also update the `teardownAll` doc comment (`orchestrator.go:259-261`) to record the preservation:

```go
// teardownAll closes every session whose name carries pr-pool's prefix — this
// pass's sessions AND strays left by a crashed prior run (the only self-healing
// behavior) — EXCEPT sessions in needs_input, which are preserved (left alive)
// so the operator can still `ccpool attach` after the pass (pg2-th35). Sessions
// outside the prefix are left untouched. Returns the number actually closed.
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd packages/pr-pool && go test ./internal/orchestrator/ -run PreservesNeedsInput -v`
Expected: PASS.

- [ ] **Step 5: Run the full orchestrator package (confirm no regression in existing teardown tests)**

Run: `cd packages/pr-pool && go test ./internal/orchestrator/ -v`
Expected: PASS — including `TestTeardownAll_purges`, `TestTeardownAll_returnsClosedCount`, `TestDrainOnce_teardownReapsStrays` (those sessions are not `needs_input`, so behavior is unchanged).

- [ ] **Step 6: Commit**

```bash
git add packages/pr-pool/internal/orchestrator/orchestrator.go packages/pr-pool/internal/orchestrator/orchestrator_test.go
git commit -m "feat(pr-pool): teardownAll preserves needs_input sessions for operator attach"
```

---

### Task 4: Document the teardown decision

The AC permits either preserve-or-document. We chose preserve; record it so a future reader knows it was deliberate (and that TTL was considered and deferred). The pr-pool package has a package doc comment at the top of `orchestrator.go`; that is the right home (no separate ADR — this is an implementation choice, not a cross-project convention).

**Files:**
- Modify: `packages/pr-pool/internal/orchestrator/orchestrator.go:1-10` (package doc)

- [ ] **Step 1: Extend the package doc comment**

In `internal/orchestrator/orchestrator.go`, the package doc currently ends (`:9`) with "...the role's typed config." Append one sentence to the existing comment block (before `package orchestrator`):

```go
// needs_input is intentionally non-terminal: the executor keeps polling such a
// session to MaxWait and alerts the operator once on the edge (executor.waitDone),
// and teardownAll preserves needs_input sessions (does not close them) so a human
// can still `ccpool attach` after the pass. A reaper TTL for preserved sessions
// was considered and deferred (pg2-th35).
```

- [ ] **Step 2: Confirm the package still builds**

Run: `cd packages/pr-pool && go build ./internal/orchestrator/`
Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add packages/pr-pool/internal/orchestrator/orchestrator.go
git commit -m "docs(pr-pool): record needs_input preserve-on-teardown decision (pg2-th35)"
```

---

### Task 5: Full verification + repo checks

- [ ] **Step 1: Full pr-pool Go test + vet**

Run: `cd packages/pr-pool && go test ./... && go vet ./...`
Expected: all PASS.

- [ ] **Step 2: Repo checks required before "complete" (per agent-support CLAUDE.md)**

Run (from repo root `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`):
```bash
prek run --all-files || pre-commit run --all-files
nix flake check
```
Expected: both PASS. (No new Go dependencies were added, so there is NO `gomod2nix.toml` change. No nix module changed.)

- [ ] **Step 3: Manual confirmation that the alert text is operator-actionable**

Run: `cd packages/pr-pool && go test ./internal/executor/ -run NeedsInput_alertsOnceOnEdge -v 2>&1 | grep -i "needs input\|attach" || true`
Expected: the test passes; the alert record carries `attach: ccpool attach pr-pool-feedback-zr-c`. (This is the exact command an operator runs — verified against `packages/ccpool/cmd/ccpool/attach.go` and `reply.go:93`.)

- [ ] **Step 4: Close the bead** (only after a human approves the PR/merge — do NOT modify beads during planning)

```bash
bd update pg2-th35 --claim
bd comment pg2-th35 "Implemented (pr-pool): edge-fire-once needs_input alert in executor.waitDone (distinct eventlog kind:needs_input + warn log naming the session and 'ccpool attach <id>'), non-terminal semantics unchanged (still polls to MaxWait); teardownAll now PRESERVES needs_input sessions (skip+leave-alive) so the operator can attach after the pass. Leaned on ccpool's existing desktop notifier (internal/notify), did not rebuild it. Tests: edge-fire-once (alertsOnceOnEdge, edgeNotEveryPoll, noNeedsInput_noAlert), teardown-skip (TestTeardownAll_preservesNeedsInput). TTL for preserved sessions considered and deferred."
bd close pg2-th35
```

---

## Self-review checklist (done while writing)

**1. Spec coverage (against bead AC):**
- AC "Investigation resolved" — already done in grooming; restated in plan Architecture. ✓
- AC "pr-pool actively alerts the operator … names the tmux session to attach to" — Task 2 emits a distinct eventlog `kind:"needs_input"` + `slog.Warn` carrying `session=<external_id>` and `attach=ccpool attach <external_id>` (the verified attach command). ✓
- AC "fires once on the edge … does not change non-terminal semantics" — Task 2's `alertedNeedsInput` latch (fire-once) + tests `alertsOnceOnEdge`/`edgeNotEveryPoll`; non-terminal proven by `cc.ListIdx >= 10` + timeout in `alertsOnceOnEdge`, and `active()`/deadline/`DoneSignal` untouched. ✓
- AC "teardown survival … preserved (skipped/left alive) OR explicitly decided and documented" — Task 3 skips `needs_input` in `teardownAll`; Task 4 documents the choice (and the deferred TTL). Both done (recommended skip path). ✓
- AC "a test covers the notification firing (and the teardown-skip)" — Task 2 (`alertsOnceOnEdge`, `edgeNotEveryPoll`, `noNeedsInput_noAlert`) + Task 3 (`preservesNeedsInput`). ✓

**2. Placeholder scan:** no TBD/TODO/"handle edge cases"; every code step shows the actual edit and exact run command + expected output.

**3. Type consistency:** `ccpool.SessionState` + `ccpool.StateNeedsInput` (`internal/ccpool/ccpool.go:13,19`) used identically in executor and orchestrator. `sessionState(ctx, externalID) (ccpool.SessionState, bool)` defined in Task 1, consumed in Task 2. `alertNeedsInput(externalID, beadID string)` defined and called with `(name, d.Item.ID)`. Eventlog `kind` string `"needs_input"` matches between emit (Task 2) and `countKind(recs, "needs_input")` (tests). `deps.Log` is `*eventlog.Writer` (`executor.go:36`), nil-guarded in `alertNeedsInput` exactly like `watchdog.emit` (`watchdog.go:53-57`). Closed-count semantics in `teardownAll` preserved (only actually-closed sessions increment `closed`).

**4. Live-line-number caveat:** plan explicitly overrides the bead's stale `297-313`/`284-295` with the live `orchestrator.go:262-279` and `executor/ccpool.go:291-302`; an executor should still re-confirm exact lines before editing (the repo may have moved).
