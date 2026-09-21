# ccpool Admission Control and Eviction Safety Implementation Plan (rev 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the ccpool reaper from killing freshly dispatched pg-router sessions, and make the pool cap an admission gate instead of a kill switch.

**Architecture:** Three coordinated changes across two Go modules in `phillipgreenii-nix-agent-support`. (1) ccpool redefines pool capacity as the number of live NON-preserved sessions and restricts cap eviction to sessions whose turn has ended (`idle`/`errored`), so a `starting`/`ready`/`working` session is never evicted for cap pressure. (2) ccpool records WHY it closed a session (a `close_reason` on the row, stamped BEFORE the tmux teardown, plus a `close` event) and exposes a `ccpool capacity` query, so consumers reason about the pool without re-deriving its rules. (3) pg-router-ccpool-handler consults `ccpool capacity` before preparing isolation or launching (admission control, the Bulkhead pattern per pool) and reports a full pool through the wire's EXISTING pre-accept busy decline (`conformance.ExitBusy`, exit 9), which the daemon already re-queues with backoff. When a session dies, the handler reads the close reason to distinguish an external close (release the bead, leave a breadcrumb, escalate after repeats) from a genuine failure (existing `onFailure`).

**Tech Stack:** Go 1.2x, `modernc.org/sqlite` (embedded migrations), tmux, `log/slog`; nix `mkGoApp` with `gomod2nix.toml`; tests via `go test ./...` per module.

**Spec:** Incident analysis of 2026-09-21 (Claude Code Insights review session), recorded on beads zr-0ma7q and pg2-o7vl7: 53 of 56 pg-router review dispatches and 44 of 46 feedback dispatches in the week of 2026-09-15 were killed 2 to 5 minutes after launch. Mechanism, verified by walking `Reap` with live = 6 `needs_input` + 2 `working` at cap 6: `capClosures = 8 - 6 = 2`, both working rows are the only non-preserved candidates, both are closed on the next 300 s reap tick. Intended committed home of this plan: `docs/superpowers/plans/2026-09-21-ccpool-admission-control-and-eviction-safety.md`.

**Review provenance:** rev 1 was reviewed 2026-09-21 by four independent read-only Sonnet reviewers (correctness, completeness, operator UX, test coverage). Every BLOCKER and MAJOR finding is folded in below; the material changes from rev 1 are: busy-decline instead of a new exit code (INV-CCH-3, DEC-WIRE-1 reserve exit 3, `roleListener.Offer` consumes non-busy errors); migration renumbered to 008; stamp-before-kill; `Runner` interface fan-out to three fakes; real fixture names; state-based sparing also reclaiming never-ingested `ready` rows via the handler; `close_reason` visible in the default list table; eviction breadcrumb and two-strike escalation on the bead; ccpool's own behavior docs and the HM module comment updated; numeric invariant ids.

## Global Constraints

- Public repo: no ZipRecruiter-specific names in code, tests, or docs (repo `CLAUDE.md`, "Public Repository"). Refer to the deployment set only as "the deployment set".
- Behavior docs first: any change to pg-router-family behavior edits the relevant `docs/behavior/` document in the SAME change (repo `CLAUDE.md`, "pg-pr / pg-router Development Rules"). This plan touches BOTH the handler's and ccpool's own `docs/behavior/`.
- ADR citations name the owning repo unless the ADR is in this repo's `docs/adr/`; cite sections by prose heading, never `§`. The ccpool surface has a mechanical `§` ban (`spec_citations_test.go`).
- Wire contract: a handler's post-accept outcome is opaque to the core (`INV-CCH-3`, handler `docs/behavior/invariants.md`); the only pre-accept signal is the busy decline `conformance.ExitBusy = 9` (`packages/pg-router/conformance/transport.go` line 57), which `wireclient.Dispatch` maps to `ErrBusy` and `roleListener.Offer` turns into `eventqueue.DeclineBusy` (re-offer with backoff). Exit codes 1, 2, 3 are taken (`ExitError`, `ExitUsage`, core `exitPrecheck`); this plan adds NO new exit code.
- Exit codes inside the `ccpool` binary follow its own convention: `1` store or runtime error, `2` usage error.
- Unit tests MUST be isolated (in-memory sqlite via `newMemStore`/`newTestStore`, `reapTmux`/`closeTmux` fakes, `dtest.FakeCC`/`dtest.ScriptBD`); never the real pool, tmux server, or `~/.local/share/ccpool/store.db`.
- Go modules build with gomod2nix; from an agent, run `go test ./...` with an explicit timeout of at least 10 minutes (L-1); whole-module gates are `nix build .#checks.<system>.ccpool-go-tests` and `.#checks.<system>.pg-router-ccpool-handler-go-tests` (confirmed attribute names in `flake.nix`).
- ADR numbers are assigned at land time with the `next-free-id?` probe; every `NNNN` below is replaced in one pass before commit (Task 8 checks none remain). Migration numbers likewise: the next free is `008` as of 2026-09-21 (`ls packages/ccpool/internal/store/migrations` shows `001`..`007`); re-check before creating the file.

## Review Focus

1. **`ccpool capacity` fails (store locked, disk I/O error, binary missing) while the handler is deciding.** A person expects no session launched into an unknown pool and the event re-offered later, not a silent fall-through to today's behavior. Pinned by Task 6's `TestRun_poolCapacityErrorDeclinesBusy`.
2. **Two handler processes dispatch into the last free slot in the same second (pg-router dispatches feedback and review pairs together).** A person expects both sessions to survive, since neither is evictable while `working`, and the pool to sit at cap+1 until one finishes. Pinned by Task 2's `TestReap_capEvictionNeverTargetsWorkingSessions`.
3. **A session whose Claude process hung mid-turn stays `working` forever.** A person expects the 30-minute `idle_ttl` to close it, not state-based sparing to make it immortal. Pinned by Task 2's `TestReap_ttlStillClosesHungWorkingSession`.
4. **An operator runs `ccpool close` on a row the handler is waiting on.** A person expects the bead released with a comment saying why, not labeled `human`. Pinned by Task 7's table row `operator`.
5. **A row closed by ccpool sits in `ccpool list` as `working` / not live.** A person expects the default table to say WHY (`cap_eviction`) without remembering a flag, and an old row from before the migration to show an empty reason and behave exactly as today. Pinned by Task 3's `TestRenderList_showsCloseReasonColumnWhenSet` and `TestList_closeReasonEmptyForLegacyRows`, and Task 7's table row `""`.

---

## File Structure

**ccpool module** (`packages/ccpool/`):

- Modify: `internal/session/reap.go` — `countedSessions`, `evictable`, Pass 2 arithmetic and filter, closes via `closeWithReason`.
- Modify: `internal/session/cancel_close.go` — `closeWithReason` (stamp first, then `/exit`, then kill), `Close` keeps its signature (`operator`), new exported `CloseReason`.
- Create: `internal/store/migrations/008_close_reason.sql`.
- Modify: `internal/store/store.go` (fields), `internal/store/ops.go` (`SetCloseReason`, `CloseReasons`, column lists).
- Modify: `internal/eventlog/eventlog.go` — kind `"close"` with `reason`, method `Close` built on the exported `Append`.
- Create: `internal/session/capacity.go` — `Capacity` type and `(*Service).Capacity`.
- Create: `cmd/ccpool/capacity.go`, `cmd/ccpool/capacity_test.go`, `cmd/ccpool/capacity_integration_test.go` (build tag `integration`, mirrors `reap_integration_test.go`).
- Modify: `cmd/ccpool/dispatch.go` — register `capacity` in `subcommandInfo`.
- Modify: `cmd/ccpool/list.go` — `listJSON.CloseReason`; default text table gains a `CLOSE_REASON` column whenever any visible row has one.
- Modify: `cmd/ccpool/close.go` — `-reason` flag, success line, `--purge` warning; Create: `cmd/ccpool/close_test.go`.
- Modify: `docs/behavior/invariants.md` (`INV-POOL-3` cap-eviction wording), `docs/behavior/interfaces.md` (`INTF-CALLER` gains `capacity`).

**handler module** (`packages/pg-router-ccpool-handler/`):

- Modify: `internal/ccpool/ccpool.go` — `Session.CloseReason`, `Capacity` type, `Runner.Capacity`.
- Modify: `internal/ccpool/cli.go` — `CLIRunner.Capacity`; `CLIRunner.Close` passes `--reason handler`.
- Modify: `internal/dtest/dtest.go` (`FakeCC.Capacity`, fields `Cap`, `CapErr`), `internal/watchdog/watchdog_test.go` (`fakeCC.Capacity`), `cmd/pg-router-ccpool-handler/preshutdown_test.go` (`fakeCC.Capacity`) — every `ccpool.Runner` implementer.
- Modify: `internal/executor/ccpool.go` — admission check before isolation; `ErrPoolAtCapacity`; not-ingested path closes the session; eviction-aware death branch; `escalateEviction`; `ErrExternallyClosed` in the wait-failure mapping.
- Modify: `cmd/pg-router-ccpool-handler/dispatch.go` — `ErrPoolAtCapacity` → `conformance.ExitBusy`.
- Modify: `docs/behavior/invariants.md` — `INV-CCH-6`, `INV-CCH-7`.

