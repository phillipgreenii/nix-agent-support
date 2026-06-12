# ccpool Unit B: Reconciled Multi-Signal State Query

**Status**: Implemented
**Date**: 2026-06-12
**Deciders**: ccpool maintainers (design authored on branch `ccpool-observability`)
**Builds on**: Unit A (`docs/superpowers/specs/2026-06-12-ccpool-unitA-cancel-confirmation-design.md`)

> **AS BUILT (review amendments R1-R4 applied).** This document is updated to match the implemented
> Unit B. Four authoritative changes from design review WON over the original draft:
>
> - **R1 — No `claude-transcript` extension.** The classifier's only transcript signal is the
>   EXISTING `IsAwaitingInput(path) (bool, error)`. There is NO `Message.StopReason`, NO
>   `LastTurnComplete`/`turn_state.go`, NO change to the `session.Transcript` port or `reply.go`.
>   `internal/state` does NOT define a `TranscriptReader`; the pure `Classify` takes `Awaiting bool`
>   directly. The brand-new/launching edge is a direct store-row check (`store.Starting` →
>   working/thinking), not a transcript `sawSubstantive` check.
> - **R2 — Streaming detection is a 3-frame pane diff.** Output-streaming carries NO live counter
>   (the counter is a THINKING-phase artifact only). `Gather`/`ClassifyFrame` capture f1; if the
>   counter matches f1 → working/thinking immediately (fast path, no sleep). Else sample f2, f3
>   ~`PaneDiffInterval` (175ms) apart (total ~350ms); counter in f2/f3 → thinking; any adjacent pair
>   differs → streaming; all identical → settled. `streaming` is best-effort (a >window pane-quiet
>   pause reads as settled; self-corrects on the next query).
> - **R3 — The `Send_NoWait` contract assertion is gated on `waitForThinking`** before reading state
>   (else it flakes to `idle` in the launch gap).
> - **R4 — `ReLiveCounter` lives in a new leaf package `internal/pane`.** Both `internal/session`
>   (Unit A's `confirmStable`) and `internal/state` import `internal/pane`. One source of truth, no
>   backwards dependency.

> **REAL-CLAUDE VALIDATION (2026-06-12) — two findings folded in:**
>
> - **Startup-draw gate (fix).** A freshly-launched session still DRAWING its TUI churns the pane,
>   which the counter-less 3-frame diff misread as `streaming/working` (intermittently failing the
>   `Lifecycle_New → idle` assertion). Fix: the streaming-via-diff branch is now gated on the store
>   row corroborating a turn (`Working`/`Starting`); the thinking-counter path stays ungated. A
>   freshly-`Ready` session with an animating pane now reads `idle` (validated deterministically).
> - **`waiting-for-human` live-detection is DEFERRED (`pg2-7a5b`).** A LIVE paused AskUserQuestion
>   persists NO `assistant` event to the JSONL (only the user prompt + metadata), so `IsAwaitingInput`
>   returns false and the picker reads `idle`. The state vocabulary + the `IsAwaitingInput` fallback
>   (correct when a dangling question IS persisted) ship in Unit B, but reliable LIVE detection needs
>   a pane picker marker (not yet pinned) and is tracked in `pg2-7a5b`. The `NeedsInput` contract
>   assertion is therefore a `pending`, not a live assert. The other four states
>   (`idle`/`working`(`thinking`|`streaming`)/`error`/`not-live`) are real-claude validated.

## Context

`ccpool doctor <name>` prints `state=<store.State>` straight from the SQLite row
(`cmd/ccpool/doctor.go:40`). That value is the **cached last-turn outcome** written by the store
`Transition` calls (and the StopFailure / SessionStart hooks). It is deliberately stale-tolerant:

- After a **thinking cancel**, Unit A's `confirmStable` flips the row to `Ready` only once the pane
  goes static — but the row can lag the actual pane state during the confirm window, and a
  `reply --no-wait` returns with the row at `Working` while the turn is genuinely in flight.
- The row has no notion of the `thinking` vs `streaming` sub-phase.
- A dangling `AskUserQuestion` only reaches `needs_input` via the §8.3 step-6 transcript fallback,
  which fires **only on a blocking wait** (`internal/session/send.go:113` `resolveOutcome`). After a
  `--no-wait` or `--queue-message` send, a pending question leaves the row at `Working` with no
  `needs_input` ever written.

So the cached `state=` answers "what was the last settled outcome the store recorded", not "what is
this session doing **right now**". The contract harness has four `pending(...)` checks that need a
**live, reconciled** answer (see [Harness-pending conversions](#harness-pending-conversions)).

This unit adds `ccpool state <name>` (human + `--json`) that computes a **reconciled** state by
combining live signals — tmux liveness, the pane, the JSONL transcript, and the store row — and
**overrides** the cached `state=`. `doctor` is unchanged (it keeps reporting the cached value; the
two are intentionally distinct — see [Should doctor surface the reconciled state?](#should-doctor-surface-the-reconciled-state)).

### Why a reconciliation, not a single source

No single signal is sufficient, and each is the authority for a **different** facet:

| Facet                                        | Authority            | Why                                                                                                                                                           |
| -------------------------------------------- | -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| live / not-live                              | tmux (`HasSession`)  | The store does not persist liveness (`store.go:1`: "Liveness is NOT stored").                                                                                 |
| in-flight + `thinking`/`streaming`           | pane (`CapturePane`) | A live turn ALWAYS mutates the pane; a settled pane is static (Unit A). The transcript can NOT disambiguate in-flight from a discarded turn (see below).      |
| settled outcome (`idle`/`waiting-for-human`) | transcript (JSONL)   | The pane's static markers (`⏺`, `Thought for Ns`) linger and are not trustworthy for _what_ settled; the transcript records the actual last substantive turn. |
| `error`                                      | store row (`Failed`) | No reliable turn-level error signal exists in the transcript (evidence below); the StopFailure hook writes `Failed`.                                          |

## Goal

Add `ccpool state <name>` reporting a reconciled state from the vocabulary below, in human and
`--json` form. Associated info (the pending question TEXT, last error/reply text) is **DEFERRED** —
not in this unit. Just the state + sub-state.

## State vocabulary

| State               | Sub-state                 | Meaning                                                            |
| ------------------- | ------------------------- | ------------------------------------------------------------------ |
| `idle`              | —                         | Live, no turn in flight, last turn settled cleanly.                |
| `working`           | `thinking` \| `streaming` | A turn is in flight.                                               |
| `waiting-for-human` | —                         | A dangling `AskUserQuestion` (needs_input) is pending.             |
| `error`             | —                         | The last turn failed (store row `Failed`).                         |
| `not-live`          | —                         | No tmux session. The last known store state is reported alongside. |

The sub-state is populated **only** for `working`. For every other state it is empty.

## Signals and what each is good for (grounded in real evidence)

All four signals were validated against real artifacts on this branch.

### 1. tmux liveness — `Tmux.HasSession` (the gate)

Not live → terminate immediately at `not-live`. Carry the cached store state alongside so the caller
still sees the last known outcome (mirrors `doctor`/`list`, which derive liveness the same way).

### 2. Pane — `Tmux.CapturePane` (the in-flight + sub-state authority)

Unit A established and real-claude-validated that **a live turn ALWAYS mutates the pane**:

- **Thinking** renders the spinner's elapsed-seconds counter, e.g.
  `✽ Envisioning… (5s · ↓ 13 tokens · thinking with xhigh effort)`. The counter ticks at least once
  per ~1s (observed `26s → 28s → 33s`), and the glyph animates between ticks.
- **Prose-streaming** appends text continuously, with **no** counter line; tokens accrue as `↑ N`.
- A **settled** pane is byte-static.
- The persistent `⏺` / `Thought for Ns` markers are **NOT** liveness — they linger after a turn ends
  (Unit A "Seam note for Unit B" calls this out explicitly: a `streaming` arm needs a
  liveness-bearing pattern, never these static markers).

Reuse Unit A's `ReLiveCounter` (`\(\d+s · `) insight: counter presence is a **fast positive** for
in-flight, and specifically discriminates `thinking` (counter shows `↓ … thinking`) from
`streaming` (no counter, prose/`↑` tokens appending). **The streaming case has no counter** (R2:
the counter is a thinking-phase artifact only — confirmed by Unit A's real-claude evidence), so it
is detected by a **3-frame diff** (the pane changed across captures ~`PaneDiffInterval` apart →
animating → in-flight). See the [in-flight detection algorithm](#in-flight-detection-the-single-shot-algorithm).

### 3. Session JSONL transcript — `claude-transcript` (the settled authority)

Path is `store.Session.TranscriptPath`. **R1: the ONLY transcript signal Unit B consumes is the
existing `IsAwaitingInput(path) (bool, error)`** (`awaiting.go`). The `claude-transcript` module is
NOT extended — there is no `Message.StopReason`, no `LastTurnComplete`/`turn_state.go`, and the
`session.Transcript` port is unchanged.

**A dangling `AskUserQuestion`** (`tool_use` name=`AskUserQuestion` with no matching `tool_result`)
= waiting. `IsAwaitingInput` detects this exactly, and is the basis of the `waiting-for-human` state.

**The settled-vs-discarded distinction is resolved by the PANE, not the transcript.** A
cancelled/rewound thinking turn has a trailing `user`-only transcript (the discarded turn was never
persisted), which is ambiguous between _in-flight_ (assistant not yet written) and _discarded_.
Checking the pane FIRST (precedence step 2) resolves it: a static pane over any transcript means
_settled_, so the residual `idle` branch is reached without consulting the transcript beyond
`IsAwaitingInput`. The original draft's `LastTurnComplete`/`stop_reason` machinery for this
distinction was DROPPED (R1) — it was dead scope, because the precedence never branched on it.

**The brand-new / launching edge is a direct store-row check, not a transcript read** (R1): a static
pane over a row in `store.Starting` is reported as `working`/`thinking` (the launch is in progress);
no `sawSubstantive` transcript scan is needed.

**No reliable turn-level error signal exists in the transcript** (evidence from all project
transcripts: "API Error" appears only as assistant text; `is_error:true` only on `tool_result`
blocks). Therefore the `error` state derives from the **store row `Failed`** (StopFailure hook), NOT
the transcript. If a structured transcript error signal appears later, step 4 can adopt it additively.

### 4. Store row — `store.Session.State` (cached hint + `error` + `not-live` fallback)

Provides: the `Failed` → `error` signal; the last-known state to report alongside `not-live`; and a
**tie-breaker** when the pane is static and the transcript is genuinely empty/unreadable (a
brand-new session whose transcript has not been written yet — fall back to the row: `Starting`/
`Working` with a static pane and no transcript → `idle` is wrong, so report the row's state mapped
into the vocabulary; see precedence step 5). The reconciled query **OVERRIDES** the cached state for
everything the live signals can determine — that is the entire point.

## Reconciliation precedence

Evaluated top to bottom; first match wins. Refined from the handoff's proposal against the code.

1. **Not live** (`!HasSession`) → `not-live`, carrying `row.State` as `LastKnown`. (No pane/transcript
   reads — they are meaningless without a session.)
2. **Live + IN-FLIGHT** (pane analysis, see algorithm) → `working`, with sub-state `thinking` or
   `streaming`.
3. **Live + settled + transcript `IsAwaitingInput`** → `waiting-for-human`.
4. **Live + settled + `row.State == Failed`** → `error`.
5. **Live + settled + `row.State == Starting`** → `working`/`thinking` (R1: the launching edge, a
   direct store-row check — the row says a turn is starting but the pane is not yet animating; trust
   the row over a presumed-idle static pane).
6. **Live + settled, otherwise** → `idle`. This is the residual: a completed assistant turn, OR a
   discarded/rewound turn (trailing `user`-only transcript with a static pane), OR
   `row.State ∈ {Ready, Working, Done}`. A static pane is the ground truth that nothing is
   animating right now. (We do NOT trust a stale `Working` row over a static pane — that stale-row
   case is exactly what this query exists to override.)

**Ordering rationale.**

- Liveness gates everything (1 first): all other signals require a session.
- In-flight (2) precedes the settled checks because the transcript can NOT see an in-flight turn
  (the assistant event is not yet written) and the pane is the only authority that can. Checking the
  pane first also resolves the discarded-vs-in-flight transcript ambiguity.
- `waiting-for-human` (3) precedes `error`/`idle`: a dangling question is a _settled_ pane (the model
  stopped to ask) but is a distinct, actionable state, so it must be detected before the generic
  settled buckets.
- `error` (4) precedes `idle` (5): a failed turn leaves a static pane and a transcript that _looks_
  settled; only the row's `Failed` distinguishes it.

### In-flight detection (the single-shot algorithm)

A `state` query must be fast. We use a **two-tier** test: a fast positive on counter presence, with
a frame diff as the streaming fallback.

```
analyzePane(name):
  p1 := CapturePane(name)
  if reLiveCounter.MatchString(p1):           # "(\d+s · " — thinking counter present
      return inFlight=true, sub=thinking       #   (no second capture needed: ~instant)
  # No counter. Could be settled, OR prose-streaming (which has no counter line).
  sleep(paneDiffInterval)                       # ~350ms
  p2 := CapturePane(name)
  if reLiveCounter.MatchString(p2):            # a counter appeared between frames -> thinking
      return inFlight=true, sub=thinking
  if p2 != p1:                                  # pane animated without a counter -> streaming
      return inFlight=true, sub=streaming
  return inFlight=false, sub=""                 # two identical counter-less frames -> settled
```

**Why this shape (cost + correctness).**

- **Counter presence is a fast positive.** If a thinking counter is on screen, the turn is in flight
  and it is `thinking` — answer in a single capture (~instant), no sleep. This is the common
  mid-thinking case and costs nothing.
- **The diff is the streaming fallback.** Prose-streaming has no counter line, so counter-absence is
  inconclusive: it could be settled or streaming. One `paneDiffInterval` diff distinguishes them — a
  changed frame means the pane is animating (prose appending / `↑` tokens), which under
  counter-absence is `streaming`. Two identical counter-less frames mean settled.
- **Single diff, not Unit A's 4-of-N stability loop.** Unit A's `confirmStable` needs `K=4` identical
  reads because it must _prove a turn STOPPED_ (a high-stakes, must-not-false-confirm decision that a
  cancel landed). A `state` query only needs to _observe whether the pane is currently animating_ — a
  far weaker, read-only claim with no destructive consequence — so a single before/after diff is
  sufficient and keeps the query at ~one `paneDiffInterval` (~350ms) worst case. We deliberately do
  NOT reuse `confirmStable` here (see the seam note below).
- **`paneDiffInterval` ≈ 350ms.** Comfortably longer than a streaming frame interval (prose appends
  many times per second) and a thinking tick, so a genuinely live turn changes within the window;
  brisk enough that a settled-session query returns in well under half a second. The handoff's
  300-400ms range; 350ms is the midpoint. Defined as a package const with the same load-bearing
  rationale comment style as Unit A's tunables.

**False-negative risk: a streaming pause.** If prose-streaming briefly stalls (a long tool call
emits nothing to the pane for >350ms), two identical counter-less frames read as settled → `idle`,
which is wrong (it is mid-turn). This is the same "tool-call residual" Unit A documents for its
confirmation loop. It is acceptable for a _read-only_ status query (the next query a moment later
reports `working` again; nothing destructive happens), and is listed in Risks. We do NOT lengthen the
window to chase it — that would slow every query for a rare transient. (Contrast Unit A, where a
false confirm is destructive, hence its conservative 6s budget.)

**Seam with Unit A (one liveness notion, no gratuitous refactor).** Unit A flagged a forward seam:
"If Unit B wants a `ClassifyPane(pane) (thinking|streaming|idle)` seam, its `Streaming` arm needs a
liveness-bearing pattern (the live counter, or a frame-to-frame diff), not the static markers." This
unit realizes that seam as the **pure** function `ClassifyFrame(p1, p2 string) PaneVerdict` plus the
single-capture fast-path above. Both `confirmStable` (Unit A) and `analyzePane` (Unit B) share the
**same liveness notion** — _byte-mutation of the pane plus `reLiveCounter`_ — but apply it to
different decisions (prove-stopped vs observe-now). To make the shared insight explicit without
touching Unit A's confirmation loop, the regex `reLiveCounter` is the single source of truth, reused
by both. We do **NOT** refactor `confirmStable` to call `analyzePane` (or vice versa): their loop
shapes, budgets, and stakes differ, and Unit A is implemented + validated. The shared seam is the
regex + the documented "pane-mutation = liveness" rule, not a shared function. (YAGNI; matches Unit
A's "do not design `ClassifyPane` there" stance — we add only what step 2 needs.)

## Architecture

### Where the classifier lives

**Decision: a new `internal/state` package containing a pure `Classifier` that takes the same small
ports `session` uses (a `Paner`, a `Transcript`, and the store row + liveness as inputs).** Not a
method on `session.Service`.

Rationale:

- **Unit-testability is the top constraint** (handoff: "MUST be unit-testable with fakes"). A
  standalone package with narrow inputs is trivially table-testable with a fake pane and fixture
  transcripts, with no `session.Service` lifecycle wiring (locks, waiters, notifiers) in the way.
- **`session.Service` is lifecycle logic** (Ensure/Send/Cancel/Close — it _mutates_ sessions). The
  reconciled state query is **read-only observation**; mixing it into `Service` blurs that boundary
  and drags the classifier behind `Service`'s larger `Deps`.
- The handoff said "extend the doctor logic", but `doctor` is a thin `cmd/` reporter with no reusable
  core; lifting reconciliation into a pure `internal/state` package _is_ the clean extension of that
  logic, and keeps it out of both `cmd/` and the mutating `Service`.
- It mirrors `renderList` (`list.go:48`): a pure, I/O-free core (`renderList` takes a `liveFn` rather
  than calling tmux directly) wrapped by a thin `cmd/` shell. We follow the same pattern.

The classifier itself is pure over its inputs; the **signal-gathering** (the two captures with a
sleep, opening the transcript) is done by a thin gatherer that the `cmd/` layer wires with real
adapters. This keeps the timing-sensitive capture-diff testable by injecting scripted captures.

### Package shape — `internal/state/state.go`

```go
// Package state computes a reconciled, multi-signal view of a session's CURRENT
// state, overriding the store's cached last-turn outcome. Read-only: it never
// mutates a session (contrast internal/session). Pure core + a thin gatherer so
// the capture-diff timing is injectable in tests.
package state

import (
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// State is the reconciled vocabulary (distinct from store.State, which is the
// cached last-turn outcome). A string type so JSON/human rendering is trivial.
type State string

const (
	Idle            State = "idle"
	Working         State = "working"
	WaitingForHuman State = "waiting-for-human"
	Error           State = "error"
	NotLive         State = "not-live"
)

// SubState qualifies Working only; empty for every other State.
type SubState string

const (
	SubNone      SubState = ""
	SubThinking  SubState = "thinking"
	SubStreaming SubState = "streaming"
)

// Result is the reconciled answer.
type Result struct {
	Name      string
	State     State
	SubState  SubState
	Live      bool
	LastKnown store.State // the cached store state (always populated; the headline for not-live)
}

// Paner is the minimal pane port (a subset of session.Tmux). The Classifier
// reuses session.Tmux at the call site; this narrow interface keeps the package
// dependency-light and the fakes tiny.
type Paner interface {
	CapturePane(name string) (string, error)
}

// TranscriptReader reads settled-turn signals. Superset of the two methods
// session.Transcript already exposes, plus LastTurnComplete (see the
// claude-transcript extension). The cmd layer satisfies it with the same
// transcriptAdapter used elsewhere.
type TranscriptReader interface {
	IsAwaitingInput(path string) (bool, error)
	LastTurnComplete(path string) (complete bool, sawSubstantive bool, err error)
}

// Inputs are the gathered signals fed to the pure Classify.
type Inputs struct {
	Name           string
	Live           bool
	Row            store.Session // for LastKnown + the Failed -> error signal
	TranscriptPath string
	Pane1          string // first capture (empty if not live)
	Pane2          string // second capture, paneDiffInterval later (empty if the fast-path short-circuited or not live)
	UsedSecond     bool   // whether Pane2 was actually captured (fast-path skips it)
}

// PaneDiffInterval is the gap between the two captures used to detect a
// counter-less (streaming) animation. Chosen > a streaming frame interval and a
// thinking tick so a live turn always mutates within the window, yet short
// enough that a settled-session query returns sub-half-second. Read-only status,
// so a transient streaming stall mis-reading as idle is acceptable (Risks) —
// unlike Unit A's confirmStable budget, which must not false-confirm.
const PaneDiffInterval = 350 * time.Millisecond
```

`reLiveCounter` is **moved/exported** so both packages share it without copying. Concretely: define
`var ReLiveCounter = regexp.MustCompile(\`\(\d+s · \`)`once (proposed home: a tiny shared spot, e.g.`internal/state`exporting it, with`internal/session`referencing`state.ReLiveCounter`). This keeps
ONE liveness regex (the seam) without refactoring Unit A's loop. If a cross-package move is judged
too invasive for this unit, the fallback is to duplicate the regex with a comment cross-referencing
both call sites; the design prefers the single exported var.

```go
// PaneVerdict is the pure pane classification.
type PaneVerdict struct {
	InFlight bool
	Sub      SubState
}

// ClassifyFrame is the pure pane analysis over up to two frames. p2 is ignored
// when usedSecond is false (the fast-path: a counter was already present in p1).
//   - counter in p1            -> in-flight, thinking
//   - counter appears in p2    -> in-flight, thinking
//   - p2 != p1 (no counter)    -> in-flight, streaming
//   - two identical, no counter-> settled
func ClassifyFrame(p1, p2 string, usedSecond bool) PaneVerdict

// Classify applies the reconciliation precedence to gathered Inputs. Pure: no
// I/O, no clock. This is the unit under test.
func Classify(in Inputs, tr TranscriptReader) (Result, error)

// Gather performs the live signal collection (the two captures with the
// fast-path short-circuit) and returns Inputs ready for Classify. The cmd layer
// calls Gather then Classify; tests call Classify directly with scripted Inputs.
func Gather(p Paner, sleep func(time.Duration), name, transcriptPath string, live bool, row store.Session) (Inputs, error)
```

`Gather` is where the single-shot algorithm runs: capture `p1`; if `ReLiveCounter` matches, set
`Pane1=p1, UsedSecond=false` and return (no sleep); else `sleep(PaneDiffInterval)`, capture `p2`,
set both. `sleep` is injectable (nil-safe no-op in tests, mirroring `Service.sleep`).

`Classify` consumes `Inputs` + the transcript reader (it calls `IsAwaitingInput` /
`LastTurnComplete` only for the live+settled branches, so a `not-live` or in-flight result does no
transcript I/O):

```
Classify(in, tr):
  res := Result{Name: in.Name, Live: in.Live, LastKnown: in.Row.State}
  if !in.Live:
      res.State = NotLive; return res         # LastKnown carries the cached state
  v := ClassifyFrame(in.Pane1, in.Pane2, in.UsedSecond)
  if v.InFlight:
      res.State = Working; res.SubState = v.Sub; return res
  # settled
  if awaiting, _ := tr.IsAwaitingInput(in.TranscriptPath); awaiting:
      res.State = WaitingForHuman; return res
  if in.Row.State == store.Failed:
      res.State = Error; return res
  res.State = Idle; return res
```

(Transcript-read errors in the settled branch are tolerated like `resolveOutcome` already does —
`awaiting, _ := …` — a missing/half-written transcript must not crash a status query; it falls
through to the `Failed`/`idle` checks.)

### claude-transcript extension

`IsAwaitingInput` is reused as-is. The settled-vs-discarded distinction needs a **last substantive
turn** helper. This is **broadly useful and general** (any consumer wanting "is the last turn done"),
so per the handoff's "prefer minimal, general additions to the shared module" it goes in
`claude-transcript`, not ccpool.

Add to `packages/claude-transcript` a new function (proposed file `turn_state.go`):

```go
// LastTurnComplete reports whether the LAST SUBSTANTIVE event (type user or
// assistant; metadata events such as mode/ai-title/last-prompt/attachment/
// custom-title/agent-name/permission-mode/file-history-snapshot/system/
// queue-operation/worktree-state are skipped) is a COMPLETED assistant turn:
// an assistant event carrying a terminal stop_reason (end_turn, tool_use,
// max_tokens, stop_sequence). sawSubstantive is false for a transcript with no
// user/assistant events at all (brand-new / metadata-only). A trailing
// user-only event (discarded/rewound thinking turn, or an in-flight turn whose
// assistant event is not yet written) yields complete=false, sawSubstantive=true
// — the pane disambiguates those two in the caller.
func LastTurnComplete(path string) (complete, sawSubstantive bool, err error)
```

This requires parsing `stop_reason`, which the module does not currently read. Add it to `Message`
in `events.go` (additive, no behavior change to existing parsers):

```go
type Message struct {
	Model      string      `json:"model"`
	Role       string      `json:"role"`
	StopReason string      `json:"stop_reason"` // NEW: "" while streaming; end_turn/tool_use/max_tokens/stop_sequence when settled
	Content    ContentList `json:"content"`
	Usage      Usage       `json:"usage"`
}
```

`LastTurnComplete` scans the transcript, tracks the last event whose `Type ∈ {user, assistant}`
(the substantive filter / metadata denylist), and at EOF reports:

- last substantive is `assistant` with non-empty `StopReason` → `complete=true, sawSubstantive=true`.
- last substantive is `assistant` with empty `StopReason` (rare mid-stream tail) → `complete=false,
sawSubstantive=true`.
- last substantive is `user` → `complete=false, sawSubstantive=true` (discarded or in-flight).
- no substantive events → `complete=false, sawSubstantive=false`.

It reuses the existing `bufio.Scanner` + per-line `json.Unmarshal(Event)` pattern from
`LastAssistantText`/`IsAwaitingInput` (tolerating non-event lines via `continue`), and the 16 MB
scanner buffer.

**Note on current Classify usage.** The precedence as written reaches `idle` whenever the pane is
static and the row is not `Failed` and the transcript is not awaiting — it does NOT actually _branch_
on `LastTurnComplete`. So is the new helper needed? **Yes, but minimally**: it is required only to
correctly handle the _transcript-empty / brand-new_ edge (precedence step 5's tie-breaker) and to
keep the design honest about the discarded-vs-complete distinction the handoff stresses. Two options:

1. **Include `LastTurnComplete` now** (recommended): `Classify`'s settled branch consults
   `sawSubstantive` so a static pane over a metadata-only transcript with a non-settled row
   (`Starting`) is reported faithfully rather than blindly `idle`. This makes the four facets each
   trace to their authority and matches the handoff's explicit interest in the discarded-turn
   evidence.
2. **Defer it**: if review deems the brand-new edge out of scope, `Classify` can reach `idle` purely
   from `pane-static + !awaiting + !Failed`, and the only claude-transcript change is the additive
   `StopReason` field (kept for the fixtures/tests to assert against). The `Transcript` port then
   gains nothing.

The doc specifies option 1 (the helper is small, general, and the evidence the handoff gathered is
about exactly this distinction). The settled branch becomes:

```
  if !sawSubstantive && in.Row.State == store.Starting:
      # transcript not yet written; trust the row over a presumed-idle static pane
      res.State = Working; res.SubState = SubThinking; return res   # launching, treat as working
```

inserted before the `idle` default. This is the only place `LastTurnComplete` is consulted; `complete`
itself is informational (asserted in tests, available for future associated-info work) and not
branched on beyond `sawSubstantive`. If review prefers a smaller surface, drop this clause and option
2 applies.

### The `Transcript` port change (`internal/session/session.go`)

`internal/state` defines its OWN `TranscriptReader` (above) — it does NOT depend on
`session.Transcript`. So `session.Transcript` itself need NOT change for this unit. However, for ONE
liveness/transcript adapter in `cmd/`, extend `transcriptAdapter` (`reply.go:24`) with the new method
so the same value satisfies both ports:

```go
func (transcriptAdapter) LastTurnComplete(p string) (bool, bool, error) { return ct.LastTurnComplete(p) }
```

Optionally also add `LastTurnComplete` to `session.Transcript` for symmetry, but that is **not
required** and YAGNI says skip it unless `session` grows a need. The narrow `state.TranscriptReader`
is the port that matters; `transcriptAdapter` satisfies it.

### The `state` command + `--json` shape

New file `cmd/ccpool/state.go`, wired into `main.go`'s dispatch.

`main.go`: add `"state": true` to the `known` map and a `case "state": os.Exit(runState(rest))`.

```go
func runState(args []string) int {
	fs := flag.NewFlagSet("state", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: ccpool state <name> [--json]")
		return 2
	}
	name := fs.Arg(0)
	// config + store (no session.Service needed — read-only).
	cfg, err := config.Load() ...
	st, err := store.Open(cfg.DBPath, clock.Real{}) ...; defer st.Close()
	row, ok, _ := st.GetByName(ctx, name)
	if !ok { fmt.Fprintln(os.Stderr, "no such session"); return 1 }

	cl := tmux.NewClient(cfg.Tmux.Socket)
	live := cl.HasSession(cfg.Tmux.Prefix + name)
	in, err := state.Gather(cl, time.Sleep, name, row.TranscriptPath, live, row) ...
	res, err := state.Classify(in, transcriptAdapter{}) ...

	if *jsonOut { /* json.NewEncoder(os.Stdout).Encode(jsonView(res)) */ }
	else { /* human line */ }
	return 0
}
```

**Human output** mirrors `doctor.go`'s `name=… state=… live=…` style (single line; sub-state appended
only when present; `last_known=` appended only for `not-live`):

```
name=alpha state=working sub=thinking live=true
name=alpha state=idle live=true
name=alpha state=waiting-for-human live=true
name=alpha state=not-live last_known=done live=false
```

**`--json` shape** — a small struct (sub-state and last-known omitted when empty, via `omitempty`):

```go
type jsonView struct {
	Name      string `json:"name"`
	State     string `json:"state"`               // idle|working|waiting-for-human|error|not-live
	SubState  string `json:"sub_state,omitempty"`  // thinking|streaming (working only)
	Live      bool   `json:"live"`
	LastKnown string `json:"last_known,omitempty"` // cached store state; primarily for not-live
}
```

```json
{ "name": "alpha", "state": "working", "sub_state": "thinking", "live": true }
{ "name": "alpha", "state": "not-live", "live": false, "last_known": "done" }
```

**Exit code**: `0` on a successful classification (the state is in the body, not the exit code) — like
`doctor`/`list`, which exit 0 and report in their output. `1` for config/store/no-such-session
errors; `2` for a usage error. We do NOT overload the exit code with the state (that would conflict
with the contract harness reading the printed state, and there is no caller need for it). `--json`
to a non-existent session still returns `1` with a stderr message (no JSON body), matching `doctor`.

### Should doctor surface the reconciled state?

**Decision: do NOT change `doctor` in this unit.** `doctor`'s `state=` is the _cached_ value by
design — it is a diagnostics command that deliberately shows the raw row alongside `live=` and
`cwd_trusted=` so a human can see the lag. Reconciliation is a separate concern with a separate
command. Adding a reconciled column to `doctor` is a plausible future nicety but risks breaking the
contract harness's `sessionLineHas(out, "alpha", "live=true")` line-shape assertions and the existing
`doctor` parsing, for no required benefit. Listed as a non-goal. (`ccpool state` is the reconciled
surface; `doctor` stays the raw-row surface.)

### File layout

```
internal/state/state.go        NEW — Classifier (pure), Gather, ClassifyFrame, types, PaneDiffInterval, ReLiveCounter
internal/state/state_test.go   NEW — table-driven unit tests with a fake Paner + fixture transcripts
internal/state/testdata/       NEW — *.jsonl fixtures (below)
cmd/ccpool/state.go            NEW — runState + jsonView + human render; wires real adapters
cmd/ccpool/state_test.go       NEW — pure render tests (human + json) over a state.Result
cmd/ccpool/main.go             EDIT — add "state" to known map + dispatch case
cmd/ccpool/reply.go            EDIT — add LastTurnComplete to transcriptAdapter
packages/claude-transcript/turn_state.go       NEW — LastTurnComplete
packages/claude-transcript/events.go           EDIT — add Message.StopReason
packages/claude-transcript/turn_state_test.go  NEW — LastTurnComplete over fixtures
```

`internal/session/cancel_close.go` EDIT is OPTIONAL and minimal: change `reLiveCounter` to reference
the shared `state.ReLiveCounter` (or leave Unit A untouched and accept one duplicated regex with a
cross-reference comment). The design prefers the shared var; the duplication fallback is explicitly
acceptable to avoid disturbing validated Unit A code.

## Test plan

### Fixtures (`internal/state/testdata/` and `claude-transcript/testdata/`)

Copy a few real transcripts (trimmed to the substantive events + a couple of trailing metadata lines
so the "last line is metadata" property is preserved) and hand-write the synthetic AskUserQuestion
fixture:

| Fixture                    | Source                                                                                               | Property exercised                                                                                                                                                                  |
| -------------------------- | ---------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `completed_idle.jsonl`     | trimmed copy of `~/.claude/projects/-Users-phillipg-phillipg-mbp/*.jsonl` ending assistant+metadata  | last substantive = `assistant` with `stop_reason:"end_turn"` (or `tool_use`), followed by `last-prompt`/`ai-title`/`mode` lines → `LastTurnComplete` = (true, true); drives `idle`. |
| `discarded_thinking.jsonl` | trimmed copy of `~/.claude/projects/-private-tmp-TestContract-Cancel-ThinkingIsUnconfirmed…/*.jsonl` | ONE `user`, ZERO `assistant`, trailing `mode`/`permission-mode` metadata → `LastTurnComplete` = (false, true); with a static pane → `idle`.                                         |
| `awaiting_question.jsonl`  | SYNTHETIC (no real fixture exists)                                                                   | last `assistant` has a `tool_use` `name:"AskUserQuestion"` with `id`, no matching `tool_result` → `IsAwaitingInput` = true; drives `waiting-for-human`.                             |
| `metadata_only.jsonl`      | hand-written: only `mode`/`last-prompt`/`ai-title` lines, zero user/assistant                        | `LastTurnComplete` = (false, false) → exercises the `sawSubstantive=false` brand-new edge.                                                                                          |

The synthetic AskUserQuestion fixture (hand-written, mirrors the real block shape):

```jsonl
{"type":"user","message":{"role":"user","content":"which path?"}}
{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"Let me ask."},{"type":"tool_use","id":"toolu_abc","name":"AskUserQuestion"}]}}
{"type":"mode","mode":"default","sessionId":"x"}
```

(The trailing `mode` line asserts the "skip metadata, find last substantive" path. `awaiting.go`
already handles this fixture's pending-set logic; the new test just pins the ccpool-side wiring.)

### Unit tests — `internal/state/state_test.go` (table-driven, fakes)

A fake `Paner` returning a scripted pair of captures (mirroring `closeTmux`'s scripted-pane pattern
from `cancel_close_test.go`), and the real `claudetranscript` reader over the fixtures (no transcript
fake needed — the fixtures ARE the fake). Table rows:

| Test                                      | Live  | Pane1 / Pane2                               | Transcript fixture         | Row.State  | Expect                                                 |
| ----------------------------------------- | ----- | ------------------------------------------- | -------------------------- | ---------- | ------------------------------------------------------ |
| `not_live_reports_last_known`             | false | —                                           | —                          | `Done`     | `not-live`, LastKnown=`done`                           |
| `inflight_thinking_fast_path`             | true  | counter pane / (unused)                     | (not read)                 | `Working`  | `working`/`thinking`, no 2nd capture                   |
| `inflight_streaming_via_diff`             | true  | prose-A / prose-B (differ, no counter)      | (not read)                 | `Working`  | `working`/`streaming`                                  |
| `inflight_thinking_counter_appears_in_p2` | true  | static / counter pane                       | (not read)                 | `Working`  | `working`/`thinking`                                   |
| `settled_idle_completed_turn`             | true  | static-X / static-X (identical, no counter) | `completed_idle.jsonl`     | `Done`     | `idle`                                                 |
| `settled_idle_discarded_thinking`         | true  | rewound-box / rewound-box (identical)       | `discarded_thinking.jsonl` | `Ready`    | `idle`                                                 |
| `settled_waiting_for_human`               | true  | static / static                             | `awaiting_question.jsonl`  | `Working`  | `waiting-for-human`                                    |
| `settled_error_from_failed_row`           | true  | static / static                             | `completed_idle.jsonl`     | `Failed`   | `error`                                                |
| `waiting_precedes_error`                  | true  | static / static                             | `awaiting_question.jsonl`  | `Failed`   | `waiting-for-human` (3 before 4)                       |
| `brand_new_metadata_only_starting`        | true  | static / static                             | `metadata_only.jsonl`      | `Starting` | `working`/`thinking` (sawSubstantive=false + Starting) |
| `transcript_unreadable_static_pane`       | true  | static / static                             | (path to nonexistent file) | `Ready`    | `idle` (read error tolerated)                          |

Plus a focused `TestClassifyFrame` table for the pure pane logic (counter-in-p1; counter-in-p2;
diff-without-counter; identical-without-counter), and a `TestGather_fastPathSkipsSecondCapture`
asserting `Gather` does NOT call `CapturePane` a second time (nor sleep) when `p1` carries the
counter — proving the single-shot fast path. The `Gather` sleep is the nil-safe injected func so the
test runs with no real delay.

### Unit tests — `claude-transcript/turn_state_test.go`

Mirror `reader_test.go`'s style:

- `LastTurnComplete("testdata/completed_idle.jsonl")` → `(true, true, nil)`.
- `LastTurnComplete("testdata/discarded_thinking.jsonl")` → `(false, true, nil)`.
- `LastTurnComplete("testdata/awaiting_question.jsonl")` → `(true, true, nil)` (assistant `tool_use`
  with `stop_reason` is a complete turn; the _awaiting_ fact comes from `IsAwaitingInput`, not here).
- `LastTurnComplete("testdata/metadata_only.jsonl")` → `(false, false, nil)`.
- `LastTurnComplete("/nonexistent")` → non-nil error (mirrors `transcript_smoke_test.go`).
- Re-assert `IsAwaitingInput("testdata/awaiting_question.jsonl")` = true in this module's existing
  awaiting test set (the fixture lives in claude-transcript testdata; ccpool's copy is for the state
  package tests).

### Unit tests — `cmd/ccpool/state_test.go` (pure render)

Following `list_test.go`'s pure-`render` testing: factor the human + json rendering into pure
`renderState(res state.Result) string` / `renderStateJSON(res) ([]byte, error)` and table-test:

- `working`/`thinking` → `state=working sub=thinking`.
- `idle` → `state=idle` (no `sub=`).
- `not-live` → `state=not-live last_known=done`.
- json `omitempty`: `working` includes `sub_state`; `idle` omits `sub_state` and `last_known`;
  `not-live` includes `last_known`, omits `sub_state`.

### Verification commands

```bash
# unit (fast, hermetic — no real claude, no clock)
go test ./internal/state/ ./cmd/ccpool/ ../claude-transcript/

# contract files compile under the build tag (skipped by gofmt/golangci pre-commit)
go vet -tags contract ./cmd/ccpool/

# full contract run (by hand, ~8-12 min, spends tokens) — see contract/README.md
go test -tags contract -run TestContract ./cmd/ccpool/
```

## Harness-pending conversions

Each conversion is a contract test in `cmd/ccpool/contract_test.go`. `ccpool state <name>` is invoked
via `sb.ccp("state", name)` (or `--json` parsed) and the printed state asserted.

| Test                                                           | Current `pending(...)`                                    | After Unit B                                                                                                                                                                                                                                                                                                                    |
| -------------------------------------------------------------- | --------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `TestContract_Lifecycle_NewReachesReadyAndLive`                | `pending("state is RECONCILED ready …")`                  | After `new alpha` + `doctor` live, run `out,_ := sb.ccp("state","alpha")` → `liveAssert(t, "reconciled state idle", strings.Contains(out, "state=idle"), true)`. Drop the `pending`. (A freshly-ready session is `idle`, not `ready`, in the reconciled vocabulary.)                                                            |
| `TestContract_Cancel_StreamingInterrupts`                      | `pending("session reaches reconciled idle after cancel")` | After the confirmed cancel, `out,_ := sb.ccp("state","s")` → `liveAssert(t, "reconciled idle after streaming cancel", strings.Contains(out, "state=idle"), true)`. Drop the `pending`.                                                                                                                                          |
| `TestContract_Cancel_ThinkingInterrupts`                       | `pending("session reaches reconciled idle after cancel")` | After the confirmed cancel, `out,_ := sb.ccp("state","k")` → `liveAssert(t, "reconciled idle after thinking cancel", strings.Contains(out, "state=idle"), true)`. Drop the `pending`.                                                                                                                                           |
| `TestContract_Send_NoWaitReturnsImmediately`                   | `pending("row is 'working' after --no-wait")`             | After `reply n … --no-wait`, `out,_ := sb.ccp("state","n")` → `liveAssert(t, "reconciled working after --no-wait", strings.Contains(out, "state=working"), true)`. (Sub-state may be `thinking` or `streaming` depending on phase — assert only `state=working`, not the sub, to avoid a flaky phase race.) Drop the `pending`. |
| `TestContract_NeedsInput_AskUserQuestionViaTranscriptFallback` | `pending("row reaches needs_input + question text …")`    | After the picker renders, `out,_ := sb.ccp("state","a")` → `liveAssert(t, "reconciled waiting-for-human", strings.Contains(out, "state=waiting-for-human"), true)`. **Keep a `pending`** for the question TEXT (associated info, DEFERRED). Partial conversion as the handoff specifies.                                        |

Notes:

- `state=` substring matching is robust to the trailing ` sub=…`/` live=…`/` last_known=…` fields and
  mirrors the harness's existing `strings.Contains(out, "ready")` style.
- The two cancel conversions depend on Unit A having flipped the row to `Ready` after a confirmed
  cancel; the reconciled query then sees a static pane + `Ready` row + settled transcript → `idle`.
  This is the cross-check that Unit A + Unit B compose.
- `state=working` for `--no-wait` is correct _before_ the turn settles. The test reads state
  immediately after the `--no-wait` returns (the turn is still running), so the pane is animating and
  `analyzePane` returns in-flight. If the (short) test prompt could finish first, use `thinkingPrompt`
  (it already does) to guarantee a multi-second in-flight window.

## Risks

1. **Streaming-pause false `idle`.** A >`PaneDiffInterval` gap with no pane output (a long tool call
   mid-stream) makes two counter-less frames identical → read as settled → `idle`, though the turn is
   in flight. Mitigation: read-only query, the next call self-corrects; we do not lengthen the window
   (it would slow every query for a rare transient). Same "tool-call residual" Unit A documents.
   ACCEPTED.
2. **No transcript error signal.** `error` derives solely from the store `Failed` row. If the
   StopFailure hook is missing/failed (one of doctor's three hang causes, §20), a genuinely failed
   turn with a static pane reads as `idle`. Mitigation: `doctor` remains the diagnostic for hook
   health; if a structured transcript error signal appears later, step 4 adopts it additively.
   ACCEPTED.
3. **Pane regex drift on a Claude Code upgrade.** `ReLiveCounter` (`\(\d+s · `) and the
   "pane-mutation = liveness" rule are contract-sensitive (the same risk Unit A carries). If a future
   render changes the counter format or updates it less than once per `PaneDiffInterval`, sub-state
   detection degrades. Mitigation: the shared regex is one source of truth (changing it fixes both
   units); the contract suite's phase gates (`reThinking`/`reStreaming`) scaffold-fail on drift,
   surfacing it. FLAGGED.
4. **Two-capture cost.** A settled-session `state` query pays one `PaneDiffInterval` (~350ms) plus two
   captures. The thinking fast-path pays ~0. Acceptable for an interactive single-shot query; no loop.
   ACCEPTED.
5. **Cross-package regex move.** Exporting `ReLiveCounter` from `internal/state` and referencing it
   from `internal/session` touches validated Unit A code. Mitigation: the move is a one-line
   substitution covered by Unit A's existing tests; the documented fallback is to duplicate the regex
   with a cross-reference comment if review wants Unit A untouched. LOW.

## Open questions

1. **Should `LastTurnComplete` be consulted at all (option 1 vs 2 in the extension section)?** The
   doc recommends a minimal use (the `sawSubstantive + Starting → working` clause). If review deems
   the brand-new edge out of scope, drop the clause and keep only the additive `StopReason` field for
   fixture assertions. Either way the `complete` bool is recorded for future associated-info work.
2. **Home for `ReLiveCounter`.** Proposed `internal/state`; an alternative is a tiny shared
   `internal/pane` package if more pane helpers accrue. YAGNI says `internal/state` until a third
   consumer appears.
3. **`PaneDiffInterval` exact value.** 350ms is the midpoint of the handoff's 300-400ms. If live
   validation shows streaming frames are sparser than expected, nudge up (trading query latency).

## Non-goals (YAGNI)

- **Associated info**: the pending question TEXT, last error/reply text — explicitly DEFERRED to a
  later unit (handoff). No fields for them here.
- **No event log, no history, no speculative fields** (handoff convention).
- **Changing `doctor`** to surface the reconciled state — `doctor` stays the raw-row diagnostic;
  `ccpool state` is the reconciled surface.
- **Refactoring Unit A's `confirmStable`** to share a function with `analyzePane` — only the regex +
  the documented liveness rule are shared; the loops stay separate (different stakes/budgets).
- **A `ClassifyPane(single-frame)` public seam** beyond what step 2 needs — Unit A deferred it; we
  add exactly `ClassifyFrame(p1, p2)` and no more.
- **Adding `LastTurnComplete` to `session.Transcript`** — the narrow `state.TranscriptReader` is the
  only port that needs it; `session.Transcript` is unchanged.

## Related decisions

- Builds directly on Unit A (`2026-06-12-ccpool-unitA-cancel-confirmation-design.md`): reuses the
  `reLiveCounter` insight and the pane-mutation-as-liveness rule; realizes Unit A's "Seam note for
  Unit B".
- Sibling: Unit C (`2026-06-12-ccpool-unitC-attend-injection-design.md`).
- Contract harness: `2026-06-11-ccpool-contract-test-harness-design.md` (the `pending`/`liveAssert`/
  `baseline` OUTCOME taxonomy these conversions feed).
