# pa-monitor Corpus Monitor — Phase 1b Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans (inline, this session) to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fold the `UsagePricing` (5h-block cost + weekly cost) and `Limits` (rate_limits window-peak) reads into the corpus `Monitor`'s single per-tick pass so a transcript line is decoded once for both the `SessionSnapshot` fold and pricing-record extraction, and the whole-corpus `WalkDir` (pricer) + status-sibling `ReadDir` walk (limits) are replaced by criteria-gated, mtime-windowed, offset-tailed reads owned by the Monitor — with production block/weekly/limits read from Monitor projections behind the existing `Poller.UseCorpusMonitor` flag, gated by an extended equivalence suite.

**Architecture:** The Monitor's `Scan(now)` gains (a) an mtime-windowed enumeration of transcript files across the whole `~/.claude/projects` tree (superset of active sessions) feeding a new `UsagePricingObserver`, and (b) an enumeration of `*.status.jsonl` siblings feeding a new `LimitsObserver`. Tails are re-keyed by **path** (one `transcript.Accumulator` per path) so a file that is both an active session's resolved transcript and a pricing-window file is read exactly once, feeding both the Snapshot fold and pricing records from a single decode. The ADR-0029 limits fold + `statusRecord` parsing move into a new leaf package `internal/core/limits` (so both the corpus observer and the daemon port share one implementation, no import cycle). The pure windowing functions `usage.ActiveBlock`/`usage.CurrentWeekly` and the moved `limits.Current` are reused verbatim — the observers only own the _record sets_, not the math. Everything stays **synchronous on the tick goroutine** (zero new concurrency; producer goroutine is phase 3). The old inline pricer/limits/weekly reads stay behind the flag for exactly this phase's equivalence diff; a follow-up bead removes the flag + inline path + old-path test arms after a live soak.

**Tech Stack:** Go 1.25 (`packages/pa-monitor`), gomod2nix build engine, `github.com/phillipgreenii/claude-transcript` sibling dep (`replace => ../claude-transcript`), nix flake (`nix build .#pa-monitor`, `pa-monitor-go-tests` flake check), OTel via the nil-safe `poller.PhaseRecorder`/`corpus.Recorder` seam.

## Global Constraints