**repo docs / modules:**

- Create: `docs/adr/NNNN-ccpool-capacity-counts-non-preserved-sessions.md`; Modify: `docs/adr/index.md`, `docs/adr/0037-ccpool-reap-spares-human-paused-sessions.md` (later-note blockquote), `home/programs/pg-router-ccpool-handler/default.nix` (comment at lines 14-21 and 826-838).

**Not in this plan (each is its own bead):**

- One-off sweep of the damage already done: release the 23 review beads left `in_progress`+`human` by evictions, and decide the 5 to 6 preserved `needs_input` sessions (pg2-o7vl7 covers the second; the first needs a bead filed when this plan is accepted).
- A bound on `needs_input` accumulation (preservation TTL or attention surfacing, pg2-84zrp).
- The deployment set's `INV-CCPOOL-*` wording (private repo).
- A gate-style attention signal when `ccpool capacity` keeps failing (pg-router core owns `~/.local/state/pg-router/gates/`; the handler only logs).
- Re-evaluating the dedicated 40-slot pool (`home/programs/pg-router-ccpool-handler/default.nix`) once cap eviction no longer kills working sessions.

---

### Task 1: Decision record and behavior docs (docs first)

**Files:**

- Create: `docs/adr/NNNN-ccpool-capacity-counts-non-preserved-sessions.md`
- Modify: `docs/adr/index.md`; `docs/adr/0037-ccpool-reap-spares-human-paused-sessions.md`
- Modify: `packages/ccpool/docs/behavior/invariants.md` (`INV-POOL-3`), `packages/ccpool/docs/behavior/interfaces.md` (`INTF-CALLER` row)
- Modify: `packages/pg-router-ccpool-handler/docs/behavior/invariants.md` (append `INV-CCH-6`, `INV-CCH-7` after `INV-CCH-5`)
- Modify: `home/programs/pg-router-ccpool-handler/default.nix` lines 14-21 and 826-838

**Interfaces:**

- Produces the vocabulary every later task cites: _preserved_ (state `needs_input`, ADR 0037), _counted_ (live and not preserved), _evictable_ (counted and state `idle` or `errored`), _free slot_ (`max(0, max_sessions - counted)`), _close reason_ (`idle_ttl` | `cap_eviction` | `operator` | `handler`), _busy decline_ (`conformance.ExitBusy`).

- [ ] **Step 1: Compute the ADR number**

```bash
printf '%04d\n' "$(( 10#$(git ls-tree -r --name-only main -- docs/adr | rg -o '/(\d{4})-' -r '$1' | sort -n | tail -1) + 1 ))"
```

Use the result for every `NNNN`.

- [ ] **Step 2: Write the ADR**

```markdown
# ccpool capacity counts non-preserved sessions; the cap is an admission gate

**Status**: Accepted (amends 0037)
**Date**: 2026-09-21
**Deciders**: Phillip Green II

## Context

ADR 0037 spares a `needs_input` session from both reap passes and lets it keep counting
toward `max_sessions`; its rationale assumed the cap "cannot starve new work" because nothing
refuses to start a session. Observed 2026-09-21: with 5 to 6 preserved sessions in a pool
capped at 6, every newly launched session was over cap, was the only non-preserved
candidate, and was closed by the next 300-second reap tick 2 to 5 minutes after its prompt
was delivered — 53 of 56 review dispatches and 44 of 46 feedback dispatches in one week.
The cap did not starve new work; it killed it.

## Decision

1. **Capacity counts only live, non-preserved sessions.** `counted = |{live rows with
state != needs_input}|`; `free = max(0, max_sessions - counted)`. Preserved rows are
   outside the cap entirely.
2. **Cap eviction targets only sessions whose turn has ended.** Pass 2 closes counted rows
   in state `idle` or `errored`, oldest-activity first, until `counted <= max_sessions` or
   no evictable row remains. A `starting`, `ready`, or `working` row is never closed for cap
   pressure. A hung `working` row is still closed by Pass 1 once `last_activity_at` exceeds
   `idle_ttl`; a `ready` row whose prompt was never ingested is closed by its dispatcher
   (reason `handler`) the moment ingestion fails.
3. **The cap is an admission gate for programmatic dispatchers.** `ccpool capacity` reports
   `max_sessions`, `live`, `preserved`, `counted`, `free`. A dispatcher MUST NOT launch when
   `free == 0` or when capacity cannot be read; it reports "not right now" through its
   transport's pre-accept busy decline and lets the caller re-offer with backoff.
4. **ccpool records why it closed a session, before it closes it.** Every close stamps
   `close_reason` and `closed_at` on the row and appends a `close` event BEFORE the tmux
   teardown, so an observer that sees the session gone can already read the reason.
   Reasons: `idle_ttl`, `cap_eviction`, `operator` (default for `ccpool close`), `handler`
   (`ccpool close --reason handler`, used by pg-router-ccpool-handler for every close it
   initiates). This is a session FACT about ccpool's own action, consistent with ADR 0015.
5. **`ccpool list` shows the close reason by default** whenever a visible row has one.

## Consequences

- A pool may hold up to `max_sessions` counted sessions plus any number of preserved ones
  plus any number of working sessions launched by humans outside the gate; `idle_ttl` is
  the only bound on that last group.
- A consumer that observes `close_reason in {idle_ttl, cap_eviction, operator}` knows the
  worker did not fail and MAY release the work item for retry, and SHOULD escalate after
  repeated evictions of the same item rather than retry forever.
- Rejected: an eviction grace period (spare `working` rows for N minutes after last
  activity). It still kills a legitimately long turn once N elapses, adds a config key, and
  depends on `last_activity_at`, which only advances on hook transitions.
- Rejected: preserved rows count and the gate refuses. Correct signal, but the pipeline
  halts until an operator closes preserved rows; ADR 0037's rationale wanted growth, not
  starvation.
- Rejected: a new handler exit code for "pool full". `INV-CCH-3` makes post-accept outcomes
  opaque, DEC-WIRE-1 reserves exit 3 for the core, and the daemon's listener consumes any
  non-busy error as accepted, which would drop the event. The busy decline already exists
  for exactly this case.
```

- [ ] **Step 3: Annotate ADR 0037 and the index**

In `docs/adr/0037-...md`, insert directly after the `**Deciders**` line (the repo's precedent is ADR 0048's later-note blockquote):

```markdown
> **Later note (2026-09-21, ADR NNNN):** the Neutral consequence "preserved sessions still
> count toward the cap" and the Context claim that the cap "cannot starve new work" are
> superseded: capacity now counts non-preserved rows only, cap eviction spares `working`
> rows, and the cap is consulted as an admission gate. Preservation itself is unchanged.
```

In `docs/adr/index.md`, add a row in the table's existing format with Status `Accepted (amends 0037)`.

- [ ] **Step 4: ccpool behavior docs**

In `packages/ccpool/docs/behavior/invariants.md`, rewrite `INV-POOL-3`'s cap-eviction sentence so it reads: "Cap eviction MUST count only live sessions not spared for a human, MUST close only sessions whose turn has ended (`idle`, `errored`), oldest-activity first, and MUST leave the pool above its cap when only spared or still-working sessions remain (ADR NNNN)." Keep the uuid comment on the bullet unchanged.

In `packages/ccpool/docs/behavior/interfaces.md`, change the `INTF-CALLER` row's port list from `dispatch, status, cancel, metadata` to `dispatch, status, capacity, cancel, metadata` and add one sentence under the table: "`capacity` reports free slots under ADR NNNN's definition so a caller can decline to dispatch instead of launching into a full pool."

- [ ] **Step 5: Handler invariants**

Append after `INV-CCH-5` in `packages/pg-router-ccpool-handler/docs/behavior/invariants.md`, matching the file's exact style (bold, backtick-wrapped, numeric):

```markdown
- **`INV-CCH-6`** — a handler MUST query pool capacity before preparing isolation or
  launching a session and MUST NOT launch when the pool reports no free slot or cannot be
  read. It signals this ONLY through the transport's pre-accept busy decline
  (`conformance.ExitBusy`), so the core re-offers the event with backoff; it mutates no bead
  and creates no worktree. This is consistent with `INV-CCH-3`: busy is a pre-accept signal,
  not a post-accept outcome.
- **`INV-CCH-7`** — when a session ends before its bead completes, the handler MUST read
  ccpool's recorded close reason. An external close (`idle_ttl`, `cap_eviction`, `operator`)
  MUST release the bead (status open, assignee cleared) with a comment naming the reason,
  regardless of the role's `on_failure`, and MUST escalate to `human` on the second
  consecutive external close of the same bead. Only an unexplained death, or a close the
  handler itself requested (`handler`), applies `on_failure`.
```

