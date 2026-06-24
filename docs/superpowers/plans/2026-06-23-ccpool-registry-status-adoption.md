# ccpool adopts the shared registry-status reader — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Bead:** `pg2-oois.5` (P3, task, labels `ccpool`/`claude-transcript`/`pa-monitor`) — closes epic `pg2-oois` (last of 4 children; `.2/.3/.4` ✓).

**Goal:** Add the shared `claude-transcript` registry-status verdict (`ClassifyActivity`) as an additional INPUT signal to ccpool's reconciled state classifier, mapping `Active→working`, `WaitingForHuman→waiting-for-human`, `Idle→idle`, while keeping ccpool's pane-derived `thinking`/`streaming` substate (which the registry cannot supply), and exposing the verdict on the result so the `pg2-yukh` ingestion guard can reuse it.

**Architecture:** ccpool's classifier is a pure core (`Classify`/`ClassifyFrame` in `internal/state/state.go`) wrapped by an impure gatherer (`Gather`). The registry row is keyed by PID at `~/.claude/sessions/<pid>.json` and carries a `sessionId`; ccpool sessions are keyed by `store.Session.ClaudeSessionID`. So the impure `Gather` shell resolves the verdict — sweep the sessions dir (`ReadSessionRegistry`), match `reg.SessionID == row.ClaudeSessionID`, **PID-gate first** (`PidAlive`), then call the pure `ClassifyActivity`. The resulting `ActivityVerdict` (plus a "found a live row" bool) becomes a new field on `Inputs`, fed to the pure `Classify`. `Classify` consults it at a defined precedence point: it corroborates / cross-checks the existing pane + row signals (it does NOT replace them — the pane is still the only source of `thinking`/`streaming`). The verdict is mirrored onto `Result` for downstream reuse. The registry resolver is injected as a function (like `awaiting`/`lastText`) so `Gather` stays fake-driven testable and the pure core takes no I/O.

**Tech Stack:** Go 1.25 (stdlib `flag`/`testing`, table tests). Repo: `phillipgreenii-nix-agent-support/packages/ccpool`. Library: `github.com/phillipgreenii/claude-transcript` — an **in-repo sibling** already wired via `replace => ../claude-transcript` (`go.mod:40`) and gomod2nix Pattern B (`packages/ccpool/default.nix:14-27`). New symbols consumed: `ReadSessionRegistry`, `PidAlive`, `ClassifyActivity`, `RegistrySession`, `ActivityVerdict`, `Activity` (`Active`/`WaitingForHuman`/`Idle`), `LastMessageActivity`.

**Branch:** `ccpool-registry-status` (off `main`).

---

## Pre-flight context (read before Task 1; do NOT skip)

The library functions are already in `packages/claude-transcript/registry.go`:

- `func ReadSessionRegistry(sessionsDir string) ([]RegistrySession, error)` — sweeps `*.json`, skips malformed, returns NON-pid-gated rows; missing dir → `(nil, nil)`.
- `func PidAlive(pid int) bool` — `kill -0` semantics; non-positive pid is never alive.
- `func ClassifyActivity(reg RegistrySession, awaitingInput bool, lastActivity time.Time, freshWindow time.Duration) ActivityVerdict` — PURE. Precedence: fresh `waiting` OR `awaitingInput` → `WaitingForHuman`; else `busy` → `Active` (TRUSTED, never demoted on staleness); else `Idle`.
- `func LastMessageActivity(path string) (time.Time, bool)` — timestamp of last real message event; feeds the `waiting`-freshness check.
- `RegistrySession{ PID int; SessionID string; Status string; WaitingFor string; StatusUpdatedAt time.Time; ... }`.
- `ActivityVerdict{ Activity Activity; Reason string }`; `Activity` constants `Idle`/`Active`/`WaitingForHuman` (iota; `Idle == 0`).

**The PID gap (key design fact):** `store.Session` (`internal/store/store.go:36-62`) has `ClaudeSessionID` and `TranscriptPath` but **NO `PID`**. The registry file is keyed by PID. So the only join key ccpool holds is `ClaudeSessionID` → `RegistrySession.SessionID`. The resolver must sweep the dir and match on `SessionID`. After the match it gets `reg.PID` from the row and gates with `PidAlive(reg.PID)`.

**Mapping table (`claude-transcript` Activity ↔ ccpool `state.State`) — document this in code and here:**

| `claudetranscript.Activity`  | ccpool `state.State` | Notes                                                                                     |
| ---------------------------- | -------------------- | ----------------------------------------------------------------------------------------- |
| `Active`                     | `Working`            | TRUSTED once pid-gated; substate (`thinking`/`streaming`) still comes ONLY from the pane. |
| `WaitingForHuman`            | `WaitingForHuman`    | A fresh registry `waiting` flag or dangling AskUserQuestion.                              |
| `Idle`                       | `Idle`               | Turn finished, or a stale `waiting` that failed the freshness check.                      |
| (no live registry row found) | —                    | No verdict; classifier falls back to the existing pane+row precedence unchanged.          |

**Reference consumer to mirror:** `packages/pa-monitor/internal/core/poller/poller.go:210-247` — `if !PidAlive { keep last-known, never Working } else { reg := RegistrySession{...}; verdict := ClassifyActivity(...); switch verdict.Activity { Active→Working; WaitingForHuman→WaitingForHuman; default→Idle } }`. And `packages/pa-monitor/internal/core/session/discovery.go:36-91` for the sweep+match+pid-gate idiom and `DefaultSessionsDir()` (`~/.claude/sessions`).

**The go.mod / gomod2nix wiring is ALREADY DONE — verify, do not re-add:**

