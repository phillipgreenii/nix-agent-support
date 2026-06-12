# ccpool Unit A: Cancel Confirmation via Pane-Stability

**Status**: Draft (revised after adversarial review)
**Date**: 2026-06-12
**Deciders**: ccpool maintainers (design authored on branch `ccpool-observability`)
**Bead**: pg2-33gl (P1)

## Context

`ccpool cancel <name>` and `ccpool reply <name> <msg> --interrupt` interrupt the current Claude
turn by sending a burst of `Escape` keys to the tmux pane, then confirm the interrupt landed by
grepping the captured pane for the substring `"Interrupted"`
(`interruptLanded`, `internal/session/cancel_close.go:30-32`).

This confirmation signal is wrong. The `⎿ Interrupted` marker is rendered **only when a turn is
interrupted during the STREAMING phase**. During the **THINKING phase** (the spinner line, e.g.
`✽ Envisioning… (5s · ↓ 13 tokens · thinking with xhigh effort)`), the Escape burst genuinely stops
the turn — but Claude Code rewinds to its edit-previous-message UI **without ever printing
`Interrupted`**. So `interruptLanded` returns `false`, `cancelLocked` returns
`ErrCancelUnconfirmed`, and the command exits 6 with the row stuck `working`, even though the turn
**was** stopped.

### Hard evidence

Prototype sweep `/tmp/cc-t9/sweep2.log` (12 reps, real `claude`, model `claude-opus-4-8`):

- **9 of 12** thinking-phase cancels: `marker=NO`, `crc=6`, row stuck `working` — a false negative.
- The only 2 that produced the `Interrupted` marker (`crc=0`) had a longer delay and had reached
  STREAMING first. 1 was an early-out (turn finished before cancel).
- Conclusion: **the marker appears only in streaming, never in pure thinking.**

Pane captures:

- `/tmp/cc-t9/sweep2_panes/rep1.pre.txt` — thinking in flight: spinner line
  `✽ Envisioning… (5s · ↓ 13 tokens · thinking with xhigh effort)`.
- `/tmp/cc-t9/sweep2_panes/rep1.post.txt` — after the Escape burst: spinner is **GONE**, **NO**
  `Interrupted` marker, the prompt text is **restored** into the input box
  (`❯ Think step by step…` plus the footer `Ctrl+Y to paste deleted text`). The turn is genuinely
  stopped (no runaway); only ccpool's confirmation signal was wrong.
- `/tmp/cc-t9/sweep_panes/rep8.post.txt` and `rep9.post.txt` — streaming cancelled: the visible pane
  **retains** the rendered conversation (`Thought for 27s`, the `⏺` assistant bullet, the streamed
  prose) plus `⎿ Interrupted · What should Claude do instead?`. `rep8.post.txt` additionally shows a
  `Rewind / Restore the code and/or conversation…` **modal menu**.
- `/tmp/cc-t9/escprobe/ep-stream1.txt` — three captures of a LIVE thinking turn at +0s/+1s/+5s: the
  elapsed counter ticks `26s → 28s → 33s` and the spinner glyph animates `· → ✢ → ✳`. This is the
  ground truth that a live turn **always mutates the visible pane** on a sub-second cadence.

### Why the original "absence-of-activity" mechanism was ALSO wrong (review findings)

The first revision of this design replaced presence-of-`Interrupted` with **absence-of-activity**:
confirm when `ClassifyPane` returns `Quiet = !ReThinking && !ReStreaming`. The adversarial review
found this unsound. All three findings reproduced against the evidence above:

1. **`ReStreaming` matches the PERSISTENT rendered conversation, not just live streaming.**
   `ReStreaming = \b\w+ for \d+s\b|⏺` matches `Thought for 27s` and the `⏺` assistant bullet, both
   of which **remain in the visible pane after a successful streaming cancel** (`rep8.post.txt`,
   `rep9.post.txt`). A cancelled streaming turn therefore classifies `Streaming` **forever** → the
   absence-of-activity poll never sees `Quiet` → exit 6 → this **regresses
   `TestContract_Cancel_StreamingInterrupts`, which works today.** `ReThinking`/`ReStreaming` are
   transient-state DETECTORS for driving the harness to a phase; they are **not liveness
   predicates**, and treating their absence as "the turn stopped" is the category error.
2. **A spinner-counter-presence signal (`(\d+s · [↓↑]`) also fails as a positive liveness check.**
   Genuine prose-streaming (`rep3.pre`) has **no** counter line — the counter is a thinking-phase
   artifact. So "counter absent" cannot mean "stopped"; a live streaming turn would false-confirm.