- [ ] **Step 6: HM module comment**

In `home/programs/pg-router-ccpool-handler/default.nix`, replace the sentence at lines 17-20 ("cap eviction then force-closes actively-working sessions with no regard for in-progress work (packages/ccpool/internal/session/reap.go's Pass 2)") with: "before ADR NNNN, cap eviction force-closed actively-working sessions; since then eviction spares working rows and the handler declines busy when the pool is full, so the dedicated pool's remaining purpose is isolation from other consumers' sessions and their reap cadence, not protection from eviction". Add the same one-line pointer to the `description` at lines 826-838 after "shared-pool default of 6".

- [ ] **Step 7: Commit**

```bash
git add docs/adr packages/ccpool/docs/behavior packages/pg-router-ccpool-handler/docs/behavior/invariants.md home/programs/pg-router-ccpool-handler/default.nix
git commit -m "docs: ADR NNNN — ccpool capacity counts non-preserved sessions; busy-decline admission; close reason"
```

---

### Task 2: Reaper — capacity over counted rows, eviction only of idle/errored

**Files:**

- Modify: `packages/ccpool/internal/session/reap.go` lines 28-39 (doc), 83-106 (Pass 2)
- Test: `packages/ccpool/internal/session/reap_test.go`

**Interfaces:**

- Consumes: `preservedForHuman(store.Session) bool` (reap.go:28); the fixture `reapFixtureStates(t *testing.T, now time.Time, ages map[string]int64, states map[string]store.State) (*Service, map[string]bool)` (reap_test.go:47) — `ages` are seconds-idle before `now`, every id absent from `states` is `store.Ready`, the returned map is keyed by tmux name `"cc-<id>"` and reads true once closed; `Reap(ctx, maxSessions int, idleTTL time.Duration) error`.
- Produces: `func countedSessions(live []store.Session) int`, `func evictable(r store.Session) bool` (package-private, reused by Task 4).

- [ ] **Step 1: Fix the two pre-existing tests that model behavior ADR NNNN forbids**

`TestReap_overCapClosesOldestFirst` (line 92) and `TestReap_recordsClosureReasonPerPass` (about line 301) build over-cap pools out of default-`Ready` rows and expect cap eviction to close them. Under the new rule `Ready` is not evictable. Change each to pass an explicit `states` map marking the rows they expect evicted as `store.Idle`, e.g. in `TestReap_recordsClosureReasonPerPass` replace the `reapFixture(...)` call with `reapFixtureStates(t, now, ages, map[string]store.State{"mid": store.Idle, "stale": store.Idle})` (keep `fresh` as `Ready` if the test expects it to survive). Note in the commit message that these fixtures modeled eviction of not-finished sessions.

- [ ] **Step 2: Write the failing tests**

```go
func TestReap_capCountsOnlyNonPreservedSessions(t *testing.T) {
	now := time.Unix(10_000, 0)
	ages := map[string]int64{"work-a": 10, "work-b": 20}
	states := map[string]store.State{"work-a": store.Working, "work-b": store.Working}
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("paused-%d", i)
		ages[id] = int64(3000 + i)
		states[id] = store.NeedsInput
	}
	s, closed := reapFixtureStates(t, now, ages, states)
	if err := s.Reap(context.Background(), 6, 0); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	for _, id := range []string{"cc-work-a", "cc-work-b"} {
		if closed[id] {
			t.Fatalf("%s was closed; preserved rows must not count toward the cap (ADR NNNN)", id)
		}
	}
}

func TestReap_capEvictionNeverTargetsWorkingSessions(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, closed := reapFixtureStates(t, now,
		map[string]int64{"idle-old": 3000, "work-1": 1000, "work-2": 10},
		map[string]store.State{"idle-old": store.Idle, "work-1": store.Working, "work-2": store.Working})
	if err := s.Reap(context.Background(), 2, 0); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if !closed["cc-idle-old"] {
		t.Error("idle-old must be evicted (only evictable row)")
	}
	if closed["cc-work-1"] || closed["cc-work-2"] {
		t.Errorf("working rows must never be cap-evicted; closed=%v", closed)
	}
}

func TestReap_capEvictionSparesStartingAndReady(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, closed := reapFixtureStates(t, now,
		map[string]int64{"starting": 3000, "ready": 2000, "errored": 1000},
		map[string]store.State{"starting": store.Starting, "ready": store.Ready, "errored": store.Errored})
	if err := s.Reap(context.Background(), 1, 0); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if closed["cc-starting"] || closed["cc-ready"] {
		t.Errorf("starting/ready rows are not evictable; closed=%v", closed)
	}
	if !closed["cc-errored"] {
		t.Error("errored row is evictable and oldest among evictable; must close")
	}
}

func TestReap_capEvictionLeavesPoolOverCapWhenOnlyWorkingRemain(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, closed := reapFixtureStates(t, now,
		map[string]int64{"w1": 3000, "w2": 2000, "w3": 1000},
		map[string]store.State{"w1": store.Working, "w2": store.Working, "w3": store.Working})
	if err := s.Reap(context.Background(), 1, 0); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(closed) != 0 {
		t.Fatalf("cap pressure must go unrelieved when only working rows remain; closed=%v", closed)
	}
}

func TestReap_ttlStillClosesHungWorkingSession(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, closed := reapFixtureStates(t, now,
		map[string]int64{"hung": 7200},
		map[string]store.State{"hung": store.Working})
	if err := s.Reap(context.Background(), 6, 30*time.Minute); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if !closed["cc-hung"] {
		t.Fatal("a working row idle past idle_ttl must still be closed by Pass 1")
	}
}
```

(`reapFixtureStates` may leave `closed[...]` unset for never-closed rows; `len(closed) != 0` in the fourth test assumes the map only gains keys on close — confirm by reading `reapTmux.KillSession` at the top of the file; if it pre-populates, assert each key is false instead.)

- [ ] **Step 3: Run to verify failure**

Run (from `packages/ccpool`): `go test ./internal/session/ -run 'TestReap_(capCountsOnlyNonPreservedSessions|capEvictionNeverTargetsWorkingSessions|capEvictionSparesStartingAndReady|capEvictionLeavesPoolOverCapWhenOnlyWorkingRemain|ttlStillClosesHungWorkingSession)' -v`
Expected: the first four FAIL; `ttlStillClosesHungWorkingSession` PASSES (it pins current behavior).

- [ ] **Step 4: Implement**

Add after `preservedForHuman` in `reap.go`:

```go
// countedSessions is the pool's occupancy for cap purposes: live rows NOT
// preserved for a human (ADR NNNN, Decision 1). Preserved rows sit outside
// max_sessions entirely.
func countedSessions(live []store.Session) int {
	n := 0
	for _, r := range live {
		if !preservedForHuman(r) {
			n++
		}
	}
	return n
}

// evictable reports whether cap eviction may close a counted row: only a
// session whose turn has ended (Stop or StopFailure). A starting/ready/working
// row is never evicted for cap pressure (ADR NNNN, Decision 2); a hung working
// row is Pass 1's job once idle_ttl elapses, and a never-ingested ready row is
// its dispatcher's job (close --reason handler).
func evictable(r store.Session) bool {
	if preservedForHuman(r) {
		return false
	}
	return r.State == store.Idle || r.State == store.Errored
}
```

Replace the Pass 2 block (lines 83-106) with:

```go
	// Pass 2: still over cap AFTER the TTL closures → close more sessions, but
	// only ones whose turn has ended, oldest-activity first (ADR NNNN). Capacity
	// counts only non-preserved rows; TTL closures count toward the cap (ADR
	// 0037's Context). If every remaining counted row is still working, the pool
	// is left over cap on purpose — admission control (ccpool capacity) bounds
	// working sessions, not eviction.
	capClosures := (countedSessions(live) - len(toClose)) - maxSessions
	for _, r := range live { // already sorted oldest-first
		if capClosures <= 0 {
			break
		}
		if !evictable(r) {
			continue
		}
		if _, ok := toClose[r.ExternalID]; !ok {
			toClose[r.ExternalID] = "cap_eviction"
			capClosures--
		}
	}
```

Pass 1 already `continue`s on preserved rows before writing `toClose`, so subtracting `len(toClose)` from the counted total is sound. Update the `Reap` doc comment (lines 30-39) to the new rule.

- [ ] **Step 5: Run the whole reap suite**

Run: `go test ./internal/session/ -run TestReap -v`
Expected: all PASS, including the two tests adjusted in Step 1 and the untouched `TestReap_ttlClosuresCountTowardCap`.

- [ ] **Step 6: Commit**

```bash
git add packages/ccpool/internal/session/reap.go packages/ccpool/internal/session/reap_test.go
git commit -m "ccpool reap: capacity counts non-preserved rows; cap eviction spares starting/ready/working (ADR NNNN)"
```

---