- `go.mod:32` already requires `github.com/phillipgreenii/claude-transcript v0.0.0` and `go.mod:40` already has `replace ... => ../claude-transcript`. (Used today by `cmd/ccpool/reply.go:15`, `retry.go`.)
- `packages/ccpool/default.nix:14-27` already roots `src` at `./..` unioning `./.` + `../claude-transcript` with `modRoot = "ccpool"` (Pattern B per ADR 0008).
- `gomod2nix.toml` tracks only third-party deps; `claude-transcript` is intentionally absent (the local replace is symlinked from source). `registry.go` imports only stdlib (`bufio`/`encoding/json`/`os`/`path/filepath`/`syscall`/`time`), so adding these calls introduces **no new third-party transitive dep** → `gomod2nix.toml` need not change. Task 6 still runs `go mod tidy` + `gomod2nix generate` to PROVE the toml is unchanged (a non-empty diff there is a BLOCKING signal that something unexpected was pulled in).

---

### Task 1: Add the registry verdict to `state.Inputs` + `state.Result` (no behavior change yet)

**Files:**

- Modify: `packages/ccpool/internal/state/state.go:69-86` (`Inputs`), `:42-59` (`Result`), imports `:13-19`
- Test: `packages/ccpool/internal/state/state_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/state/state_test.go` (top of file imports already has `time`; add the lib import). First add the import — change the import block (`state_test.go:3-9`) to:

```go
import (
	"errors"
	"testing"
	"time"

	ct "github.com/phillipgreenii/claude-transcript"
	"github.com/phillipgreenii/ccpool/internal/store"
)
```

Then add this test (asserts the new fields exist and are zero-valued by default; pure-compile guard):

```go
func TestInputsAndResult_carryRegistryVerdict(t *testing.T) {
	in := Inputs{
		Name: "a", Live: true, Row: store.Session{State: store.Working},
		RegistryFound: true,
		Registry:      ct.ActivityVerdict{Activity: ct.Active},
	}
	res := Classify(in)
	// The verdict is mirrored onto Result for downstream reuse (pg2-yukh).
	if !res.RegistryFound {
		t.Error("RegistryFound = false, want true (mirrored from Inputs)")
	}
	if res.Registry.Activity != ct.Active {
		t.Errorf("Registry.Activity = %v, want Active (mirrored from Inputs)", res.Registry.Activity)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./internal/state/ -run RegistryVerdict -v`
Expected: FAIL — compile error, `Inputs`/`Result` have no fields `RegistryFound`/`Registry`.

- [ ] **Step 3: Add the import to `state.go`**

In `internal/state/state.go`, change the import block (`:13-19`) to add the library alias:

```go
import (
	"fmt"
	"time"

	ct "github.com/phillipgreenii/claude-transcript"
	"github.com/phillipgreenii/ccpool/internal/pane"
	"github.com/phillipgreenii/ccpool/internal/store"
)
```

- [ ] **Step 4: Add the fields to `Inputs`**

In `internal/state/state.go`, inside `type Inputs struct` (after `Awaiting`, before the `Frame1..Frame3` comment, around line 77):

```go
	// RegistryFound is true when Gather located a LIVE (pid-gated) Claude
	// session-registry row for this session (matched by ClaudeSessionID). When
	// false, Registry is the zero verdict and Classify ignores it — the
	// classifier falls back to the pane+row precedence unchanged.
	RegistryFound bool
	// Registry is the shared claude-transcript activity verdict for this
	// session's registry row (Active/WaitingForHuman/Idle), already pid-gated
	// and freshness-cross-checked by Gather. Consulted by Classify only when
	// RegistryFound. It NEVER supplies the thinking/streaming substate — that
	// remains pane-derived (the registry has no such field). Mapping:
	// Active->Working, WaitingForHuman->WaitingForHuman, Idle->Idle.
	Registry ct.ActivityVerdict
```

- [ ] **Step 5: Add the fields to `Result` and mirror them in `Classify`**

In `internal/state/state.go`, inside `type Result struct` (after `Question`, around line 58):

```go
	// RegistryFound / Registry mirror the gathered registry signal onto the
	// result so downstream consumers (the pg2-yukh ingestion guard) can reuse
	// the same verdict ccpool classified with, without re-reading the registry.
	// RegistryFound is false when no live row was matched (Registry is zero).
	RegistryFound bool
	Registry      ct.ActivityVerdict
```

In `Classify`, set them on `res` immediately after `res` is constructed (`state.go:146`, right after the `res := Result{...}` line, before the `!in.Live` check):

```go
	res.RegistryFound = in.RegistryFound
	res.Registry = in.Registry
```

- [ ] **Step 6: Run test to verify it passes (and nothing else broke)**

Run: `cd packages/ccpool && go test ./internal/state/ -v`
Expected: PASS — new test passes; every existing `TestClassify`/`TestGather` case still passes (registry fields default to zero/false → ignored).

- [ ] **Step 7: Commit**

```bash
git add internal/state/state.go internal/state/state_test.go
git commit -m "feat(ccpool): carry claude-transcript registry verdict on state Inputs/Result"
```

---

### Task 2: `Classify` consults the registry verdict at a defined precedence

**Files:**

- Modify: `packages/ccpool/internal/state/state.go:134-194` (`Classify` doc + body)
- Test: `packages/ccpool/internal/state/state_test.go`

**Precedence design (document in the `Classify` doc comment):** the registry is an INPUT signal that corroborates the existing signals; it must not regress the hook-set `NeedsInput` path (PRIMARY) nor steal the pane's `thinking`/`streaming` substate. New ordering when `RegistryFound`:

