# pr-pool budget/time watchdog — design (chunk B)

**Status:** Draft (2026-06-11)
**Bead:** `pg2-y991`
**Builds on:** chunk A (`pg2-spgx`) — the Go `pr-pool` orchestrator. This chunk
attaches a per-worker-session watchdog to A's per-bead drive loop, using the
B-seams A already shipped (`ccpool.Runner.Cancel`, `Send(…, ModeQueue)`,
`List()`→`TranscriptPath`, and the worker-nudge budget interpolation point).
**Related specs:** `2026-06-11-pr-pool-user-journeys.md` (J9),
`2026-06-11-pr-pool-go-port-design.md` (A), `2026-06-11-pr-pool-worker-contract-design.md`.

## Summary

While a worker session runs, pr-pool tracks its usage against a configurable
**budget** with three independent dimensions — **cumulative tokens**, **estimated
cost**, and **wall-clock time** — and escalates as usage approaches the limit:

- **~72.5%** (reminder) → a queued reminder that the limit is near;
- **90%** (cancel) → cancel the turn + a strong "wrap up now, save your notes,
  finish" message;
- **100%** (hard stop) → second cancel → terminal sequence: reset the worker's
  git worktree (guarded), note the bead, **unclaim** it (re-dispatchable),
  close the session, emit `ErrBudgetExceeded`, and record a structured JSONL
  event.

Escalation fires on **`max(%)` across whichever dimensions are set** (an unset
dimension is _unlimited_ and never escalates). The budget is also stated **in the
worker prompt** so the agent is aware of it.

Token and cost reading depend on the session transcript, which pr-pool obtains
from `ccpool list --json` (`transcript_path`) — that is ccpool enhancement **N3
(`pg2-7mnq.4`), not yet built**. So, exactly like A: the token/cost path is built
against the contract and **mocked in tests**; the **time** dimension needs no
ccpool dependency and is fully live now. The watchdog drops in for real once N3
lands.

## Decisions (settled in brainstorming)

1. **Three budget dimensions: cumulative tokens, estimated cost, wall-clock
   time.** Escalate on the max percentage across the dimensions that are set.
2. **Token meter = cumulative tokens** processed across the whole session
   (Σ over assistant turns of input + cache-creation + cache-read + output), not
   context-window occupancy.
3. **"Unlimited" is a first-class budget value** — representable per dimension; an
   unlimited dimension never escalates. (Until N3, tokens + cost are simply
   unlimited and the time cap carries v1.)
4. **Cost is an estimate** from per-turn tokens × a per-model price table
   (`$/MTok` for input/output/cache-write/cache-read). "Reasonable estimate"
   accepted; the price table is **configurable data**, not buried constants.
5. **A `Budget` data object holds the maxes + thresholds + price table; nothing is
   hardcoded.** It is **passed per session**, so future per-agent / per-session
   limits are "construct a different `Budget`" with no refactor. Today: one budget
   for all workers, built from config defaults.
6. **Token access goes through a B-owned `usage.Reader` interface** (anti-corruption
   boundary, like `ccpool.Runner`). The implementation wraps the shared
   `claude-transcript` library; B depends on its own `Snapshot`, never on the
   library's types. Tests mock `Reader`.
7. **Hard stop → unclaim** (J9): the bead is noted and returned to `open`
   (re-dispatchable next pass to continue), **not** `human`-flagged — budget hits
   are recoverable, unlike A's crash/timeout-`human`.
8. **Hard stop emits a Go error** (`ErrBudgetExceeded`) — for now that is the
   signal ("later we might do more"). It propagates up `workOne` and is logged by
   the drain loop.
9. **Hard stop is also recorded as a structured JSONL event** in **pr-pool's own
   per-run session-event log** — a **net-new component** (A has no structured-log
   seam today, only `slog` to the default logger; `LogDir` + `PR_POOL_LOG_DIR` are
   new). This is pr-pool's log — B never writes to Claude's transcript (read-only).