### Task 3: Close reason — store, event, stamp-before-kill, CLI, list output

**Files:**

- Create: `packages/ccpool/internal/store/migrations/008_close_reason.sql` (run `ls packages/ccpool/internal/store/migrations | tail -1` first; use the next number if 008 is taken by then)
- Modify: `packages/ccpool/internal/store/store.go` lines 36-62; `packages/ccpool/internal/store/ops.go` (every column list that names `retry_window_started_at`; new method)
- Modify: `packages/ccpool/internal/eventlog/eventlog.go` lines 43-60 (`Event`), after line 126 (new method)
- Modify: `packages/ccpool/internal/session/cancel_close.go` lines 151-176; `packages/ccpool/internal/session/reap.go` line 125
- Modify: `packages/ccpool/cmd/ccpool/close.go`; `packages/ccpool/cmd/ccpool/list.go` lines 233-247 and the text renderer
- Test: `packages/ccpool/internal/store/ops_test.go`, `packages/ccpool/internal/session/eventlog_test.go`, `packages/ccpool/internal/session/cancel_close_test.go`, `packages/ccpool/internal/session/reap_test.go`, `packages/ccpool/cmd/ccpool/list_test.go` (exists; extend), `packages/ccpool/cmd/ccpool/close_test.go` (create)

**Interfaces:**

- Consumes: `newTestStore(t) *Store` (store_test.go:11); `mustInsert(t *testing.T, st *Store, externalID, csid string)` (ops_test.go:185); `st.GetByExternalID(ctx, id)`; `newMemStore(t) *store.Store` (session_test.go:540); `closeTmux` (cancel_close_test.go:16); `newLoggedStore(t, el *eventlog.Logger)` and `actions(evs []eventlog.Event) []string` (eventlog_test.go:17,28); `eventlog.Read(path)`; the exported `(*Logger).Append(Event)`.
- Produces: `store.Session.CloseReason string`, `store.Session.ClosedAt int64`; `store.CloseReasons map[string]bool`; `func (s *Store) SetCloseReason(ctx context.Context, externalID, reason string) error`; `func (l *Logger) Close(ts time.Time, name, reason string)`; `func (s *Service) closeWithReason(ctx, externalID, reason string, purge bool) error`; `func (s *Service) CloseReason(ctx, externalID, reason string, purge bool) error`; `ccpool close [-purge] [-reason R] <id>`; `listJSON.CloseReason` as `"close_reason"`.

- [ ] **Step 1: Failing store tests** (`ops_test.go`)

```go
func TestSetCloseReason_stampsReasonAndTime(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustInsert(t, st, "s1", "csid-1")
	if err := st.SetCloseReason(ctx, "s1", "cap_eviction"); err != nil {
		t.Fatal(err)
	}
	row, err := st.GetByExternalID(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if row.CloseReason != "cap_eviction" || row.ClosedAt == 0 {
		t.Fatalf("row = reason %q closed_at %d", row.CloseReason, row.ClosedAt)
	}
}

func TestSetCloseReason_rejectsUnknownReason(t *testing.T) {
	st := newTestStore(t)
	mustInsert(t, st, "s1", "csid-1")
	if err := st.SetCloseReason(context.Background(), "s1", "because"); err == nil {
		t.Fatal("unknown reason accepted")
	}
}

func TestList_closeReasonEmptyForLegacyRows(t *testing.T) {
	st := newTestStore(t)
	mustInsert(t, st, "legacy", "csid-l")
	row, err := st.GetByExternalID(context.Background(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if row.CloseReason != "" || row.ClosedAt != 0 {
		t.Fatalf("legacy row: reason %q closed_at %d; defaults must be empty/0", row.CloseReason, row.ClosedAt)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run (from `packages/ccpool`): `go test ./internal/store/ -run 'TestSetCloseReason|TestList_closeReasonEmpty' -v` — Expected: compile FAIL.

- [ ] **Step 3: Migration, fields, method, event**

`migrations/008_close_reason.sql`:

```sql
ALTER TABLE sessions ADD COLUMN close_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN closed_at INTEGER NOT NULL DEFAULT 0;
```

`store.go`, after `RetryWindowStartedAt`:

```go
	// CloseReason records WHY ccpool itself ended this session — a fact about
	// ccpool's own action (ADR 0015 permits facts, forbids work judgments): one of
	// idle_ttl | cap_eviction | operator | handler; "" until ccpool closes it.
	CloseReason string
	// ClosedAt is the unix time CloseReason was stamped; 0 when unset.
	ClosedAt int64
```

`ops.go`:

```go
// CloseReasons is the closed vocabulary SetCloseReason accepts (ADR NNNN, Decision 4).
var CloseReasons = map[string]bool{"idle_ttl": true, "cap_eviction": true, "operator": true, "handler": true}

