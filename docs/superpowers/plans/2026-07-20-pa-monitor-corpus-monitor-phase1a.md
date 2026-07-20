# pa-monitor Corpus Monitor — Phase 1a Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans (inline, this session) to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce a reactive corpus `Monitor` that owns transcript discovery, resolution, and per-file tailing (transcript + subagent classes) feeding a `SessionSnapshot` and a `SubagentError` observer; `Poller.Snapshot` delegates its per-session corpus reading to it, eliminating the two dominant per-tick hotspots (`ResolveTranscript→transcriptHasTitle` and the uncached per-session `LastSubagentError`/`maxActivity` subagent scans) and returning the daemon to near-idle CPU.

**Architecture:** New package `internal/core/corpus` owns the join of the two on-disk trees (`~/.claude/sessions/*.json` PID-keyed → `~/.claude/projects/<slug>/*.jsonl` cwd-keyed). One `Monitor.Scan(now)` per tick: (1) `Discoverer.Discover()` (unchanged, includes dead-PID sessions); (2) resolve each session's transcript via a lazily-populated, mtime-gated **header index** (`path → {customTitle, mtime}`) that reads a candidate's `custom-title` wherever it lands (killing the dead `titleScanLines=200` cap); (3) criteria-gated per-file tail — reuse `transcript.Accumulator` for the resolved transcript (SessionSnapshot projection) and an incremental fold of `<sid>/subagents/agent-*.jsonl` (SubagentError projection); (4) expose per-session projections + topology (`resolvedPath`, `mtime`, `maxActivity`). `Poller.Snapshot` reads those projections instead of calling `ResolveTranscript`/`ScanIncremental`/`LastSubagentError`/`maxActivity` inline. Everything stays **synchronous on the tick goroutine** — zero new concurrency (the producer goroutine + `DerivedState` is Phase 3). The old inline path is kept behind a `Poller` flag, defaulting to the new path, purely so the behavioral-equivalence test can diff old-vs-new this session; a follow-up bead removes it after a live soak.

**Tech Stack:** Go 1.25 (`packages/pa-monitor`), gomod2nix build engine, `github.com/phillipgreenii/claude-transcript` sibling dep (`replace => ../claude-transcript`), nix flake (`nix build .#pa-monitor`, `pa-monitor-go-tests` flake check), OTel via the nil-safe `poller.PhaseRecorder` seam.

## Global Constraints

