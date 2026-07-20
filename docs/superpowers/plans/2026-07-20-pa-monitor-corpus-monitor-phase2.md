# pa-monitor Corpus Monitor — Phase 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move pa-monitor's point-in-time PULL lookups (`git` branch, `pgrep` subshell, `tmux`/`cmux` terminal-host, `ps -E` env, `git config` repo-label, `gh` PR) behind concrete caching **providers** in a single **nested cache** (`internal/core/provider`), keyed for correctness and evicted on lifecycle transitions, so `subprocess.spawns_total{kind}` drops and Phase 3 can move all providers onto the producer goroutine as one unit — plus fix the latent unbounded/never-expire `PRCache` bug (15-minute found-entry TTL + prune vanished `(cwd,branch)`).

**Architecture:** A new `internal/core/provider` package exposes a `*provider.Cache` with domain-typed accessors (`Env`, `GitBranch`, `Subshell`, `TerminalHost`, `RepoLabel`, `PR`) backed by two eviction subtrees: a **PID/session subtree** (`bySession`, keyed by **session-id** — env, terminal-host, and subshell all live in one `sessionNode`, so a reused PID never cross-contaminates: this is the design's sanctioned PID-reuse-staleness fix) and a **cwd subtree** (`byCwd` — git-branch `UntilFileChanges(.git/HEAD)`, repo-label `LongLived`) refcount-evicted when the last session in a cwd dies. PR is the existing file-backed `session.PRCache`, now bounded, with the provider tracking live `(cwd,branch)` keys to drive its prune. Each provider's fetch/exec boundary is **injectable** (so provider tests stay subprocess-free) and each of the four metered lookups records `RecordSubprocess(kind,d)` **only on an actual fetch** (cache miss), re-homing the metric from the poller into the providers — so the counts drop as designed. The Cache is accessed **only from the (still-synchronous) tick goroutine** in Phase 2; the producer goroutine is Phase 3. A single mutex (never held across a subprocess backend) is included now so Phase 3 needs no retrofit.

**Tech Stack:** Go 1.25 (`packages/pa-monitor`), gomod2nix build engine, `github.com/phillipgreenii/claude-transcript` sibling dep (`replace => ../claude-transcript`), nix flake (`nix build .#pa-monitor`, `pa-monitor-go-tests` flake check), OTel via the nil-safe `poller.PhaseRecorder`/`provider.Recorder` seam.

## Global Constraints

- **No new external deps.** Add nothing to `go.mod`/`gomod2nix.toml`. Providers use only `session`/`signal`/`bridge`/`subshell` + stdlib.
- **Import direction (no cycles — verified):** the new `internal/core/provider` MAY import `internal/core/session`, `internal/core/subshell`, `internal/signal`, `internal/bridge` + stdlib; it MUST NOT import `internal/core/poller`, `internal/daemon`, `internal/core/corpus`, `internal/labels`, or `internal/otel`. `poller` imports `provider`. `internal/labels/detectors` does NOT import `provider` — `detectors.Repo` uses an **inline anonymous interface** field, so there is no import edge at all. (`session`/`signal`/`subshell` import no internal pkg; `bridge` imports only `internal/proto`; `labels/detectors` imports only `labels` — so `provider` is cycle-free.)
- **Providers accessed ONLY from the tick goroutine in Phase 2** (confinement). The producer goroutine is Phase 3. A single `sync.Mutex` guards both subtrees; the lock invariant is **"never hold `c.mu` across a subprocess backend (`ps`/`gh`)"** — the git-branch path holds it across `os.Stat`/local FS walks (cheap, tick-confined), env releases it around the `ps` fetch, and `Reconcile` calls the PR prune (file I/O + its own lock) **after** releasing `c.mu`. The code is written so Phase 3 can move the Cache behind the producer without re-keying or re-locking.
- **Metric contract (pg2-sewtz) preserved — re-homed, not changed.** The four subprocess kinds stay `git_branch` / `subshell` / `terminal_host` / `pr_lookup`. Each is recorded **inside its provider, only when a real fetch happens** (miss/expiry). The poller STOPS recording these four kinds (its four `RecordSubprocess` call sites are deleted). **No kind is added or removed.** `env` and `repo-label` have **no** existing subprocess metric and MUST NOT gain one. Net effect: `subprocess.spawns_total{git_branch}` and `{pr_lookup}` **drop sharply** (today the poller wraps the *lookup call* every tick — git_branch fires per-session-per-tick unconditionally, pr_lookup per-winning-branch-per-tick wrapping `Get` not the spawn); `{subshell}` and `{terminal_host}` already fire only on miss, so their shape is preserved and they too trend to 0 at steady state.
- **pg2-sewtz presence parity is INSTRUMENT-level, not per-label-value** (the instruments are created eagerly at `internal/otel/emitter.go` init, independent of whether a `{kind}` ever fires). **Do NOT add a `!= 0` assertion keyed on a specific subprocess `{kind}` label on a warm start** — moving `pr_lookup` onto real `gh` spawns means the `{pr_lookup}` time-series is legitimately **absent** until the first cache miss/TTL-expiry (the file-backed `PRCache` persists across restarts, so a cold daemon with a `<15m`-warm cache never spawns `gh`). Task 11's perf guard drives a cold cache so all four fire there.
- **Behavior-preserving except the TWO explicitly-corrected latent bugs.** Corrected (design §8): (1) **PID-reuse staleness** in terminal-host/env — fixed by session-id keying; (2) **unbounded/never-expire `PRCache`** — fixed by a 15-minute found-entry TTL + prune of vanished `(cwd,branch)` keys. Everything else observable stays identical: the git-branch string (same walk + `readHead` logic — incl. **subdirectory cwds** and worktree subdirs, and mid-session `git init` still detected because negative resolutions are NOT cached, see below), the subshell count (same `(path,mtime)` invalidation — **no** wall-clock TTL added), the terminal-host string incl. the "re-probe `unknown` each tick" exception and the poller-side cmux/bridge refinement, the env map (dead-PID → empty, matching today's failed-`ps`), and the repo-label value.
- **`session.GitBranch` is a FILE READ, not a subprocess** (verified `internal/core/session/git.go:12-24`): it walks parents (`resolveHeadPath` at git.go:27 checks only `dir/.git`; the parent walk is the `for` loop in `GitBranch` itself) then `readHead`s `.git/HEAD`. It is recorded under the `git_branch` kind (a historical misnomer) and today runs per-session-per-tick uncached — that is why it dominates `subprocess.spawns_total`. The provider caches it `UntilFileChanges(.git/HEAD)` (stat the resolved HEAD mtime; re-read only on change) — the win is eliminating the per-tick walk/read and dropping the metric, NOT eliminating a spawn.
  - **CRITICAL (critique blocker #1):** the HEAD-path resolver MUST replicate `GitBranch`'s **full parent walk**, not the one-level `resolveHeadPath`. Task 3 extracts `session.ResolveHeadPath(dir)` = the whole `GitBranch` walk loop (returns the first *ancestor* HEAD path), and `session.ReadHead(headPath)`; `GitBranch` is refactored to `ResolveHeadPath`+`ReadHead` (body-identical). A subdirectory cwd (`TestGitBranchSubdirectory` git_test.go:47) and a worktree subdir (`TestGitBranchWorktreeSubdirectory` git_test.go:87) MUST resolve to the real branch — extracting only the one-level `resolveHeadPath` would regress these to `""`.
  - **Only POSITIVE resolutions are cached** (critique blocker #1 / should-fix #5). If `ResolveHeadPath(cwd)` finds a repo → cache `headPath`+HEAD-mtime+branch (`UntilFileChanges`). If it does NOT (not a repo) → return `""` and DO NOT cache — the next tick re-walks. Non-repo cwds are rare (claude sessions are almost always in a repo), so the re-walk cost is the same as today for those few, and mid-session `git init` is still detected (no G6 3rd change). `git_branch` still fires per-tick for non-repo cwds (same as today) but drops to ~0 for the repo majority.
- **`ps -E` env IS a real subprocess on darwin** (`internal/core/session/env_darwin.go:20` shells `ps -E -ww -o command= -p <pid>`), read once per session per tick inside `session.Discoverer.Discover` (discovery.go:64), uncached and unmetered. On linux it is a `/proc/<pid>/environ` file read (`env_linux.go:14`). Caching it `WhilePIDAlive` **keyed by session-id** (critique should-fix #3: PID keying has a reuse race where a recycled PID whose old session file vanished the same tick serves the prior process's env) is a real darwin CPU win; env has **no** metric surface (do not add one).
- **`repo-label` is already cached per-session** by the daemon's `labelCache` (lifecycle.go:439, keyed by session.id, pruned on vanish). The per-cwd `LongLived` provider only dedups the N-sessions-share-one-cwd case. Keep the provider thin; it emits **no** metric.
- **Provider fetch boundaries MUST be injectable** (precedent: `session.Discoverer.ReadEnv`, `session.PidAlive`, `subshell.Counter.RunPs`, `PRLookupFn`), so provider tests never spawn a subprocess. Tests inject a fake clock (`now func() time.Time`) for PR `FoundTTL`; git-branch freshness uses `os.Stat().ModTime()` (tests drive it with `os.Chtimes`, not `now`).
- **Behavioral equivalence gates the phase** (three layers): (1) the existing poller/corpus/aggregate suites stay green (provider-internal changes; block/limits/session projections unchanged); (2) provider-source unit tests per freshness policy + PID-reuse + cwd-refcount eviction + injected fetch + fake clock; (3) **a poller-level old-vs-new gate that actually exercises real resolution** — a fixture session whose cwd is a real subdirectory of a temp git repo, asserting the provider-backed `Branch` equals `session.GitBranch(cwd)` (critique: the Phase-1 equivalence fixtures use non-repo `/tmp` cwds → branch `""` both ways → they cannot catch the resolver blocker). Build every corpus/temp dir in `t.TempDir()` — never the real `~/.claude`.
- **Gate per landing:** `cd packages/pa-monitor && go test ./...` + `nix build .#pa-monitor` MUST pass. `go test -race ./internal/core/provider/... ./internal/core/poller/... ./internal/core/corpus/... ./internal/daemon/...` as a manual pre-merge step. **No flake interface change** → **Tier-1** gate (`nix build .#pa-monitor` + `nix build .#pa-monitor-go-tests` + the golangci check); no `pn workspace flake-check`. Invoke `pn-workspace-rules` before `nix build`.
- **Worktree discipline (R-1..R-9):** canonical clone stays on `main`; work in the `pg2-ll4fl-corpus-phase2` worktree (subset workforest set); `nix run .#install-pre-commit-hooks` was run at set creation. Never `--no-verify`. Commit subjects carry the bead id `(pg2-ll4fl)`. Integrate via the `integrate-branch` skill → `ff-merge-to-main`. **R-8:** before the ff-merge, verify the canonical clone is on `main` and clean; halt + report if not.

---

## File Structure

**Create:**

- `internal/core/provider/doc.go` — package doc (two subtrees, freshness policies, tick-confinement + Phase-3 note, no-otel/no-labels import discipline).
- `internal/core/provider/cache.go` — `Cache` struct (`bySession`/`byCwd` maps + `prLiveKeys` + mutex + `now` + `rec` + injectable fetch fields), `New`, `SetRecorder`, `Recorder` interface, `record`/`Record` helpers, `BeginScan`, `Reconcile`, unexported `sessionNode`/`cwdNode`.
- `internal/core/provider/cache_test.go` — construction, `SetRecorder` nil-safety, `BeginScan`, `Reconcile` eviction (cross-provider), `Record`/`record`.
- `internal/core/provider/env.go` — `(*Cache).Env(sessionID string, pid int) (map[string]string, error)` (session-id-keyed `WhilePIDAlive`; dead pid → empty, no spawn).
- `internal/core/provider/env_test.go`.
- `internal/core/provider/gitbranch.go` — `(*Cache).GitBranch(cwd string) string` (cwd-keyed `UntilFileChanges(.git/HEAD)`; positive-only caching; `git_branch` metric on miss).
- `internal/core/provider/gitbranch_test.go`.
- `internal/core/provider/subshell.go` — `(*Cache).Subshell(sessionID string, pid int, path string, mtime time.Time) int` (session-id-keyed, `(path,mtime)` invalidation; `subshell` metric on miss).
- `internal/core/provider/subshell_test.go`.
- `internal/core/provider/terminalhost.go` — `(*Cache).TerminalHost(sessionID string, pid int) string` (session-id-keyed `WhilePIDAlive`; re-probe on `unknown`; `terminal_host` metric on miss).
- `internal/core/provider/terminalhost_test.go`.
- `internal/core/provider/repolabel.go` — `(*Cache).RepoLabel(cwd string) (string, bool)` (cwd-keyed `LongLived`; no metric).
- `internal/core/provider/repolabel_test.go`.
- `internal/core/provider/pr.go` — `(*Cache).PR(ctx, cwd, branch string) (*session.PRInfo, error)` (delegates to `PRBackend`; records the live `(cwd,branch)` key for prune; `pr_lookup` metric fires inside the wired `PRCache.LookupFn`, see Task 7/10).
- `internal/core/provider/pr_test.go`.
- `internal/core/provider/reconcile_test.go` — nested eviction: session-absent, cwd refcount, PID-reuse, tombstone (dead-not-GC'd session keeps frozen terminal-host), PR prune cascade.
- `internal/core/provider/perfguard_test.go` — injected fetch-counter: each boundary called ≤1× per key per scan and 0× on a steady 2nd scan; all four kinds fire ≥1× on the cold scan.

**Modify:**

- `internal/core/session/git.go` — extract `ResolveHeadPath(dir string) (string, bool)` (the **full parent walk** currently inside `GitBranch`) + `ReadHead(headPath string) string` (exported `readHead`); refactor `GitBranch` to call them (body-identical). Pure, non-behavioral.
- `internal/core/session/discovery.go` — widen `Discoverer.ReadEnv` from `func(pid int) (map[string]string, error)` to `func(sessionID string, pid int) (map[string]string, error)`; `Discover` calls `readEnv(r.SessionID, r.PID)`; the nil default becomes `func(_ string, pid int) (map[string]string, error) { return ReadProcessEnv(pid) }`.
- `internal/core/session/discovery_test.go` — `TestDiscover_PopulatesEnvViaInjectedReader` updated to the 2-arg `ReadEnv` (assert the session-id is threaded).
- `internal/core/session/pr_cache.go` — add `FoundTTL time.Duration` (0 = never expire, old behavior); found-entry read short-circuit also checks `c.FoundTTL == 0 || c.Now().Sub(e.FetchedAt) < c.FoundTTL`; **fix the write-back guard** so a fresh found result overwrites an EXPIRED found entry (critique blocker #2 — otherwise it re-spawns `gh` every tick after 15m); add `func PRCacheKey(cwd, branch string) string` (exported wrapper) and `func (c *PRCache) Prune(live map[string]bool)`.
- `internal/core/session/pr_cache_test.go` — `TestPRCache_FoundEntryExpiresAfterFoundTTL`, `TestPRCache_ExpiredThenRefetchedIsCachedAgain` (the blocker-#2 guard: after the post-TTL re-fetch, an immediate `Get` does NOT call `LookupFn`), `TestPRCache_FoundEntryNeverExpiresWhenTTLZero`, `TestPRCache_PruneDropsVanishedKeys`.
- `internal/core/poller/poller.go` — add `Providers *provider.Cache`; rewire `Snapshot` (details Task 9); delete `terminalHostCache`/`subshellCache`/`subshellCacheEntry`/`countSubshellCached` + the four `RecordSubprocess` sites + the `terminalHostCache`/`subshellCache` prune loops + `PRLookupFn` field + the `if p.PRLookupFn != nil` gate; keep `refineCmuxTerminalHost` + `detectTerminalHost` (the latter reused by the `FetchTerminalHost` default closure — or lift to `signallayer.DetectHost`); `SetPhaseRecorder` also fans to `p.Providers`; `Snapshot` lazily constructs a default `provider.New(p.Now)` when `p.Providers == nil` (bare/test poller — nil fetch boundaries → `unknown`/`""`/no-PR, matching today's nil-`PRLookupFn`).
- `internal/core/poller/poller_test.go` — recorder test: `*provider.Cache` + `fakeRec`, four kinds fire on cold scan, no increase on steady 2nd scan; Tree `Branch`/`TerminalHost`/`SubshellCount` equal injected fetch values. **Add the real-repo-subdir equivalence fixture** (Global Constraints layer 3).
- `internal/labels/detectors/repo.go` — `Repo` gains an optional inline-interface field `Cache interface{ RepoLabel(cwd string) (string, bool) }`; `Detect` delegates when non-nil, else runs the existing inline `git config` path unchanged (`Repo{}` zero-value keeps working). Export `NormaliseOrigin` (rename `normaliseOrigin`, update callers) so `buildPoller`'s `FetchRepoLabel` reuses it DRY.
- `internal/labels/detectors/repo_test.go` — `TestRepo_UsesCacheWhenSet` (fake cache consulted, `git` not spawned); existing `Repo{}` tests unchanged.
- `cmd/pa-monitor/daemon.go` — `buildPoller`: build `provider.New(time.Now)`, set injectable fetch fields (`PidAlive`, `FetchTerminalHost`=closure over `signalers`, `FetchRepoLabel`=git-config/`NormaliseOrigin`/`local:<hash>`, `PRBackend`=`prCache.Get`, `PRPrune`=`prCache.Prune`), set `prCache.FoundTTL = 15*time.Minute` and `prCache.LookupFn`=a timing wrapper that calls `cache.Record("pr_lookup", …)` around `session.LookupPR` (fires only on a real `gh` spawn), wire `Discoverer.ReadEnv = cache.Env`, `p.Providers = cache`, and register `detectors.Repo{Cache: cache}` in `Detectors`.
- `packages/pa-monitor/README.md` / repo `CLAUDE.md` — only if module-structure notes require it (new `internal/core/provider`).

**Do NOT touch in Phase 2:** the producer-goroutine / `DerivedState` / `ChangeSource` design (Phase 3); the `RecordPhase` re-homing (Phase 3); `grafana/pa-monitor-overview.json` (Phase 3, coordinate pg2-gpbqe); the gRPC/proto + clickable-PR TUI (Phase 4); `capture-status.bash`; the corpus Monitor's transcript/status folds (Phase 1).

---

## Interfaces (locked signatures — every task references these)

```go
// internal/core/provider/cache.go
package provider

import (
	"context"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// Recorder receives the subprocess timings the providers emit. Defined HERE (not
// imported from internal/otel or internal/core/poller) so provider has no
// dependency on either. A nil Recorder disables recording.
type Recorder interface {
	RecordSubprocess(kind string, d time.Duration)
}

type Cache struct {
	now func() time.Time
	mu  sync.Mutex // guards both subtrees + prLiveKeys; never held across a ps/gh backend
	rec Recorder   // nil-safe

	bySession  map[string]*sessionNode // env + terminal-host + subshell (PID/session lifecycle)
	byCwd      map[string]*cwdNode     // git-branch + repo-label (workspace lifecycle; refcount-evicted)
	prLiveKeys map[string]bool         // (cwd,branch) keys touched this scan; drives PR prune

	// Injectable fetch boundaries (nil → the documented default / no-op).
	PidAlive          func(pid int) bool                                                     // default session.DefaultPidAlive
	FetchEnv          func(pid int) (map[string]string, error)                               // default session.ReadProcessEnv
	FetchGitBranch    func(cwd string) (branch, headPath string, ok bool)                    // default: session.ResolveHeadPath + ReadHead
	FetchSubshell     func(pid int) (int, error)                                             // default (&subshell.Counter{}).Count
	FetchTerminalHost func(pid int) string                                                   // default nil → "unknown" (buildPoller sets a signalers closure)
	FetchRepoLabel    func(cwd string) (string, bool)                                        // default nil → ("",false) (buildPoller sets git-config closure)
	PRBackend         func(ctx context.Context, cwd, branch string) (*session.PRInfo, error) // default nil → (nil,nil); buildPoller sets prCache.Get
	PRPrune           func(live map[string]bool)                                             // default nil → no-op; buildPoller sets prCache.Prune
}

func New(now func() time.Time) *Cache // now nil → time.Now
func (c *Cache) SetRecorder(r any)     // stores r iff r.(Recorder); nil-safe. (*Cache does NOT satisfy Recorder — see Record.)

// Record forwards a subprocess timing to the wired Recorder (nil-safe). Named
// Record (NOT RecordSubprocess) so *Cache does not accidentally satisfy Recorder
// — otherwise SetRecorder(cache) would self-recurse. The PRCache.LookupFn timing
// wrapper (buildPoller) calls this so the pr_lookup metric fires only on a real gh spawn.
func (c *Cache) Record(kind string, d time.Duration)

// BeginScan clears prLiveKeys. Called by the poller once per tick, immediately
// after Monitor.Scan and BEFORE the per-session loop's PR calls.
func (c *Cache) BeginScan()

// Reconcile evicts nodes whose lifecycle ended. sessions is the full current set
// (alive + dead-PID pre-GC — so a dead-not-GC'd session's node is KEPT, preserving
// its frozen terminal-host; env still returns empty for it via the alive gate).
// Called once per tick AFTER the per-session loop + PR calls. Evicts bySession
// ids ∉ sessions; byCwd cwds with 0 referencing sessions (cascades git-branch +
// repo-label); then, AFTER releasing c.mu, calls PRPrune(prLiveKeys). Alive/live
// sets are built from the sessions slice (no PidAlive re-probe).
func (c *Cache) Reconcile(sessions []*session.Session)

// Typed accessors (fetch-on-miss, honor freshness):
func (c *Cache) Env(sessionID string, pid int) (map[string]string, error) // dead pid → ({}, nil), no spawn; returns a copy
func (c *Cache) GitBranch(cwd string) string
func (c *Cache) Subshell(sessionID string, pid int, path string, mtime time.Time) int
func (c *Cache) TerminalHost(sessionID string, pid int) string // bare Name(); cmux refine stays in poller
func (c *Cache) RepoLabel(cwd string) (string, bool)
func (c *Cache) PR(ctx context.Context, cwd, branch string) (*session.PRInfo, error)
```

```go
// unexported node types (internal to package provider)
type sessionNode struct {
	pid          int
	env          map[string]string // cached-while-alive (nil until first alive fetch)
	envFetched   bool
	terminalHost string // "" = not yet detected
	subPath      string
	subMtime     time.Time
	subCount     int
	subValid     bool
}

type cwdNode struct {
	headPath    string    // resolved ancestor .git/HEAD ("" = not cached / negative)
	branch      string
	branchMtime time.Time
	branchValid bool // true only for a POSITIVE resolution (repo found)
	repoLabel   string
	repoKnown   bool
}
```

```go
// internal/core/session/git.go — extracted (pure, body-identical)
func ResolveHeadPath(dir string) (string, bool) // FULL parent walk; first ancestor's .git/HEAD path
func ReadHead(headPath string) string           // exported readHead
// GitBranch(dir) == { hp, ok := ResolveHeadPath(dir); if !ok { return "" }; return ReadHead(hp) }
```

```go
// internal/core/session/pr_cache.go — additions
type PRCache struct { /* ...existing... */ FoundTTL time.Duration } // 0 = never expire (old behavior)
func PRCacheKey(cwd, branch string) string     // exported wrapper over prCacheKey
func (c *PRCache) Prune(live map[string]bool)  // drop entries whose key ∉ live; persist if changed
```

```go
// internal/core/session/discovery.go — widened
ReadEnv func(sessionID string, pid int) (map[string]string, error) // was func(pid int)...
```

```go
// internal/labels/detectors/repo.go — additions
type Repo struct {
	Cache interface{ RepoLabel(cwd string) (string, bool) } // optional; nil → inline git-config path
}
func NormaliseOrigin(url string) string // exported (was normaliseOrigin)
```

```go
// internal/core/poller/poller.go — additions/removals
type Poller struct { /* ...existing minus PRLookupFn... */ Providers *provider.Cache }
// DELETED: terminalHostCache, subshellCache, subshellCacheEntry, countSubshellCached, PRLookupFn field.
```

---

## Task 1: `provider` package foundation — Cache, Recorder seam, BeginScan, Reconcile, PR-prune hook

**Files:** Create `internal/core/provider/doc.go`, `cache.go`, `cache_test.go`.

**Interfaces produced:** `provider.Cache`, `New`, `Recorder`, `SetRecorder`, `Record`, `BeginScan`, `Reconcile`, `sessionNode`, `cwdNode`, and the `prLiveKeys`/`PRBackend`/`PRPrune` fields (so Task 7's PR tests can land without re-touching `Reconcile`).

**Key logic:** `New(now)` defaults `now`→`time.Now`, allocates `bySession`/`byCwd`/`prLiveKeys`. `SetRecorder(r any)` stores `r` iff `r.(Recorder)` ok. `Record(kind, d)` → `if c.rec != nil { c.rec.RecordSubprocess(kind, d) }`; `record(kind string, start time.Time)` = `c.Record(kind, c.now().Sub(start))`. `BeginScan()` → under `c.mu`, `c.prLiveKeys = map[string]bool{}`. `Reconcile(sessions)`: build `liveSessions map[string]bool` + `cwdRef map[string]int` (count non-empty cwd per session) from `sessions`; under `c.mu` drop `bySession` ids ∉ `liveSessions` and `byCwd` cwds with `cwdRef==0`; **snapshot `prLiveKeys` + read `PRPrune` under the lock, then release, then call `PRPrune(snapshot)`** (never hold `c.mu` across the backend). No `PidAlive` re-probe (env's alive gate lives in `Env`).

- [ ] **Step 1 — failing tests** `cache_test.go`: `TestNew_DefaultsNow`, `TestSetRecorder_NilSafe` (`nil` + a `struct{}{}` both ignored; a metered call does not panic), `TestSetRecorder_CacheNotRecorder` (`SetRecorder(c)` on the Cache itself is a no-op — guards the self-recursion footgun), `TestBeginScan_ClearsLiveKeys`, `TestReconcile_DropsAbsentSession`, `TestReconcile_DropsZeroRefcountCwd`, `TestReconcile_CallsPRPruneWithLiveKeys` (set `PRPrune` to a recorder fn; `BeginScan`; add a key via a direct `prLiveKeys` populate helper or a `PR` call stub; `Reconcile` → `PRPrune` seen with that key).
- [ ] **Step 2 — run, expect FAIL** (`go test ./internal/core/provider/`).
- [ ] **Step 3 — implement** `doc.go` + `cache.go` (struct, `New`, `SetRecorder`, `Record`/`record`, `BeginScan`, `Reconcile`, node types).
- [ ] **Step 4 — run, expect PASS.**
- [ ] **Step 5 — commit** `feat(provider): nested-cache foundation (Cache, Recorder seam, BeginScan, Reconcile) (pg2-ll4fl)`.

## Task 2: Env provider — session-id-keyed WhilePIDAlive

**Files:** Create `internal/core/provider/env.go`, `env_test.go`.

**Interfaces produced:** `(*Cache).Env(sessionID string, pid int) (map[string]string, error)`.

**Key logic:** `pidAlive := c.PidAlive; if nil { pidAlive = session.DefaultPidAlive }`; `fetch := c.FetchEnv; if nil { fetch = session.ReadProcessEnv }`. Under `c.mu`: get/create `node := c.bySession[sessionID]` (store `node.pid = pid`). If `!pidAlive(pid)` → return `(map[string]string{}, nil)` (dead → empty, **no spawn**; matches today's failed `ps`; leave any stale `node.env` untouched — it's never served while dead, and the node is dropped when the session leaves the set). If `node.envFetched` → return a **copy** of `node.env`. Else: **release `c.mu`** (a `ps` must not hold the lock), `env, _ := fetch(pid)`, **re-acquire**, re-check `node` still present (it is — tick-confined), store `node.env = env, node.envFetched = true`, return a copy. Env has **no** metric. Session-id keying (not PID) makes it reuse-safe: a recycled PID is a new session-id → fresh fetch; a dead session's node returns `{}` via the alive gate regardless.

- [ ] **Step 1 — failing tests** `env_test.go`: `TestEnv_FetchesOnceWhileAlive` (inject `FetchEnv` counter + `PidAlive`→true; two `Env("s",42)` → fetch once, same map), `TestEnv_DeadPidEmptyNoFetch` (`PidAlive`→false → empty, fetch never called), `TestEnv_ReturnsCopy`, `TestEnv_ReusedPidFreshPerSession` (`Env("old",42)`→A while alive; then `Env("new",42)` (different session-id, same pid, fetch→B) → B, not A — the reuse-safety property).
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** `env.go`.
- [ ] **Step 4 — run, expect PASS.**
- [ ] **Step 5 — commit** `feat(provider): env provider (session-keyed WhilePIDAlive, dead-pid empty no-spawn) (pg2-ll4fl)`.

## Task 3: GitBranch provider — cwd-keyed UntilFileChanges(.git/HEAD), full parent walk

**Files:** Modify `internal/core/session/git.go`, `git_test.go` (extraction stays green). Create `internal/core/provider/gitbranch.go`, `gitbranch_test.go`.

**Interfaces produced:** `session.ResolveHeadPath`, `session.ReadHead`, `(*Cache).GitBranch(cwd string) string`.

**Key logic — `git.go`:** extract the `GitBranch` **for-loop** into `ResolveHeadPath(dir) (string, bool)` (returns the first ancestor whose `.git` resolves to a HEAD path — reuses the existing one-level `resolveHeadPath` inside the loop) and `readHead`→`ReadHead(headPath) string`; `GitBranch(dir)` becomes `hp, ok := ResolveHeadPath(dir); if !ok { return "" }; return ReadHead(hp)`. Behavior-identical (git_test.go must stay green, incl. subdirectory + worktree-subdir cases).

**Key logic — `gitbranch.go`:** `fetch := c.FetchGitBranch; if nil { fetch = defaultFetchGitBranch }` where `defaultFetchGitBranch(cwd) (string,string,bool)` = `hp, ok := session.ResolveHeadPath(cwd); if !ok { return "","",false }; return session.ReadHead(hp), hp, true`. Under `c.mu`: `node := c.byCwd[cwd]` (create). If `node.branchValid` → `st, err := os.Stat(node.headPath)`; if `err==nil && st.ModTime().Equal(node.branchMtime)` → return `node.branch` (**no read, no metric**). Otherwise (invalid, or mtime changed/err): `start := c.now(); branch, headPath, ok := fetch(cwd); c.record("git_branch", start)`. If `ok`: `node.headPath = headPath; node.branch = branch; node.branchValid = true`; if `st2, e := os.Stat(headPath); e == nil { node.branchMtime = st2.ModTime() }`; return `branch`. If `!ok` (**not a repo**): `node.branchValid = false` (do NOT cache a negative — re-walk next tick so a mid-session `git init` is still seen); return `""`. Holding `c.mu` across the local FS walk is allowed (invariant is only "no lock across `ps`/`gh`"). **Metric fires only on the fetch branch** → `git_branch` drops to ~0 for repo cwds at steady state.

- [ ] **Step 1 — failing tests** `gitbranch_test.go`: build a temp git repo (`.git/HEAD`=`ref: refs/heads/foo`). `TestGitBranch_ReadsThenCachesUntilHeadChanges` (call → "foo" + 1 metric; call again, HEAD unchanged → "foo" + 0 new metric; rewrite HEAD=`ref: refs/heads/bar` + `os.Chtimes` bump → "bar" + 1 metric). `TestGitBranch_SubdirectoryCwd` (cwd = a nested subdir of the repo → "foo", NOT "" — guards blocker #1). `TestGitBranch_DetachedHeadShortSha` (HEAD=40-char sha → 7-char prefix). `TestGitBranch_NonRepoNotCachedRewalks` (a non-repo temp dir → ""; inject a counting `FetchGitBranch` and assert it is called on BOTH of two successive calls — negatives are not cached). `TestGitBranch_RecordsOnlyOnRead` (`fakeRec`: 1 after two same-HEAD calls).
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** the git.go extraction + `gitbranch.go`.
- [ ] **Step 4 — run, expect PASS**, plus `go test ./internal/core/session/...` (git subdir/worktree tests green).
- [ ] **Step 5 — commit** `feat(provider): git-branch provider (UntilFileChanges .git/HEAD, full walk, positive-only cache) (pg2-ll4fl)`.

## Task 4: Subshell provider — session-id-keyed (path,mtime) invalidation

**Files:** Create `internal/core/provider/subshell.go`, `subshell_test.go`.

**Interfaces produced:** `(*Cache).Subshell(sessionID string, pid int, path string, mtime time.Time) int`.

**Key logic:** under `c.mu`: `node := c.bySession[sessionID]` (create, store `pid`). If `node.subValid && node.subPath == path && node.subMtime.Equal(mtime)` → return `node.subCount` (**no spawn, no metric**) — exactly today's `countSubshellCached` `(path,mtime)` reuse. Else: `fetch := c.FetchSubshell; if nil { fetch = (&subshell.Counter{}).Count }`; `start := c.now()`; **release `c.mu`**, `n, _ := fetch(pid)`, **re-acquire** (`pgrep` is a subprocess); `c.record("subshell", start)`; if `path != ""` set `node.subPath/subMtime/subCount=n, subValid=true` (skip storing for `path==""`, matching today). Return `n`. **No wall-clock TTL** — the `(path,mtime)` gate IS the transcript-change event; a TTL would add `pgrep` spawns for idle sessions (regression).

- [ ] **Step 1 — failing tests** `subshell_test.go`: `TestSubshell_CachesUntilMtimeChanges` (counter fetch; two same-`(path,mtime)` → fetch once; bump mtime → twice), `TestSubshell_PathEmptyAlwaysFetches`, `TestSubshell_RecordsOnlyOnFetch`, `TestSubshell_TwoSessionsSamePidIndependent` (sessions "a"/"b", same pid, different paths → independent counts).
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** `subshell.go`.
- [ ] **Step 4 — run, expect PASS.**
- [ ] **Step 5 — commit** `feat(provider): subshell provider (session-keyed path/mtime cache) (pg2-ll4fl)`.

## Task 5: TerminalHost provider — session-id-keyed WhilePIDAlive (re-probe unknown)

**Files:** Create `internal/core/provider/terminalhost.go`, `terminalhost_test.go`.

**Interfaces produced:** `(*Cache).TerminalHost(sessionID string, pid int) string` (bare host; `refineCmuxTerminalHost` stays in the poller and wraps this).

**Key logic:** under `c.mu`: `node := c.bySession[sessionID]` (create). If `node.terminalHost != "" && node.terminalHost != "unknown"` → return it (cached; `unknown` re-probes). Else: `fetch := c.FetchTerminalHost; if fetch == nil { return "unknown" }` (bare Cache never panics — the metric is not recorded for the nil path since no fetch happened). `start := c.now()`; **release `c.mu`**, `host := fetch(pid)`, **re-acquire** (terminal detect may `ps`/`tmux`/`cmux`); `c.record("terminal_host", start)`; `node.terminalHost = host`; return `host`. Session-id keying is the **PID-reuse fix**: a reused PID = a new session-id → its own fresh detection, never a dead session's value; a dead-not-GC'd session keeps its own frozen host.

- [ ] **Step 1 — failing tests** `terminalhost_test.go`: `TestTerminalHost_CachesNonUnknown`, `TestTerminalHost_ReprobesUnknown` (fetch "unknown" then "cmux" → first "unknown"+fetch, second re-probes "cmux"), `TestTerminalHost_RecordsOnlyOnFetch`, `TestTerminalHost_PidReuseNoCrossContamination` (session "dead" cached "tmux" @pid42; session "new" @pid42 fetch→"cmux" → "new"="cmux", "dead" still "tmux"), `TestTerminalHost_NilFetchUnknownNoMetric` ("unknown", no panic, `fakeRec` records 0).
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** `terminalhost.go`.
- [ ] **Step 4 — run, expect PASS.**
- [ ] **Step 5 — commit** `feat(provider): terminal-host provider (session-keyed, PID-reuse-safe) (pg2-ll4fl)`.

## Task 6: RepoLabel provider — cwd-keyed LongLived

**Files:** Create `internal/core/provider/repolabel.go`, `repolabel_test.go`.

**Interfaces produced:** `(*Cache).RepoLabel(cwd string) (string, bool)`.

**Key logic:** under `c.mu`: `node := c.byCwd[cwd]` (create). If `node.repoKnown` → return `(node.repoLabel, node.repoLabel != "")`. Else: `fetch := c.FetchRepoLabel; if fetch == nil { return "", false }`; `v, ok := fetch(cwd)`; `node.repoLabel = v; node.repoKnown = true`; return `(v, ok && v != "")`. `LongLived` — a cwd's origin is stable. **No metric.** The `FetchRepoLabel` default (set in Task 10's `buildPoller`) reproduces `detectors.Repo.Detect` exactly (origin→`NormaliseOrigin`; on `git config` error→`local:<hash of git-common-dir>`; `""` cwd→`("",false)`); a fetch that hits a transient error MAY return `("",false)` and, because the provider caches on `repoKnown`, that empty is cached LongLived — acceptable for repo-label (a cwd that is not a repo stays not a repo; the per-session `labelCache` remains the authoritative retry layer).

- [ ] **Step 1 — failing tests** `repolabel_test.go`: `TestRepoLabel_FetchesOncePerCwd` (counter; two `RepoLabel("/a")` → fetch once), `TestRepoLabel_DistinctCwds`, `TestRepoLabel_NilFetchEmpty`, `TestRepoLabel_EmptyResultCachedLongLived` (fetch→`("",false)` once; second call does not re-fetch).
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** `repolabel.go`.
- [ ] **Step 4 — run, expect PASS.**
- [ ] **Step 5 — commit** `feat(provider): repo-label provider (LongLived per-cwd) (pg2-ll4fl)`.

## Task 7: PR provider + bounded PRCache (15m found-TTL + prune) — write-back guard fixed

**Files:** Modify `internal/core/session/pr_cache.go`, `pr_cache_test.go`. Create `internal/core/provider/pr.go`, `pr_test.go`.

**Interfaces produced:** `session.PRCache.FoundTTL`, `session.PRCacheKey`, `(*session.PRCache).Prune`, `(*Cache).PR`.

**Key logic — `pr_cache.go`:** add `FoundTTL time.Duration`. Found-entry read short-circuit (pr_cache.go:66-70) becomes `if ok && e.PR != nil && (c.FoundTTL == 0 || c.Now().Sub(e.FetchedAt) < c.FoundTTL) { return e.PR, nil }` (`0` = today's never-expire). **Fix the write-back guard (blocker #2):** the existing `if !alreadySet || (existing.PR == nil && entry.PR != nil)` never stores a refreshed found entry, so a post-TTL found result is dropped → re-spawn every tick. Change to also overwrite an expired found entry: `stale := existing.PR != nil && c.FoundTTL > 0 && c.Now().Sub(existing.FetchedAt) >= c.FoundTTL; if !alreadySet || existing.PR == nil || stale { c.entries[key] = entry; …marshal + writeFile… }` (still never lets a not-found clobber a live, unexpired found entry). `PRCacheKey(cwd, branch)` wraps `prCacheKey`. `Prune(live map[string]bool)`: under `c.mu`, delete keys ∉ `live`; if any dropped, marshal + `writeFile` outside the lock. The `gh` subprocess is spawned only inside `LookupFn` on a miss/expiry.

**Key logic — `provider/pr.go`:** `PR(ctx, cwd, branch)`: `backend := c.PRBackend; if backend == nil { return nil, nil }`; under `c.mu` set `c.prLiveKeys[session.PRCacheKey(cwd, branch)] = true`, release; `return backend(ctx, cwd, branch)`. **No metric here** — `pr_lookup` timing lives in the wired `PRCache.LookupFn` (Task 10), which is the only layer that knows hit vs. `gh`-spawn (single cache, no double-caching; the metric fires only on a real spawn).

- [ ] **Step 1 — failing tests** `pr_cache_test.go`: `TestPRCache_FoundEntryExpiresAfterFoundTTL` (inject `Now`, `FoundTTL=15m`; miss fetches; within 15m → `LookupFn` NOT called; advance 16m → re-fetch), `TestPRCache_ExpiredThenRefetchedIsCachedAgain` (after the 16m re-fetch, an immediate `Get` does NOT call `LookupFn` — the blocker-#2 guard), `TestPRCache_FoundEntryNeverExpiresWhenTTLZero`, `TestPRCache_PruneDropsVanishedKeys`. `pr_test.go`: `TestPR_TracksLiveKeys` (`BeginScan`; `PR("/a","foo")`; assert `prLiveKeys` holds `PRCacheKey("/a","foo")` and the backend was called), `TestPR_NilBackendNoPR`.
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** the `pr_cache.go` additions + `provider/pr.go`.
- [ ] **Step 4 — run, expect PASS**, plus `go test ./internal/core/session/...`.
- [ ] **Step 5 — commit** `fix(session): bound PRCache (15m found-TTL + refresh write-back + prune); feat(provider): PR provider (pg2-ll4fl)`.

## Task 8: Nested-cache eviction — cwd refcount cascade + PID reuse + tombstone + PR prune

**Files:** Modify `internal/core/provider/cache.go` (Reconcile is already implemented in Task 1; this task only ADDS cascade/refcount test coverage and any refinement the tests surface). Create `internal/core/provider/reconcile_test.go`.

**Interfaces produced:** the verified `Reconcile` semantics (no signature change from Task 1).

**Key logic:** confirm `Reconcile` (Task 1): `bySession` ids ∉ `sessions` dropped; `byCwd` cwds with refcount 0 dropped (dropping the node cascades git-branch + repo-label — no orphans); `PRPrune(prLiveKeys)` after unlock. **Tombstone:** a dead-PID session that is still in `sessions` (pre-GC) is NOT evicted — its `sessionNode` (frozen terminal-host) survives; `Env` returns `{}` for it via the alive gate (no re-probe of a dead pid). This is the design §8 "tombstone" behavior realized by keeping the node while the session is in the set.

- [ ] **Step 1 — failing tests** `reconcile_test.go`: `TestReconcile_CwdRefcountKeepsAliveWithSecondSession` (two sessions in `/a`; both → `/a` kept; drop one → kept (1 ref); drop both → evicted, next `GitBranch("/a")` re-fetches), `TestReconcile_CascadeDropsBranchAndRepoLabel`, `TestReconcile_PrunesVanishedPRKeys` (`BeginScan`; `PR("/a","foo")`; `Reconcile` → `PRPrune` called with exactly `{PRCacheKey("/a","foo")}`), `TestReconcile_DeadNotGCdSessionKeepsFrozenTerminalHost` (session "s" pid42 alive → `TerminalHost`="tmux"; now pid42 dead but "s" still in `sessions`; `Reconcile`; `TerminalHost("s",42)` still "tmux" WITHOUT calling `FetchTerminalHost` again — tombstone), `TestReconcile_PidReuseEndToEnd` (session "old" pid42 alive → env A + terminal "tmux"; "old" leaves `sessions`; `Reconcile`; session "new" pid42 alive → env B + terminal from a fresh fetch, never A/"tmux").
- [ ] **Step 2 — run, expect FAIL** (or reveal a Reconcile gap).
- [ ] **Step 3 — implement** any Reconcile refinement the tests require.
- [ ] **Step 4 — run, expect PASS**, plus `go test ./internal/core/provider/...`.
- [ ] **Step 5 — commit** `test(provider): nested eviction — cwd refcount, PID reuse, tombstone, PR prune (pg2-ll4fl)`.

## Task 9: Wire providers into `Poller.Snapshot`

**Files:** Modify `internal/core/poller/poller.go`, `poller_test.go`.

**Interfaces produced:** `Poller.Providers`, the provider-backed `Snapshot` (behaviorally equivalent).

**Key logic:** add `Providers *provider.Cache`; delete the `PRLookupFn` field. In `Snapshot`, after `sessions, err := p.Monitor.Scan(now)` + the lazy-init block: if `p.Providers == nil`, lazily construct `provider.New(p.Now)` (fetch boundaries nil → `unknown`/`""`/no-PR; PidAlive defaults) so bare/test pollers work; then `p.Providers.BeginScan()`. In the per-session loop, replace:
- `shells := p.countSubshellCached(...)` → `shells := p.Providers.Subshell(s.SessionID, s.PID, path, mtime)`.
- the git-branch block → `s.Branch = p.Providers.GitBranch(s.Cwd)`.
- the terminal-host detect/cache block → `s.TerminalHost = p.Providers.TerminalHost(s.SessionID, s.PID)`; **keep** `if s.TerminalHost == "cmux" { s.TerminalHost = refineCmuxTerminalHost(p.Signalers, p.BridgeRegistry, s.PID) }`.

Delete `terminalHostCache`/`subshellCache` fields + `subshellCacheEntry` + `countSubshellCached` + the two prune loops (Reconcile owns eviction). `detectTerminalHost` stays (the `FetchTerminalHost` closure in `buildPoller` calls it — or lift to `signallayer.DetectHost`). Replace the PR loop body: `info, err := p.Providers.PR(ctx, cwd, branch)` (drop the `RecordSubprocess("pr_lookup")` wrapper — now in `LookupFn`); drop the `if p.PRLookupFn != nil` guard (always attempt PR; a nil `PRBackend` returns `(nil,nil)`). After the loop + PR calls, `p.Providers.Reconcile(sessions)`. `SetPhaseRecorder`: also `if p.Providers != nil { p.Providers.SetRecorder(r) }`. **Note:** the label/gauge pipeline (lifecycle) calls `RepoLabel`/`GitBranch` for LIVE sessions *after* `Snapshot` returns; those cwds have refcount ≥ 1 so `Reconcile` never evicts them mid-tick — no eviction race (same tick goroutine).

- [ ] **Step 1 — adjust tests** `poller_test.go`: recorder test wires a `*provider.Cache` (fetch closures return fixed values) + `fakeRec`; assert cold scan fires all four kinds and a steady 2nd scan does NOT increase `git_branch`/`pr_lookup`/`subshell`/`terminal_host`; assert `tree.Sessions()[i].Branch`/`.TerminalHost`/`.SubshellCount` equal the injected values. **Add `TestSnapshot_GitBranchEqualsSessionGitBranch_RealRepoSubdir`:** a fixture session whose cwd is a real subdirectory of a `t.TempDir()` git repo → the provider-backed `tree` `Branch` equals `session.GitBranch(cwd)` (the layer-3 equivalence gate; catches the resolver blocker). Keep the Phase-1 corpus-equivalence + incremental tests untouched (must stay green).
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** the poller rewire + lazy-default provider + field/helper deletions.
- [ ] **Step 4 — run, expect PASS**, plus `go test ./internal/core/poller/...` (all Phase-1 equivalence/incremental tests green).
- [ ] **Step 5 — commit** `refactor(poller): read git-branch/subshell/terminal-host/PR from provider cache; drop PRLookupFn (pg2-ll4fl)`.

## Task 10: Wire env into the Discoverer + repo-label into the label pipeline + `buildPoller`

**Files:** Modify `internal/core/session/discovery.go`, `discovery_test.go`, `internal/labels/detectors/repo.go`, `repo_test.go`, `cmd/pa-monitor/daemon.go`; extend `internal/core/poller/poller_test.go` or a daemon smoke test.

**Key logic:**
- `discovery.go`: widen `ReadEnv` to `func(sessionID string, pid int) (map[string]string, error)`; `Discover` calls `readEnv(r.SessionID, r.PID)`; nil default `func(_ string, pid int) (map[string]string, error) { return ReadProcessEnv(pid) }`. Update `discovery_test.go`.
- `detectors/repo.go`: add `Cache interface{ RepoLabel(cwd string) (string, bool) }` field. `Detect`: `if r.Cache != nil { if v, ok := r.Cache.RepoLabel(s.CWD); ok { return labels.Set{"workspace.repo": v} }; return nil }` else the existing inline path. Export `NormaliseOrigin`.
- `daemon.go buildPoller`, after `signalers`/`prCache`:
  ```
  prCache.FoundTTL = 15 * time.Minute
  cache := provider.New(time.Now)
  cache.PidAlive = session.DefaultPidAlive
  cache.FetchTerminalHost = func(pid int) string { return detectTerminalHost(signalers, pid) }
  cache.FetchRepoLabel = func(cwd string) (string, bool) {
      if cwd == "" { return "", false }
      out, err := exec.Command("git", "-C", cwd, "config", "--get", "remote.origin.url").Output()
      if err != nil {
          gcd, gErr := exec.Command("git", "-C", cwd, "rev-parse", "--git-common-dir").Output()
          if gErr != nil { return "", false }
          abs, _ := filepath.Abs(strings.TrimSpace(string(gcd)))
          sum := sha256.Sum256([]byte(abs)); return "local:" + hex.EncodeToString(sum[:6]), true
      }
      return detectors.NormaliseOrigin(strings.TrimSpace(string(out))), true
  }
  cache.PRBackend = prCache.Get
  cache.PRPrune  = prCache.Prune
  prCache.LookupFn = func(ctx context.Context, cwd, branch string) (session.PRInfo, bool, error) {
      start := time.Now()
      info, found, err := session.LookupPR(ctx, cwd, branch)
      cache.Record("pr_lookup", time.Since(start)) // fires only on a real gh spawn (LookupFn = miss/expiry)
      return info, found, err
  }
  disc := &session.Discoverer{SessionsDir: session.DefaultSessionsDir(), PidAlive: session.DefaultPidAlive, ReadEnv: cache.Env}
  // mon := corpus.New(claudeHome, disc); register observers as today; p.Providers = cache
  ```
  Register the repo detector as `detectors.Repo{Cache: cache}` (daemon.go:196). (`detectTerminalHost` is currently in `poller`; either export `signallayer.DetectHost(signalers, pid)` and use it in both the poller default and this closure, or inline the 4-line loop here. Prefer the shared helper.)

  > **Ordering:** `cache.Env` is called during `Monitor.Scan → Discover` (per-session, session-id-keyed); `Reconcile` (end of `Snapshot`) evicts absent session-ids. Dead pid → `{}` no-spawn.

- [ ] **Step 1 — failing/adjust tests** `repo_test.go`: `TestRepo_UsesCacheWhenSet` (fake cache→`("acme/x",true)`; `Repo{Cache:fake}.Detect({CWD:"/a"})`→`{"workspace.repo":"acme/x"}`, `git` never spawned). `discovery_test.go`: 2-arg `ReadEnv`. A daemon/poller smoke: with a `provider.Cache` whose closures return fixed env/branch/host/repo, a fixture scan yields those values (and `workspace.repo` from the cache when the labeler is wired).
- [ ] **Step 2 — run, expect FAIL.**
- [ ] **Step 3 — implement** the widening + detector field + `NormaliseOrigin` export + `signallayer.DetectHost` (or inline) + `buildPoller` composition.
- [ ] **Step 4 — run, expect PASS**, plus `go test ./internal/core/session/... ./internal/labels/... ./internal/daemon/...`.
- [ ] **Step 5 — commit** `feat(daemon): compose provider cache — env in Discoverer, repo-label detector, PR TTL (pg2-ll4fl)`.

## Task 11: Metric-parity + perf-guard

**Files:** Create `internal/core/provider/perfguard_test.go`.

**Key logic:** drive a `provider.Cache` directly (no poller) with injected fetch counters + a `fakeRec` over two scans (`BeginScan` + typed `Get`s for a fixed topology + `Reconcile`). Assert: **scan 1** — each fetch boundary called exactly once per distinct key (git-branch once per cwd, subshell once per session, terminal-host once per session, env once per alive pid); all four **metered** kinds fire ≥1× (cold-scan presence). **Scan 2** (unchanged inputs: same HEAD mtime via a fixed temp file, same transcript `(path,mtime)`, same alive pids) — **every** fetch boundary called **0** times and the `fakeRec` records **0** new subprocess events (the source-level near-idle proof: `subprocess.spawns_total` flat across a steady tick). Do NOT assert on a specific `{kind}` label being present after a warm start elsewhere (Global Constraints: presence is instrument-level).

- [ ] **Step 1 — write the guard; run, expect FAIL.**
- [ ] **Step 2 — fix any real divergence** (a 2nd-scan spawn is a real caching bug — do NOT weaken the assertion).
- [ ] **Step 3 — run, expect PASS.**
- [ ] **Step 4 — commit** `test(provider): perf guard (steady scan = 0 fetches/spawns) + cold-scan metric presence (pg2-ll4fl)`.

## Task 12: Full gate + acceptance + follow-ups

- [ ] **Step 1** — `cd packages/pa-monitor && go test ./...` → all pass.
- [ ] **Step 2** — `go test -race ./internal/core/provider/... ./internal/core/poller/... ./internal/core/corpus/... ./internal/daemon/...` → pass (manual `-race` gate; the Cache mutex + confinement must be clean).
- [ ] **Step 3** — from the worktree, invoke `pn-workspace-rules`, then `nix build .#pa-monitor` → builds; `nix build .#pa-monitor-go-tests` → passes; the golangci check target → passes. (Tier-1 — no flake interface change.)
- [ ] **Step 4 — acceptance review (no code):** (a) the four metered lookups record `RecordSubprocess` **only on a real fetch** (git_branch/subshell/terminal_host in-provider on miss; pr_lookup in the `LookupFn` wrapper on a real `gh` spawn); the poller's four sites are gone; Task 11 proves 2nd-scan = 0. (b) git-branch `UntilFileChanges(.git/HEAD)` behavior-preserving incl. subdirectory/worktree cwds + mid-session `git init` (negatives not cached); PRCache bounded (15m TTL + refreshed write-back + prune) — latent bug fixed, no post-TTL spawn storm. (c) PID-reuse staleness fixed (session-id keying for env/terminal/subshell); env `WhilePIDAlive` dead→empty no-spawn — darwin `ps` storm gone. (d) subshell re-homed behavior-identically (no TTL); repo-label a thin per-cwd `LongLived` provider (labelCache still the per-session layer). (e) nested eviction: cwd refcount cascade (no orphans) + tombstone (dead-not-GC'd keeps frozen host); tick-confined with a Phase-3-ready mutex. (f) all Phase-1 equivalence/incremental/metric-parity tests green; the real-repo-subdir gate passes. Update `README.md`/`CLAUDE.md` only if the new package warrants a module-structure note.
- [ ] **Step 5 — record design corrections** on the epic via `bd comment pg2-fclt1`: (1) `session.GitBranch` is a `.git/HEAD` FILE READ (not a subprocess) — the `git_branch` metric measured a file read and dominated only because it was the one uncached metered kind; the resolver MUST walk parents (one-level `resolveHeadPath` would regress subdir cwds); negatives are not cached (mid-session `git init` preserved). (2) the real darwin `ps` storm was `env` (`ps -E` per-session-per-tick in `Discoverer.Discover`), unmetered; now `WhilePIDAlive` session-id-keyed. (3) subshell + terminal-host were already ad-hoc poller-side caches (re-homed, not newly cached). (4) repo-label already per-session cached via `labelCache`; per-cwd provider dedups only the shared-cwd case. (5) PID-reuse staleness fixed via session-id keying (env too, closing the recycled-PID-file-vanished race), NOT evict-on-PID-death (which would regress dead-session display). (6) `pr_lookup` timing lives in `PRCache.LookupFn` (single cache; the metric fires only on a real spawn; `{pr_lookup}` series may be absent on a warm-cache start — instrument-level presence parity). (7) PRCache write-back guard had to be widened so a refreshed found entry is stored (else a post-TTL re-fetch storm).
- [ ] **Step 6 — file follow-ups** via `bd create`: (a) **Phase 3** producer goroutine + `ChangeSource` two-tier poll + immutable `DerivedState` atomic swap + re-home `RecordPhase(discover/pricer/limits/weekly)` to the producer + `poll.tick.duration`→thin emit + move the provider Cache behind the producer (mutex ready) + update `grafana/pa-monitor-overview.json` (coordinate pg2-gpbqe). (b) **Phase 4** clickable `PR#<n>` OSC-8 in the TUI (thread `PRInfo.State` through the gRPC proto). `bd dep` both after pg2-ll4fl.
- [ ] **Step 7 — commit** any residual (README/CLAUDE.md/bd export), then integrate via the `integrate-branch` skill (`ff-merge-to-main`); **R-8:** verify canonical on `main` + clean first.

---

## Deferred (explicitly out of Phase 2)

- **Phase 3:** producer goroutine + `ChangeSource` two-tier poll + immutable `DerivedState` atomic-publish; re-home `pricer`/`limits`/`weekly`/`discover` phase timers; `poll.tick.duration`→thin emit; move the provider Cache behind the producer; update the Grafana dashboard (coordinate pg2-gpbqe).
- **Phase 4:** clickable `PR#<n>` OSC-8 hyperlink in the TUI (thread `PRInfo.State` through the gRPC state proto).

## Self-review checklist (run before dispatching critique)

1. **Spec coverage:** design §5 providers+freshness → Tasks 2-7; nested cache + eviction cascade + cwd refcount + PID-reuse + tombstone → Tasks 1,8; git-branch `UntilFileChanges` (full walk, positive-only) → Task 3; PRCache bound (TTL+refresh+prune) → Task 7; `RecordSubprocess` re-home (drop) → Tasks 3,4,5,10 + poller-drop Task 9; injectable fetch boundaries → every provider task; env `WhilePIDAlive` session-keyed → Tasks 2,10; repo-label `LongLived` → Tasks 6,10; wiring → Tasks 9,10; equivalence (incl. real-repo-subdir) + metric-parity + perf guard → Tasks 9,11; Tier-1 gate → Task 12.
2. **Placeholders:** the `FetchRepoLabel` closure body is fully written (Task 10); the `PRCache.LookupFn` timing wrapper is fully written (Task 10). No `TODO`/"similar to"/uncoded steps.
3. **Type consistency:** `provider.Cache`/`Recorder`/`New`/`Record`/`BeginScan`/`Reconcile`; accessors `Env(sid,pid)`/`GitBranch(cwd)`/`Subshell(sid,pid,path,mtime)`/`TerminalHost(sid,pid)`/`RepoLabel(cwd)`/`PR(ctx,cwd,branch)`; `session.ResolveHeadPath`/`ReadHead`/`PRCache.FoundTTL`/`PRCacheKey`/`Prune`/`Discoverer.ReadEnv(sid,pid)`; `detectors.Repo.Cache`/`NormaliseOrigin`; `Poller.Providers` (PRLookupFn removed) used consistently Tasks 1-12.
```