3. **A wall-clock deadline poll INFINITE-LOOPS in unit tests.** The proposed loop used
   `s.now().Add(budget)`; every existing fake wires a **constant** `Now` (`time.Unix(1, 0)`), and
   the injected `sleep` is a no-op in tests. A deadline-based loop with a frozen clock and a no-op
   sleep never advances past the deadline check → it spins forever. The poll must be **count-bounded**,
   not clock-bounded.

### Why the pane, not the JSONL

The transcript of a cancelled thinking turn
(`~/.claude/projects/-private-tmp-TestContract-Cancel-ThinkingInterrupts*/*.jsonl`) contains
**one `user` event and zero `assistant` events** — the discarded turn was never persisted. So the
JSONL alone cannot distinguish "thinking in flight" from "cancelled-and-rewound"; both look like
user/no-assistant. The **live pane is the only disambiguator** at cancel time. (This is mainly Unit
B's concern, but it is why Unit A must confirm against the pane.)

## Goal

Make `cancel` and `reply --interrupt` correctly confirm an interrupt **in both the thinking and the
streaming phases**, eliminate the stale-marker false positive, and give `reply --interrupt` a
distinct exit code for an unconfirmed cancel — with no new dependencies and a fully unit-testable
confirmation loop that needs **no real `claude` and no clock fake**.

## Decisions

1. **Confirmation = pane-stability, not presence-of-marker and not absence-of-activity.** A live
   turn ALWAYS mutates the visible pane: during thinking the elapsed-seconds counter `(Ns` ticks at
   least every ~1s (observed `5s → 26s → 28s → 33s`) and the glyph animates (`✽`/`✢`/`✳`/`·`);
   during prose-streaming the assistant text appends continuously. A **stopped** turn — interrupted,
   rewound / edit-previous, idle, or sitting on the Rewind/Restore menu — produces a **static** pane.
   So we confirm the cancel landed when **K consecutive `CapturePane` reads are byte-identical** (the
   turn stopped animating).

2. **Render-independence is the whole point.** Pane-stability does **not** depend on `⏺`,
   `Thought for Ns`, `Interrupted`, or any affordance string. It therefore IGNORES the persistent
   markers that broke the absence-of-activity approach (#1 above), and it **dissolves the
   stale-marker false-positive for free**: a stale `Interrupted` line is static text — it does not
   make the pane change, so it cannot cause a premature confirm; and a genuinely-live fresh turn
   keeps the pane animating, so it cannot confirm until it actually stops.

3. **Count-bounded poll, NOT a clock deadline.** Sample the pane every interval `I`, track a
   run-length of identical consecutive captures, confirm when the run-length reaches `K`, and give up
   (→ `ErrCancelUnconfirmed`) after `N` total samples. No `now()` / clock dependency anywhere in the
   loop. This is deterministic under the existing fakes (frozen `Now`, no-op `sleep`) because the
   bound is a call count, not elapsed time.

4. **Defense-in-depth guard.** Additionally require the final (confirming) pane to NOT contain the
   live spinner-counter line — a tight transient pattern `\(\d+s · ` that is present only mid-turn.
   This is _largely_ redundant with stability (a present counter is animating, so the pane is not
   stable), but it makes the intent explicit and gives a second, independent reason to reject a
   pathological "stable while a counter is frozen on screen" capture. It is a guard, not the primary
   signal.

5. **`reThinking` / `reStreaming` are phase-drive regexes, not liveness predicates.** They stay in
   the contract harness as transient-state detectors used only by `waitForThinking` /
   `waitForStreaming` to DRIVE a session to a phase. Unit A's confirmation does **not** route through
   them. (See "Seam note for Unit B" — a product `ClassifyPane` is explicitly out of scope here.)

6. **Distinct exit code 6 for `reply --interrupt` unconfirmed.** In `reply.go`, add an
   `errors.Is(err, session.ErrCancelUnconfirmed)` branch **before** the generic `return 1`, reusing
   exit code **6** (identical meaning to standalone `cancel`'s unconfirmed) for cross-command
   consistency, via the existing `cancelExitCode` helper. Plumbing is confirmed: `sendLocked` wraps
   the cancel error as `fmt.Errorf("interrupt: %w", err)` (`send.go:51`), so `errors.Is` sees through
   it. The distinct exit 6 fires only when confirmation genuinely fails — which, post-fix, a live
   thinking interrupt no longer does.

## Architecture

### File layout

| File                                    | Change                                                                                        |
| --------------------------------------- | --------------------------------------------------------------------------------------------- |
| `internal/session/cancel_close.go`      | Delete `interruptLanded`; add `confirmStable` (count-bounded pane-stability poll) + tunables. |
| `internal/session/cancel_close_test.go` | Change the fake's `CapturePane` from one fixed pane to a **scripted sequence**; add cases.    |
| `cmd/ccpool/reply.go`                   | Add `ErrCancelUnconfirmed` → exit 6 branch before the generic `return 1`.                     |
| `cmd/ccpool/contract_test.go`           | Convert the four affected asserts (see conversion table).                                     |
| `cmd/ccpool/contract_harness_test.go`   | Keep `reThinking`/`reStreaming` as phase-drive regexes; rename the interrupt probe constant.  |

No new file, no new package, no new exit-code value (YAGNI). The classifier file
(`paneactivity.go`) proposed by the first revision is **not** created — pane-stability is purely a
byte-comparison loop and needs no regex classifier.

### `internal/session/cancel_close.go` (proposed change)

Delete `interruptLanded` and its hypothesis comment entirely. Add the tunables and the
count-bounded pane-stability poll, then call it from `cancelLocked` in place of the old
single-capture verify block.

```go
import "regexp" // new import for the defense-in-depth guard

const (
	// Pane-stability confirmation tunables (see "Confirmation algorithm" for the
	// full rationale). The loop confirms a cancel when cancelStableRun consecutive
	// CapturePane reads are byte-identical (the turn stopped animating), and gives
	// up after cancelMaxSamples total reads.
	cancelStableInterval = 400 * time.Millisecond // gap between captures (I)
	cancelStableRun      = 4                       // identical consecutive reads to confirm (K)
	cancelMaxSamples     = 16                      // total captures before giving up (N)
)

// reLiveCounter matches the live thinking spinner's elapsed-seconds counter,
// e.g. "(5s · ↓ 13 tokens · thinking…". It is present ONLY mid-turn (it ticks
// each ~1s). Used ONLY as a defense-in-depth guard on the confirming pane — NOT
// as the primary signal. This is intentionally tighter than the harness's
// reThinking phase-drive regex.
var reLiveCounter = regexp.MustCompile(`\(\d+s · `)

// confirmStable polls the pane until it is STATIC — cancelStableRun consecutive
// CapturePane reads are byte-identical — which means the turn stopped animating.
// A live turn always mutates the pane (the thinking counter ticks ≥ ~1/s and the
// glyph animates; streaming text appends), so it can never accumulate K identical
// reads; a stopped/rewound/idle turn renders nothing new, so it does.
//
// This is render-independent: it does NOT look for "Interrupted", "⏺",
// "Thought for Ns", or any affordance string, so it works in BOTH the
// thinking-rewind path (rep1.post.txt) and the streaming-interrupt path
// (rep8/rep9.post.txt, whose panes RETAIN those markers as static text) and is
// immune to a stale "Interrupted" line in scrollback.
//
// Count-bounded (cancelMaxSamples), NOT clock-bounded: the existing fakes freeze
// Now and no-op Sleep, so a wall-clock deadline would infinite-loop. The bound is
// a capture count, so the loop is deterministic under those fakes.
func (s *Service) confirmStable(tmuxName string) (bool, error) {
	prev, err := s.d.Tmux.CapturePane(tmuxName)
	if err != nil {
		return false, fmt.Errorf("verify cancel: %w", err)
	}
	run := 1 // we have one sample so far
	for i := 1; i < cancelMaxSamples; i++ {
		s.sleep(cancelStableInterval) // nil-safe no-op in tests
		cur, err := s.d.Tmux.CapturePane(tmuxName)
		if err != nil {
			return false, fmt.Errorf("verify cancel: %w", err)
		}
		if cur == prev {
			run++
		} else {
			run = 1
			prev = cur
		}
		if run >= cancelStableRun && !reLiveCounter.MatchString(cur) {
			return true, nil
		}
	}
	return false, nil
}
```

Then in `cancelLocked`, replace the old capture-and-`interruptLanded` block
(`cancel_close.go:66-73`) with:

```go
	confirmed, err := s.confirmStable(tmuxName)
	if err != nil {
		return err
	}
	if !confirmed {
		return ErrCancelUnconfirmed // row left as-is (working); caller fails safely
	}
	_, err = s.d.Store.Transition(ctx, name, store.Ready, "", "")
	return err
```

**No `s.now()` helper is needed or added.** The loop is count-bounded, so it never reads the clock.
`s.sleep` stays the injected nil-safe wrapper (`session.go:226`); in tests it is a no-op, so the
loop runs at full speed against the scripted pane sequence — exactly what we want for a hermetic
count-based test. The existing burst (`escapeBurst = 3`, `escapeSpacing = 200ms`) and `clearInput`
are unchanged; `confirmStable` runs after them.

> **Sticky-last-value note for the fake (see test plan):** because tests drive the loop with a
> scripted slice, the fake's `CapturePane` returns the LAST scripted pane once the script is
> exhausted, so a "goes stable" sequence reaches `cancelStableRun` identical reads naturally and an
> "always changing" sequence (each read distinct) never does. Real tmux behaves the same way: a
> stopped turn keeps returning the same bytes.

### `cmd/ccpool/reply.go` (proposed change)

```go
	res, err := svc.Send(context.Background(), name, prompt, mode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reply:", err)
		if errors.Is(err, session.ErrBusy) {
			return 5 // dedicated "busy" exit code (spec §12/§20)
		}
		if errors.Is(err, session.ErrCancelUnconfirmed) {
			// --interrupt could not confirm the cancel; mirror standalone
			// `cancel`'s exit 6 instead of collapsing into the generic 1
			// (exit 1 stays the catch-all; a specific outcome needs its own code).
			// sendLocked wraps the cancel error as "interrupt: %w", so errors.Is
			// sees through the wrap (send.go:51).
			return cancelExitCode(err)
		}
		return 1
	}
```

`cancelExitCode(err)` already maps `ErrCancelUnconfirmed` → 6 and is in the same `package main`
(`cancel.go:47`), so it is reusable directly. (If the team prefers not to couple `reply.go` to a
helper named for `cancel`, an inline `return 6` is equivalent; reusing the helper keeps the mapping
in one place.)

### Seam note for Unit B (do NOT build now)

If Unit B wants a product `ClassifyPane(pane) (thinking | streaming | idle)` seam, note that its
`Streaming` arm needs a **liveness-bearing** pattern (e.g. the live counter, or a frame-to-frame
diff), **not** the static `⏺` / `Thought for Ns` markers — those persist after a cancel and are not
liveness. Unit A's CONFIRMATION deliberately routes through pane-stability, not through any
`ClassifyPane`, precisely to avoid this trap. This is a one-line forward note only; do not design or
add `ClassifyPane` here (YAGNI).

## Confirmation algorithm (concrete parameters + rationale)

| Parameter              | Value   | Rationale                                                                                                                                                                                                                                                                                  |
| ---------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `cancelStableInterval` | `400ms` | The gap `I` between captures. Chosen with `K` so the stability window `(K-1)*I` exceeds the ~1s max live-quiet period (see below). 400ms is also brisk enough that the happy path confirms in about a second.                                                                              |
| `cancelStableRun`      | `4`     | Identical consecutive reads `K` to confirm. Stability window `(K-1)*I = 3 × 400ms = 1.2s > 1s`. The thinking counter ticks at least every ~1s (observed `26s → 28s → 33s`); over a 1.2s window a LIVE turn is guaranteed to change at least once, so it can never reach 4 identical reads. |
| `cancelMaxSamples`     | `16`    | Total captures `N` before giving up. Budget ≈ `(N-1)*I = 15 × 400ms = 6s`, comfortably above the 1.2s confirm window so a turn that stops slightly late still confirms, while a genuinely-live turn fails fast enough not to feel hung (CLI stays interactive at <7s).                     |

**The load-bearing inequality.** The stability window must EXCEED the maximum interval over which a
live turn could appear unchanged: `(K-1)*I > liveQuietMax`. The thinking counter's worst-case quiet
period is ~1s (it ticks each whole second; `ep-stream1.txt` shows `26s/28s/33s` and the glyph also
animates between ticks). With `K=4`, `I=400ms` the window is **1.2s > ~1s**, so a live thinking turn
**cannot** produce 4 byte-identical consecutive captures. If a future model rendered a counter that
updated less often than once per second, `K` or `I` would need to grow to keep `(K-1)*I` above that
period — flagged in Risks.

**Cost on the happy path** (turn already stopped — rewound, idle, or post-streaming-cancel static
pane): the first capture plus 3 more identical captures `400ms` apart → confirmed at the 4th sample,
roughly **1.2s**. **Cost on a true miss** (turn genuinely still animating): the run-length keeps
resetting, so it never reaches 4 → exhausts all 16 samples (~6s) → `ErrCancelUnconfirmed`.

**Why byte-identity (not a fuzzy match).** tmux `capture-pane -p` of a stopped turn is
deterministic — the same cells render the same bytes. We do not need to tolerate cursor blink or
clocks: Claude Code's static post-cancel pane has no animated element (verified: `rep1.post.txt`,
`rep8.post.txt`, `rep9.post.txt` are inert renders). The live counter and glyph are the only
animated elements, and they are exactly what we are keying off the ABSENCE of.

**Defense-in-depth guard.** Even at `run >= K`, we also require `!reLiveCounter.MatchString(cur)`.
This is largely redundant (a live counter ticks, so the pane would not be stable), but it makes the
intent explicit and rejects a pathological capture where a counter is frozen on screen yet
byte-stable. It is cheap and additive; it never causes a false MISS on a legitimately stopped turn
(those panes have no `(Ns · ` counter).

**Existing burst timing is unchanged.** `escapeBurst = 3` and `escapeSpacing = 200ms` stay as-is;
the burst already spans the thinking→streaming window (a single Escape missed 1/7 in live
verification).

## Behavior change and harness-assert conversion

The central behavior change: **`cancel` and `reply --interrupt` during THINKING now SUCCEED** (the
pane stops animating → confirmed) and `--interrupt` then **delivers the new prompt**. The conversion
table below re-derives each affected contract assert **under pane-stability**.

| Test                                           | Today (baseline)                                                                    | After fix (re-derived under pane-stability)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| ---------------------------------------------- | ----------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `TestContract_Cancel_ThinkingIsUnconfirmed`    | `baseline(pg2-33gl, exit, 6)` + `pending(...)`                                      | Cancel during thinking now confirms (the pane goes static once the spinner stops) → exit **0**. Convert to `liveAssert(t, "cancel during thinking exits 0", code, 0)`. **Rename** to `TestContract_Cancel_ThinkingInterrupts` (the old name asserts the bug). Drop the `pending`; keep the reconciled-state check as a `pending` only if state verification is still unavailable (Unit B).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `TestContract_Cancel_StreamingInterrupts`      | `liveAssert(exit, 0)` + `pending(reconciled idle)`                                  | **Unchanged — must still exit 0**, now justified via **pane-stability** (NOT the old, since-disproven claim that the post-cancel prose scrolled off / matched nothing). After a streaming cancel the pane is **static**: prose stopped appending, `⎿ Interrupted` is shown, no glyph/counter animates (`rep8.post.txt`, `rep9.post.txt`). It does NOT matter that those panes still contain `Thought for 27s`/`⏺` — those are static text and stability ignores them. Keep the `liveAssert`. The reconciled-idle `pending` stays (Unit B).                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `TestContract_Cancel_StaleMarkerFalsePositive` | `baseline(pg2-33gl, exit, 0)` (false positive off stale `Interrupted`)              | **KEEP it** (do not retire). Re-derive why it is now safe under pane-stability: the stale `Interrupted` line is **static text** — it does not animate the pane, so it cannot cause a premature confirm; the fresh thinking turn keeps the pane animating until the burst stops it, so confirmation only fires once the new turn actually stops. Assert the second cancel confirms correctly: `liveAssert(t, "stale Interrupted line does not affect cancel", code, 0)`. **Rename** to `TestContract_Cancel_StaleMarkerIgnored`. The `reInterrupted`-scrolled-out scaffold precondition (`contract_test.go:98-105`) is **no longer needed** (stability does not depend on the marker being present) — **drop it**. **Add a regression assert** that the post-streaming-cancel pane STILL contains the static markers, locking in that the predicate ignores them: `liveAssert(t, "post-streaming-cancel pane retains static markers (predicate ignores them)", reStreaming.MatchString(sb.cap("m")), true)`. |
| `TestContract_Interrupt_ThinkingAborts`        | `baseline(pg2-33gl, exit, 1)` + "probe must NOT deliver" + `pending(distinct code)` | **INVERT.** Interrupt during thinking now cancels (pane goes static) + delivers → exit **0**, and the probe **DOES** deliver. **Rename** to `TestContract_Interrupt_ThinkingCancelsAndDelivers`. Asserts: `liveAssert(t, "interrupt during thinking exits 0", code, 0)` and flip the paste check to `liveAssert(t, "interrupt delivers the probe after cancelling", strings.Contains(sb.cap("x"), "PROBE_SHOULD_DELIVER"), true)`. **Rename the probe constant** from `PROBE_MUST_NOT_DELIVER` to `PROBE_SHOULD_DELIVER`. **Drop** the distinct-exit-code `pending` — the exit-6-on-unconfirmed path is now implemented and covered by a fake-driven unit test; a live thinking interrupt no longer hits it (it succeeds).                                                                                                                                                                                                                                                                                  |

**Why the streaming-cancel justification had to change.** The original design claimed the
post-streaming-cancel pane classified `Quiet` because the prose "scrolled off" and matched no
liveness regex. The evidence (`rep8.post.txt`, `rep9.post.txt`) **disproves** this: the pane retains
`Thought for 27s` and `⏺`, both of which `reStreaming` matches. The correct justification is
pane-stability: those markers are **static**, the pane stops changing after the cancel, so stability
confirms regardless of what static text remains.

### Unit-test assert conversions (in `internal/session/cancel_close_test.go`)

| Existing unit test                                          | Conversion                                                                                                                                                                                                                                                                                                                                                                                       |
| ----------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `TestCancel_burstThenVerify_landed_resetsReady`             | Fake `pane` was the single string `"  ⎿  Interrupted · …"`. Change the fake to a **scripted sequence** that goes stable, e.g. `[active1, active2, stable, stable, stable, stable]` (a couple of changing captures, then ≥ `cancelStableRun` byte-identical), so `confirmStable` confirms and the row resets to `Ready`. Keep the Escape-count assert.                                            |
| `TestCancel_burstThenVerify_missed_staysWorking_returnsErr` | Fake `pane` was the single string `"  42. still streaming a fact..."` — under stability a single fixed pane would be byte-stable and FALSELY confirm. Change the fake to an **always-changing** sequence (each capture distinct, e.g. an incrementing counter line like `rep1.pre.txt` with a ticking `(Ns`), so the run-length never reaches `K` → `ErrCancelUnconfirmed`, row stays `working`. |
| `TestSendInterrupt_abortsOnUnconfirmedCancel`               | Same fix — drive an **always-changing** sequence so the cancel cannot confirm; assert `ErrCancelUnconfirmed` and that nothing was pasted. (Validates that an unconfirmed cancel still aborts `--interrupt` before delivering.)                                                                                                                                                                   |

## Test plan

### Fake `Tmux` shape — scripted pane sequence (no clock fake)

The current `closeTmux` fake returns one fixed pane via a `pane string` field and a
`CapturePane(string) (string, error)` that returns it (`cancel_close_test.go:20,37`). Change it to
return a **scripted sequence**, indexed by call count, last value sticky:

```go
type closeTmux struct {
	// ... existing fields (live, killed, keys, pasted, goneAfter, calls) ...
	panes    []string // scripted CapturePane sequence; last element is sticky
	capCalls int
}

func (c *closeTmux) CapturePane(string) (string, error) {
	if len(c.panes) == 0 {
		return "", nil
	}
	i := c.capCalls
	if i >= len(c.panes) {
		i = len(c.panes) - 1 // sticky last value (a stopped turn keeps the same bytes)
	}
	c.capCalls++
	return c.panes[i], nil
}
```

A slice-plus-index is preferred over a `func` field for readability and because the sticky-last
behavior models real tmux (a stopped turn keeps returning identical bytes). Existing `Close*` tests
that never set `panes` keep working: an empty slice returns `""` (and `Close` does not call
`CapturePane`). **No clock fake is required**: the loop is count-bounded, so `Now` is irrelevant and
the no-op `sleep` simply makes the loop run at full speed through the scripted slice.

Two canonical sequences exercise both outcomes:

- **Goes stable → confirms:** `[active, active, stableA, stableA, stableA, stableA]` (or rely on the
  sticky-last value: `[active, active, stableA]` then the loop reads `stableA` repeatedly). Once
  `cancelStableRun` identical reads accumulate and the live-counter guard passes, `confirmStable`
  returns `true`.
- **Always changing → exhausts → `ErrCancelUnconfirmed`:** every element distinct (e.g. a ticking
  `(Ns` counter line), enough elements that none repeats `cancelStableRun` times within
  `cancelMaxSamples`. To keep it always-changing past the slice end, make the final two elements
  differ AND ensure the sticky-last value differs from the one before it is reached — simplest is to
  provide `cancelMaxSamples` distinct elements so the slice never goes sticky during the run.

### New / updated unit tests — `internal/session/cancel_close_test.go`

- `TestCancel_confirmsWhenPaneGoesStable`: scripted `[active1, active2, stableA, …(sticky)]` → confirmed,
  row `Ready`. Assert the loop polled more than once (`tm.capCalls > 1`) and the row is `Ready`.
- `TestCancel_neverStableStaysWorking`: an **always-changing** sequence (`cancelMaxSamples` distinct
  panes, e.g. a ticking counter) → `ErrCancelUnconfirmed`, row stays `working`. Assert it stopped at
  the sample bound (`tm.capCalls == cancelMaxSamples`).
- `TestCancel_liveCounterBlocksFalseConfirm` (guard): a pane that repeats byte-identically but
  CONTAINS a `(\d+s · ` counter line → must NOT confirm (the defense-in-depth guard rejects it) →
  `ErrCancelUnconfirmed`. (Documents the guard's intent even though stability alone would usually
  catch a live turn via animation.)
- Plus the three conversions above.

All use the existing fake-Tmux pattern; **no real `claude`, no clock fake**.

### New unit tests are NOT needed for a `ClassifyPane` classifier

The first revision proposed `paneactivity_test.go` for a regex classifier; that file is **not**
created. Pane-stability is a byte-comparison loop with one tiny guard regex (`reLiveCounter`), tested
inline by the loop tests above plus one focused guard test.

### Contract suite (`-tags contract`, run by hand, not CI)

Flips, per the conversion table:

- `TestContract_Cancel_ThinkingIsUnconfirmed` → `TestContract_Cancel_ThinkingInterrupts`,
  `baseline 6` → `liveAssert 0`.
- `TestContract_Cancel_StreamingInterrupts` → unchanged (`liveAssert 0` still holds, now justified by
  pane-stability).
- `TestContract_Cancel_StaleMarkerFalsePositive` → `TestContract_Cancel_StaleMarkerIgnored`,
  `baseline 0` → `liveAssert 0`; drop the `reInterrupted` scaffold precondition; add the
  "pane retains static markers" regression `liveAssert`.
- `TestContract_Interrupt_ThinkingAborts` → `TestContract_Interrupt_ThinkingCancelsAndDelivers`,
  `baseline 1` → `liveAssert 0` + probe-delivers `liveAssert true`; rename the probe constant to
  `PROBE_SHOULD_DELIVER`.

Harness regexes: in `contract_harness_test.go`, **keep** `reThinking` / `reStreaming` as the
phase-drive detectors for `waitForThinking` / `waitForStreaming` — they are NOT liveness predicates
and Unit A's confirmation does not use them. `reInterrupted` becomes unused once the stale-marker
precondition is dropped **except** for the new regression assert, which uses `reStreaming` (the
static `⏺`/`Thought for Ns`); if `reInterrupted` is left unused, remove it to satisfy the compiler.
**Verify** the contract files compile with `go vet -tags contract ./cmd/ccpool/`.

### Verification commands

```bash
# unit (fast, hermetic — no real claude, no clock fake)
go test ./internal/session/... ./cmd/ccpool/...
# contract files compile (build-tagged; skipped by gofmt/golangci pre-commit)
go vet -tags contract ./cmd/ccpool/
# full contract run (by hand, ~8-12 min, spends tokens) — see contract/README.md
nix run .#ccpool-contract
```

## Real-claude validation requirement (NEW — must run before claiming the fix)

The prototype captures that ground this design are **burst-only**: they fired the Escape burst and
captured the pane, but they did **not** exercise ccpool's full cancel path, which also runs
`clearInput` (C-u) after the burst, nor did they verify the session is usable for the NEXT `reply`.
A thinking-phase triple-Escape was observed to land in **either** of two end states:

- prompt **restored into the input box** (`rep1.post.txt`), or
- a **modal `Rewind / Restore the code` menu** (`rep8.post.txt`).

Pane-stability confirms BOTH (both are static), which is correct for "the turn stopped." But the
following are **NOT determinable from the prototype captures** and **MUST be validated against real
`claude`** via the contract scenarios before this fix is claimed done:

1. **Post-cancel session state.** Is the session left sitting in the modal rewind/restore menu? Does
   ccpool's `clearInput` (C-u) dismiss that menu, or is C-u inert / does it do something unexpected
   inside the menu? Validate by inspecting `sb.cap(name)` after `cancel` in
   `TestContract_Cancel_ThinkingInterrupts`.
2. **Does the NEXT `reply` work after a cancel?** This is the real acceptance test for
   `--interrupt`: `TestContract_Interrupt_ThinkingCancelsAndDelivers` must show the probe
   (`PROBE_SHOULD_DELIVER`) actually delivered and answered, not swallowed by a lingering menu.
3. **The tool-call phase.** The prototype captured thinking and streaming; it did **not**
   characterize the tool-call phase. Assumption: a tool call also animates the pane (spinner / output
   stream), so pane-stability behaves correctly. **Validate** this assumption against real `claude`;
   if a tool call can render a sustained static pane while still live, `confirmStable` could
   false-confirm (see Risks).

**Do NOT speculatively add keystrokes now.** If real-claude validation shows the session wedged in a
modal menu that `clearInput` does not dismiss, a menu-dismissal keystroke (e.g. an extra `Escape` or
`q`) may need to be added to `cancelLocked` — but let validation drive that; do not pre-add it. This
is the single largest residual unknown and is called out in Risks.

## Edge cases

- **Stale `Interrupted` in scrollback.** Dissolved by construction: stability keys off the pane
  _changing_, and a stale `Interrupted` line is static text. It cannot cause a premature confirm.
- **Live thinking counter / glyph animation.** Caught by the `(K-1)*I = 1.2s > ~1s` window: a live
  turn changes within the window, so it can never reach `cancelStableRun` identical reads.
- **Turn finishes on its own before cancel** (the sweep "early-out"): the pane is already static →
  confirms within `cancelStableRun` samples → `Ready`. Correct.
- **Already-idle session.** `cancelLocked` still short-circuits at the top (`Ready`/`Done` →
  normalize to `Ready`, no burst/poll). Unchanged.
- **Post-streaming-cancel pane retains `⏺` / `Thought for Ns` / `Interrupted`.** Irrelevant — those
  are static; stability ignores text content entirely. (This is exactly the case that broke the
  absence-of-activity approach and is now handled cleanly.)
- **`CapturePane` error mid-poll.** Propagated as `fmt.Errorf("verify cancel: %w", err)` (existing
  behavior preserved); the caller fails safely, row stays `working`.
- **Nil `Sleep` in fakes.** The `sleep` wrapper is nil-safe (`session.go:226`); the loop then spins
  at full speed against the scripted pane sequence — fine for count-based tests. **No `Now` is read**,
  so a nil/frozen `Now` is irrelevant.

## Risks

1. **A still-live turn could render a sustained STATIC pane → false confirm.** Highest residual risk.
   Pane-stability assumes a live turn always mutates the pane within the `(K-1)*I` window. The
   thinking and streaming phases provably do (counter/glyph; appending prose). The **tool-call phase
   is unvalidated** — if a long tool call renders nothing new for >1.2s while still live,
   `confirmStable` could confirm prematurely. Mitigated by: the Escape burst genuinely interrupts
   (it does not merely pause), the count-bounded budget, and the real-claude validation item above.
   Worth a code comment.
2. **Model render change could lengthen the live-quiet period.** If a future Claude Code renders a
   counter that updates less often than ~1/s (or removes the counter), the `(K-1)*I > liveQuietMax`
   inequality could break and a live turn could go stable for a window. Mitigated: `I`/`K`/`N` are
   named constants in one place, re-tuning is a one-line change, and the contract suite is the
   feedback loop. The defense-in-depth `reLiveCounter` guard is a second line for the counter case
   specifically.
3. **Post-cancel modal-menu state is unverified** (see Real-claude validation). If real-claude leaves
   the session in a Rewind/Restore menu that `clearInput` does not dismiss, the NEXT `reply` could
   misbehave even though `cancel` correctly returns 0. This is a session-usability risk orthogonal to
   confirmation correctness; the contract scenarios are the gate.
4. **Tuning is empirical (n=12 sweep + one escprobe trace).** `400ms / 4 / 16` are justified against
   the observed ~1/s counter cadence (`26s → 28s → 33s`) but not exhaustively swept across
   models/load. Named constants; contract suite is the feedback loop.

## Open questions

1. **Should `confirmStable` distinguish WHY it gave up** (always-animating vs. exhausted-just-short)
   in the error? Today both map to a single `ErrCancelUnconfirmed`. A richer error would aid
   debugging but expands scope; deferred unless cheap.
2. **First-capture timing after the burst.** The design captures immediately after `clearInput` and
   relies on the loop's own `cancelStableInterval` for settling. A single-frame race on the very
   first capture is theoretically possible but harmless — it just costs at most one extra
   non-matching sample before the run accumulates. No settle delay added.

## Non-goals (YAGNI)

- **Unit B's reconciled classifier** (pane + JSONL + store generation) and the JSONL event log.
  This design defines only Unit A's pane-stability confirmation. Do **not** add JSONL/store-state
  inputs, and do **not** add a product `ClassifyPane` here (only the forward note above).
- **Reconciled state queries** for the contract suite (the `pending(...)` "reconciled state query"
  items stay pending; they are Unit B / v2 observability).
- **Changing the Escape burst** count/timing.
- **Speculative menu-dismissal keystrokes** — gated on real-claude validation.
- **A new exit code value** — reuse 6 for cross-command consistency.
- **A new Go package or a `paneactivity.go` classifier file** — the stability loop lives in the
  existing `cancel_close.go`.

## Related decisions

- Contract harness outcome buckets and the `baseline`/`liveAssert`/`pending`/`scaffoldFail`
  protocol: `packages/ccpool/contract/README.md`.
- Project exit-code rule (exit 1 = generic catch-all; specific outcomes get codes `>= 2`): enforced
  here for `reply --interrupt` (exit 6).
- **Superseded approaches** (this revision): presence-of-`Interrupted` (original; misses thinking
  cancels) and absence-of-activity / `ClassifyPane` `Quiet` (first revision; regresses streaming
  cancel because `reStreaming` matches the persistent rendered conversation). Both rejected in favor
  of pane-stability. The affordance-matching alternative (match the Rewind/Restore menu or the
  restored-input footer) was also considered and **rejected** as render-fragile (string-couples to
  TUI affordances that change between Claude Code versions) and because it does NOT dissolve the
  stale-marker case for free the way render-independent stability does.