- **No new external deps.** `otelgrpc` is already present; add nothing to `go.mod`/`gomod2nix.toml`.
- **Import direction:** `corpus` MAY import `session`, `transcript`, and `github.com/phillipgreenii/claude-transcript` (`ct`); **none may import `corpus`** (no cycle). SessionSnapshot reuses `transcript.ScanIncremental`/`Accumulator`/`Snapshot` byte-based — **no `scanState.feed(Event)` refactor in 1a** (deferred to 1b, where UsagePricing becomes a second transcript-decode consumer).
- **Subagent errors use `ct.LastAPIError` verbatim, cached per file.** The SubagentError fold MUST call `ct.LastAPIError(agentFile)` (NOT `scanState.finalize`) so `IsContextLimit` (apierror.go:209, feeds `RecordContextLimitHit` lifecycle.go:1180) and every other field are byte-identical to today's `ct.LastSubagentError`. `scanState.finalize` (snapshot.go:277-282) omits `IsContextLimit` and MUST NOT be reused for subagents, and MUST NOT be changed (that would alter the main-transcript path too). The CPU win comes from **caching** each agent file's `ct.LastAPIError` result keyed on `(size, mtime)` and re-running only on change — `size` in the key catches a same-mtime append (design §8), so there is no incremental-fold blind spot and no whole-corpus re-read of finished agent files.
- **Subshell count stays poller-side (Phase 2 concern).** Its `(path,mtime)` cache-hit reuse is entangled in the block being moved (poller.go:196/212-219/336). Keep `subshellCounter.Count` + `RecordSubprocess("subshell")` in `Poller.Snapshot`, backed by a poller-owned `map[sessionID]{path,mtime,count}` invalidated via `Monitor.ResolvedPath`. Do NOT move subshell into the Monitor.
- **Metric contract (pg2-sewtz) preserved:** the Monitor's transcript tail MUST emit `RecordScan(mode, d, bytes)` with modes `full`/`incremental`/`cache_hit` exactly as `poller.Snapshot` does today (poller.go:198,210) — **including `RecordScan("full",0,0)` for a `path==""` session** (poller.go:208-210 fires it today; match it, N2). The `discover` phase timer MUST wrap **only** the Monitor's internal `Discoverer.Discover()` call (~71ms today, poller.go:153-156) — NOT resolve/tail, whose cost is already reported by `transcript.scan.duration`; wrapping all of `Monitor.Scan` in `discover` would double-attribute scan time (S1). The `git_branch`/`subshell`/`terminal_host`/`pr_lookup` subprocess metrics stay in `Poller.Snapshot`, unchanged (Phase 2). No metric is added or removed.
- **Behavioral equivalence gates the phase:** old inline path vs new Monitor-backed path MUST produce a **deep-equal `aggregate.Tree`**, normalizing only `Tree.GeneratedAt` (the sole `time.Now()` field; aggregate.go:82). Build the corpus in `t.TempDir()` — never the real `~/.claude`.
- **Oracles kept:** `transcript.FirstPrompt`/`LatestContext`/`OpenSubagents` stay as test-only cross-check oracles (no production callers today; snapshot_test.go / incremental_test.go use them).
- **Local test fakes (N3):** `_test.go` files are not importable across packages, so the `corpus` package tests (Tasks 1-5) need their OWN local `fakeRec` (mirror poller_test.go:125-140) and corpus-builder helper (mirror `makeSessionFixture` incremental_poller_test.go:18-40); only the poller-package equivalence test (Task 6) may reuse the existing `makeSessionFixture`.
- **`ActiveOnly` semantics:** "has a session record (alive OR dead-PID pre-GC)", NOT `PidAlive`. `Discover()` already returns dead-PID sessions; the Monitor tails their transcripts+subagents so Model/tokens/first-prompt/LongIdle are preserved until GC.
- **Behavior-preserving except the two documented corrections:** the dead title cap (resolution now actually matches a title at line 500) and nothing else in 1a (the unbounded `PRCache` is Phase 2). A resumed named session in a shared cwd MAY resolve to a _different, correct_ file than today — those resolution goldens are new expected values, not regressions.
- **Gate per landing:** `go test ./...` (in `packages/pa-monitor`) + `nix build .#pa-monitor` MUST pass. Run `-race` on the corpus + poller packages as a manual pre-merge step (the nix `pa-monitor-go-tests` check runs plain `go test`). Full `pn workspace flake-check` only if the flake interface changes — it does **not** in 1a.
- **Worktree discipline (R-1..R-9):** canonical clone stays on `main`; work in a worktree on branch `pg2-uojfm-corpus-phase1a`. Run `nix run .#install-pre-commit-hooks` in the fresh worktree before the first commit. Never `--no-verify`. Commit subjects carry the bead id (`pg2-uojfm`; epic `pg2-fclt1`). Integrate via `wrap-up-session` → `integrate-branch` → `ff-merge-to-main`.

---

## File Structure

**Create:**

- `internal/core/corpus/doc.go` — package doc (architecture paragraph, import-direction rule).
- `internal/core/corpus/criteria.go` — `FileClass`, `Position`, `Criteria`, `Observer` interface.
- `internal/core/corpus/topology.go` — `Topology` (active set, per-session `resolvedPath`/`mtime`/`maxActivity`), the session↔project join, header index.
- `internal/core/corpus/resolve.go` — header index (`path→{customTitle,mtime}`, mtime-gated/lazy) + 3-tier `resolve`.
- `internal/core/corpus/tail_transcript.go` — resolved-transcript tail (wraps `transcript.ScanIncremental`/`Accumulator`), emits `RecordScan`.
- `internal/core/corpus/tail_subagent.go` — subagent-dir incremental fold → last-terminal `transcript.ErrorRecord` (FromSubagent), reproducing `ct.LastSubagentError`.
- `internal/core/corpus/observer_session_snapshot.go` — SessionSnapshot observer (holds `map[sessionID]transcript.Snapshot`).
- `internal/core/corpus/observer_subagent_error.go` — SubagentError observer (holds `map[sessionID]*transcript.ErrorRecord`).
- `internal/core/corpus/monitor.go` — `Monitor` (`Register`, `Scan(now)`, projection accessors, `PhaseRecorder` seam, open-counter test hook).
- Tests: `criteria_test.go`, `resolve_test.go`, `tail_transcript_test.go`, `tail_subagent_test.go`, `monitor_test.go`, `monitor_perfguard_test.go`.