1. `!Live` → not-live (unchanged; precedes everything).
2. pane in-flight → working + pane substate (unchanged; the pane is the only substate source, and a live counter is the most precise turn signal).
3. settled + row `NeedsInput` → waiting-for-human (unchanged; PRIMARY hook-set signal).
4. **NEW** settled + `RegistryFound` + `Registry.Activity == WaitingForHuman` → waiting-for-human (registry cross-check; carries the row's pending question if any). Placed here so the hook-set row still wins, but the registry catches a wait the hook missed — same role the existing `Awaiting` fallback plays, and ahead of it.
5. settled + `Awaiting` (transcript) → waiting-for-human (unchanged fallback).
6. **NEW** settled + `RegistryFound` + `Registry.Activity == Active` → working (substate `SubNone`; the pane was settled this sample so no substate, but the registry says a turn is underway — TRUSTED, mirrors pa-monitor). Placed AFTER the waiting checks (a fresh wait wins over busy, matching `ClassifyActivity`'s own internal order) and BEFORE the row-`Errored`/`Starting`/idle fallbacks.
7. settled + row `Errored` → error (unchanged).
8. settled + row `Starting` → working/thinking (unchanged).
9. **NEW** settled + `RegistryFound` + `Registry.Activity == Idle` → idle (explicit; same as the final fallback, but documents that a live registry `idle` row positively confirms idle rather than merely defaulting).
10. else → idle (unchanged).

- [ ] **Step 1: Write the failing tests**

Add to `internal/state/state_test.go` a focused table test (separate from `TestClassify` so the existing cases stay untouched):

```go
func TestClassify_registrySignal(t *testing.T) {
	const staticPane = "❯ ready for input\n  -- INSERT --"
	const counter = "✽ Envisioning… (5s · ↓ 13 tokens · thinking with xhigh effort)"
	settled := func(rowState store.State) Inputs {
		return Inputs{
			Name: "a", Live: true,
			Frame1: staticPane, Frame2: staticPane, Frame3: staticPane, NumFrames: 3,
			Row: store.Session{State: rowState},
		}
	}
	withReg := func(in Inputs, a ct.Activity) Inputs {
		in.RegistryFound = true
		in.Registry = ct.ActivityVerdict{Activity: a}
		return in
	}

	cases := []struct {
		name    string
		in      Inputs
		want    State
		wantSub SubState
	}{
		{
			// NEW: settled pane + registry says Active -> working (no substate; pane was settled).
			name:    "settled_pane_registry_active_is_working",
			in:      withReg(settled(store.Ready), ct.Active),
			want:    Working,
			wantSub: SubNone,
		},
		{
			// NEW: settled pane + registry says WaitingForHuman -> waiting-for-human.
			name: "settled_pane_registry_waiting_is_waiting_for_human",
			in:   withReg(settled(store.Ready), ct.WaitingForHuman),
			want: WaitingForHuman,
		},
		{
			// NEW: settled pane + registry says Idle -> idle.
			name: "settled_pane_registry_idle_is_idle",
			in:   withReg(settled(store.Ready), ct.Idle),
			want: Idle,
		},
		{
			// REGRESSION GUARD: the hook-set NeedsInput row stays PRIMARY even when
			// the registry says Active — waiting-for-human wins, question preserved.
			name: "needs_input_row_beats_registry_active",
			in: func() Inputs {
				in := withReg(settled(store.NeedsInput), ct.Active)
				in.Row.PendingQuestion = "Alpha or Bravo?"
				return in
			}(),
			want: WaitingForHuman,
		},
		{
			// SUBSTATE GUARD: a live pane counter still yields working/THINKING even
			// when the registry only says Active (no substate) — the pane owns substate.
			name: "pane_counter_keeps_thinking_substate_over_registry",
			in: func() Inputs {
				in := Inputs{Name: "a", Live: true, Frame1: counter, NumFrames: 1, Row: store.Session{State: store.Working}}
				return withReg(in, ct.Active)
			}(),
			want:    Working,
			wantSub: SubThinking,
		},
		{
			// FALLBACK GUARD: when no live registry row was found, behavior is the
			// pre-change pane+row precedence (settled Ready row -> idle).
			name: "no_registry_row_falls_back_to_idle",
			in:   settled(store.Ready),
			want: Idle,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.in)
			if got.State != tc.want {
				t.Errorf("State = %q, want %q", got.State, tc.want)
			}
			if got.SubState != tc.wantSub {
				t.Errorf("SubState = %q, want %q", got.SubState, tc.wantSub)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./internal/state/ -run registrySignal -v`
Expected: FAIL — `settled_pane_registry_active_is_working` reports `idle` (registry not yet consulted); `..._waiting...` reports `idle`.

- [ ] **Step 3: Update the `Classify` doc comment**

In `internal/state/state.go`, replace the precedence list in the `Classify` doc comment (`:137-144`) with:

```go
// Precedence (first match wins). The registry verdict (in.Registry, valid only
// when in.RegistryFound) is an additive cross-check: it NEVER supplies substate
// (thinking/streaming stay pane-derived) and NEVER overrides the hook-set
// NeedsInput row. Mapping: Active->working, WaitingForHuman->waiting-for-human,
// Idle->idle.
//  1. !Live                                  -> not-live (carry LastKnown = row state)
//  2. in-flight (pane)                        -> working + sub (thinking|streaming)
//  3. settled + row NeedsInput                -> waiting-for-human (hook-set; PRIMARY)
//  4. settled + registry WaitingForHuman      -> waiting-for-human (registry cross-check)
//  5. settled + Awaiting (transcript)         -> waiting-for-human (transcript FALLBACK)
//  6. settled + registry Active               -> working (substate none; busy TRUSTED)
//  7. settled + row Failed                    -> error
//  8. settled + row Starting                  -> working/thinking (launching)
//  9. settled + registry Idle                 -> idle (positive idle confirmation)
// 10. else                                    -> idle
```

- [ ] **Step 4: Implement the new branches in `Classify`**

In `internal/state/state.go`, in the settled section of `Classify`: insert the registry-waiting branch right AFTER the `NeedsInput` branch (`:169-173`) and BEFORE the `Awaiting` branch (`:176`):

```go
	// Registry cross-check (precedence 4): a pid-gated registry verdict of
	// WaitingForHuman classifies waiting-for-human even when the hook never
	// fired. Surfaces the row's pending question (may be empty). Placed after the
	// hook-set NeedsInput row so that PRIMARY signal still wins.
	if in.RegistryFound && in.Registry.Activity == ct.WaitingForHuman {
		res.State = WaitingForHuman
		res.Question = in.Row.PendingQuestion
		return res
	}
```

Then insert the registry-active branch right AFTER the `Awaiting` fallback branch (`:176-180`) and BEFORE the `Errored` branch (`:181`):

```go
	// Registry cross-check (precedence 6): the pane settled this sample but a
	// pid-gated registry verdict of Active says a turn is underway (busy is
	// TRUSTED — never demoted on transcript/pane staleness; mirrors pa-monitor).
	// No substate: the pane was settled, so thinking/streaming is unknown.
	if in.RegistryFound && in.Registry.Activity == ct.Active {
		res.State = Working
		res.SubState = SubNone
		return res
	}
```

(The `Idle` branch — precedence 9 — needs no code: a registry `Idle` verdict falls through to the existing final `res.State = Idle` at `:192-193`, which is exactly the documented behavior. The test case `settled_pane_registry_idle_is_idle` passes via that fall-through.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd packages/ccpool && go test ./internal/state/ -v`
Expected: PASS — `TestClassify_registrySignal` all green; the original `TestClassify`/`TestClassifyFrame`/`TestGather*` cases (which leave `RegistryFound=false`) all still pass.

- [ ] **Step 6: Commit**

```bash
git add internal/state/state.go internal/state/state_test.go
git commit -m "feat(ccpool): consult registry verdict in Classify (cross-check, keep pane substate)"
```

---

### Task 3: `Gather` resolves the registry verdict via an injected resolver

**Files:**

- Modify: `packages/ccpool/internal/state/state.go:196-267` (`Gather` signature + body + doc)
- Test: `packages/ccpool/internal/state/state_test.go`

**Design:** add a `registry func() (ct.ActivityVerdict, bool)` parameter to `Gather` (alongside `awaiting`/`lastText`). It returns the verdict and a "found a live row" bool. Resolve it whenever the session is live (cheap; the cmd-layer adapter does the dir sweep + pid-gate + `ClassifyActivity`). Set `in.Registry`/`in.RegistryFound` from it before calling `Classify`. The resolver pattern (a closure, error swallowed into the bool) keeps `Gather` pure-shell and fake-testable, exactly like `awaiting`/`lastText`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/state/state_test.go` these helpers and tests:

```go
// staticRegistry returns a registry resolver yielding a fixed verdict+found.
func staticRegistry(a ct.Activity, found bool) func() (ct.ActivityVerdict, bool) {
	return func() (ct.ActivityVerdict, bool) {
		return ct.ActivityVerdict{Activity: a}, found
	}
}

// noRegistry is a resolver that reports no live row (the common fallback path).
func noRegistry() (ct.ActivityVerdict, bool) { return ct.ActivityVerdict{}, false }

func TestGather_registryActiveOverSettledPane(t *testing.T) {
	// Pane is settled (sticky single frame), row is Ready, but the registry
	// reports a live busy row -> working. Verdict mirrored onto Result.
	const staticPane = "❯ ready\n  -- INSERT --"
	p := &fakePaner{live: true, panes: []string{staticPane}}
	sl := &recordingSleep{}
	row := store.Session{Name: "w", State: store.Ready}

	res, err := Gather(p, sl.Sleep, staticAwaiting(false), noText, staticRegistry(ct.Active, true), "cc-w", "w", row)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if res.State != Working || res.SubState != SubNone {
		t.Errorf("got %s/%s, want working/<none>", res.State, res.SubState)
	}
	if !res.RegistryFound || res.Registry.Activity != ct.Active {
		t.Errorf("Result registry = %v/%v, want found/Active", res.RegistryFound, res.Registry.Activity)
	}
}

func TestGather_noRegistryRowFallsBack(t *testing.T) {
	// No live registry row -> classifier uses the pane+row precedence (idle).
	const staticPane = "❯ ready\n  -- INSERT --"
	p := &fakePaner{live: true, panes: []string{staticPane}}
	sl := &recordingSleep{}
	row := store.Session{Name: "i", State: store.Ready}

	res, err := Gather(p, sl.Sleep, staticAwaiting(false), staticLastText("a reply"), noRegistry, "cc-i", "i", row)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if res.State != Idle {
		t.Errorf("State = %s, want idle (no registry row -> fallback)", res.State)
	}
	if res.RegistryFound {
		t.Error("RegistryFound = true, want false (no live row)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./internal/state/ -run 'Gather_registryActive|Gather_noRegistry' -v`
Expected: FAIL — `Gather` does not accept a `registry` argument (compile error: too many arguments). The existing `TestGather_*` calls also now fail to compile until updated in Step 4.

- [ ] **Step 3: Add the `registry` parameter to `Gather` and use it**

In `internal/state/state.go`, change the `Gather` signature (`:216`) to add the resolver after `lastText`:

```go
func Gather(p Paner, sleep func(time.Duration), awaiting func() (bool, error), lastText func() (string, error), registry func() (ct.ActivityVerdict, bool), tmuxName, name string, row store.Session) (Result, error) {
```

Update the `Gather` doc comment (`:196-215`) — add a bullet describing the resolver after the `awaiting`/`lastText` bullets:

```go
//   - registry wraps the cmd layer's registry lookup: it sweeps the Claude
//     session registry (~/.claude/sessions), matches the row by
//     ClaudeSessionID, PID-gates the match (PidAlive), and returns
//     ClassifyActivity's verdict plus a "found a live row" bool. A missing dir,
//     no match, or a dead pid yields (zero verdict, false) — Classify then
//     ignores it and uses the pane+row precedence. Resolved whenever the
//     session is live (cheap relative to the pane captures).
```

Then, in the body, after the `awaiting()` block (`:253-255`) and before `res := Classify(in)` (`:256`), resolve the registry signal:

```go
	// Resolve the registry verdict whenever the session is live. The resolver
	// swallows its own errors into found=false (a missing dir / no match / dead
	// pid all read as "no live row"), so a registry hiccup never crashes a status
	// query — Classify then ignores it and falls back to pane+row.
	in.Registry, in.RegistryFound = registry()
```

- [ ] **Step 4: Update the existing `Gather` call sites in the test file**

In `internal/state/state_test.go`, every existing `Gather(...)` call must pass the new resolver. They model "no live registry row" so they keep their pre-change behavior. Update each existing call (`TestGather_fastPathSkipsSecondCapture`, `TestGather_streamingViaThreeFrames`, `TestGather_settledIdle`, `TestGather_awaitingWaitsForHuman`, `TestGather_needsInputRowWaitsForHumanWithQuestion`, `TestGather_awaitingReadDespiteStreamingDiffOnNonWorkingRow`, `TestGather_notLiveSkipsCapture`, `TestGather_awaitingErrorTolerated`, `TestGather_lastTextPopulatedForIdleAndError`, `TestGather_lastTextErrorTolerated`) by inserting `noRegistry` as the argument immediately after the `lastText` argument. For example, in `TestGather_fastPathSkipsSecondCapture`:

```go
	res, err := Gather(p, sl.Sleep, staticAwaiting(false), noText, noRegistry, "cc-a", "a", row)
```

Apply the identical insertion (the `noRegistry` resolver right before the `tmuxName` string argument) to all ten existing `Gather(...)` call sites.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd packages/ccpool && go test ./internal/state/ -v`
Expected: PASS — new `Gather` registry tests green; all ten updated existing `Gather` tests green; pure `Classify`/`ClassifyFrame` tests untouched.

- [ ] **Step 6: Commit**

```bash
git add internal/state/state.go internal/state/state_test.go
git commit -m "feat(ccpool): inject registry resolver into state.Gather"
```

---

### Task 4: cmd-layer registry adapter + wire it into `ccpool state`

**Files:**

- Create: `packages/ccpool/cmd/ccpool/registry.go`
- Modify: `packages/ccpool/cmd/ccpool/state.go:55-74` (build + pass the resolver)
- Test: `packages/ccpool/cmd/ccpool/registry_test.go`

**Design:** a pure-ish helper `registryVerdict(sessionsDir, claudeSessionID, transcriptPath string, freshWindow time.Duration) (ct.ActivityVerdict, bool)` that: sweeps `ReadSessionRegistry(sessionsDir)`; finds the row whose `SessionID == claudeSessionID`; if none, returns `(zero, false)`; PID-gates with `PidAlive(reg.PID)` (dead → `(zero, false)`); computes `awaitingInput` via `ct.IsAwaitingInput(transcriptPath)` (error tolerated → false) and `lastActivity` via `ct.LastMessageActivity(transcriptPath)`; returns `ClassifyActivity(reg, awaitingInput, lastActivity, freshWindow)` with `found=true`. `runState` builds the closure over `DefaultSessionsDir`, `row.ClaudeSessionID`, `row.TranscriptPath`, and a fixed `freshWindow`.

**`freshWindow` value:** use `5 * time.Minute` as the ccpool default tolerance for the `waiting`-freshness cross-check, declared as a named const `registryWaitingFreshWindow` in `registry.go` with a doc comment. (Rationale: long enough that a genuine human-blocked wait stays "fresh" across a normal status-poll cadence, short enough that an abandoned wait whose transcript later advanced is demoted to idle. The library leaves this policy to the caller; this mirrors pa-monitor delegating the window to its caller.)

- [ ] **Step 1: Write the failing test**

Create `cmd/ccpool/registry_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeRegFile writes a minimal Claude session-registry JSON keyed by pid.
func writeRegFile(t *testing.T, dir string, pid int, sessionID, status string) {
	t.Helper()
	body := `{"pid":` + itoa(pid) + `,"sessionId":"` + sessionID + `","status":"` + status + `"}`
	if err := os.WriteFile(filepath.Join(dir, itoa(pid)+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestRegistryVerdict_noMatchingSessionIsNotFound(t *testing.T) {
	dir := t.TempDir()
	writeRegFile(t, dir, os.Getpid(), "OTHER-SESSION", "busy")
	_, found := registryVerdict(dir, "MY-SESSION", "", registryWaitingFreshWindow)
	if found {
		t.Error("found = true, want false (no row matches the ClaudeSessionID)")
	}
}

func TestRegistryVerdict_deadPidIsNotFound(t *testing.T) {
	dir := t.TempDir()
	// PID 1 is init; on a normal host the test process cannot signal it, but use
	// an unused high pid to be deterministic-dead.
	writeRegFile(t, dir, 2147480000, "MY-SESSION", "busy")
	_, found := registryVerdict(dir, "MY-SESSION", "", registryWaitingFreshWindow)
	if found {
		t.Error("found = true, want false (matched row's pid is dead)")
	}
}

func TestRegistryVerdict_liveBusyRowIsActive(t *testing.T) {
	dir := t.TempDir()
	// Use this test process's own pid so PidAlive is true.
	writeRegFile(t, dir, os.Getpid(), "MY-SESSION", "busy")
	v, found := registryVerdict(dir, "MY-SESSION", "", registryWaitingFreshWindow)
	if !found {
		t.Fatal("found = false, want true (live matching row)")
	}
	if v.Activity.String() != "active" {
		t.Errorf("Activity = %q, want active", v.Activity.String())
	}
}

func TestRegistryVerdict_missingDirIsNotFound(t *testing.T) {
	_, found := registryVerdict(filepath.Join(t.TempDir(), "does-not-exist"), "MY-SESSION", "", registryWaitingFreshWindow)
	if found {
		t.Error("found = true, want false (missing sessions dir)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run RegistryVerdict -v`
Expected: FAIL — `registryVerdict` and `registryWaitingFreshWindow` are undefined (compile error).

- [ ] **Step 3: Create the adapter**

Create `cmd/ccpool/registry.go`:

```go
package main

import (
	"time"

	ct "github.com/phillipgreenii/claude-transcript"
)

// registryWaitingFreshWindow is ccpool's tolerance for the registry "waiting"
// freshness cross-check (ClassifyActivity). A "waiting" flag is trusted iff the
// transcript has not advanced well past statusUpdatedAt + this window. Long
// enough that a genuine human-blocked wait stays fresh across a status poll;
// short enough that an abandoned wait whose transcript later advanced is demoted
// to idle. The library leaves this policy to the caller (see ClassifyActivity).
const registryWaitingFreshWindow = 5 * time.Minute

// registryVerdict resolves the shared claude-transcript activity verdict for one
// ccpool session. It is the cmd-layer adapter the state.Gather resolver wraps.
//
// ccpool sessions are keyed by ClaudeSessionID; the per-process Claude registry
// is keyed by PID (~/.claude/sessions/<pid>.json) with a sessionId field — so
// the join is: sweep the dir, match reg.SessionID == claudeSessionID. The match
// is PID-GATED (PidAlive) before the verdict is trusted: a "busy" row can
// survive a crash (the file lingers until GC), so a dead pid reports
// (zero, false). awaitingInput and lastActivity come from the transcript (both
// error-tolerant). A missing dir, no match, or a dead pid all return
// (zero verdict, false) — the classifier then ignores the registry and falls
// back to its pane+row precedence.
//
// Mapping back to ccpool state (applied by state.Classify):
//
//	ct.Active          -> state.Working
//	ct.WaitingForHuman -> state.WaitingForHuman
//	ct.Idle            -> state.Idle
func registryVerdict(sessionsDir, claudeSessionID, transcriptPath string, freshWindow time.Duration) (ct.ActivityVerdict, bool) {
	if claudeSessionID == "" {
		return ct.ActivityVerdict{}, false
	}
	rows, err := ct.ReadSessionRegistry(sessionsDir)
	if err != nil {
		return ct.ActivityVerdict{}, false
	}
	for _, reg := range rows {
		if reg.SessionID != claudeSessionID {
			continue
		}
		if !ct.PidAlive(reg.PID) {
			return ct.ActivityVerdict{}, false // stale row from a dead/crashed pid
		}
		awaitingInput := false
		if transcriptPath != "" {
			if a, aerr := ct.IsAwaitingInput(transcriptPath); aerr == nil {
				awaitingInput = a
			}
		}
		var lastActivity time.Time
		if transcriptPath != "" {
			if t, ok := ct.LastMessageActivity(transcriptPath); ok {
				lastActivity = t
			}
		}
		return ct.ClassifyActivity(reg, awaitingInput, lastActivity, freshWindow), true
	}
	return ct.ActivityVerdict{}, false
}
```

- [ ] **Step 4: Run the adapter tests to verify they pass**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run RegistryVerdict -v`
Expected: PASS — all four `TestRegistryVerdict_*` cases green.

- [ ] **Step 5: Wire the resolver into `runState`**

In `cmd/ccpool/state.go`, add the import for the sessions-dir default. The `claude-transcript` module does not export `DefaultSessionsDir` (that lives in pa-monitor); resolve it locally. Add a tiny helper in `registry.go` rather than importing pa-monitor (ccpool must stay self-contained). Append to `cmd/ccpool/registry.go`:

```go
// defaultSessionsDir returns ~/.claude/sessions (the Claude Code per-process
// session registry directory). Empty when the home dir is unresolved.
func defaultSessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "sessions")
}
```

and add `"os"` + `"path/filepath"` to `registry.go`'s import block:

```go
import (
	"os"
	"path/filepath"
	"time"

	ct "github.com/phillipgreenii/claude-transcript"
)
```

Then in `cmd/ccpool/state.go`, after the `lastText` closure (`:68-73`) and before the `state.Gather(...)` call (`:74`), add the registry resolver closure:

```go
	// registry resolves the shared claude-transcript activity verdict for this
	// session: it sweeps ~/.claude/sessions, matches the row by ClaudeSessionID,
	// PID-gates it, and returns ClassifyActivity's verdict. No live match -> false,
	// and Gather/Classify then ignore it (pane+row fallback).
	registry := func() (ct.ActivityVerdict, bool) {
		return registryVerdict(defaultSessionsDir(), row.ClaudeSessionID, row.TranscriptPath, registryWaitingFreshWindow)
	}
```

Update the `state.Gather(...)` call (`:74`) to pass it after `lastText`:

```go
	res, err := state.Gather(cl, time.Sleep, awaiting, lastText, registry, tmuxName, externalID, row)
```

Add the `ct` import alias to `cmd/ccpool/state.go`'s import block (`:3-18`) — insert `ct "github.com/phillipgreenii/claude-transcript"`:

```go
	"github.com/phillipgreenii/ccpool/internal/tmux"
	ct "github.com/phillipgreenii/claude-transcript"
)
```

- [ ] **Step 6: Run the cmd package tests to verify everything passes**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -v`
Expected: PASS — `TestRegistryVerdict_*` green; the existing `TestRunState`/state-render tests still pass (a real `~/.claude/sessions` sweep in CI finds no matching live row → fallback, identical to today's output).

- [ ] **Step 7: Commit**

```bash
git add cmd/ccpool/registry.go cmd/ccpool/registry_test.go cmd/ccpool/state.go
git commit -m "feat(ccpool): registry-verdict adapter + wire into 'ccpool state'"
```

---

### Task 5: Document the mapping in the package + a regression check for needs_input

**Files:**

- Modify: `packages/ccpool/internal/state/state.go:1-11` (package doc — add the mapping table)
- Test: `packages/ccpool/internal/state/state_test.go` (an explicit `needs_input` regression assertion)

This satisfies AC (2) "documented mapping" at the package level and AC (3) "no regression to needs_input/pending-question handling" with a named guard.

- [ ] **Step 1: Write the failing/anchoring regression test**

Add to `internal/state/state_test.go` an explicitly-named regression test (the behavior is already correct from Task 2's branch ordering — this test pins it so a future refactor cannot silently regress it):

```go
// TestClassify_needsInputUnregressedByRegistry pins AC(3): a hook-set NeedsInput
// row (PRIMARY signal) plus its pending question survives the registry signal in
// EVERY registry activity (Active/WaitingForHuman/Idle) and when no row is found.
func TestClassify_needsInputUnregressedByRegistry(t *testing.T) {
	const staticPane = "❯ ready\n  -- INSERT --"
	base := Inputs{
		Name: "a", Live: true,
		Frame1: staticPane, Frame2: staticPane, Frame3: staticPane, NumFrames: 3,
		Row: store.Session{State: store.NeedsInput, PendingQuestion: "Alpha or Bravo?"},
	}
	regCases := []struct {
		name  string
		found bool
		act   ct.Activity
	}{
		{"no_row", false, ct.Idle},
		{"registry_active", true, ct.Active},
		{"registry_waiting", true, ct.WaitingForHuman},
		{"registry_idle", true, ct.Idle},
	}
	for _, rc := range regCases {
		t.Run(rc.name, func(t *testing.T) {
			in := base
			in.RegistryFound = rc.found
			in.Registry = ct.ActivityVerdict{Activity: rc.act}
			got := Classify(in)
			if got.State != WaitingForHuman {
				t.Errorf("State = %q, want waiting-for-human (hook-set NeedsInput is PRIMARY)", got.State)
			}
			if got.Question != "Alpha or Bravo?" {
				t.Errorf("Question = %q, want the row's pending question (pg2-7a5b)", got.Question)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it passes (behavior already correct from Task 2)**

Run: `cd packages/ccpool && go test ./internal/state/ -run needsInputUnregressed -v`
Expected: PASS — all four sub-cases green (the `NeedsInput` branch at precedence 3 returns before any registry branch).

If any sub-case FAILS, the Task 2 branch ordering is wrong (a registry branch was placed before the `NeedsInput` check); fix the ordering in `Classify` so the `NeedsInput` branch precedes both registry branches, then re-run.

- [ ] **Step 3: Add the mapping table to the package doc**

In `internal/state/state.go`, extend the package doc comment (`:1-11`) — append a paragraph after the existing description (before the `package state` line):

```go
// Registry signal (pg2-oois.5): Gather also folds in the shared
// claude-transcript session-registry activity verdict (ClassifyActivity) as an
// additive INPUT signal, pid-gated and matched to this session by
// ClaudeSessionID. It cross-checks — it does not replace — the pane+row signals,
// and it NEVER supplies the thinking/streaming substate (the registry has no
// such field; substate stays pane-derived). Mapping:
//
//	claudetranscript.Active          -> state.Working
//	claudetranscript.WaitingForHuman -> state.WaitingForHuman
//	claudetranscript.Idle            -> state.Idle
//
// When no live registry row is found the verdict is ignored entirely. The
// verdict is mirrored onto Result (RegistryFound/Registry) so the pg2-yukh
// ingestion guard can reuse it without re-reading the registry.
```

- [ ] **Step 4: Run the full state package test**

Run: `cd packages/ccpool && go test ./internal/state/ -v`
Expected: PASS — all tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/state/state.go internal/state/state_test.go
git commit -m "docs(ccpool): document registry<->state mapping; pin needs_input regression guard"
```

---

### Task 6: Full verification + dependency proof + bead close

**Files:** none (verification only).

- [ ] **Step 1: Full Go test + vet**

Run: `cd packages/ccpool && go test ./... && go vet ./...`
Expected: all PASS (no failures, no vet diagnostics).

- [ ] **Step 2: Prove the gomod2nix dependency wiring is unchanged**

The `claude-transcript` require + replace already exist (`go.mod:32,40`) and `registry.go` adds only stdlib transitive deps, so this should be a no-op. Run it to PROVE it:

```bash
cd packages/ccpool
go mod tidy
nix run github:nix-community/gomod2nix -- generate
git status --short go.mod go.sum gomod2nix.toml
```

Expected: `go.mod`/`go.sum`/`gomod2nix.toml` show NO changes (clean `git status --short`). If `gomod2nix.toml` changes, an unexpected third-party dep was pulled in — STOP and investigate (a non-empty diff here is a blocking signal, not something to commit blindly). `claude-transcript` must NOT appear in `gomod2nix.toml` (it is the symlinked local replace, per `default.nix:23-27`).

- [ ] **Step 3: Confirm the default.nix src fileset already includes the sibling (no change needed)**

Run: `cd packages/ccpool && grep -n 'claude-transcript\|modRoot\|fileset' default.nix`
Expected: shows `../claude-transcript` in the union (`:18`) and `modRoot = "ccpool";` (`:21`). No edit required — Pattern B is already in place. (If, contrary to this, the union did NOT include `../claude-transcript`, add it to the `lib.fileset.unions` list and re-run `nix flake check`.)

- [ ] **Step 4: Repo checks required before "complete" (per agent-support CLAUDE.md)**

Run (from repo root `phillipgreenii-nix-agent-support`):

```bash
prek run --all-files || pre-commit run --all-files
nix flake check
```

Expected: both PASS. `nix flake check` builds the `ccpool` package (exercising the Pattern-B sibling build) and runs its Go test suite under nix.

- [ ] **Step 5: Manual smoke of `ccpool state` (optional, non-hermetic)**

Run against a real session if one exists: `cd packages/ccpool && go run ./cmd/ccpool state <some-external-id> --json`
Expected: emits the reconciled JSON. For a live `busy` Claude session whose `ClaudeSessionID` matches a `~/.claude/sessions/<pid>.json` row, the state reflects `working`; with no matching live row the output is identical to pre-change (pane+row fallback). This is operator-only; CI relies on the hermetic tests above.

- [ ] **Step 6: Close the bead (closes epic `pg2-oois`)**

```bash
bd update pg2-oois.5 --claim          # if not already claimed
bd comment pg2-oois.5 "Implemented: ccpool's state.Gather now folds in the shared claude-transcript registry verdict (ClassifyActivity) as an additive INPUT signal — sweep ~/.claude/sessions, match by ClaudeSessionID, PID-gate, then ClassifyActivity. Mapping Active->working/WaitingForHuman->waiting-for-human/Idle->idle documented in internal/state/state.go package doc + Classify precedence comment. Pane thinking/streaming substate retained (registry has no substate). needs_input/pending-question regression pinned (TestClassify_needsInputUnregressedByRegistry). Verdict mirrored onto state.Result (RegistryFound/Registry) for pg2-yukh ingestion-guard reuse. go.mod/gomod2nix unchanged (sibling already wired; registry.go is stdlib-only)."
bd close pg2-oois.5
```

Expected: `pg2-oois.5` closes; epic `pg2-oois` auto-closes (last of 4 children).

---

## Self-review checklist (run while writing)

**1. Spec coverage (against `bd show pg2-oois.5` AC + the roadmap C1 corrected scope):**

- AC(1) "ccpool can consume the shared registry reader" → Tasks 3+4 (resolver in `Gather`, `registryVerdict` adapter calling `ReadSessionRegistry`/`PidAlive`/`ClassifyActivity`).
- AC(2) "documented mapping between registry status and ccpool's state enum" → Task 5 Step 3 (package doc table) + Task 2 Step 3 (`Classify` precedence comment) + the Pre-flight mapping table here.
- AC(3) "no regression to needs_input/pending-question handling" → Task 5 Steps 1-2 (`TestClassify_needsInputUnregressedByRegistry`, four registry states) + Task 2's branch ordering (NeedsInput precedence 3, before both registry branches).
- Roadmap C1 specifics: PID-gate first (`registryVerdict` gates with `PidAlive` before trusting) ✓; trust `busy` (via `ClassifyActivity`, never demoted) ✓; cross-check `waiting` freshness (`registryWaitingFreshWindow` passed to `ClassifyActivity`) ✓; keep pane `thinking`/`streaming` (Task 2 substate guard test + the pane in-flight branch unchanged at precedence 2) ✓.
- Synergy (pg2-yukh ingestion guard reuse): verdict mirrored onto `Result.RegistryFound`/`Result.Registry` (Task 1 Step 5) ✓.
- go.mod sibling / gomod2nix Pattern B: verified already present; Task 6 Steps 2-3 PROVE no change needed (the prompt's go-get/generate/fileset steps are present as verification because the wiring pre-exists — documented explicitly so the worker does not blindly re-add it).

**2. Placeholder scan:** every code step shows the actual edit (real current code: `Classify` precedence list, `Gather` signature, `runState` closures, the `state_test.go` helper/fake names `fakePaner`/`recordingSleep`/`staticAwaiting`/`staticLastText`/`noText`). No "TODO"/"handle edge cases"/"similar to". The `itoa` helper in `registry_test.go` is written out (avoids a `strconv` import only if the worker prefers; `strconv.Itoa` is an acceptable equivalent).

**3. Type consistency:** `Inputs.RegistryFound bool` / `Inputs.Registry ct.ActivityVerdict` ↔ `Result.RegistryFound`/`Result.Registry` ↔ resolver `func() (ct.ActivityVerdict, bool)` ↔ adapter `registryVerdict(...) (ct.ActivityVerdict, bool)` — names and the `(verdict, found)` tuple order are consistent everywhere. `ct` is the import alias used by the existing `cmd/ccpool/reply.go:15` and `internal/core/session/discovery.go` (pa-monitor), and I introduce the SAME alias in `internal/state/state.go` and `cmd/ccpool/state.go`. `registryWaitingFreshWindow` const name is used identically in `registry.go`, `state.go`, and `registry_test.go`. The `Gather` argument order `(..., lastText, registry, tmuxName, ...)` is applied identically at the one production call site (`state.go:74`) and all ten test call sites (Task 3 Step 4).