// SetCloseReason stamps close_reason/closed_at and appends a "close" event. It
// does NOT change State (ADR 0015: the row keeps its last observed state).
func (s *Store) SetCloseReason(ctx context.Context, externalID, reason string) error {
	if !CloseReasons[reason] {
		return fmt.Errorf("close reason %q not one of idle_ttl|cap_eviction|operator|handler", reason)
	}
	now := s.clock.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET close_reason = ?, closed_at = ? WHERE external_id = ?`, reason, now, externalID)
	if err != nil {
		return fmt.Errorf("set close reason: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set close reason: no row for %q", externalID)
	}
	s.events.Close(time.Unix(now, 0), externalID, reason) // nil-safe
	return nil
}
```

Add `close_reason, closed_at` to every SELECT/INSERT column list and scan in `ops.go` (search `retry_window_started_at` and mirror each occurrence).

`eventlog.go`: add `Reason string \`json:"reason,omitempty"\``to`Event`, document Kind as `"transition" | "input" | "close"`, and add after `Input`:

```go
// Close records that ccpool itself ended the session and why (ADR NNNN).
func (l *Logger) Close(ts time.Time, name, reason string) {
	if l == nil {
		return
	}
	l.Append(Event{Ts: stamp(ts), Name: name, Kind: "close", Reason: reason})
}
```

(Mirror how `Transition` and `Input` call `Append` and `stamp`; if `Append` is not nil-safe on its own, keep the guard.)

- [ ] **Step 4: Run store tests** — `go test ./internal/store/ -v` — Expected: PASS (the existing migration test opens `:memory:` through the embedded glob, so 008 is exercised automatically).

- [ ] **Step 5: Failing session tests**

`cancel_close_test.go` (arrange exactly as the file's existing tests do: `st := newMemStore(t); tm := &closeTmux{...}; s := New(Deps{...})`):

```go
func TestClose_defaultReasonIsOperator(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	tm := &closeTmux{live: map[string]bool{"cc-s1": true}}
	s := New(Deps{Store: st, Tmux: tm, Prefix: "cc"}) // copy the remaining Deps fields from the neighbouring test
	insertLiveRow(t, st, "s1")                            // the file's existing insert helper, or store.Insert inline as the neighbours do
	if err := s.Close(ctx, "s1", false); err != nil {
		t.Fatal(err)
	}
	row, _ := st.GetByExternalID(ctx, "s1")
	if row.CloseReason != "operator" {
		t.Fatalf("close_reason = %q, want operator", row.CloseReason)
	}
}

func TestClose_stampsReasonBeforeTeardown(t *testing.T) {
	// The tmux fake records the row's close_reason at the moment KillSession runs.
	ctx := context.Background()
	st := newMemStore(t)
	var seen string
	tm := &closeTmux{live: map[string]bool{"cc-s1": true}}
	tm.onKill = func(name string) { row, _ := st.GetByExternalID(ctx, "s1"); seen = row.CloseReason }
	s := New(Deps{Store: st, Tmux: tm, Prefix: "cc"})
	insertLiveRow(t, st, "s1")
	if err := s.CloseReason(ctx, "s1", "cap_eviction", false); err != nil {
		t.Fatal(err)
	}
	if seen != "cap_eviction" {
		t.Fatalf("reason at kill time = %q; must be stamped BEFORE teardown", seen)
	}
}

func TestClose_purgeSkipsStamp(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	tm := &closeTmux{live: map[string]bool{"cc-s1": true}}
	s := New(Deps{Store: st, Tmux: tm, Prefix: "cc"})
	insertLiveRow(t, st, "s1")
	if err := s.CloseReason(ctx, "s1", "operator", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetByExternalID(ctx, "s1"); err == nil {
		t.Fatal("purge must delete the row")
	}
}
```

`closeTmux` needs an `onKill func(string)` hook invoked from its `KillSession` (add it; existing tests leave it nil). If `waitGone` returns true before `KillSession` runs (the fake flips liveness on `/exit`), hook `onKill` into the fake's `/exit` delivery instead — the assertion is "reason visible before the session disappears".

`eventlog_test.go` (session package; reuse `newLoggedStore`/`actions`/`eventlog.Read` exactly as the file's existing tests do):

```go
func TestSetCloseReason_appendsCloseEvent(t *testing.T) {
	dir := t.TempDir()
	el := eventlog.New(filepath.Join(dir, "events.jsonl")) // the constructor the file already uses
	st := newLoggedStore(t, el)
	mustInsertStore(t, st, "s1") // the file's insert helper
	if err := st.SetCloseReason(context.Background(), "s1", "idle_ttl"); err != nil {
		t.Fatal(err)
	}
	evs, err := eventlog.Read(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	last := evs[len(evs)-1]
	if last.Kind != "close" || last.Reason != "idle_ttl" || last.Name != "s1" {
		t.Fatalf("last event = %+v", last)
	}
}
```

`reap_test.go`:

```go
func TestReap_stampsCloseReasonOnRow(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, _ := reapFixtureStates(t, now,
		map[string]int64{"idle-old": 3000, "idle-new": 10},
		map[string]store.State{"idle-old": store.Idle, "idle-new": store.Idle})
	if err := s.Reap(context.Background(), 1, 0); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	row, err := s.d.Store.GetByExternalID(context.Background(), "idle-old")
	if err != nil {
		t.Fatal(err)
	}
	if row.CloseReason != "cap_eviction" {
		t.Fatalf("close_reason = %q, want cap_eviction", row.CloseReason)
	}
}
```

- [ ] **Step 6: Run to verify failure** — `go test ./internal/session/ -run 'TestClose_|TestSetCloseReason_appendsCloseEvent|TestReap_stampsCloseReasonOnRow' -v` — Expected: compile FAIL / FAIL.

- [ ] **Step 7: Implement stamp-before-kill**

`cancel_close.go`, replacing `Close`:

```go
// closeWithReason ends the local REPL and records WHY (ADR NNNN). The reason is
// stamped BEFORE the tmux teardown so any observer that sees the session gone
// can already read it (the handler polls `ccpool list` every 10 s and must never
// see live=false with an empty reason for a close ccpool made). State is not
// touched (ADR 0015). If teardown then fails, the row carries a reason for a
// session tmux still reports live; the next close attempt re-stamps and retries.
func (s *Service) closeWithReason(ctx context.Context, externalID, reason string, purge bool) error {
	return s.withLock(externalID, func() error {
		if !purge {
			if err := s.d.Store.SetCloseReason(ctx, externalID, reason); err != nil {
				return err
			}
		}
		tmuxName := TmuxName(s.d.Prefix, externalID)
		if s.d.Tmux.HasSession(tmuxName) {
			if err := s.deliverCommand(tmuxName, "/exit"); err != nil {
				return err
			}
			if !s.waitGone(tmuxName, 3*time.Second) {
				if err := s.d.Tmux.KillSession(tmuxName); err != nil {
					return fmt.Errorf("force kill: %w", err)
				}
			}
		}
		if purge {
			return s.d.Store.Delete(ctx, externalID)
		}
		return nil
	})
}

// Close is the operator/CLI entry point: closeWithReason with reason "operator".
func (s *Service) Close(ctx context.Context, externalID string, purge bool) error {
	return s.closeWithReason(ctx, externalID, "operator", purge)
}

// CloseReason is Close with an explicit reason (`ccpool close --reason`).
func (s *Service) CloseReason(ctx context.Context, externalID, reason string, purge bool) error {
	return s.closeWithReason(ctx, externalID, reason, purge)
}
```

`reap.go` line 125: `s.Close(ctx, r.ExternalID, false)` → `s.closeWithReason(ctx, r.ExternalID, reason, false)`.

- [ ] **Step 8: CLI — `close --reason`, list column**

`cmd/ccpool/close.go`: add `-reason` (default `operator`; reject values not in `store.CloseReasons` with exit 2 and the vocabulary in the message); call `svc.CloseReason(...)`; on success print `closed <external_id> (reason=<reason>)`, or `purged <external_id>` when `-purge`; when both `-purge` and a non-default `-reason` are given print `warning: --purge deletes the row; reason discarded` to stderr.

`cmd/ccpool/list.go`: add `CloseReason string \`json:"close_reason"\``to`listJSON`(lines 234-247) and populate it. In the text renderer add a`CLOSE_REASON`column whenever at least one visible row has a non-empty reason (blank cell otherwise), independent of`-all`; a reaped row then reads `working no cap_eviction` at a glance.

Tests: extend `cmd/ccpool/list_test.go` with `TestRenderList_showsCloseReasonColumnWhenSet` (one row with reason, one without: header contains `CLOSE_REASON`, the first row's cell is the reason, the second is blank) and `TestRenderListJSON_includesCloseReason`. Create `cmd/ccpool/close_test.go` with `TestClose_rejectsUnknownReasonFlag` (exit 2, message names the vocabulary) and `TestClose_purgeWithReasonWarns` (stderr contains `reason discarded`), driving `runClose` the way the neighbouring `*_test.go` files drive their `run*` functions (in-memory store, fake tmux).

- [ ] **Step 9: Run the module** — `go test ./... -timeout 10m` — Expected: PASS (update any golden file that captures `list --json`).

- [ ] **Step 10: Commit**

```bash
git add packages/ccpool
git commit -m "ccpool: record close reason before teardown (row + event); close --reason; list shows close_reason (ADR NNNN)"
```

---

### Task 4: `ccpool capacity`

**Files:**

- Create: `packages/ccpool/internal/session/capacity.go`, `packages/ccpool/internal/session/capacity_test.go`
- Create: `packages/ccpool/cmd/ccpool/capacity.go`, `packages/ccpool/cmd/ccpool/capacity_test.go`, `packages/ccpool/cmd/ccpool/capacity_integration_test.go`
- Modify: `packages/ccpool/cmd/ccpool/dispatch.go` (`subcommandInfo` slice, lines 30-48)

**Interfaces:**

- Consumes: `countedSessions` (Task 2), `preservedForHuman`, `s.d.Store.List`, `s.d.Tmux.HasSession`, `TmuxName`, `cfg.Pool.MaxSessions` (how `cmd/ccpool/reap.go` loads it), `reapFixtureStates`.
- Produces:

```go
type Capacity struct {
	MaxSessions int `json:"max_sessions"`
	Live        int `json:"live"`      // tmux-live rows, any state
	Preserved   int `json:"preserved"` // live and needs_input (outside the cap)
	Counted     int `json:"counted"`   // live and not preserved
	Free        int `json:"free"`      // max(0, MaxSessions - Counted)
}
func (s *Service) Capacity(ctx context.Context, maxSessions int) (Capacity, error)
```

CLI: `ccpool capacity` prints `free=<n> counted=<n> preserved=<n> live=<n> max=<n>`; `--json` prints the struct; exit 0 whenever the store is readable (even when `free == 0`), exit 1 on a store error (the same convention `list`/`close`/`state`/`doctor` use), exit 2 on a usage error.

- [ ] **Step 1: Failing tests** (`capacity_test.go`, session package)

```go
func TestCapacity_countsOnlyNonPreservedLiveRows(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, _ := reapFixtureStates(t, now,
		map[string]int64{"paused": 100, "work": 200, "idle": 300},
		map[string]store.State{"paused": store.NeedsInput, "work": store.Working, "idle": store.Idle})
	// a dead row must not count as live: insert one without a tmux session
	if err := s.d.Store.Insert(context.Background(), store.Session{ExternalID: "dead", ClaudeSessionID: "csid-dead", State: store.Working}); err != nil {
		t.Fatal(err)
	}
	c, err := s.Capacity(context.Background(), 6)
	if err != nil {
		t.Fatal(err)
	}
	want := Capacity{MaxSessions: 6, Live: 3, Preserved: 1, Counted: 2, Free: 4}
	if c != want {
		t.Fatalf("got %+v want %+v", c, want)
	}
}

func TestCapacity_freeFloorsAtZero(t *testing.T) {
	now := time.Unix(10_000, 0)
	s, _ := reapFixtureStates(t, now,
		map[string]int64{"w0": 1, "w1": 2, "w2": 3},
		map[string]store.State{"w0": store.Working, "w1": store.Working, "w2": store.Working})
	c, err := s.Capacity(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if c.Free != 0 || c.Counted != 3 {
		t.Fatalf("got %+v", c)
	}
}
```

(`store.Insert`'s exact signature: copy from `reapFixtureStates`' own insert call.)

- [ ] **Step 2: Run to verify failure** — `go test ./internal/session/ -run TestCapacity -v` — compile FAIL.

- [ ] **Step 3: Implement**

`internal/session/capacity.go`:

```go
package session

import (
	"context"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// Capacity is the pool's occupancy under ADR NNNN: preserved rows sit outside
// the cap; Free is what an admission gate consults.
type Capacity struct {
	MaxSessions int `json:"max_sessions"`
	Live        int `json:"live"`
	Preserved   int `json:"preserved"`
	Counted     int `json:"counted"`
	Free        int `json:"free"`
}

// Capacity derives liveness from tmux exactly as Reap does and applies the one
// capacity definition Reap's Pass 2 uses (countedSessions).
func (s *Service) Capacity(ctx context.Context, maxSessions int) (Capacity, error) {
	rows, err := s.d.Store.List(ctx)
	if err != nil {
		return Capacity{}, err
	}
	var live []store.Session
	for _, r := range rows {
		if s.d.Tmux.HasSession(TmuxName(s.d.Prefix, r.ExternalID)) {
			live = append(live, r)
		}
	}
	c := Capacity{MaxSessions: maxSessions, Live: len(live)}
	c.Counted = countedSessions(live)
	c.Preserved = c.Live - c.Counted
	if c.Free = maxSessions - c.Counted; c.Free < 0 {
		c.Free = 0
	}
	return c, nil
}
```

`cmd/ccpool/capacity.go`: build the service the way `cmd/ccpool/reap.go` does, read `cfg.Pool.MaxSessions`, parse `-json`, print as specified, return 1 on error after `fmt.Fprintf(os.Stderr, "capacity: %v\n", err)`. Register `{"capacity", "report pool occupancy and free slots", runCapacity}` in `dispatch.go`'s `subcommandInfo` slice beside `list`. Add `capacity_integration_test.go` (tag `integration`) mirroring `reap_integration_test.go`: start the real binary against a temp pool, assert `ccpool capacity --json` parses and `free == max_sessions` on an empty pool.

- [ ] **Step 4: Run tests** — `go test ./... -timeout 10m` — PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/ccpool/internal/session/capacity.go packages/ccpool/internal/session/capacity_test.go packages/ccpool/cmd/ccpool/capacity.go packages/ccpool/cmd/ccpool/capacity_test.go packages/ccpool/cmd/ccpool/capacity_integration_test.go packages/ccpool/cmd/ccpool/dispatch.go
git commit -m "ccpool: add capacity query (max/live/preserved/counted/free) for admission control (ADR NNNN)"
```

---

### Task 5: Handler ccpool seam — `Runner.Capacity`, `CloseReason`, `--reason handler`, three fakes

**Files:**

- Modify: `packages/pg-router-ccpool-handler/internal/ccpool/ccpool.go` lines 49-77
- Modify: `packages/pg-router-ccpool-handler/internal/ccpool/cli.go` (`Capacity`; `Close` argv)
- Modify: `packages/pg-router-ccpool-handler/internal/dtest/dtest.go` (`FakeCC`), `packages/pg-router-ccpool-handler/internal/watchdog/watchdog_test.go` (`fakeCC`), `packages/pg-router-ccpool-handler/cmd/pg-router-ccpool-handler/preshutdown_test.go` (`fakeCC`)
- Test: `packages/pg-router-ccpool-handler/internal/ccpool/cli_test.go`

**Interfaces:**

- Consumes: `newSpy() (*CLIRunner, *[][]string, func(out []byte))` (cli_test.go:17) — returns the runner, a pointer to the recorded argv list, and a setter for canned stdout.
- Produces:

```go
type Capacity struct {
	MaxSessions int `json:"max_sessions"`
	Live        int `json:"live"`
	Preserved   int `json:"preserved"`
	Counted     int `json:"counted"`
	Free        int `json:"free"`
}
// Session gains:
	CloseReason string `json:"close_reason"` // "" | idle_ttl | cap_eviction | operator | handler
// Runner interface gains:
	Capacity(ctx context.Context) (Capacity, error)
// dtest.FakeCC gains fields Cap ccpool.Capacity, CapErr error, and method Capacity.
```

`List` already runs `ccpool list --all --json` (cli.go:224) — no change there.

- [ ] **Step 1: Failing tests** (`cli_test.go`)

```go
func TestCLI_ListParsesCloseReason(t *testing.T) {
	cli, _, setOut := newSpy()
	setOut([]byte(`[{"external_id":"x","state":"working","live":false,"close_reason":"cap_eviction"}]`))
	got, err := cli.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].CloseReason != "cap_eviction" {
		t.Fatalf("close_reason = %q", got[0].CloseReason)
	}
}

func TestCLI_Capacity(t *testing.T) {
	cli, argv, setOut := newSpy()
	setOut([]byte(`{"max_sessions":6,"live":7,"preserved":5,"counted":2,"free":4}`))
	got, err := cli.Capacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Free != 4 || got.Preserved != 5 {
		t.Fatalf("got %+v", got)
	}
	last := (*argv)[len(*argv)-1]
	if strings.Join(last, " ") != "capacity --json" {
		t.Fatalf("argv = %v", last)
	}
}

func TestCLI_ClosePassesHandlerReason(t *testing.T) {
	cli, argv, _ := newSpy()
	if err := cli.Close(context.Background(), "x", false); err != nil {
		t.Fatal(err)
	}
	last := strings.Join((*argv)[len(*argv)-1], " ")
	if !strings.Contains(last, "--reason handler") {
		t.Fatalf("handler-initiated close must carry --reason handler; argv = %q", last)
	}
}
```

For the error case, set `cli.run` directly to return `(nil, []byte("capacity: store: disk I/O error"), errors.New("exit status 1"))` and assert `Capacity` returns a non-nil error (`TestCLI_CapacityErrorSurfaces`).

- [ ] **Step 2: Run to verify failure** — from `packages/pg-router-ccpool-handler`: `go test ./internal/ccpool/ -run 'TestCLI_(ListParsesCloseReason|Capacity|ClosePassesHandlerReason)' -v` — compile FAIL.

- [ ] **Step 3: Implement**

Add `Capacity` to the `Runner` interface and the struct; implement `CLIRunner.Capacity` via `c.ccpool(ctx, quickCallTimeout, "capacity", "--json")` + `json.Unmarshal`; add `CloseReason` to `Session`; change `CLIRunner.Close`'s argv to include `"--reason", "handler"` (purge closes too; ccpool discards it on purge). Then make the three fakes compile:

```go
// internal/dtest/dtest.go — on FakeCC
	Cap    ccpool.Capacity // returned by Capacity(); zero value means Free==0 (full), so tests that dispatch MUST set Free>0
	CapErr error
func (f *FakeCC) Capacity(context.Context) (ccpool.Capacity, error) { return f.Cap, f.CapErr }
// internal/watchdog/watchdog_test.go — on fakeCC
func (f *fakeCC) Capacity(context.Context) (ccpool.Capacity, error) { return ccpool.Capacity{Free: 1}, nil }
// cmd/pg-router-ccpool-handler/preshutdown_test.go — on fakeCC
func (f *fakeCC) Capacity(context.Context) (ccpool.Capacity, error) { return ccpool.Capacity{Free: 1}, nil }
```

- [ ] **Step 4: Run the module** — `go test ./... -timeout 10m` — Expected: PASS. (Existing executor tests that reach `Ensure` will start failing in Task 6 until their `FakeCC` sets `Cap.Free > 0`; that is Task 6's Step 4.)

- [ ] **Step 5: Commit**

```bash
git add packages/pg-router-ccpool-handler
git commit -m "pg-router-ccpool-handler: Runner.Capacity, Session.CloseReason, close --reason handler; fakes updated"
```

---

### Task 6: Handler admission gate as a busy decline; reclaim never-ingested sessions

**Files:**

- Modify: `packages/pg-router-ccpool-handler/internal/executor/ccpool.go` lines 51-62 (insert gate), 96-107 (not-ingested path)
- Modify: `packages/pg-router-ccpool-handler/cmd/pg-router-ccpool-handler/dispatch.go` lines 156-160
- Test: `packages/pg-router-ccpool-handler/internal/executor/ccpool_test.go`; `cmd/pg-router-ccpool-handler`'s existing dispatch test file (locate with `rg -l 'runDispatch' cmd/pg-router-ccpool-handler/*_test.go`; create `dispatch_busy_test.go` if none drives `runDispatch`)

**Interfaces:**

- Consumes: `newExec(cc *dtest.FakeCC, bd *dtest.ScriptBD, cfg config.Config) *ccpoolRun` (ccpool_test.go:31), `fastCfg()`, `workerRole(cfg)`, `DispatchContext{Role, Item: item.Item{ID}}`, the unexported entry point `(*ccpoolRun).run(ctx, d) (report.Result, error)`; `dtest.FakeCC.Ensured []string`, `dtest.ScriptBD.Updates []string`, `dtest.NoopGitOpener{Calls []string}` assigned to `Deps.GitOpener`; `conformance.ExitBusy`; `ccpool.IsNotIngested(err)`.
- Produces: `var ErrPoolAtCapacity = errors.New("ccpool: no free slot")` (package `executor`); `runDispatch` returns `conformance.ExitBusy` with no body when `errors.Is(err, executor.ErrPoolAtCapacity)`.

- [ ] **Step 1: Failing executor tests**

```go
func TestRun_poolFullDeclinesBusy(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{Cap: ccpool.Capacity{MaxSessions: 6, Counted: 6, Free: 0}}
	e := newExec(cc, bd, cfg)
	g := &dtest.NoopGitOpener{}
	e.deps.GitOpener = g
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	_, err := e.run(context.Background(), d)
	if !errors.Is(err, ErrPoolAtCapacity) {
		t.Fatalf("err = %v, want ErrPoolAtCapacity", err)
	}
	if len(cc.Ensured) != 0 {
		t.Fatal("Ensure must not be called when the pool is full")
	}
	if len(g.Calls) != 0 {
		t.Fatal("no worktree may be prepared when the pool is full")
	}
	if len(bd.Updates) != 0 {
		t.Fatalf("bead mutated: %v", bd.Updates)
	}
}

func TestRun_poolCapacityErrorDeclinesBusy(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{CapErr: errors.New("capacity: store: disk I/O error")}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	_, err := e.run(context.Background(), d)
	if !errors.Is(err, ErrPoolAtCapacity) {
		t.Fatalf("unknown pool must fail closed as busy; err = %v", err)
	}
	if len(cc.Ensured) != 0 || len(bd.Updates) != 0 {
		t.Fatal("launched or mutated despite unknown capacity")
	}
}

func TestRun_notIngestedClosesSession(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{Cap: ccpool.Capacity{Free: 3}, SendErr: ccpool.ErrPromptNotIngested} // use the FakeCC field that makes Send fail and the sentinel IsNotIngested recognizes
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	_, _ = e.run(context.Background(), d)
	if len(cc.Closed) != 1 { // FakeCC records Close calls in Closed (confirm the field name; add it if absent)
		t.Fatalf("a never-ingested session must be closed so it does not occupy a counted slot; closes=%v", cc.Closed)
	}
	if !dtest.HasUpdate(bd, "--status=open --assignee=") {
		t.Fatalf("bead must be unclaimed (existing behavior); updates=%v", bd.Updates)
	}
}
```

(Confirm the `FakeCC` field that fails `Send` and the not-ingested sentinel's exact name by reading `dtest.go` lines 84-94 and `internal/ccpool/ccpool.go`'s `IsNotIngested`; the assertions are the contract.)

- [ ] **Step 2: Run to verify failure** — `go test ./internal/executor/ -run 'TestRun_' -v` — compile FAIL.

- [ ] **Step 3: Implement**

In `executor/ccpool.go`, package level:

```go
// ErrPoolAtCapacity: the pool reported free == 0, or could not be queried. No
// bead was mutated and no isolation prepared; the cmd layer maps this to the
// transport's pre-accept busy decline so the core re-offers the event with
// backoff (INV-CCH-6). Unknown capacity fails CLOSED: launching blind is the
// failure mode the gate exists to stop, and a re-offer costs nothing.
var ErrPoolAtCapacity = errors.New("ccpool: no free slot")
```

Between the duplicate-absorb block (ends line 53) and `newIsolation(...)` (line 62):

```go
	capacity, capErr := r.deps.CC.Capacity(ctx)
	if capErr != nil {
		slog.Warn("dispatch declined: pool capacity unknown", "role", d.Role.Name, "bead", d.Item.ID, "err", capErr)
		return report.Result{}, fmt.Errorf("%w: %v", ErrPoolAtCapacity, capErr)
	}
	if capacity.Free == 0 {
		slog.Info("dispatch declined: pool at capacity", "role", d.Role.Name, "bead", d.Item.ID,
			"counted", capacity.Counted, "max", capacity.MaxSessions, "preserved", capacity.Preserved)
		return report.Result{}, fmt.Errorf("%w: counted=%d max=%d preserved=%d",
			ErrPoolAtCapacity, capacity.Counted, capacity.MaxSessions, capacity.Preserved)
	}
```

In the not-ingested branch (lines 103-107), before `beads.Unclaim`, add:

```go
			// The session never took the task: close it (reason handler, via the
			// CLI runner) so a ready-but-empty row does not hold a counted slot until
			// idle_ttl — cap eviction no longer reclaims ready rows (ADR NNNN).
			_ = r.deps.CC.Close(ctx, r.deps.ExternalID, false)
```

In `cmd/pg-router-ccpool-handler/dispatch.go` lines 157-160:

```go
	result, err := executor.For(role.Type).Dispatch(context.Background(), dctx, deps)
	if errors.Is(err, executor.ErrPoolAtCapacity) {
		// Pre-accept busy decline (INV-CONC-1, DEC-WIRE-1 exit 9): no body. The
		// core's listener re-offers the event with backoff; the activity ring
		// records it as "declined", distinct from "dispatch_failed".
		return conformance.ExitBusy
	}
	if err != nil {
		writeErrorReply(os.Stdout, err.Error())
		return conformance.ExitError
	}
```

- [ ] **Step 4: Make the existing executor tests dispatch again**

Every existing test that expects `Ensure` to run constructs `&dtest.FakeCC{...}` without `Cap`; the zero value is `Free: 0` and would now decline. Either give `FakeCC.Capacity` a "unset means Free: 1" default (simplest: in `dtest.go`, `if f.Cap == (ccpool.Capacity{}) && f.CapErr == nil { return ccpool.Capacity{MaxSessions: 1, Free: 1}, nil }`) and document it on the field, or set `Cap` in each affected test. Prefer the default; the two gate tests above set `Free: 0` or `CapErr` explicitly.

- [ ] **Step 5: cmd-level test for the busy mapping**

In the dispatch test file: arrange a fake executor (or a `FakeCC{Cap: Free 0}` through the real wiring, whichever the file already supports) so `Dispatch` returns `ErrPoolAtCapacity`, call `runDispatch`, and assert the return value is `conformance.ExitBusy` and stdout is empty (`TestRunDispatch_poolFullExitsBusyNoBody`).

- [ ] **Step 6: Run the module** — `go test ./... -timeout 10m` — PASS.

- [ ] **Step 7: Commit**

```bash
git add packages/pg-router-ccpool-handler
git commit -m "pg-router-ccpool-handler: admission gate declines busy when pool is full or unknown; close never-ingested sessions (INV-CCH-6)"
```

---

### Task 7: Handler eviction-aware failure path with breadcrumb and two-strike escalation

**Files:**

- Modify: `packages/pg-router-ccpool-handler/internal/executor/ccpool.go` lines 460-473 (death branch), 496-499 (`fail`), the wait-failure mapping (locate with `rg -n 'waitFailureResult' internal/executor/ccpool.go`)
- Test: `packages/pg-router-ccpool-handler/internal/executor/ccpool_test.go`

**Interfaces:**

- Consumes: `beads.Unclaim`, `beads.Comment`, `beads.HasLabel`, `beads.AddLabel`, `beads.RemoveLabel`, `beads.AddHuman` (all in `internal/beads/issue.go`), `escalateLaunchFailure` (ccpool.go:341) as the two-strike idiom to mirror, `dtest.FakeCC.ListSeq [][]ccpool.Session`, `dtest.HasUpdate(bd, sub)`, `report.Unclaimed`, `report.Escalated`.
- Produces: `var ErrExternallyClosed = errors.New("session closed externally by ccpool")`; `var externalCloseReasons = map[string]bool{"idle_ttl": true, "cap_eviction": true, "operator": true}`; `func (r *ccpoolRun) closeReason(ctx, externalID) string` (one bounded re-read after `PollInterval` when the first read is empty); `func (r *ccpoolRun) escalateEviction(ctx, beadID string) bool` (label `pool-evicted` on the first, `human` on the second consecutive external close; the label is removed on a successful completion in `finishWait`'s success path).

- [ ] **Step 1: Failing table-driven test**

```go
func TestWaitDone_deathByCloseReason(t *testing.T) {
	cases := []struct {
		name        string
		reason      string // "" = unstamped; "absent" = row missing from list
		wantUnclaim bool
		wantHuman   bool
		wantComment bool
	}{
		{"cap_eviction", "cap_eviction", true, false, true},
		{"idle_ttl", "idle_ttl", true, false, true},
		{"operator", "operator", true, false, true},
		{"handler is not external", "handler", false, true, false},
		{"unstamped death", "", false, true, false},
		{"absent row", "absent", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fastCfg()
			bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "in_progress", "in_progress"}}}
			row := ccpool.Session{ExternalID: "pg-router-worker-zr-w", Live: false, State: ccpool.StateWorking, CloseReason: tc.reason}
			var seq [][]ccpool.Session
			if tc.reason == "absent" {
				seq = [][]ccpool.Session{{}, {}, {}}
			} else {
				seq = [][]ccpool.Session{{row}, {row}, {row}}
			}
			cc := &dtest.FakeCC{ListSeq: seq}
			e := newExec(cc, bd, cfg)
			d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}} // workerRole's OnFailure is add-human
			err := e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w")
			if err == nil {
				t.Fatal("death must return an error")
			}
			if got := dtest.HasUpdate(bd, "--status=open --assignee="); got != tc.wantUnclaim {
				t.Errorf("unclaimed = %v, want %v; updates=%v", got, tc.wantUnclaim, bd.Updates)
			}
			if got := dtest.HasUpdate(bd, "--add-label=human") || dtest.HasUpdate(bd, "--add-label human"); got != tc.wantHuman {
				t.Errorf("human = %v, want %v; updates=%v", got, tc.wantHuman, bd.Updates)
			}
			if got := len(bd.Comments) > 0; got != tc.wantComment { // confirm ScriptBD's comment-recording field name; add one if absent
				t.Errorf("comment = %v, want %v", got, tc.wantComment)
			}
		})
	}
}

func TestWaitDone_secondExternalCloseEscalates(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "in_progress"}}, Labels: map[string][]string{"zr-w": {"pool-evicted"}}} // confirm how ScriptBD answers HasLabel; seed pool-evicted
	row := ccpool.Session{ExternalID: "pg-router-worker-zr-w", Live: false, State: ccpool.StateWorking, CloseReason: "cap_eviction"}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}, {row}}}
	e := newExec(cc, bd, cfg)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	_ = e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w")
	if !dtest.HasUpdate(bd, "--status=open --assignee=") {
		t.Fatal("still released")
	}
	if !(dtest.HasUpdate(bd, "--add-label=human") || dtest.HasUpdate(bd, "--add-label human")) {
		t.Fatalf("second consecutive external close must add human; updates=%v", bd.Updates)
	}
}

func TestWaitFailureResult_externallyClosedMapsToUnclaimed(t *testing.T) {
	got := waitFailureResult(roles.AddHuman, fmt.Errorf("zr-w: %w", ErrExternallyClosed), "zr-w") // adapt to the real signature found by rg
	if got.Actions[0].Verb != report.Unclaimed {
		t.Fatalf("got %+v", got)
	}
}
```

(The exact `HasUpdate` substrings for add-label come from how `beads.AddLabel`/`AddHuman` build argv — read `internal/beads/issue.go` lines 108-125 and use the literal it produces.)

- [ ] **Step 2: Run to verify failure** — `go test ./internal/executor/ -run 'TestWaitDone_deathByCloseReason|TestWaitDone_secondExternalCloseEscalates|TestWaitFailureResult_externallyClosedMapsToUnclaimed' -v` — the external-reason rows FAIL (human label applied today); the `handler`/unstamped/absent rows PASS (they pin current behavior).

- [ ] **Step 3: Implement**

Beside `active`:

```go
// externalCloseReasons: SOMETHING ELSE ended the session while the worker was
// fine — the reaper (idle_ttl, cap_eviction) or an operator. "handler" is this
// process's own close (watchdog hard-stop, not-ingested cleanup), which already
// owns its outcome. (INV-CCH-7, ADR NNNN)
var externalCloseReasons = map[string]bool{"idle_ttl": true, "cap_eviction": true, "operator": true}

// ErrExternallyClosed wraps a death whose close reason was external; the wait
// failure mapping reports it as Unclaimed.
var ErrExternallyClosed = errors.New("session closed externally by ccpool")

// closeReason returns ccpool's recorded close reason for the session, "" when
// absent, unstamped, or unreadable. ccpool stamps before teardown, so an empty
// reason on a just-dead row is rare; one bounded re-read covers a list that
// raced the stamp.
func (r *ccpoolRun) closeReason(ctx context.Context, externalID string) string {
	read := func() (string, bool) {
		sessions, err := r.deps.CC.List(ctx)
		if err != nil {
			return "", false
		}
		for _, s := range sessions {
			if s.ExternalID == externalID {
				return s.CloseReason, true
			}
		}
		return "", false
	}
	reason, present := read()
	if reason == "" && present {
		if err := r.deps.waitPoll(ctx, r.deps.Cfg.PollInterval); err == nil {
			reason, _ = read()
		}
	}
	return reason
}

// escalateEviction mirrors escalateLaunchFailure for external closes: label on
// the first, human on the second consecutive one (INV-CCH-7).
func (r *ccpoolRun) escalateEviction(ctx context.Context, beadID string) bool {
	already, err := beads.HasLabel(ctx, r.deps.BD, beadID, "pool-evicted")
	if err != nil {
		return false
	}
	if already {
		_ = beads.AddHuman(ctx, r.deps.BD, beadID)
		return true
	}
	_ = beads.AddLabel(ctx, r.deps.BD, beadID, "pool-evicted")
	return false
}
```

Replace lines 469-471:

```go
			if won() {
				if reason := r.closeReason(ctx, name); externalCloseReasons[reason] {
					_ = beads.Comment(ctx, r.deps.BD, d.Item.ID,
						fmt.Sprintf("released: ccpool closed the session (%s) before the bead completed; retrying", reason))
					_ = beads.Unclaim(ctx, r.deps.BD, d.Item.ID)
					r.escalateEviction(ctx, d.Item.ID)
					return fmt.Errorf("%s: %w (%s)", d.Item.ID, ErrExternallyClosed, reason)
				}
				return r.fail(ctx, d, "session exited before completing")
			}
```

In the wait-failure mapping add a branch `errors.Is(err, ErrExternallyClosed)` → `report.Unclaimed` (and `report.Escalated` when `escalateEviction` returned true; carry that via a second sentinel `ErrExternallyClosedEscalated` or a bool on the error struct — pick the struct form: `type externallyClosed struct{ reason string; escalated bool }` implementing `error` and `Is(ErrExternallyClosed)`). In the success path of `finishWait`, `beads.RemoveLabel(ctx, r.deps.BD, d.Item.ID, "pool-evicted")` best-effort so the strike counter is consecutive, mirroring line 94's `pool-launch-fail` removal.

- [ ] **Step 4: Run the module** — `go test ./... -timeout 10m` — PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pg-router-ccpool-handler
git commit -m "pg-router-ccpool-handler: release + comment on external close, two-strike pool-evicted escalation (INV-CCH-7)"
```

---

### Task 8: Whole-module gates, placeholder scan, land, live check

- [ ] **Step 1: Whole-module Go gates** (background, 20-minute timeout):

```bash
nix build .#checks.aarch64-darwin.ccpool-go-tests --no-link
nix build .#checks.aarch64-darwin.pg-router-ccpool-handler-go-tests --no-link
```

- [ ] **Step 2: Placeholder scan** — `rg -n 'NNNN' docs packages home` → no output.
- [ ] **Step 3: Land** via `integrate-branch:integrate-branch` (its `ff-merge-to-main` handler runs `nix flake check` at land time).
- [ ] **Step 4: After `pn workspace apply`:** `ccpool capacity` prints `free=`; `ccpool list --all --json | jq '.[0] | has("close_reason")'` prints `true`; dispatch a pg-router pair and confirm both rows still read `working` with LIVE `yes` in `ccpool list` after two reap ticks (10 minutes); `pg-router status`'s ACTIVITY shows `declined` (not `dispatch_failed`) when the pool is deliberately filled. Record on the tracking bead.

---

## Self-Review

- **Spec coverage:** incident mechanism → Task 2; eviction-vs-crash indistinguishable → Tasks 3 and 7; cap not an admission gate → Tasks 4, 5, 6; orphaned claims → Task 7 forward-looking, one-off sweep excluded explicitly; docs-first → Task 1 (both modules' behavior docs, ADR, HM comment).
- **Reviewer findings folded:** exit-code collision and INV-CCH-3 (busy decline, Task 6); listener consumes non-busy errors (busy decline); migration 008; stamp before kill and bounded re-read (Tasks 3, 7); `Runner` fan-out to three fakes (Task 5); real fixture names throughout; `l.Append`; never-ingested `ready` rows (Task 6); `close_reason` in the default table (Task 3); INFO vs WARN split (Task 6); `ccpool capacity` exit 1 on store error (Task 4); `handler` reason emitted by the handler's own closes (Task 5); bead comment and two-strike label (Task 7); INV-POOL-3 and INTF-CALLER (Task 1); numeric INV-CCH ids (Task 1); HM comment (Task 1); ADR later-note precedent (Task 1); `ccpool status` replaced by `ccpool list` (Task 8); `TestReap_recordsClosureReasonPerPass` fixture (Task 2); table-driven death test (Task 7); integration test for the new verb (Task 4).
- **Placeholder scan:** `NNNN` only (Task 1 Step 1 computes it; Task 8 Step 2 verifies none remain). Fixture-name uncertainties are limited to fields the plan tells the implementer to confirm by reading a named file range and, if absent, to add (FakeCC `Closed`/`SendErr`, ScriptBD `Comments`/`Labels`, closeTmux `onKill`).
- **Type consistency:** `Capacity` fields and tags identical in Tasks 4 and 5; `CloseReason` JSON tag `close_reason` in Tasks 3 and 5; `ErrPoolAtCapacity`, `ErrExternallyClosed`, `externalCloseReasons`, `escalateEviction`, `closeReason` introduced once and used by name.
- **Review Focus:** each of the five items names the test that pins it.