**Modify:**

- `internal/core/poller/poller.go` — add `Monitor *corpus.Monitor` + `UseCorpusMonitor bool` fields; in `Snapshot`, when the flag is set, read resolution/snapshot/subagent-error/maxActivity from the Monitor instead of the inline calls (poller.go:176, 191-222, 286-295, 312). Keep the inline path when the flag is off.
- `internal/core/poller/incremental_poller_test.go` (or new `corpus_equivalence_test.go`) — behavioral-equivalence test (old vs new → deep-equal Tree).
- `cmd/pa-monitor/daemon.go` — in `buildPoller` (daemon.go:332-368), construct the `Monitor` (register the two observers, wire `SetPhaseRecorder`), set `poller.Monitor` + `poller.UseCorpusMonitor = true`.

**Do NOT touch in 1a:** `usage/native_pricer.go`, `daemon/sibling_limits.go`, `opts.WeeklyFn`/`opts.Limits` wiring, `aggregate.Build`, the gRPC/proto layer, the Grafana dashboard.

---

## Interfaces (locked signatures — every task references these)

```go
// internal/core/corpus/criteria.go
package corpus

type FileClass int
const (
	Transcript FileClass = iota // <slug>/<id>.jsonl (excludes *.status.jsonl)
	Subagent                    // <slug>/<id>/subagents/agent-*.jsonl
)

// Position is an ADVISORY reduction hint, not a gate. 1a observers use AllLines.
type Position int
const ( AllLines Position = iota; FirstMatch; LastMatch )

// Criteria GATES which files the Monitor opens.
type Criteria struct {
	Classes    []FileClass
	Window     time.Duration // open only files with mtime >= now-Window (0 = no bound)
	ActiveOnly bool          // only files owned by a discovered session (alive or dead-PID)
	Position   Position
}

// Observer declares which files it cares about (for gating) and prunes state for
// absent sessions. 1a keeps observers CONCRETE-TYPED (the Monitor drives a
// class-specific fold and populates the typed store) — there is a single
// consumer per class, so the generic Observer.OnLine(*Event) firehose is
// deferred to 1b. The interface exists only for criteria gating + pruning.
type Observer interface {
	Criteria() Criteria
	Prune(activeIDs map[string]bool) // drop projection state for sessions absent this Scan
}
```

```go
// internal/core/corpus/monitor.go
package corpus

type Monitor struct { /* ClaudeHome, Discoverer deps, observers, header index, tail caches, Rec, openCounter */ }

func New(claudeHome string, disc *session.Discoverer) *Monitor
func (m *Monitor) Register(o Observer)                       // before first Scan
func (m *Monitor) SetPhaseRecorder(r any)                    // nil-safe; RecordScan only in 1a
// Scan discovers + resolves + tails in one pass and PRUNES all Monitor-owned
// state (transcript Accumulator map, subagent per-file caches, header/title
// cache) to the live session/path set every call (S2 — else these maps leak for
// the daemon's lifetime, the exact defect this epic fixes).
func (m *Monitor) Scan(now time.Time) ([]*session.Session, error) // returns the session slice (with TranscriptMTime set)

// Topology accessors (read after Scan):
func (m *Monitor) ResolvedPath(sessionID string) (path string, mtime time.Time, ok bool)
func (m *Monitor) MaxActivity(sessionID string) time.Time    // max(transcript mtime, max subagent agent-*.jsonl mtime), from the SAME ReadDir the subagent fold uses

// Perf-guard test hooks (Monitor-initiated work in the last Scan — a faithful
// proxy since the actual os.Open lives inside transcript.ScanIncremental and
// ct.LastAPIError, which the corpus package cannot intercept, N1):
func (m *Monitor) TranscriptScansLastScan() int // transcript ScanIncremental calls initiated (skips cache_hit)
func (m *Monitor) TitleProbesLastScan() int     // custom-title file reads initiated (0 in steady state — title cache is write-once, S3)
func (m *Monitor) SubagentReadDirsLastScan() int // subagents-dir ReadDir calls (MUST be <=1 per active session — pg2-fvuk1)
func (m *Monitor) SubagentFileReadsLastScan() int // ct.LastAPIError calls initiated (skips unchanged (size,mtime) files)
```