10. **Worktree path comes from ccpool** (the session's working path), not the
    fragile `<WORKTREE_DIR>/<slug>-pr<n>` naming convention — with a hard safety
    guard (below).
11. **Watchdog scope = worker sessions only.** Feedback cycles are short; J9 is
    worker-only. (The budget object is generic, so extending later is trivial.)

## Architecture & package layout

New packages under `packages/pr-pool/internal/`, plus small additions to A's
`config`, `roles`, `ccpool`, and `orchestrator`:

```
internal/
  usage/                 # B-owned token reading + cost estimation
    usage.go             #   Reader interface + Snapshot{model, components}; Total(); MeterTokens()
    transcript.go        #   claudeTranscriptReader: wraps claude-transcript, aggregates per-turn Usage
    cost.go              #   PriceTable + EstimateCents(Snapshot, PriceTable)
    *_test.go
  budget/                # B-owned budget model + evaluation (pure, no I/O)
    budget.go            #   Limit, Budget, Thresholds, Level; Evaluate(Usage, elapsed) -> (pct, Level)
    budget_test.go
  watchdog/              # ties it together; the goroutine + escalation ladder + terminal sequence
    watchdog.go          #   Watchdog{Reader, CC, BD, Log, Budget, ...}; Run(ctx, sess) -> error
    terminal.go          #   the 100% sequence (guarded reset, note, unclaim, close)
    *_test.go
  eventlog/              # B-owned structured JSONL session-event log (NET-NEW)
    eventlog.go          #   Writer.Emit(event): sync.Mutex around marshal+O_APPEND write
    eventlog_test.go     #   (one writer, many worker watchdogs at cap>1 => mutex required)
```

Changes to A's packages (some additive, some NOT — see **Prerequisites**):

- `internal/ccpool`: add `CWD string` to `Session` (the session's working path,
  from `ccpool list --json`'s `cwd` field). Additive on pr-pool's side; reuse
  existing `Cancel`, `Send(ModeQueue)`, `List()` unchanged.
- `internal/beads`: add a `Comment(ctx, r, id, text)` helper — **it does not exist
  yet** (the package has Status/ShowObj/Ready/Unclaim/AddHuman only).
- `internal/config`: add the budget defaults (token/cost/time maxes — default
  _unlimited_ for token+cost, a finite default for time **strictly below A's
  `MaxWait` (30m)**, e.g. `25m` — see Prerequisite P1/I3), threshold fractions,
  the price table, the reminder/wrap-up message templates, and `LogDir`. All
  `PR_POOL_*`-overridable.
- `internal/roles`: add a `BudgetLine(budget) string` helper (returns the budget
  sentence, "" when fully unlimited). **Keep `Worker.Nudge`'s signature unchanged**
  — the orchestrator appends `BudgetLine` to the worker nudge for worker dispatches
  only. This keeps the blast radius to a pure addition (no edit to `Nudge`,
  `TestWorkerNudge_contract`, or `TestFeedbackNudge_contract`).
- `internal/orchestrator`: `workOne` runs the watchdog **concurrently** with
  `waitDone` for worker dispatches (§Integration) — which requires reworking
  `waitDone` to honor context cancellation (Prerequisite P1).

Import DAG (acyclic): `budget`, `eventlog` are stdlib-only leaves; `usage` depends
on `claude-transcript` (see P2). `watchdog` → {usage, budget, eventlog, ccpool,
beads, roles, config}. `orchestrator` → +watchdog.

## Prerequisites (B is NOT purely additive)

An adversarial review against A's shipped code surfaced four prerequisites the
plan MUST front-load before the watchdog itself:

- **P1 — `waitDone` must honor context cancellation _and_ get a clock seam.** A's
  `waitDone` (`orchestrator.go:106`) loops on `time.Now().Before(deadline)` and
  never reads `ctx`. Rework it to `select` on `ctx.Done()` vs a poll tick. **Two
  non-obvious sub-requirements:**
  - **Clock seam (was missed):** the deadline itself uses real wall-clock
    (`time.Now()`), and A's existing timeout tests rely on a real `MaxWait=50ms`
    deadline with a no-op `o.sleep` (orchestrator_test.go:119,126). A bare ticker
    rework leaves the deadline timing-coupled/flaky. Add an injectable
    `now func() time.Time` (or drive the deadline from `context.WithDeadline`) so
    both `waitDone` and the watchdog poll instantly under test. **Tests to
    re-verify/rewrite:** `TestWaitDone_workerTimeoutAddsHumanNoUnclaim`,
    `TestWaitDone_feedbackTimeoutUnclaims`, `TestWaitDone_paneDiesStillInProgress_failure`.
  - **Structural single-terminal guarantee (not ordering alone):** on `ctx`
    cancellation `waitDone` MUST return `ctx.Err()` and **NOT** call `o.fail`
    (which would `AddHuman`). Run `waitDone` + watchdog under an `errgroup`/
    `context.WithCancel` where the first to reach a terminal result cancels the
    other and **the loser, seeing `ctx.Err() != nil`, returns it without running
    any failure action.** Ordering (**I3:** Time default 25m < `MaxWait` 30m)
    only makes the watchdog _usually_ win first — it does not eliminate the race
    where a just-cancelled `waitDone` iteration still fires `AddHuman` alongside
    the watchdog's unclaim. The skip-action-on-cancel rule is what eliminates it.
- **P2 — importing `claude-transcript` ends pr-pool's stdlib-only/`vendorHash=null`
  status.** Add `require` + `replace github.com/phillipgreenii/claude-transcript
=> ../claude-transcript` to `go.mod`, and rewrite `default.nix` to mirror ccpool:
  root `src` at `./..`, `fileset.unions [ ./. ../claude-transcript ]`, add
  `modRoot = "pr-pool"`. **Determine `vendorHash` empirically** (claude-transcript
  is itself stdlib-only, so `null` may still hold — build and see; do not assume).
- **P3 — `beads.Comment` does not exist.** Add it (`r.Run(ctx, "comment", id,
text)`) before the terminal sequence can note the bead.
- **P4 — `session.ErrCancelUnconfirmed` is ccpool-internal**, unreachable across
  pr-pool's CLI seam. Define a pr-pool-side `ccpool.ErrCancelUnconfirmed` that
  `CLIRunner.Cancel` returns when `ccpool cancel` exits **6**. This is **cheap, no
  runner restructuring**: `CombinedOutput()` already returns `*exec.ExitError`
  wrapped with `%w` (cli.go), so `Cancel` adds `errors.As(err, &exitErr) &&
exitErr.ExitCode() == 6` (the same pattern beads/runner.go already uses). (A
  retry-once-on-any-error fallback is acceptable but unnecessary given how cheap
  the typed path is.)

## The `usage.Reader` interface (anti-corruption boundary)

B defines exactly what it needs; the implementation wraps `claude-transcript`.

```go
package usage

// Snapshot is the token view B needs for one session at one instant. It exposes
// the raw components (so cost and alternate meters stay possible — no corner) and
// the model (for cost). All counts are CUMULATIVE across the session's turns.
type Snapshot struct {
    Model               string
    InputTokens         int
    CacheCreationTokens int
    CacheReadTokens     int
    OutputTokens        int
}

// Total is the cumulative-tokens meter (decision 2): every component summed.
func (s Snapshot) Total() int {
    return s.InputTokens + s.CacheCreationTokens + s.CacheReadTokens + s.OutputTokens
}

// Reader reads a session's cumulative usage from its transcript.
type Reader interface {
    // Read parses the transcript at path and returns cumulative usage. A
    // not-yet-existent / empty transcript yields a zero Snapshot, nil error
    // (the worker hasn't produced a turn yet — that is not an error).
    Read(ctx context.Context, transcriptPath string) (Snapshot, error)
}
```

`transcript.go` implements `Reader` using `claude-transcript`'s **exported types**
(`Event`/`Message`/`Usage` — field names `InputTokens`, `CacheCreationInputTokens`,
`CacheReadInputTokens`, `OutputTokens`, `Message.Model`). Note: claude-transcript
exposes **no event-iterator / parse function** (only `LastAssistantText` /
`IsAwaitingInput`), so B hand-rolls the JSONL scan itself — a `bufio.Scanner` with
a large buffer (`scanner.Buffer(make([]byte, 1<<20), 1<<24)`; transcript lines are
huge), `json.Unmarshal` per line into `claudetranscript.Event`, summing the
`Usage` of **`Event.Type == "assistant"` events only** (decision 2 = "over
assistant turns"; user/system/synthetic lines must be excluded or cache_read
double-counts — mirror `LastAssistantText`'s own assistant filter) and taking the
last non-empty assistant `Model`. The testdata fixture MUST include a non-assistant
line carrying a stray `usage` to prove it is excluded. B owns the aggregation and
depends only on the exported types, not on pa-monitor.

## Cost estimation

```go
// PriceTable maps a model id to its per-MTok prices (USD). Configurable data.
type ModelPrice struct{ InputPerMTok, OutputPerMTok, CacheWritePerMTok, CacheReadPerMTok float64 }
type PriceTable map[string]ModelPrice

// EstimateCents returns the estimated cost in integer cents (int64, matching
// budget.Limit) for a Snapshot. Rounding is explicit (truncate toward zero after
// summing per-component float costs). int64 avoids 32-bit overflow at the
// tens-of-millions-of-tokens magnitudes cache_read reaches. Unknown model ->
// falls back to a configured default price (and the eventlog notes the fallback)
// so a new model id never silently reads as $0. A unit test locks the arithmetic
// at a ~50M-token magnitude.
func EstimateCents(s Snapshot, t PriceTable) int64
```

The default `PriceTable` is a **fresh small literal in `cost.go`** authored from
current published Anthropic per-model pricing. (The review confirmed pa-monitor has
**no** reusable per-token table — it only has subscription dollar-caps and defers
actual cost to the external `ccusage` CLI — so there is nothing to reuse.) The
table lives on the config/Budget so it is overridable and updatable without
touching logic. Estimate need not be exact; an unknown model id falls back to a
configured default price (and the eventlog notes the fallback) so a new model never
silently reads as $0.

## The `Budget` data object

```go
package budget

// Limit is a budget ceiling in its dimension's units (tokens or cents).
// The zero value (or any value <= 0) means Unlimited — that dimension never
// escalates. This makes "unlimited" a first-class, representable value.
type Limit int64
func (l Limit) Unlimited() bool { return l <= 0 }

type Thresholds struct { Reminder, Cancel, Hard float64 } // e.g. 0.725, 0.90, 1.00

type Budget struct {
    Tokens     Limit          // cumulative-token cap (Unlimited until N3)
    Cost       Limit          // estimated-cost cap in cents (Unlimited until N3)
    Time       time.Duration  // wall-clock cap; <= 0 means Unlimited
    Thresholds Thresholds
    Prices     usage.PriceTable
}

type Level int
const ( None Level = iota; Reminder; Cancel; Hard )

// Evaluate returns the current max fraction-of-budget across the set dimensions
// and the escalation Level it implies. An Unlimited dimension contributes 0.
func (b Budget) Evaluate(s usage.Snapshot, elapsed time.Duration) (pct float64, level Level)
```

`Evaluate` is pure (no I/O), so the threshold logic is unit-tested directly with
table cases (each dimension alone, combinations, unlimited dimensions, the
max-selection, and exact boundary values 0.725 / 0.90 / 1.00).

`config.Config` gains `WorkerBudget() budget.Budget` built from
`PR_POOL_BUDGET_*` env + defaults (default: Tokens/Cost **unlimited**, Time **25m**
— strictly below `MaxWait`'s 30m default so the hard stop precedes `waitDone`'s
timeout, per P1/I3 — thresholds 0.725/0.90/1.00, default price table). Today one
budget for all workers; it is passed **per session** into the watchdog.

## The watchdog (integration with A's drive loop)

For a **worker** dispatch, `orchestrator.workOne` runs the watchdog concurrently
with the existing `waitDone`:

```
Ensure -> Send(nudge incl. budget line) -> run { waitDone(bead poll) || watchdog } -> Close (via teardownAll)
```

- The two run under a shared `context`; the **first to reach a terminal outcome
  cancels the other and owns the result.** Normal completion (bead leaves
  `in_progress`) → `waitDone` wins, cancels the watchdog, success. Budget hard
  stop → the watchdog wins, cancels `waitDone`, returns `ErrBudgetExceeded`.
  **Requires P1** (today `waitDone` ignores `ctx` and would run to `MaxWait`
  regardless). The single-terminal-handler discipline + P1's cancellation prevent
  any double-handling (e.g. watchdog-unclaim racing `waitDone`'s timeout-`human`).
- Feedback dispatches keep A's behavior unchanged (no watchdog).

**Watchdog loop** (tick every `PollInterval`; `start = now()` at first tick):

1. Resolve `transcriptPath` for the session from `CC.List()` (by name).
2. `snap = Reader.Read(ctx, transcriptPath)`; `elapsed = now() - start`.
3. `pct, level = Budget.Evaluate(snap, elapsed)`.
4. Escalate by `level`, **each level firing at most once** (monotonic — track the
   highest level reached):
   - `Reminder` → `CC.Send(name, reminderMsg, ModeQueue)`; emit eventlog `reminder`.
   - `Cancel` → `CC.Cancel(name)` (retry once per P4), then
     `CC.Send(name, wrapUpMsg, ModeQueue)`; emit eventlog `cancel`.
   - `Hard` → run the terminal sequence (§Terminal), return `ErrBudgetExceeded`.

Messages (`reminderMsg`, `wrapUpMsg`) are config templates that state the budget
and remaining headroom.

## Terminal sequence (100% hard stop)

In order, each step best-effort (a failure is logged to the eventlog but does not
abort the rest):

1. `CC.Cancel(name)` — the second cancel (idempotent/safe per ccpool: a
   `Ready`/`Done` session is a no-op; retry once per P4).
2. **Reset the worker's worktree (guarded — §Safety):** resolve the session's
   working path from ccpool (`Session.CWD`); reset it only if the guard passes.
3. `beads` note: `bd comment <bead> "interrupted — budget (tokens/cost/time …)"`.
4. `beads.Unclaim(<bead>)` — status `open`, assignee cleared (decision 7).
5. session close is handled by the pass-level `teardownAll` (prefix scan), as in A.
6. emit eventlog `hard_stop` (the budget dimensions + values + the worktree action
   taken).
7. return `ErrBudgetExceeded` (decision 8).

### Worktree-reset safety guard (critical)

A stray `git reset --hard` on the monorepo would be catastrophic. The reset runs
**only if ALL hold** (AND-ed), else it safely no-ops (and the eventlog records
"skipped"). All path comparisons use `filepath.EvalSymlinks` on **both** sides
first (macOS `/var`→`/private/var` etc.) and `filepath.Rel`-based boundary checks,
**never** raw `strings.HasPrefix` (so `/state/worktrees-evil` can't pass as inside
`/state/worktrees`):

- the ccpool-reported path is non-empty **and (after EvalSymlinks) != `cfg.RepoRoot`**;
- the path is **inside** `cfg.WorktreeDir` (`filepath.Rel(WorktreeDir, path)` does
  not start with `..` and is not absolute) — defense in depth;
- **the backstop:** the path is a real git worktree whose **toplevel == the path
  itself** (`git -C <path> rev-parse --show-toplevel` equals `<path>` after
  EvalSymlinks), i.e. a worktree root, never the monorepo root. This clause alone
  guarantees safety even if `WorktreeDir` is mis-set to an ancestor of `RepoRoot`.

When it passes: `git -C <path> reset --hard` then `git -C <path> clean -fd`
(discard unknown code — J9), or `git worktree remove --force <path>` if we prefer
full removal (resolved in the plan; default: reset + clean, leaving the worktree
for the re-dispatched continuation). **Open contract item:** ccpool's `Session.CWD`
must report the path the session actually operates in. A launches with
`--cwd REPO_ROOT` (the worktree doesn't exist yet at launch), so if ccpool only
records the launch cwd, `CWD == REPO_ROOT` and the guard makes reset a safe no-op.
Reporting the **live** path requires a ccpool capability (e.g. tmux
`pane_current_path`) — pinned as a contract item on the ccpool side. The guard
guarantees safety regardless of which ccpool provides.

## Worker nudge budget line

`roles.BudgetLine(budget) string` returns a budget sentence so the agent knows its
limits (J9): e.g. _"You have a budget of up to N tokens / $X / Tm minutes for this
bead; if you receive a 'wrap up' message, commit your notes and finish promptly."_
Unlimited dimensions are omitted; a fully-unlimited budget yields `""`. The
**orchestrator appends `BudgetLine(budget)` to the worker nudge** for worker
dispatches (a pure concatenation at the call site) — `Worker.Nudge`'s signature
and the feedback nudge are untouched (no test blast radius).

## ccpool contract additions (we own ccpool)

No new escalation primitives — `Cancel`, `Send(ModeQueue)`, and `List()` already
exist (A's seams). Note: the **entire `ccpool list --json` capability is unbuilt**
today (`ccpool list` renders only a text table — no `--json`, no transcript/cwd
columns); it is N3 (`pg2-7mnq.4`). So B's live token/cost/worktree paths are
blocked on more than one field. Items to pin on N3:

- `ccpool list --json` exposes `transcript_path` per session — B's token/cost
  source (already pinned in A's contract notes).
- `ccpool list --json` adds a **`cwd`** field per session — the session's working
  path; ideally the **live** path (e.g. tmux `pane_current_path`), since A launches
  with `--cwd REPO_ROOT` and a launch-cwd value makes the worktree reset a safe
  no-op (§Safety). pr-pool maps it to `Session.CWD` (additive on pr-pool's side).

## Phasing & the N3 dependency

- **Live now:** the **time** dimension (no ccpool dependency), the watchdog
  goroutine, the escalation ladder, the terminal sequence, the eventlog, the
  worktree-reset guard, the nudge budget line — all unit-tested with mocked
  `usage.Reader` + `ccpool.Runner` + `beads.Runner`.
- **Blocked on N3 (`pg2-7mnq.4`):** real token/cost reading needs
  `transcript_path` (and `cwd`) from `ccpool list --json`. Until then those
  dimensions are _unlimited_ in the live default budget, and the `usage.Reader`
  CLI path / live verification waits — same boundary as A's live-verify.

## Testing

All with injected fakes — no live processes, no real transcripts:

- `budget.Evaluate`: table tests — each dimension alone, combinations, the
  `max(%)` selection, unlimited dimensions contribute 0, exact boundaries
  (0.725 / 0.90 / 1.00), and the `Limit.Unlimited()` semantics.
- `usage`: a fake transcript (in-memory JSONL fixture) → assert cumulative
  `Snapshot` aggregation + `Total()`; `EstimateCents` table (known model, unknown
  model fallback, cache pricing).
- `watchdog`: fake `Reader` returning a scripted usage ramp + a fake clock →
  assert each level fires **once** in order; reminder→`Send(ModeQueue)`,
  cancel→`Cancel`+`Send`, hard→terminal; `ErrBudgetExceeded` returned; the
  bead-poll-vs-watchdog race (normal completion cancels the watchdog with no
  escalation; hard stop cancels the poll).
- `watchdog` terminal: worktree-reset guard table — skip when path==RepoRoot /
  outside WorktreeDir / not a worktree; reset when valid (assert the `git -C`
  argv via an injected runner); note+unclaim argv; eventlog `hard_stop` emitted.
- `eventlog`: JSONL line shape per event type.
- `orchestrator`: worker dispatch runs the watchdog; feedback does not; a hard
  stop yields the unclaim (not `human`) + `ErrBudgetExceeded` while teardown still
  runs.

`nix flake check` gates as in A (gofmt + golangci-lint + `go test` via doCheck).

## Out of scope

- Live verification (blocked on N3) and any change to A's bash retirement.
- **The worktree reset firing in v1.** It is built + guarded + tested, but because
  A launches sessions with `--cwd REPO_ROOT` and the guard fails closed when the
  reported path == `RepoRoot`, the reset is a guaranteed **no-op until ccpool
  reports the live session cwd** (an N3-adjacent capability). So v1 effectively
  ships cancel + note + unclaim + close + eventlog at the hard stop; the reset
  activates later. The plan must not present worktree-reset as a working v1 action.
- Per-agent / per-session distinct budgets (the object supports it; not wired).
- Pausing/resuming a session under budget pressure beyond the J9 ladder.
- Reacting to the emitted `ErrBudgetExceeded` beyond logging + unclaim ("later we
  might do more").
- Feedback-cycle watchdog.

## Open items (resolve in the plan / with the ccpool work)

- **Exact reminder threshold:** `0.725` (midpoint of J9's "70–75%") unless you
  prefer `0.70` or `0.75`.
- **`Session.CWD` semantics** (launch cwd vs live path) — pinned on the ccpool
  side; the guard keeps B safe either way.
- **Worktree action:** `reset --hard && clean -fd` (keep worktree for the
  re-dispatch) vs `git worktree remove --force` (full nuke). Default: reset+clean.
- **Price-table values:** author the literal table in `cost.go` from current
  Anthropic pricing (no reusable source exists). Confirm the per-model `$/MTok`
  numbers at implementation time.
- **`vendorHash` after adding claude-transcript (P2):** likely stays `null`
  (claude-transcript is stdlib-only) but must be confirmed by building.
- **Cumulative-token cap magnitude (when N3 lands):** because the meter sums
  `cache_read` every turn (Claude re-reads the full cached context each turn), the
  cumulative total reaches **tens of millions** on a large-context, many-turn
  session — dominated by cache reuse, not new work. Set the default `Tokens` cap
  against that magnitude, not per-turn intuition; document the expected scale next
  to the default. (`Snapshot` keeps the raw components, so a future "new-tokens-only"
  meter stays possible.)