- **No new external deps.** Add nothing to `go.mod`/`gomod2nix.toml`.
- **Import direction (no cycles):** `corpus` MAY import `session`, `transcript`, `usage`, the new `limits`, and `claude-transcript` (`ct`); none of those may import `corpus`. `transcript` already imports `usage` (snapshot.go:14) — that stays. The new `limits` package is a leaf: it imports only stdlib + `session` (for `IsStatusSiblingFile`). `daemon` MAY import `limits` and `corpus` (via `poller`); `limits`/`corpus` MUST NOT import `daemon`.
- **`Event` placement correction (vs epic DESIGN §1):** the design sketched `corpus/event.go` with `Observer.OnLine(*Event)`. Placing `Event` in `corpus` while `transcript.scanState.feed` consumes it would require `transcript`→`corpus`, a cycle (corpus imports transcript). **The decoded-line `Event` type lives in `transcript`** (it is the transcript-line vocabulary), and the single decode happens inside `transcript.scanState` (which already centralizes the one `json.Unmarshal` per line). The generic per-observer `OnLine(*Event)` firehose + producer dispatch is deferred to phase 3; 1b keeps concrete-typed observers (matching phase 1a).
- **Behavior-preserving except one documented, tested correction** (the analogue of 1a's dead title cap):
  - **Pricing/limits windowing:** the old `NativePricer` walked the corpus **unbounded** (entire history, every file ever); the Monitor opens only transcript files with `mtime >= now - W`, where **`W = max(now.Sub(usage.MondayAnchor(now)), 12*time.Hour)`** computed each `Scan` (the design's "never open files older than the current week", §2/§8, plus a ≥12h block-anchor safety margin for early-week). Because a file's `mtime` ≥ its newest record's timestamp, this drops **only** records older than `now-W` and retains **every** record newer than `now-W`; the observer's record set is always a subset of full-history, never a superset.
  - **Weekly is EXACTLY equivalent:** `CurrentWeekly` (native_weekly.go:23-31) filters to `[MondayAnchor(now), +7d)`; every current-week record has timestamp `> now-7d > now-W`, so all are retained and extras outside the week are ignored. No weekly divergence for any `W ≥ 7d`-worth-of-week (guaranteed by the `MondayAnchor` term).
  - **Block equivalence & why it holds (corrected reasoning):** `usage.ActiveBlock` (native.go:44-73) tiles blocks by **distance-from-anchor** — a new block starts when `r.Timestamp.Sub(cur.start) >= 5h`, `cur.start = r.Timestamp.Truncate(hour)` — and returns the **last** block iff `now.Sub(last.start) < 5h`. Block boundaries **cascade from the first record of the set**, so dropping older records CAN re-phase the tiling — the earlier "a superset never changes the last block" claim was WRONG. Equivalence holds because: whenever a ≥5h **record-to-record gap** exists within `W`, the first record after that gap `q` satisfies `q - (any prior block start) >= (q - p) >= 5h`, so `q` starts a fresh block **regardless of pre-`W` history**; that fresh chain plus the ≤5h-old active block are fully inside `W`, so the bounded `ActiveBlock` equals the full-history `ActiveBlock` byte-for-byte. The **only** residual divergence is a workload with **no ≥5h gap for the entire window `W`** (continuous activity across a full ISO week), where the block estimate re-phases from the week's first record. This is a **locked design decision** (§2 non-goal "never open files older than the current week"), affects only the **local cost estimate** (the authoritative server-side 5h% flows through the separate Limits path), and is gated by Task 11's realistic (gap-having) fixture. `usage.MondayAnchor` must be **exported** from `native_weekly.go` (currently unexported `mondayAnchor`) so the Monitor computes `W` DRY (Task 7).
  - Nothing else changes.
- **Metric VALUES shift (parity is presence, not magnitude):** folding the whole in-window corpus into the Monitor's tail makes `transcript.scan.files_total`/`bytes_total` (emitter.go:1055-1057) rise (more files folded per tick), and a genuine two-distinct-sessions-resolving-to-one-path case shifts `full+full` → `full+cache_hit`. The pg2-sewtz parity tests assert instrument/label **presence** (`!= 0`), which stays green. Confirm in Task 12 that no dashboard/alert keys on those **absolute** counter values before the removal phase.
- **`ActiveBlock` windowing is DISTANCE-FROM-ANCHOR-START, not a gap split** (design correction, verified `internal/core/usage/native.go:47`): a new block starts when `r.Timestamp.Sub(cur.start) >= 5h`, anchor = `r.Timestamp.Truncate(time.Hour)`. The active block is the **last** block iff `now` is within its 5h window. `UsagePricingObserver` MUST feed all retained records to `usage.ActiveBlock` verbatim — it must NOT reimplement or pre-window the block math.
- **Reuse the pure funcs verbatim:** `usage.ActiveBlock(records []usage.Record, prices usage.PriceTable, now time.Time) *usage.Block` (native.go:35) and `usage.CurrentWeekly(records, prices, now) *usage.WeeklyEntry` (native_weekly.go:22). The observer supplies the `[]usage.Record`; the math is untouched.
- **Pricer record skip is exact** (native_pricer.go:218-224): keep a record iff `Type=="assistant" && !IsApiErrorMessage && Message.Model != "" && !(all four usage token counts == 0)`. `Record{Timestamp, Model, Tokens: usage.ModelTokens{Input,Output,CacheCreation,CacheRead}}`.
- **ADR-0029 limits fold is exact:** `currentWindowLimits`/`windowPeak`/`newestPct` move byte-identically to `internal/core/limits`; the `statusRecord` fields (`ts`,`five_hour_pct`,`five_hour_resets_at`,`seven_day_pct`,`seven_day_resets_at`, all pointer/optional), the two-level `os.ReadDir` discovery, `IsStatusSiblingFile` + `.jsonl` ext filter, and the `appendRecordsInFile` `os.ReadFile`+hand-split are preserved. `capture-status.bash` (the writer) is untouched.
- **Metric contract (pg2-sewtz) preserved:** every instrument + label that fires today MUST still fire. Scan metrics (`RecordScan` full/incremental/cache_hit) now cover the wider file set the Monitor tails. The `pricer`/`limits`/`weekly` **phase timers stay at their current call sites and keep firing** (they measure the now-cheap projection read; the fold cost has moved into `transcript.scan.duration` during `Monitor.Scan`). **No metric is added or removed in 1b.** Precise phase re-homing (pricer/limits/weekly → producer goroutine) is **phase 3** per design §11 — do NOT do it here.
- **Behavioral equivalence gates the phase** (two layers): (1) the existing poller-level deep-equal `aggregate.Tree` (old inline vs Monitor-backed) now also covers `ActiveBlock`/`CostProbed`/`CostProbeErr` since block comes from the Monitor; (2) **source-level** equivalence unit tests asserting `UsagePricingObserver.Block/Weekly/Probed` == `NativePricer.ActiveBlock/CurrentWeekly/Probed` and `LimitsObserver.Current` == `SiblingLimitsSource.Current` on the SAME temp corpus. Build every corpus in `t.TempDir()` — never the real `~/.claude`.
- **Oracles kept:** `transcript.FirstPrompt`/`LatestContext`/`OpenSubagents` stay as test-only oracles (unchanged from 1a).
- **Gate per landing:** `cd packages/pa-monitor && go test ./...` + `nix build .#pa-monitor` MUST pass. `go test -race ./internal/core/corpus/... ./internal/core/poller/... ./internal/core/limits/...` as a manual pre-merge step. Full `pn workspace flake-check` only if the flake interface changes — it does **not** in 1b (no new package needs a flake wiring change; `internal/core/limits` is an internal Go package). Invoke `pn-workspace-rules` before `nix build`.
- **Worktree discipline (R-1..R-9):** canonical clone stays on `main`; work in a worktree on branch `pg2-5sxkb-corpus-phase1b`. Run `nix run .#install-pre-commit-hooks` in the fresh worktree before the first commit. Never `--no-verify`. Commit subjects carry the bead id `(pg2-5sxkb)`. Integrate via the `integrate-branch` skill → `ff-merge-to-main`. **R-8:** before the ff-merge, verify the canonical clone is on `main` and clean; halt + report if not.
- **Flag is already `true` in production** (`daemon.go:376`, set in 1a). So on landing, block/weekly/limits are read from the Monitor **immediately** — there is no flag-off production fallback; the "soak" observes the live folded path. This is the SAME shipping model as 1a (the Monitor's SessionSnapshot/SubagentError path went live authoritative on landing, equivalence-tested pre-merge, then soaked). The pre-merge equivalence suite (Tasks 8, 11) is the correctness proof; the soak is production confirmation before the dead-code removal. The one behavior that is equivalence-tested-only-for-realistic-data is the >1-week-continuous-no-gap block estimate (Global Constraints) — a locked design decision, estimate-only.
- **Removal deferred:** dropping `UseCorpusMonitor` + the inline poller path + `newInlinePoller`/`TestSnapshot_CorpusMonitorEqualsInline`/`TestSnapshot_TitleAtLine500_CorrectedResolution`'s old arms + `SiblingLimitsSource` is **out of scope for 1b** — a P0 follow-up bead, done after a live soak confirms the folded path near-idle (Task 12 files it).

---

## File Structure

**Create:**

- `internal/core/limits/limits.go` — `Limits` DTO + `Record` (moved `statusRecord`) + `Current([]Record) *Limits` (moved `currentWindowLimits`) + `windowPeak`/`newestPct` + `ReadStatusRecords(path) []Record` (moved `appendRecordsInFile`, exported for the corpus observer).
- `internal/core/limits/limits_test.go` — the ported ADR-0029 suite (window-peak, peak-across-files, greatest-resets_at, releases-peak, all-nil-pct, no-window fallback, 7d symmetry, real-0-vs-unknown, equal-ts tiebreak) driving `limits.Current`.
- `internal/core/corpus/observer_usage_pricing.go` — `UsagePricingObserver` (per-path `[]usage.Record`, `prices`, `now`, `probed`/`lastErr`; `Block`/`Weekly`/`Probed`; criteria `{Transcript, Window}`).
- `internal/core/corpus/observer_usage_pricing_test.go`.
- `internal/core/corpus/observer_limits.go` — `LimitsObserver` (per-path `[]limits.Record`; `Current() *limits.Limits`; criteria `{StatusSibling, AllLines}`).
- `internal/core/corpus/observer_limits_test.go`.
- `internal/core/corpus/tail_status.go` — status-sibling tail (per-path `(size,mtime)`-cached `[]limits.Record`, `readDirs`/`reads` counters, `prune`).
- `internal/core/corpus/tail_status_test.go`.
- `internal/core/corpus/walk.go` — mtime-windowed enumeration of the projects tree: transcript candidates (for pricing) + status siblings (for limits), returning `[]walkFile{path, class, mtime, size}`; one `os.ReadDir` per project dir.
- `internal/core/corpus/walk_test.go`.
- `internal/core/poller/corpus_pricing_equivalence_test.go` — source-level: `UsagePricingObserver` vs `NativePricer`, `LimitsObserver` vs `SiblingLimitsSource`, on a shared temp corpus. (In the poller package so it can reuse `makeSessionFixture`; observers are in `corpus`, sources in `usage`/`daemon`, all importable from a `poller`-package `_test.go`.)

**Modify:**

- `internal/core/transcript/snapshot.go` — introduce exported `Event` (= decoded line) + `decodeEvent(line []byte) (Event, bool)`; refactor `scanState.feed(line []byte)` → `feed(ev *Event)` with the one `json.Unmarshal` moved to `decodeEvent`; add `records []usage.Record` accumulation in `feed` (pricer skip); add `func (a *Accumulator) Records() []usage.Record`. `foldReader` decodes then feeds. `Snapshot`/`ScanIncremental` signatures UNCHANGED.
- `internal/core/usage/native_weekly.go` — **export** `mondayAnchor` → `MondayAnchor(t time.Time) time.Time` (rename + update its callers in `native_weekly.go`), so the Monitor computes the pricing walk window `W` DRY. Pure non-behavioral extraction (body unchanged).
- `internal/core/corpus/criteria.go` — add `StatusSibling` to `FileClass`; add `newestActivity`/`Window`-only gating already present (no `ActiveOnly` for pricing). `matches` unchanged; `classIn` unchanged.
- `internal/core/corpus/tail_transcript.go` — re-key from `sessionID` to **path** (`map[string]tcacheEntry` keyed by path; `accs` keyed by path); `fold(path, mtime, rec)` → Snapshot + records; expose the fold's `records` (for pricing). `prune(activePaths)`.
- `internal/core/corpus/monitor.go` — `Scan` runs the corpus walk (Task 7), tails each in-window transcript path once (feeding SessionSnapshot for active + UsagePricing for all), tails status siblings (feeding Limits), keeps the per-session subagent + resolution path. New accessors `Block(now)`, `Weekly(now)`, `CostProbed()`, `Limits()`. New `Register` cases for the two observers. Extend prune + perf counters (`StatusReadsLastScan`, `PricingFilesLastScan`).
- `internal/core/corpus/topology.go` — add resolved-path→sessionID reverse map helper if needed for Snapshot routing (or store `snapshotByPath`).
- `internal/core/poller/poller.go` — Snapshot branch for the block: when `UseCorpusMonitor`, `block = p.Monitor.Block(now)`, `costProbed, costProbeErr = p.Monitor.CostProbed()` instead of `p.Pricer.ActiveBlock(ctx)`/`p.Pricer.Probed()` (poller.go:474-488). Add `Poller.MonitorLimits() *limits.Limits` and `Poller.MonitorWeekly(now) *usage.WeeklyEntry` delegators for lifecycle.
- `internal/daemon/lifecycle.go` — tick (536-565): when the poller exposes Monitor projections, read limits/weekly from them instead of `opts.Limits.Current`/`opts.WeeklyFn`; keep the old calls behind the flag for equivalence. `phase("limits")`/`phase("weekly")` timers stay.
- `internal/daemon/ports.go` — `Limits` type becomes an alias/re-export of `limits.Limits` (or the interface's return type switches to `*limits.Limits`); `applyLimits` signature follows.
- `internal/daemon/sibling_limits.go` — `statusRecord`/`currentWindowLimits`/`windowPeak`/`newestPct`/`appendRecordsInFile` DELETED and re-pointed to `limits.*`; `SiblingLimitsSource.Current` returns `*limits.Limits` via `limits.Current(limits.ReadStatusRecords...)`. (Kept for equivalence; deleted at removal.)
- `internal/daemon/sibling_limits_test.go` — the ADR-0029 cases MOVE to `internal/core/limits/limits_test.go`; leave only the `SiblingLimitsSource` discovery/wiring smoke tests here (or thin wrappers).
- `cmd/pa-monitor/daemon.go` — `buildPoller`: register `corpus.NewUsagePricingObserver(acct.PriceTable(), time.Now, pricingWindow)` + `corpus.NewLimitsObserver()`; the Monitor now owns pricing/limits so `opts.Limits`/`opts.WeeklyFn`/`opts.WeeklyEvery` stay wired (old path/equivalence) but production reads come from the Monitor via the poller.

**Do NOT touch in 1b:** `home/programs/claude-status-line/capture-status.bash`, the gRPC/proto layer, `grafana/pa-monitor-overview.json`, the producer-goroutine/`DerivedState`/`ChangeSource` design (phase 3), the `git_branch`/`subshell`/`terminal_host`/`pr_lookup` providers (phase 2).

---

## Interfaces (locked signatures — every task references these)

```go
// internal/core/transcript/snapshot.go — new/changed
package transcript

// Event is one decoded transcript line, parsed exactly once by decodeEvent and
// folded by scanState. Exported so the single decode is an explicit, testable
// seam (the design's "generic Event", homed in transcript to avoid a
// transcript->corpus cycle). Fields are the former scanEv fields.
type Event struct {
	Type              string
	Subtype           string
	Timestamp         time.Time
	RetryInMs         int64
	Message           Message
	Error             json.RawMessage
	IsApiErrorMessage bool
}

func decodeEvent(line []byte) (Event, bool)          // json.Unmarshal; ok=false on error
func (st *scanState) feed(ev *Event)                 // was feed(line []byte)
func (a *Accumulator) Records() []usage.Record       // copy of the file's pricing records (post-skip)
```

```go
// internal/core/limits/limits.go
package limits

type Limits struct {
	FiveHourPct      *float64
	FiveHourResetsAt time.Time
	SevenDayPct      *float64
	SevenDayResetsAt time.Time
	CapturedAt       time.Time
}

type Record struct { // was daemon.statusRecord
	TS               *int64
	FiveHourPct      *float64
	FiveHourResetsAt *int64
	SevenDayPct      *float64
	SevenDayResetsAt *int64
}

func Current(recs []Record) *Limits                  // was currentWindowLimits; nil if len==0
func ReadStatusRecords(path string) []Record         // was appendRecordsInFile (returns, not appends)
```

```go
// internal/core/corpus/criteria.go — add
const (
	Transcript FileClass = iota
	Subagent
	StatusSibling                                      // <slug>/<id>.status.jsonl
)
```

```go
// internal/core/corpus/observer_usage_pricing.go
// NO clock field — the authoritative `now` is threaded from the Monitor (which
// the poller sources from p.Now()); Block/Weekly take it as a param so the
// injected clock in tests and production is honored (ship-blocker: a nowFn=time.Now
// would price against wall-clock and make the injected-clock equivalence tests
// unreachable). Window is NOT a construction param — the Monitor owns the walk
// window W (see Global Constraints); the observer's Criteria gates class only.
type UsagePricingObserver struct { /* recs map[string][]usage.Record; prices usage.PriceTable; probed bool; lastErr error */ }
func NewUsagePricingObserver(prices usage.PriceTable) *UsagePricingObserver
func (o *UsagePricingObserver) Criteria() Criteria   // {Classes:[Transcript]}  (Window 0 — Monitor's walk applies W; NOT ActiveOnly)
func (o *UsagePricingObserver) setRecords(path string, recs []usage.Record)
func (o *UsagePricingObserver) noteScanErr(err error) // records the first non-nil pricing-file scan error this Scan (reset each Scan) -> CostProbeErr parity with NativePricer.firstErr
func (o *UsagePricingObserver) resetErr()             // called by Monitor at the top of each Scan before folding
func (o *UsagePricingObserver) Prune(activeIDs map[string]bool) // no-op on ids; pruning is by path via prunePaths
func (o *UsagePricingObserver) prunePaths(activePaths map[string]bool)
func (o *UsagePricingObserver) Block(now time.Time) *usage.Block          // usage.ActiveBlock(all records, prices, now); sets probed=true
func (o *UsagePricingObserver) Weekly(now time.Time) *usage.WeeklyEntry   // usage.CurrentWeekly(all records, prices, now); sets probed=true
func (o *UsagePricingObserver) Probed() (bool, error)                     // (probed, lastErr)

// internal/core/corpus/observer_limits.go
type LimitsObserver struct { /* recs map[string][]limits.Record */ }
func NewLimitsObserver() *LimitsObserver
func (o *LimitsObserver) Criteria() Criteria         // {Classes:[StatusSibling]}  (AllLines, no Window)
func (o *LimitsObserver) setRecords(path string, recs []limits.Record)
func (o *LimitsObserver) Prune(activeIDs map[string]bool)
func (o *LimitsObserver) prunePaths(activePaths map[string]bool)
func (o *LimitsObserver) Current() *limits.Limits    // limits.Current(flatten all files' recs)
```

```go
// internal/core/corpus/monitor.go — new accessors
func (m *Monitor) Block(now time.Time) *usage.Block
func (m *Monitor) Weekly(now time.Time) *usage.WeeklyEntry
func (m *Monitor) CostProbed() (bool, error)         // from UsagePricingObserver.Probed()
func (m *Monitor) Limits() *limits.Limits
// perf-guard additions
func (m *Monitor) PricingFilesLastScan() int         // transcript files opened for pricing (incl. non-active)
func (m *Monitor) StatusReadsLastScan() int          // status files re-read (skips unchanged (size,mtime))
```

```go
// internal/core/poller/poller.go — new delegators (used by lifecycle)
func (p *Poller) MonitorLimits() *limits.Limits            // nil when no Monitor
func (p *Poller) MonitorWeekly(now time.Time) *usage.WeeklyEntry
func (p *Poller) UsesCorpusMonitor() bool                  // exposes the flag to lifecycle for the rewire
```

---

## Task 1: transcript single-decode + pricing-record accumulation (the "Event" refactor)

**Files:** Modify `internal/core/transcript/snapshot.go`; add cases to `internal/core/transcript/snapshot_test.go` (or a new `records_test.go`).

**Interfaces produced:** `transcript.Event`, `decodeEvent`, `feed(*Event)`, `(*Accumulator).Records() []usage.Record`. `Snapshot`/`Scan`/`ScanIncremental` signatures UNCHANGED (blast-radius zero for existing callers).

**Key logic:** move the `var ev scanEv; json.Unmarshal(line, &ev)` out of `feed` into `decodeEvent(line) (Event, bool)`; `feed` takes `*Event`. In `foldReader`, replace `st.feed(line[:n-1])` with `if ev, ok := decodeEvent(line[:n-1]); ok { st.st.feed(&ev) }`. Add `records []usage.Record` to `scanState`; in the `case "assistant":` non-error branch, after the existing `ModelTokens` accumulation, append a record **iff** the pricer skip passes (`ev.Message.Model != "" && !allZero(u)`): `st.records = append(st.records, usage.Record{Timestamp: ev.Timestamp, Model: ev.Message.Model, Tokens: usage.ModelTokens{Input:u.InputTokens, Output:u.OutputTokens, CacheCreation:u.CacheCreationInputTokens, CacheRead:u.CacheReadInputTokens}})`. (`IsApiErrorMessage` is already excluded by being in the `if !ev.IsApiErrorMessage` block.) `Accumulator.Records()` returns a fresh copy of `a.st.records`.

- [ ] **Step 1 — failing tests** `records_test.go`: `TestAccumulatorRecords_MatchesPricerSkip` (build a transcript with an assistant record, an `isApiErrorMessage` assistant record, an all-zero-usage assistant record, a user record; assert `Records()` yields exactly the one non-error non-zero assistant record with correct ts/model/tokens); `TestFeed_SingleDecodeEquivalence` (a transcript folded via `Scan` yields the SAME `Snapshot` as before — reuse an existing snapshot golden — proving the decode split is behavior-preserving); `TestAccumulatorRecords_IncrementalEqualsCold` (append then incremental-scan yields the same `Records()` as a cold full parse).
- [ ] **Step 2 — run, expect FAIL** (`go test ./internal/core/transcript/ -run 'Records|SingleDecode'`).
- [ ] **Step 3 — implement** the `decodeEvent`/`feed(*Event)`/`records`/`Records()` changes.
- [ ] **Step 4 — run, expect PASS**, plus `go test ./internal/core/transcript/...` (all existing snapshot/incremental tests still green — this is the behavior-preservation gate).
- [ ] **Step 5 — commit** `refactor(transcript): single-decode Event + pricing-record accumulation (pg2-5sxkb)`.

## Task 2: `internal/core/limits` package — move the ADR-0029 fold out of `daemon`

**Files:** Create `internal/core/limits/limits.go`, `internal/core/limits/limits_test.go`. Modify `internal/daemon/sibling_limits.go`, `internal/daemon/ports.go`, `internal/daemon/sibling_limits_test.go`, and `internal/daemon/lifecycle.go` (`applyLimits` signature).

**Interfaces produced:** `limits.Limits`, `limits.Record`, `limits.Current`, `limits.ReadStatusRecords` (signatures above).

**Key logic:** cut `statusRecord`→`limits.Record`, `currentWindowLimits`→`limits.Current`, `windowPeak`, `newestPct` **byte-identically** into `limits.go`; `appendRecordsInFile(dst, path)`→`ReadStatusRecords(path) []Record` (same `os.ReadFile`+hand-split+`json.Unmarshal`+`TS==nil` drop, returning a fresh slice). In `daemon`: `type Limits = limits.Limits` (alias) so `ports.go`/`applyLimits`/`blockToStoreBlockWithLimits` need no field rename; `LimitsSource.Current` returns `*Limits` (= `*limits.Limits`); `SiblingLimitsSource.Current` builds `recs` via `limits.ReadStatusRecords` and returns `limits.Current(recs)`.

- [ ] **Step 1 — move tests** copy the ADR-0029 cases from `sibling_limits_test.go` into `limits_test.go`, retargeted to call `limits.Current(recs)` directly (build `[]limits.Record` in-code, no disk): `TestCurrent_HoldsWindowPeakOnRegression`, `_PeakAcrossFilesSameWindow` (records from "two files" = one flat slice), `_LaggingOldWindowRecordDoesNotMaskNewWindow`, `_NewWindowReleasesPeak`, `_WindowAllNilPctStaysUnknown`, `_NewestMissingResetsKeepsWindowPeak`, `_SevenDayWindowPeak`, `_NoWindowFallbackNewestByTS`, `_AbsentFieldsStayUnknown`, `_RealZeroDistinctFromUnknown`, `_EqualTSTiebreak`. Plus `TestReadStatusRecords_SkipsTslessAndUnparseable`.
- [ ] **Step 2 — run, expect FAIL** (`go test ./internal/core/limits/`): undefined `limits.Current`.
- [ ] **Step 3 — implement** `limits.go`; re-point `daemon` (alias + delegations); delete the moved funcs from `sibling_limits.go`; in `sibling_limits_test.go` keep `_EmptyDir`, `_IgnoresTranscripts`, `_ImplementsPort`, `_NewestAcrossFilesByTS`, **AND retain ONE disk-driven window-peak case** (`_HoldsWindowPeakOnRegression` or `_PeakAcrossFilesSameWindow`) so the two-level `ReadDir` + `IsStatusSiblingFile`+`.jsonl` filter + `appendRecordsInFile` hand-split path stays covered end-to-end (the in-memory `limits_test.go` cases exercise the fold, not the disk read).
- [ ] **Step 4 — run, expect PASS**: `go test ./internal/core/limits/... ./internal/daemon/...`.
- [ ] **Step 5 — commit** `refactor(limits): extract ADR-0029 window-peak fold to internal/core/limits (pg2-5sxkb)`.

## Task 3: corpus criteria — `StatusSibling` class

**Files:** Modify `internal/core/corpus/criteria.go`; add to `internal/core/corpus/criteria_test.go`.

**Key logic:** add `StatusSibling` as the third `FileClass` const. No change to `matches`/`classIn`. Confirm a `{Window>0}`-only criteria (no `ActiveOnly`) matches an in-window file regardless of active-ownership (this is the pricing gate).

- [ ] **Step 1 — failing test** `TestCriteria_StatusSiblingClass` (a `{Classes:[StatusSibling]}` criteria matches a StatusSibling file and rejects a Transcript file) + `TestCriteria_WindowOnlyMatchesInactive` (`{Classes:[Transcript], Window:1h}` matches an in-window file with `isActive=false`).
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** the const.
- [ ] **Step 4 — run, expect PASS.**
- [ ] **Step 5 — commit** `feat(corpus): StatusSibling file class for the limits observer (pg2-5sxkb)`.

## Task 4: path-keyed transcript tail + expose records (records MUST survive cache-hit)

**Files:** Modify `internal/core/corpus/tail_transcript.go`, `internal/core/corpus/monitor.go` (the sole `m.tt.fold(...)` caller, monitor.go:103 — updated in the SAME task so the package compiles), `internal/core/corpus/tail_transcript_test.go`.

**Key logic:** re-key `accs`/`cache` from `sessionID` to **path** (a file is tailed once regardless of how many sessions resolve to it). Add a `records []usage.Record` field to `tcacheEntry` alongside `snap`. `fold(path string, mtime time.Time, rec Recorder) (transcript.Snapshot, []usage.Record, error)`:

- `path==""` → zero Snapshot, nil records, `RecordScan("full",0,0)`, nil error (unchanged 1a parity for a transcript-less session; still fired here because the session loop calls `fold` with `path==""`).
- **cache_hit** (unchanged path+mtime) → return the CACHED `c.snap` **AND `c.records`** (SHIP-BLOCKER: without caching records, the dominant cache-hit tick returns nil records and `setRecords(path,nil)` wipes the corpus, flapping block/weekly to empty), `RecordScan("cache_hit",0,0)`, nil error.
- **scan** → `snap, acc, stats, err := transcript.ScanIncremental(path, prevAcc)`; `records := acc.Records()`; store `tcacheEntry{path,mtime,snap,records}`; `RecordScan(stats.Mode,dur,stats.BytesFolded)`; **return `err`** (SHIP-BLOCKER 4: propagate so the Monitor can thread it to `UsagePricingObserver.noteScanErr` for `CostProbeErr` parity — do NOT swallow with `_`).

Update the `monitor.go:103` call site: `snap, _, _ := m.tt.fold(path, mtime, m.rec)` (session-loop still ignores records+err there; Task 7 rewires the loop to route records to pricing and err to `noteScanErr`). `prune(activePaths map[string]bool)`.

- [ ] **Step 1 — failing tests** `TestTranscriptTail_PathKeyedCacheHit` (same path twice → cache_hit, no re-parse); `TestTranscriptTail_CacheHitReturnsRecords` (a cache-hit fold returns the SAME records as the preceding scan — guards the wipe bug); `TestTranscriptTail_ReturnsRecordsAndErr`; `TestTranscriptTail_TwoSessionsOnePathScannedOnce`; `TestTranscriptTail_PruneByPath`.
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** the re-keying + `records` in `tcacheEntry` + err return + the monitor.go call-site update.
- [ ] **Step 4 — run, expect PASS**, plus `go test ./internal/core/corpus/...` (package compiles + all 1a tests green).
- [ ] **Step 5 — commit** `refactor(corpus): key transcript tail by path, cache+expose pricing records (pg2-5sxkb)`.

## Task 5: `UsagePricingObserver`

**Files:** Create `internal/core/corpus/observer_usage_pricing.go`, `observer_usage_pricing_test.go`.

**Key logic:** `recs map[string][]usage.Record` (per path) + `probed bool` + `lastErr error`. `setRecords(path, recs)` stores the file's records (**replace**, not append — the tail returns the full file each fold; combined with Task 4's cache-hit records this is stable across ticks). `Block(now time.Time)`: flatten all paths' records into one `[]usage.Record`, call `usage.ActiveBlock(all, o.prices, now)` (the `now` PASSED IN — no internal clock; SHIP-BLOCKER 2), set `o.probed=true`. `Weekly(now time.Time)`: flatten, `usage.CurrentWeekly(all, o.prices, now)`, set `o.probed=true`. `resetErr()` clears `o.lastErr` (Monitor calls it at the top of each Scan); `noteScanErr(err)` sets `o.lastErr` only if currently nil and `err != nil` (first-error, matching `NativePricer.scanRecordsCached`'s `firstErr`). `Probed()` returns `(o.probed, o.lastErr)`. `prunePaths(activePaths)`. `Criteria(){Classes:[Transcript]}` (no Window — the Monitor's walk applies `W`; no ActiveOnly). `Prune(ids)` no-op.

- [ ] **Step 1 — failing tests** `TestUsagePricing_BlockMatchesActiveBlock` (records across 2 paths, `Block(now)` == `usage.ActiveBlock(concat, prices, now)` for an injected `now`); `TestUsagePricing_WeeklyMatchesCurrentWeekly`; `TestUsagePricing_ProbedTrueAfterBlock`; `TestUsagePricing_NoteScanErrSurfacesInProbed` (after `noteScanErr(someErr)`, `Probed()` returns non-nil; `resetErr()` clears it); `TestUsagePricing_UsesPassedNowNotWallClock` (inject a `now` far from wall-clock; assert the block is active for the fixture — guards the clock ship-blocker); `TestUsagePricing_PrunePathsDropsRecords`; `TestUsagePricing_EmptyReturnsNilBlock`.
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement.**
- [ ] **Step 4 — run, expect PASS.**
- [ ] **Step 5 — commit** `feat(corpus): UsagePricing observer (block+weekly over decoded records) (pg2-5sxkb)`.

## Task 6: `LimitsObserver` + status-sibling tail

**Files:** Create `internal/core/corpus/observer_limits.go`, `observer_limits_test.go`, `tail_status.go`, `tail_status_test.go`.

**Key logic — `tail_status.go`:** `statusTail` with `cache map[string]statusEntry{size,mtime,recs []limits.Record}` + `reads`/`readDirs` counters. `foldFile(path string, size int64, mtime time.Time) []limits.Record`: cache hit on `(size,mtime)` → reuse; else `limits.ReadStatusRecords(path)` (`reads++`), cache, return. `prune(activePaths)`. **Status files are tiny** (design), so whole-file re-read on change is fine (no offset tail). **`LimitsObserver`:** `recs map[string][]limits.Record`; `setRecords(path, recs)`; `Current()` flattens all paths **in sorted-path order** then `limits.Current(all)` (SHOULD-FIX 7: Go map iteration is random; `limits.Current`'s no-window fallback `newestPct` returns the first record at `capturedTS` — order-sensitive — and the old `SiblingLimitsSource` iterates sorted `ReadDir`, so `sort.Strings(paths)` before appending each path's recs keeps the tiebreak deterministic and equivalence-stable); `Criteria(){Classes:[StatusSibling]}` (no Window — status siblings are tiny and the fold needs the greatest-`resets_at` window which may live in an older file; matches the old source's unbounded read).

- [ ] **Step 1 — failing tests** `tail_status_test.go`: `TestStatusTail_CacheHitOnUnchanged`, `TestStatusTail_ReReadOnSizeChange` (same-mtime append), `TestStatusTail_ParsesRecords`. `observer_limits_test.go`: `TestLimits_CurrentMatchesFold` (records across 2 paths → `Current()` == `limits.Current(concat)`, exercising a peak-across-files case), `TestLimits_DeterministicNoWindowTiebreak` (multiple paths, equal-ts no-reset records → `Current()` stable across repeated calls, guarding the map-order flake), `TestLimits_PrunePaths`.
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement.**
- [ ] **Step 4 — run, expect PASS.**
- [ ] **Step 5 — commit** `feat(corpus): Limits observer + status-sibling tail (ADR-0029 window-peak) (pg2-5sxkb)`.

## Task 7: corpus walk + `Monitor.Scan` orchestration

**Files:** Create `internal/core/corpus/walk.go`, `walk_test.go`. Modify `internal/core/corpus/monitor.go`, `topology.go`, `monitor_test.go`, `monitor_perfguard_test.go`.

**Key logic — `walk.go`:** `walkCorpus(claudeHome string, window time.Duration, now time.Time) ([]walkFile, error)` where `walkFile{path string; class FileClass; mtime time.Time; size int64}`. One `os.ReadDir(projects)`; per project dir one `os.ReadDir`; classify each entry via `session.IsTranscriptFile` (→ `Transcript`, gated `mtime >= now.Add(-window)`) or `session.IsStatusSiblingFile`+`filepath.Ext==".jsonl"` (→ `StatusSibling`, **no** window gate). Missing `projects` → empty, no error (`os.IsNotExist`).

**Key logic — `Monitor.Scan(now)`:** compute the pricing walk window **`W := max(now.Sub(usage.MondayAnchor(now)), 12*time.Hour)`** (the design's current-week bound + block-anchor margin; see Global Constraints). Call `usagePricingObs.resetErr()` at the top. After `Discover()` (unchanged, `discover` phase timer) and the per-session resolve+subagent loop (SessionSnapshot + SubagentError + topology as in 1a — the transcript fold now uses the **path-keyed** tail, and for each resolved path the loop records `foldedPaths[path] = (records, err)` and routes `records`→`usagePricingObs.setRecords(path,records)` + `err`→`usagePricingObs.noteScanErr(err)`), run `walkCorpus(claudeHome, W, now)`: for each `Transcript` walkFile whose path is **not** in `foldedPaths`, `snap,records,err := tt.fold(path, mtime, rec)` → `usagePricingObs.setRecords(path,records)` + `noteScanErr(err)`; for each `StatusSibling`, `statusTail.foldFile(path,size,mtime)` → `limitsObs.setRecords(path,recs)`. Track `activeTranscriptPaths` (resolved session paths ∪ walked in-window transcript paths) + `activeStatusPaths`; prune the transcript tail + UsagePricing by the former, the status tail + Limits by the latter, plus the existing per-session prunes. Perf counters `PricingFilesLastScan`/`StatusReadsLastScan` reset per Scan. **`RecordScan` fires once per transcript file folded** (active + pricing-only); the `RecordScan("full",0,0)` for a transcript-less **session** (`path==""`) still fires from the session-loop `tt.fold("",...)` call exactly as in 1a — walk files are real paths, never `""`.

**Dedup rule (critical):** a resolved session transcript that is ALSO an in-window walkFile MUST be folded once. The session loop populates `foldedPaths[path]`; the walk loop skips any path already in `foldedPaths` **but the session loop already routed that path's records to UsagePricing**, so pricing still sees it. `PricingFilesLastScan` counts distinct folded transcript paths (session ∪ walk-only) — a shared path is counted once.

- [ ] **Step 1 — failing tests** `walk_test.go`: `TestWalkCorpus_ClassifiesAndWindows` (transcripts gated by window, status siblings ungated, a stale transcript excluded); `TestWalkCorpus_MissingProjectsDir`. `monitor_test.go`: `TestScan_PopulatesPricingAndLimits` (a non-active in-window transcript contributes to `Block`; a status sibling contributes to `Limits`); `TestScan_ActiveTranscriptFoldedOnceFeedsBoth` (an active session's transcript feeds SessionSnapshot AND UsagePricing from ONE fold — assert via `PricingFilesLastScan` not double-counting the path); `TestScan_PrunesVanishedPricingAndStatus`. `monitor_perfguard_test.go`: extend the steady-state (2nd scan) assertions — `StatusReadsLastScan()==0` (unchanged status files reused), and each in-window transcript scanned ≤1×/scan (`PricingFilesLastScan` counts distinct paths, no path twice).
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** `walk.go` + the `Scan` orchestration + accessors (`Block`/`Weekly`/`CostProbed`/`Limits`) + `Register` cases for the two new observers.
- [ ] **Step 4 — run, expect PASS**, plus `go test ./internal/core/corpus/...`.
- [ ] **Step 5 — commit** `feat(corpus): Monitor tails in-window transcripts + status siblings for pricing/limits (pg2-5sxkb)`.

## Task 8: `Poller.Snapshot` reads block from the Monitor + equivalence

**Files:** Modify `internal/core/poller/poller.go`; add to `internal/core/poller/corpus_equivalence_test.go`.

**Key logic:** at poller.go:474-488, add the flag branch: `if p.UseCorpusMonitor { block = p.Monitor.Block(now); costProbed, costProbeErr = p.Monitor.CostProbed() } else { block, _ = p.Pricer.ActiveBlock(ctx); costProbed, costProbeErr = p.Pricer.Probed() }`. `now` is the SAME `now := p.Now()` taken at the top of `Snapshot` (poller.go:179) — the value `Monitor.Scan(now)` and the walk window `W` also used. Everything downstream (aggregate.Build's `block` arg, `tree.CostProbed`) is unchanged. Add `Poller.UsesCorpusMonitor()`/`MonitorLimits()`/`MonitorWeekly(now)` delegators. **`CostProbeErr` note:** `tree.CostProbeErr` is an `error` interface; the folded path CAN set it (Task 4/5/7 thread the scan error), but the concrete error _value_ differs from `NativePricer`'s, so the equivalence deep-equal MUST normalize it (zero it in `zeroVolatile`, like `GeneratedAt`) — value-level error equality is not the contract; the presence/absence is covered by a targeted test below.

- [ ] **Step 1 — extend test** `corpus_equivalence_test.go`: register the UsagePricing + Limits observers in `newMonitorPoller` (so the Monitor produces a block); give `newInlinePoller` a `NativePricer` over the SAME temp corpus. Extend the equality fixture's assistant records with **near-`now` timestamps** (`eqAssistant` currently emits none → zero-time → nil block; without timestamps "block covered" is vacuous) so `tree.ActiveBlock` is non-nil, and extend `zeroVolatile` to also zero `CostProbeErr`; assert `TestSnapshot_CorpusMonitorEqualsInline` deep-equal still holds (now covering `ActiveBlock`/`CostProbed`). Add `TestSnapshot_MonitorBlockEqualsPricer` (block from Monitor == block from `NativePricer.ActiveBlock` on the same corpus) and `TestSnapshot_MonitorCostProbeErrSet` (a corrupt/oversized pricing file → folded path's `tree.CostProbeErr != nil`, proving the "5h unavailable" signal survives the fold — SHOULD-FIX 4).
- [ ] **Step 2 — run, expect FAIL** (Monitor block not wired).
- [ ] **Step 3 — implement** the poller branch + delegators.
- [ ] **Step 4 — run, expect PASS** (deep-equal Tree holds incl. block).
- [ ] **Step 5 — commit** `feat(poller): read active block from corpus Monitor behind flag (pg2-5sxkb)`.

## Task 9: lifecycle reads limits + weekly from the Monitor

**Files:** Modify `internal/daemon/lifecycle.go`; add to a `internal/daemon/lifecycle_limits_test.go` (or extend an existing lifecycle test).

**Key logic:** tick (536-565): `useMon := false; if pm, ok := opts.Poller.(interface{ UsesCorpusMonitor() bool }); ok { useMon = pm.UsesCorpusMonitor() }`. Define a tick-scoped `tickNow := time.Now().UTC()` near line 536 (the existing `nowUTC` locals live inside the WriteService blocks at 573/584 and are not in scope here — NIT). **Limits** (read every tick today → preserved): `if useMon { if lr := monLimits(opts.Poller); lr != nil { applyLimits(tree, lr) } } else if opts.Limits != nil { if lr, err := opts.Limits.Current(ctx); err == nil { applyLimits(tree, lr) } }` — wrapped in `phase("limits", ...)`. **Weekly** (MUST keep the `WeeklyEvery` cadence — SHOULD-FIX 6): keep the existing `fetchWeek := opts.WeeklyFn != nil && (opts.WeeklyEvery <= 0 || tickCount%opts.WeeklyEvery == 0)` guard, and ALSO gate the Monitor read + `tree.ActiveWeek` assignment + `phase("weekly")` behind it so the `weekly` histogram sample-rate and the downstream `UpsertWeek`/week-contribution DB writes (lifecycle.go:583-591) keep their ~1/12-tick cadence: `if fetchWeek { start:=time.Now(); if useMon { if w := monWeekly(opts.Poller, tickNow); w != nil { tree.ActiveWeek = w } } else if w,err := opts.WeeklyFn(ctx); err==nil && w!=nil { tree.ActiveWeek = w }; phase("weekly", start) }`. (When `useMon`, `fetchWeek`'s `opts.WeeklyFn != nil` guard still holds because buildPoller keeps `opts.WeeklyFn` wired.) Use type-assert helpers `monLimits(opts.Poller) *limits.Limits` / `monWeekly(opts.Poller, t) *usage.WeeklyEntry` on anonymous interfaces so lifecycle doesn't hard-import `poller`.

- [ ] **Step 1 — failing test** `lifecycle_limits_test.go`: a fake Poller implementing `Snapshot` + `UsesCorpusMonitor()==true` + `MonitorLimits()`/`MonitorWeekly()` returning known values; run ticks; assert (a) `tree.FiveHourPct` reflects the Monitor limits every tick; (b) `tree.ActiveWeek` reflects the Monitor weekly ONLY on `WeeklyEvery`-cadence ticks (with `WeeklyEvery=2`, set on tick 0/2, not tick 1 — guarding the cadence fix); (c) `opts.Limits`/`opts.WeeklyFn` (fakes that fail the test if called) are NOT invoked.
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** the flagged rewire + helpers.
- [ ] **Step 4 — run, expect PASS**, plus `go test ./internal/daemon/...`.
- [ ] **Step 5 — commit** `feat(daemon): read limits+weekly from corpus Monitor behind flag (pg2-5sxkb)`.

## Task 10: wire the observers into `buildPoller`

**Files:** Modify `cmd/pa-monitor/daemon.go`; add to `internal/core/poller/poller_test.go`.

**Key logic:** in `buildPoller` (after the existing `mon.Register(...)` for SessionSnapshot/SubagentError): `mon.Register(corpus.NewUsagePricingObserver(acct.PriceTable()))` and `mon.Register(corpus.NewLimitsObserver())`. **No window or clock param** — the Monitor computes the pricing walk window `W = max(now.Sub(usage.MondayAnchor(now)), 12h)` per `Scan(now)` (Task 7) and threads that same `now` into `Block`/`Weekly`; the observer holds neither. `opts.Limits`/`opts.WeeklyFn`/`opts.WeeklyEvery=12` stay wired (old path + equivalence oracle); once the flag routes production reads to the Monitor they are exercised only by the equivalence test until the removal follow-up.

- [ ] **Step 1 — failing/adjust test** `poller_test.go`: extend the corpus-monitor recorder test so, with all four observers registered + a `fakeRec`, `rec.scans` still fires and (smoke) `p.Monitor.Block(now)`/`Limits()` are non-nil for a fixture corpus with usage + status records.
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** the two `Register` calls.
- [ ] **Step 4 — run, expect PASS.**
- [ ] **Step 5 — commit** `feat(daemon): register UsagePricing+Limits observers on the Monitor (pg2-5sxkb)`.

## Task 11: source-level equivalence suite

**Files:** Create `internal/core/poller/corpus_pricing_equivalence_test.go`.

**Key logic:** build ONE temp `~/.claude` corpus (transcripts with assistant usage across several files + timestamps spanning a 5h block boundary WITH a ≥5h gap, plus `*.status.jsonl` siblings with an ADR-0029 window-peak scenario). Drive a Monitor (all four observers) via `Scan(now)`, and independently a `NativePricer{ClaudeHome, Prices, Now}` + `SiblingLimitsSource{ClaudeHome}`. Assert: `reflect.DeepEqual(mon.Block(now), pricer.ActiveBlock(ctx))`; `reflect.DeepEqual(mon.Weekly(now), pricer.CurrentWeekly(ctx))`; `mon.CostProbed()` == `pricer.Probed()`; `reflect.DeepEqual(mon.Limits(), sibling.Current(ctx))`. Add `TestPricingWindow_DropsAncientButKeepsBlock` (a record older than the window in a file with old mtime is excluded, but because a ≥5h gap separates it the active `Block` is byte-identical to the full-history pricer — the documented, tested windowing bound).

- [ ] **Step 1 — write the suite; run, expect FAIL** (observers/accessors exercised end-to-end).
- [ ] **Step 2 — fix any real divergences** in the observers/walk (do NOT weaken assertions — a divergence on the gap-fixture is a real bug).
- [ ] **Step 3 — run, expect PASS.**
- [ ] **Step 4 — commit** `test(corpus): source-level pricing+limits equivalence vs native pricer/sibling source (pg2-5sxkb)`.

## Task 12: full gate + acceptance + follow-ups

- [ ] **Step 1** — `cd packages/pa-monitor && go test ./...` → all pass.
- [ ] **Step 2** — `go test -race ./internal/core/corpus/... ./internal/core/poller/... ./internal/core/limits/... ./internal/daemon/...` → pass (manual `-race` gate).
- [ ] **Step 3** — from repo root, invoke `pn-workspace-rules`, then `nix build .#pa-monitor` → builds; `nix build .#pa-monitor-go-tests` (the flake check) → passes.
- [ ] **Step 4 — acceptance review (no code):** (a) the pricer whole-corpus `WalkDir` and the sibling-limits walk are gone from the production path (Monitor owns both; `PricingFilesLastScan`/`StatusReadsLastScan` steady-state ≈ 0 re-reads); (b) a transcript line is decoded once feeding both Snapshot + pricing (Task 4/7 dedup); (c) block/weekly/limits behavior-equivalent on realistic fixtures, windowing bound documented + tested; (d) metric instruments/labels all still fire (parity test green); (e) ADR-0029 fold preserved (moved, same tests). Update `packages/pa-monitor/README.md` and this repo's `CLAUDE.md` only if module structure notes require it (new `internal/core/limits` package).
- [ ] **Step 5 — record the design correction** on the epic via `bd comment pg2-fclt1` (Event homed in `transcript` not `corpus`; pricing/limits windowing bound; ADR-0029 fold moved to `internal/core/limits`).
- [ ] **Step 6 — file the removal follow-up** `bd create` P0: "Post-soak: remove UseCorpusMonitor flag + inline poller path + SiblingLimitsSource + old-path equivalence arms (pg2-fclt1 phase-1 cleanup)", `bd dep` it after this bead; note it needs a live soak of the folded daemon first.
- [ ] **Step 7 — commit** any residual (README/CLAUDE.md/bd export).

---

## Deferred (explicitly out of 1b)

- **Post-soak removal:** `UseCorpusMonitor` flag, the inline `Snapshot` arms (ResolveTranscript/ScanIncremental/LastSubagentError/maxActivity/pricer/limits), `SiblingLimitsSource`, `newInlinePoller`, and the two-arm equivalence tests (`TestSnapshot_CorpusMonitorEqualsInline`, `TestSnapshot_TitleAtLine500_CorrectedResolution`, `incremental_poller_test.go` inline arm) — the follow-up bead (Task 12 Step 6).
- **Phase 2:** git-branch/subshell/terminal-host/PR providers + nested cache.
- **Phase 3:** producer goroutine + `ChangeSource` two-tier poll + immutable `DerivedState` atomic swap; re-home the `pricer`/`limits`/`weekly`/`discover` phase timers to the producer; update the Grafana dashboard (coordinate pg2-gpbqe).
- **Phase 4:** clickable PR in the TUI.

## Self-review checklist (run before dispatching critique)

1. **Spec coverage:** design §1 single-decode/criteria → Tasks 1,3,4,7; UsagePricing observer (own record store, block/weekly/probed, isApiError+zero skip) → Tasks 1,5; Limits observer (AllLines window-peak, ADR-0029, not LastMatch) → Tasks 2,6; rewire opts.WeeklyFn/opts.Limits → Task 9; block via Monitor → Task 8; equivalence gates → Tasks 8,11; metric parity preserved → constraints + Task 7/10. Removal explicitly deferred (soak-gated, user-approved).
2. **Placeholders:** none — every task has concrete file paths, signatures, test names, key logic, and gate commands (production bodies at interface+key-logic altitude, same-session TDD implementer, matching the accepted phase-1a plan precedent).
3. **Type consistency:** `usage.Record`/`usage.ModelTokens`/`usage.Block`/`usage.WeeklyEntry`/`usage.PriceTable`, `limits.Limits`/`limits.Record`/`limits.Current`, `transcript.Event`/`(*Accumulator).Records()`, `Monitor.Block/Weekly/CostProbed/Limits`, `UsagePricingObserver`/`LimitsObserver` accessors, `Poller.UsesCorpusMonitor/MonitorLimits/MonitorWeekly` used consistently Tasks 1-11.