```go
// internal/core/corpus/observer_session_snapshot.go
type SessionSnapshotObserver struct { /* map[sessionID]transcript.Snapshot */ }
func NewSessionSnapshotObserver() *SessionSnapshotObserver
func (o *SessionSnapshotObserver) Snapshot(sessionID string) (transcript.Snapshot, bool)

// internal/core/corpus/observer_subagent_error.go
type SubagentErrorObserver struct { /* map[sessionID]*transcript.ErrorRecord */ }
func NewSubagentErrorObserver() *SubagentErrorObserver
func (o *SubagentErrorObserver) LastTerminal(sessionID string) (*transcript.ErrorRecord, bool)
```

```go
// internal/core/poller/poller.go additions
type Poller struct {
	// ...existing...
	Monitor          *corpus.Monitor
	UseCorpusMonitor bool // when true, Snapshot reads corpus projections instead of inline ResolveTranscript/ScanIncremental/LastSubagentError/maxActivity
}
```

---

## Task 1: `corpus` package skeleton + criteria gating

**Files:** Create `internal/core/corpus/doc.go`, `criteria.go`, `criteria_test.go`.

**Interfaces:** Produces `FileClass`, `Position`, `Criteria`, `Observer` (signatures above).

- [ ] **Step 1 — failing test** `criteria_test.go`: `TestCriteriaMatches` — table of `(Criteria, class, mtime, isActive, now)` → expected `matches bool`. Cases: class not in `Classes` → false; `Window>0` and `mtime < now-Window` → false; `ActiveOnly` and `!isActive` → false; all satisfied → true; `Window==0` disables the age gate.
- [ ] **Step 2 — run, expect FAIL** (`go test ./internal/core/corpus/ -run TestCriteriaMatches`): undefined `Criteria`/`matches`.
- [ ] **Step 3 — implement** `criteria.go` with the types above plus an unexported `func (c Criteria) matches(class FileClass, mtime time.Time, isActive bool, now time.Time) bool`.
- [ ] **Step 4 — run, expect PASS.**
- [ ] **Step 5 — commit** `feat(corpus): criteria gating types (pg2-uojfm)`.

## Task 2: header index + 3-tier resolution (kills the dead 200-line cap)

**Files:** Create `resolve.go`, `resolve_test.go`.

**Interfaces:** Consumes `session.Session`, `session.IsTranscriptFile` (via `s.Cwd` — note `slugify` is UNEXPORTED in `session`; replicate the `strings.NewReplacer("/","-","_","-")` rule locally or add an exported `session.Slug`; prefer adding `session.Slug(cwd)` and having `ResolveTranscript` reuse it — a tiny non-behavioral extraction). Produces (unexported) `titleCache` with `func (h *titleCache) resolve(claudeHome string, s *session.Session) (path string, mtime time.Time, ok bool)` and `func (h *titleCache) customTitle(path string, mtime time.Time) (title string)`.

**Behavior (replicate `session.ResolveTranscript` precedence BYTE-IDENTICALLY, minus the dead cap):** `os.ReadDir(projects/<slug>)` in the SAME order as today, build `[]cand{path,mtime}` from `IsTranscriptFile` entries, then `sort.Slice(cands, func(i,j){cands[i].mtime.After(cands[j].mtime)})` — the IDENTICAL non-stable comparator (S4: do NOT seed candidates from a map, or equal-mtime ties tie-break differently and the whole session's Tree diverges). (1) if `s.Name != ""`, first candidate whose `customTitle == s.Name`; (2) else exact `<SessionID>.jsonl` basename; (3) else newest (cands[0]). `ok=false` when dir missing / no candidates.

**Title cache is WRITE-ONCE and persistent, NOT mtime-gated (S3):** `custom-title` is written once near a transcript's start and never changes, so once a title (or a confirmed-absent sentinel) is cached for a path it survives mtime bumps — otherwise the active transcript's per-tick mtime bump would re-scan it to line ~490-1100 every tick, a replay of the very hotspot being removed. Only re-scan a path whose cached entry is the "absent" sentinel AND whose size has grown (a title could appear as an initially-short file grows). Cache: `map[string]titleEntry{ title string; found bool; scannedSize int64 }`.

- [ ] **Step 1 — failing tests** `resolve_test.go` (build a temp `projects/<slug>/` corpus): `TestResolve_TitleAtLine500` (custom-title at line ~500 → that file is resolved, proving the cap is gone); `TestResolve_SessionIDArm`; `TestResolve_NewestFallback`; `TestResolve_ExcludesStatusSibling` (`<id>.status.jsonl` never selected); `TestResolve_MultiSessionSharedCwd` (two named sessions each resolve to their own titled file); `TestResolve_MissingDir` (`ok==false`); `TestResolve_EqualMtimeTieMatchesReadDirOrder` (two candidates with identical mtime resolve identically to the old comparator — guards S4); `TestTitleCache_NotReprobedOnMtimeBump` (bump mtime of a titled file, assert `customTitle` does not re-open it — assert via an open-counter).
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** `resolve.go`. `customTitle(path,mtime)`: if cached `found` → return cached title; if cached "absent" and size unchanged → return ""; else open+scan (bufio, 16MiB line cap matching transcript, defensive cap e.g. 5000 lines) for the first `{"type":"custom-title","customTitle":...}`, cache `{title, found, scannedSize}`, return it.
- [ ] **Step 4 — run, expect PASS.**
- [ ] **Step 5 — commit** `feat(corpus): header-index resolution, remove dead title cap (pg2-uojfm)`.

## Task 3: transcript tail → SessionSnapshot projection + RecordScan parity

**Files:** Create `tail_transcript.go`, `observer_session_snapshot.go`, `tail_transcript_test.go`.

**Interfaces:** Consumes `transcript.ScanIncremental`/`Accumulator`/`Snapshot`/`ScanStats`. The tail holds `map[sessionID]*transcript.Accumulator` and, on each `Scan`, for the resolved transcript: if `path==""` → zero Snapshot; else if `(path,mtime)` unchanged from last Scan → `cache_hit` (reuse cached Snapshot, `RecordScan("cache_hit",0,0)`); else `ScanIncremental(path, prevAcc)` and `RecordScan(stats.Mode, dur, stats.BytesFolded)`. Feeds `SessionSnapshotObserver`.

- [ ] **Step 1 — failing tests** `tail_transcript_test.go`: `TestTranscriptTail_IncrementalEqualsCold` (append + re-scan == fresh full parse, mirroring incremental_test.go `foldPartition`); `TestTranscriptTail_CacheHitOnUnchanged` (same path+mtime → cached Snapshot returned, no re-parse); `TestTranscriptTail_RecordScanModes` (fakeRec-style counter: first scan `full`, unchanged `cache_hit`, appended `incremental`).
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** `tail_transcript.go` + `observer_session_snapshot.go`.
- [ ] **Step 4 — run, expect PASS.**
- [ ] **Step 5 — commit** `feat(corpus): transcript tail + SessionSnapshot observer, RecordScan parity (pg2-uojfm)`.

## Task 4: subagent tail → SubagentError projection + maxActivity

**Files:** Create `tail_subagent.go`, `observer_subagent_error.go`, `tail_subagent_test.go`.

**Interfaces:** Reproduce `ct.LastSubagentError` **byte-identically** by calling `ct.LastAPIError` per file (NOT `scanState` — B1: preserves `IsContextLimit` & every field). Subdir = `strings.TrimSuffix(resolvedPath, ".jsonl") + "/subagents"` (identical to `apierror.go:249` and `poller.go:571`). ONE `os.ReadDir(subdir)` per session per scan; for each `agent-*.jsonl` entry take `size`+`mtime` from `entry.Info()`; if the per-file cache `map[path]{size,mtime,rec *ct.ErrorRecord,ok bool}` matches `(size,mtime)` → reuse (skip the read — the CPU win over the old always-re-read path); else call `ct.LastAPIError(path)` and cache `{size,mtime,rec,ok}` (size in the key catches same-mtime append, S7). Aggregate: among cached results across files, keep the one with `ok && rec.Kind != "" && rec.IsTerminal` and the latest `rec.At`; set `FromSubagent=true`; that is the session's projection. Missing subdir (`os.ReadDir` err) → no projection (graceful — the resumed/forked case). `maxActivity` for the session = max(resolved transcript mtime, max `mtime` over `agent-*.jsonl`) — from the SAME ReadDir (eliminates today's double ReadDir at poller.go:288-LastSubagentError + :312-maxActivity).

- [ ] **Step 1 — failing tests** `tail_subagent_test.go`: `TestSubagentTail_MatchesLastSubagentError` (build a subagents dir, assert the projection == `ct.LastSubagentError(mainPath)` field-for-field, incl. `FromSubagent`); `TestSubagentTail_ContextLimitPreserved` (an invalid_request/"prompt is too long" subagent error → projection `IsContextLimit==true`, guarding B1); `TestSubagentTail_SurfacesLatestTerminal` (two files, later `.At` terminal wins); `TestSubagentTail_RecoveryClearsTerminal` (later non-error line → not surfaced); `TestSubagentTail_SameMtimeAppendReRead` (append changing size but not mtime → re-read, new error surfaced, guarding S7); `TestSubagentTail_UnchangedFileNotReRead` (stable size+mtime → cache reused, no `ct.LastAPIError` call — assert via a counter); `TestSubagentTail_MissingSubdirGraceful`; `TestSubagentTail_MaxActivity` (max over transcript + subagent mtimes; single ReadDir).
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** `tail_subagent.go` + `observer_subagent_error.go`.
- [ ] **Step 4 — run, expect PASS.**
- [ ] **Step 5 — commit** `feat(corpus): subagent tail + SubagentError observer + maxActivity (pg2-uojfm)`.

## Task 5: `Monitor.Scan` orchestration + perf guard

**Files:** Create `monitor.go`, `topology.go`, `monitor_test.go`, `monitor_perfguard_test.go`.

**Interfaces:** `Monitor.Scan(now)` = record `discover` phase around ONLY `Discoverer.Discover()` (S1) → for each session, `resolve` (Task 2) → criteria-gated tail of transcript (Task 3, emits `RecordScan`) + subagent files (Task 4) in ONE pass → populate observers + topology (`ResolvedPath`, `MaxActivity`) → **prune ALL Monitor-owned maps** (transcript Accumulators, subagent per-file caches, title cache entries for vanished paths) AND call each observer's `Prune(activeIDs)` (S2). The perf-guard counters (`TranscriptScansLastScan`/`TitleProbesLastScan`/`SubagentReadDirsLastScan`/`SubagentFileReadsLastScan`) are reset at the top of each Scan and incremented at each Monitor-initiated read.

- [ ] **Step 1 — failing tests** `monitor_test.go`: `TestScan_PopulatesProjectionsAndTopology` (fake corpus, assert SessionSnapshot + SubagentError + ResolvedPath + MaxActivity for each session); `TestScan_DeadPidSessionEnriched` (dead-PID session still tailed → non-zero Snapshot); `TestScan_PrunesVanishedSessionState` (session disappears between scans → its Accumulator/title/subagent-cache entries are gone — assert map sizes shrink, guarding S2). `monitor_perfguard_test.go` (STEADY-STATE framing, S3): run Scan twice on an unchanged corpus, then assert on the SECOND scan: `TitleProbesLastScan()==0` (write-once title cache), `SubagentReadDirsLastScan() <= activeSessionCount` AND each active session's subagents dir counted exactly once (**pg2-fvuk1: never twice**), `SubagentFileReadsLastScan()==0` (unchanged agent files reused), `TranscriptScansLastScan()==0` (all cache_hit); and `TestScan_SkipsOutOfWindowFiles` (a stale-mtime file outside `Window` is never resolved/tailed/opened).
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** `monitor.go` + `topology.go`.
- [ ] **Step 4 — run, expect PASS.**
- [ ] **Step 5 — commit** `feat(corpus): Monitor.Scan single-pass orchestration + perf guard (pg2-uojfm)`.

## Task 6: `Poller.Snapshot` delegates behind a flag + behavioral-equivalence gate

**Files:** Modify `poller.go`; create `internal/core/poller/corpus_equivalence_test.go`.

**Interfaces:** Add `Monitor`/`UseCorpusMonitor` fields + a poller-side `subshellCache map[string]struct{path string; mtime time.Time; count int}`. In `Snapshot`, when `UseCorpusMonitor`: call `p.Monitor.Scan(now)` for the session slice; per session read `path,mtime := p.Monitor.ResolvedPath(sid)` (set `s.TranscriptMTime=mtime`), `snap := p.Monitor`'s SessionSnapshot result, subagent error from the SubagentError observer, `p.Monitor.MaxActivity(sid)` — replacing poller.go:176, the transcript-cache block MINUS subshell (191-222), 286-295, 312.

**MUST preserve verbatim (S5):**

- The subagent-surfacing GATE: overwrite `snap.LastError` with the subagent result ONLY when `path != "" && (snap.LastError == nil || !snap.LastError.IsTerminal)` (poller.go:286-287) — never clobber a genuine main-transcript terminal error; re-derive `snap.LastErrorRetryable = transcript.Retryable(snap.LastError)` after (poller.go:293).
- `maxActivity` is used ONLY in the alive branch (poller.go:312); the dead-PID branch keeps `s.TranscriptMTime` (poller.go:310). Replace ONLY :312.
- **Subshell (B2):** keep `subshellCounter.Count(s.PID)` + `RecordSubprocess("subshell",…)` in the poller; reuse `p.subshellCache[sid].count` when its `(path,mtime)` matches the Monitor's `ResolvedPath`, else recount + update. This preserves both the count AND the no-spawn-on-unchanged behavior.
- **RecordScan parity (N2):** the Monitor already fires `RecordScan("full",0,0)` for `path==""`; do not re-fire in the poller.

Everything else downstream (burn-rate `prevTotalTokens` keyed by sid, status/blocker, git_branch/terminal_host/pr_lookup, aggregate.Build, DB writes, all pruning at poller.go:346-368) is **unchanged**.

- [ ] **Step 1 — failing tests** `corpus_equivalence_test.go`. Extend `makeSessionFixture` (or add a richer local builder) to cover, in the EQUALITY fixture, ONLY cases where old and new agree (S6): named sessions whose `custom-title` is within 200 lines (or single-candidate, or exact `<SessionID>.jsonl`); a subagent terminal error; a **main terminal error + a subagent error on the same session** (gate coverage, S5); a **dead-PID session whose subagents are newer than its transcript** (alive-only maxActivity coverage, S5); a multi-session shared cwd. `TestSnapshot_CorpusMonitorEqualsInline`: run `UseCorpusMonitor=false` vs `=true`, zero `GeneratedAt` on both, `reflect.DeepEqual` the two `*aggregate.Tree`. Separately, `TestSnapshot_TitleAtLine500_CorrectedResolution`: a title at line 500 — assert the NEW path resolves to the titled file and the OLD path does not (the one intended, documented divergence).
- [ ] **Step 2 — run, expect FAIL** (delegation not wired).
- [ ] **Step 3 — implement** the flagged delegation in `poller.go`.
- [ ] **Step 4 — run, expect PASS.** Deep-equal must hold on the equality fixture (both paths use the same `transcript` scan, so `LastError *ErrorRecord` values are equal; `time.Time` fields come from identical code — no monotonic-clock skew, N3). If a divergence appears, it is a real bug in the delegation — fix it, do not weaken the assertion.
- [ ] **Step 5 — commit** `feat(poller): delegate corpus reading to Monitor behind flag + equivalence gate (pg2-uojfm)`.

## Task 7: wire Monitor into the daemon + metric parity

**Files:** Modify `cmd/pa-monitor/daemon.go` (`buildPoller`); add assertions to `internal/core/poller/poller_test.go`.

**Interfaces:** In `buildPoller`: `mon := corpus.New(p.ClaudeHome, &session.Discoverer{SessionsDir, PidAlive})`; `mon.Register(corpus.NewSessionSnapshotObserver())`; `mon.Register(corpus.NewSubagentErrorObserver())`; set `p.Monitor = mon`, `p.UseCorpusMonitor = true`. Thread `SetPhaseRecorder` so the Monitor tail's `RecordScan` reaches the same emitter (via `p.SetPhaseRecorder` fan-out, or `mon.SetPhaseRecorder` alongside).

- [ ] **Step 1 — failing/adjust test** `poller_test.go`: extend `TestSnapshotRecordsPhases` (or add `TestSnapshotRecordsScanModes_CorpusMonitor`) so, with `UseCorpusMonitor=true` + a `fakeRec`, `rec.scans["full"]`/`["incremental"]`/`["cache_hit"]` and `rec.phases["discover"]` all fire from the Monitor path.
- [ ] **Step 2 — run, expect FAIL** (recorder not threaded).
- [ ] **Step 3 — implement** the wiring in `daemon.go` + recorder threading.
- [ ] **Step 4 — run, expect PASS.**
- [ ] **Step 5 — commit** `feat(daemon): wire corpus Monitor into buildPoller, preserve scan metrics (pg2-uojfm)`.

## Task 8: full gate + acceptance

- [ ] **Step 1** — `cd packages/pa-monitor && go test ./...` → all pass.
- [ ] **Step 2** — `go test -race ./internal/core/corpus/... ./internal/core/poller/...` → pass (manual `-race` gate).
- [ ] **Step 3** — from repo root, invoke `pn-workspace-rules` skill, then `nix build .#pa-monitor` → builds.
- [ ] **Step 4 — acceptance review (no code):** confirm against the epic acceptance subset for 1a — (a) each corpus file opened ≤1×/scan, out-of-window/stale files never opened (perf guard green); (b) single component owns transcript+subagent read+parse feeding criteria-gated observers; (c) the dead title cap is the only behavior correction; (d) equivalence + metric parity green; (e) **pg2-fvuk1 requirement:** subagent-dir scans eliminated (no per-session per-tick `LastSubagentError`/double-`ReadDir`), asserted by the perf guard.
- [ ] **Step 5 — commit** any residual (docs/README note if module structure changed).

---

## Deferred to later sub-phases / next session (explicitly out of 1a)

- **1b:** UsagePricing + Limits observers folded into the same Monitor pass; rewire `opts.WeeklyFn`/`opts.Limits` to Monitor projections; generic `Event` + `scanState.feed(Event)` refactor (needed once UsagePricing is a 2nd transcript-decode consumer); remove the `UseCorpusMonitor` flag + old inline path after live soak.
- **Correction to fold into the epic design:** aggregate.Build has no `Inputs` struct (5 positional args); the 5h-block anchor is distance-from-anchor, not a "last ≥5h gap"; `Weekly()` is `CurrentWeekly(ctx)`. (Recorded via `bd` note on pg2-fclt1.)
- **Phase 3 note (pg2-gpbqe coordination):** land pg2-gpbqe's Grafana fixes before/with the phase-3 dashboard update to avoid double-editing the same panels.

## Self-review checklist (run before dispatching critique)

1. **Spec coverage:** phase-1 §7 item 1 (Monitor+observers, delegate, synchronous) → Tasks 1-7; "kill ResolveTranscript hotspot" → Task 2; "kill duplicate subagent reads" → Task 4/5; RecordScan parity → Task 3/7; equivalence gate → Task 6; oracles kept → constraints; perf guard → Task 5/8. UsagePricing/Limits deliberately deferred (documented + user-approved 1a/1b split).
2. **Placeholders:** none — each task has concrete test names, files, and gate commands. (Production code kept at interface+key-logic altitude because the implementer is the same session with full context; TDD produces the bodies.)
3. **Type consistency:** `Monitor.Scan(now) ([]*session.Session, error)`, `ResolvedPath`/`MaxActivity`, observer accessors `Snapshot`/`LastTerminal`, poller fields `Monitor`/`UseCorpusMonitor` used consistently across Tasks 5-7.
