# Invariants — pg-router-ccpool-handler

Rules this module's implementation MUST hold (see [README](README.md)'s Realization gaps for the
residual the build has not yet caught up to). A rule pg-router's own core
already states (event shape, at-least-once delivery, the offer/decline machinery) is not restated
here — see `packages/pg-router/docs/behavior/invariants.md`. These are the rules specific to this
module as an **implementer** of `INTF-HANDLER`/`INTF-SOURCE`.

- **`INV-CCH-1`** — this module's Go module MUST import `packages/pg-router` (its wire schemas and
  `conformance` package) and `packages/pg-router` MUST NOT import this module back. The dependency
  is one-way (`phillipgreenii-nix-agent-support` ADR 0065's "New module" decision); a reverse
  import would put concrete participant behavior back inside the generic core it was extracted
  out of.
- **`INV-CCH-2`** — a handler session MUST tolerate a duplicate event and MUST support the
  deferred-ack reply form, matching `INTF-HANDLER`'s obligations on every implementer; this module
  states no exception of its own.
- **`INV-CCH-3`** — a handler session's post-accept outcome (`retryable`, `resource-limit`,
  `critical`) MUST be surfaced only on this module's own logs/metrics or turned into a new event —
  never reported back to the core as anything but the opaque completion-outcome string
  `INTF-HANDLER` already grants (`packages/pg-router/docs/behavior/interfaces.md`'s `INTF-HANDLER`
  section, "A handler's run status is not part of this contract").
- **`INV-CCH-4`** — this module's own coverage-thresholds file
  (`packages/pg-router-ccpool-handler/tests/coverage-thresholds.txt`) is never merged with, or copied
  into, `packages/pg-router/tests/coverage-thresholds.txt`. The module boundary is also the
  coverage-gate boundary: a bar activated on one side never silently moves the other side's ratchet
  (`phillipgreenii-nix-agent-support` ADR 0065's "New module" decision).
- **`INV-CCH-5`** — this module MUST NOT name a concrete backing tool (`ccpool`, `bd`, `pg-pr`, or
  any operator-configured command) anywhere in `packages/pg-router`'s own contract surface (its
  `--help` text, config schema, or wire messages). Naming a backing tool is this module's own
  business, per the Floor stated in both this set's and pg-router's own `## Scope`.
- **`INV-CCH-6`** — a handler MUST query pool capacity before preparing isolation or
  launching a session and MUST NOT launch when the pool reports no free slot or cannot be
  read. It signals this ONLY through the transport's pre-accept busy decline
  (`conformance.ExitBusy`), so the core re-offers the event with backoff; it mutates no bead
  and creates no worktree. This is consistent with `INV-CCH-3`: busy is a pre-accept signal,
  not a post-accept outcome. A pool that reports its account usage window at its limit
  (`packages/ccpool/docs/behavior · INV-POOL-4`) has no free slot by construction, so it takes this
  same path under the same decline reason, and a launch that ccpool refuses for that reason after
  the capacity read is the same decline: the handler MUST NOT stamp or escalate the bead for it. A command-backed role whose configured argv exits `9` is the same
  signal: the handler MUST report it as the pre-accept busy decline (reason `command-busy`), not
  as a handler failure, and MUST NOT carry the command's stderr into that decline.
- **`INV-CCH-7`** — when a session ends before its bead completes, the handler MUST read
  ccpool's recorded close reason. An external close (`idle_ttl`, `cap_eviction`, `operator`)
  MUST release the bead (status open, assignee cleared) with a comment naming the reason,
  regardless of the role's `on_failure`, and MUST escalate to `human` on the second
  consecutive external close of the same bead. Only an unexplained death, or a close the
  handler itself requested (`handler`), applies `on_failure`.
- **`INV-CCH-8`** — when preparing per-bead isolation (`git worktree add`) fails because the
  filesystem has run out of room, the handler MUST NOT treat it as a per-bead launch failure
  (no `pool-launch-fail`/`human` escalation) — a full disk is a transient, system-wide
  condition that would hit whichever bead happened to dispatch next, not a defect in the one
  that hit it first. It MUST signal this ONLY through the transport's pre-accept busy decline
  (`conformance.ExitBusy`, reason `low-disk`), mutating no bead, so the core simply re-offers
  the event later with backoff. This is consistent with `INV-CCH-3`/`INV-CCH-6`: a system-wide
  resource shortage is a pre-accept signal, not a post-accept or per-bead outcome.
- **`INV-CCH-9`** — whenever this module records why a handler session failed, or why a check
  it runs before launching one failed, it MUST name the cause as exactly one failure signature
  from a closed set: `git-auth`, `git-network`, `mount-or-path`, `budget`, `index-lock`,
  `api-transient`, `api-rate-limit`, `api-auth`, `context-limit`, `session-errored`,
  `session-idle`, `session-gone`, or `unknown`. It MUST NOT use a free-form or empty label. The
  signature MUST come from one classification that every caller in this module shares, applied in a fixed precedence order,
  so two callers never label the same failure text differently. Text that matches no rule is
  `unknown`. That includes text that shows success even though the process exited non-zero, and
  a word such as "oauth" in unrelated prose. Any evidence recorded beside the signature MUST be
  the matched context with credentials masked (URL userinfo, bearer tokens, provider-prefixed
  tokens, long hex/base64 runs, private-key blocks). The masking MUST happen BEFORE the evidence
  is cut to at most 300 characters, so a credential that straddles the cut cannot leave a
  fragment behind. The raw failure text MUST never be logged or returned. Naming a cause is a
  judgment about the work, not an observed session fact, so this classification lives on the
  handler side and never in ccpool (`phillipgreenii-nix-agent-support` ADR 0015's "Decision").
  - **Dispatch result.** When a dispatched session fails, exits without completing its bead, or is
    hard-stopped by the budget watchdog, the handler MUST record one `dispatch_result` event-log
    entry (and a log line) carrying `failure_signature`, `signature_evidence` (redacted, at most
    300 characters, and never empty for a dispatched session), `role`, `pool`, `bead`, and
    `session`. The signature MUST be classified from the last 64 KB of the session transcript
    (its `tool_result` and text content, with a truncated first line dropped) together with the
    ccpool session facts last observed for the session (its state, whether it was live, whether
    it was still listed, and its close reason), and MUST be captured BEFORE any step that closes
    or purges the session, because teardown can make the transcript unreadable. An empty or
    unreadable transcript path is classified on the session facts alone. A transcript match
    always outranks the session facts: `api-transient` (a 5xx or `529 Overloaded` API error, a
    dropped socket or idle stream), `api-rate-limit`, `api-auth`, and `context-limit` name an
    error Claude Code wrote into the transcript. When no transcript text explains the exit, the
    facts decide: `session-errored` (state `errored`), `session-idle` (the turn ended and the
    bead is not complete), `session-gone` (the pane is dead or the row vanished). When the
    signature is `unknown` or one of the `session-*` signatures, the evidence MUST be the
    session facts followed by the last three non-empty transcript lines, so it is never empty.
    ccpool reports no process exit status or signal (`ADR 0015`: observed state only), so none is
    recorded. `budget` MUST be set only for a watchdog hard stop and reuses its `limit`, `used`,
    and `cap` fields; there MUST be no second budget
    counter. The raw transcript MUST never be logged. There is no new metric or store: "first
    seen" and "how often" are answered from the event log's own timestamps (see
    `docs/runbooks/dispatched-session-failure-signatures.md` in the repo root).
- **`INV-CCH-10`** — before accepting a dispatch, the handler MUST decline it when the git
  origin the dispatch needs is unavailable, and MUST NOT decline a dispatch for any other
  reason of origin state. Availability is per origin, keyed by a normalized repo key
  `<host>/<org>/<repo>`; a dispatch whose repo root is not a watched origin's repo root has no
  origin and MUST NEVER be gated, and a gated origin MUST NOT decline a dispatch into a different
  origin. The decline is the transport's pre-accept busy decline (`conformance.ExitBusy`) with the
  fixed reason `origin-unavailable`: it MUST mutate no bead, make no beads or ccpool call, set no
  `human`, and trigger no per-bead escalation, and the core re-offers the event as for
  `INV-CCH-6`/`INV-CCH-8`. The origin key MUST appear only in the handler's own log and event
  records, never in the reason, so the reason stays a bounded label. The gate lives here, at the
  handler boundary, because only the handler knows which repo a dispatch runs in; the core's
  global gate over its fixed named set (`INV-LIFE-2`) is not changed by it.
  - The probe MUST check that the repo root exists and holds a git checkout, then MUST ask the
    origin for its HEAD under a hard timeout, in the same hermetic git environment every other git
    child of this module gets (repository-naming variables dropped; credential delivery such as
    the ssh agent socket kept) with terminal prompts disabled. It MUST name the outcome as one of
    `ok`, `mount-missing`, `auth-unavailable`, `network`, `timeout`, or `unknown`, deriving all but
    `ok`, `timeout`, and a missing repo root from the shared failure-signature classification of
    `INV-CCH-9` rather than any table of its own. Anything it records or logs about a failure MUST
    be that classifier's redacted output; raw probe output MUST NOT be logged or persisted.
  - An origin MUST be gated only after K consecutive failed probes (default 2), so one transient
    failure does not gate; the first ok probe MUST clear it. A result MAY be reused for a TTL
    (default 60 seconds), except that a result showing fewer than K consecutive failures MUST NOT
    be reused, so a confirming probe is not delayed. While gated, each dispatch attempt MUST
    re-probe subject to the TTL, so recovery is noticed without a timer.
  - An operator MUST be able to list watched origins with their class, since, and last error, and
    to switch a single origin's probe off (a kill switch under which the probe is a no-op and the
    origin never declines). An operator MUST NOT be offered a hand-set "pause this origin": the
    automatic clear would fight it, and the existing global pg-router pause is the tool for that.
  - **Trade-off.** A declined event is re-offered only until it expires (`INV-EVT-4`). An outage
    longer than the event lifetime therefore lets events expire. That is accepted: events
    re-derive from their source on discovery once the origin returns.
  - **Telemetry.** The handler has no metrics emitter. The decline reaches
    `pg_router_failures{class=declined,reason=origin-unavailable}` through the core's existing
    handling of a busy decline's reason. Gated and cleared transitions are structured log and
    event-log records carrying the origin key, class, duration, and redacted stderr tail. An alert
    on them is separate work.
- **`INV-CCH-11`** — when the budget watchdog hard-stops a session it owns the terminal outcome
  of (it won the single-terminal claim against the bead-poll), the handler MUST record that stop
  on the bead BEFORE returning it to the pool, and MUST NOT change any budget. The record is one
  label `budget-stop:<session-id>` per stopped session; the per-bead count is the number of
  DISTINCT such labels, so writing the same session id again is a no-op and two racing writers
  cannot lose an increment. It is written through the same bd runner (and tracker) the handler
  already uses for that bead. On any bd failure the handler MUST log it and fall back to today's
  plain unclaim (the safe direction). Each recorded stop adds the bead comment
  `budget stop <n> of <threshold> (session <id>)` and the `hard_stop` event-log record gains
  `budget_stops=<n>`. The record is cleared only when the bead is CLOSED; a reopened bead keeps
  its history. The threshold is the role setting `budget_stop_escalate_after` (default 3, `0`
  disables all of the above). This invariant only counts; acting on the count is `INV-CCH-12`.
  The handler has no metrics emitter, so it emits no metric for this.
- **`INV-CCH-12`** — when a hard stop brings a bead's recorded stop count (`INV-CCH-11`) to the
  threshold or above, the handler MUST, BEFORE the unclaim, escalate: (a) a bead dispatched to the
  `review` role, to a triage role (role name containing `triage`), or already carrying `was-split`
  or any `split-from:*` label MUST go straight to a human — add `human` and comment
  `budget stops reached threshold <t>; escalated to human (<reason>; not split). stop sessions:
<ids>. last stop: session=<id> limit=<kind> used=<n> cap=<n>` (no second split round, and the
  triage role never re-enters the split path); (b) any other bead MUST get the label
  `needs-split-review` and the comment `budget stops reached threshold <t>; queued for split
review`. The claim is still released (status open, assignee cleared) — the label, not the claim,
  keeps the bead out of the pool: every beads-ready discovery the handler runs MUST exclude
  `needs-split-review` (it is added to the query's exclude labels and re-checked client-side,
  whatever the query config says; the ZR discovery queries also name it). The hard-stop event-log
  record gains `escalation=<split-review|human>` and a `budget_escalation` record carries role,
  pool, outcome, reason, `budget_stops`, `threshold`. The count is never reset, so an operator
  removing `human` or `needs-split-review` makes the NEXT stop re-evaluate and re-escalate; a
  reopened bead likewise keeps its history. A bead found closed at escalation time gets no
  escalation write and is NOT unclaimed (that would reopen it). A failed escalation write is
  logged and falls back to the plain unclaim. Threshold `0` disables escalation (and counting).
  Budgets are never changed. The handler has no metrics emitter, so
  `pg_router_budget_escalations_total` is NOT emitted; the event log and bead comments are the
  record until a metric transport exists.
- **`INV-CCH-13`** — the split-triage role (bead pg2-47rsh) works a bead labelled
  `needs-split-review` (`INV-CCH-12`) and MUST record exactly one outcome through the handler's own
  `split` subcommand, never by hand-built `bd` calls. Its completion is `close-or-split-triage`: the
  dispatch is done when the bead is closed or the `needs-split-review` label is gone (a hand-back
  and a new comment are NOT outcomes). (a) `split apply <id>` (JSON plan on stdin; at least 2
  children, each with title, description and acceptance criteria) creates each child labelled with
  the parent's labels MINUS every `budget-stop:*`, `needs-split-review`, `was-split`, `human`,
  `escalated` and `split-from:*` label PLUS `split-from:<id>` (so a child inherits no stop count),
  wires the parent BLOCKED-BY each child (`bd dep add <parent> <child>`, blocked id first, default
  `blocks` type) and verifies the edges with `bd dep list <parent>`, adds `was-split`, comments the
  decomposition, releases a held claim (status open + assignee cleared in one update), and removes
  `needs-split-review` LAST. It refuses a closed, `was-split` or `split-from:*` parent, writing
  nothing. (b) `split unsplittable <id>` (reason on stdin) adds `human`, comments the reason plus
  the stop history, releases a held claim, and removes `needs-split-review` last. If a split fails
  after children exist, the parent fails safe to `human` (label removed, claim released) rather than
  being retried. A triage session that hits its own budget is routed to `human` by `INV-CCH-12`
  and, additionally, has `needs-split-review` removed; it never re-enters the split path. No part
  of this role changes any budget. All bead text reaches `bd` as discrete argv elements from stdin
  JSON, never via a shell. The reference prompt is `docs/roles/split-triager-prompt.txt`; the
  role, its discovery query (`ready --label needs-split-review --exclude-label human`) and the
  `allowedTools` grant for the `split` subcommand are wired in the deployment config.
- **`INV-CCH-14`** — when the daemon shuts down (this module's `preShutdown` hook), the handler
  MUST SPARE every session that is actively working: a session in `starting`, `ready`, or
  `working` is left alive, and its worktree and anchor branch are left untouched, even if its bead
  has already closed. Only a session closable for another reason is purged (with its worktree and
  anchor branch): one whose turn has ended (`idle`, `errored`), or one in `needs_input` whose bead
  is already closed. A `needs_input` session whose bead is still open, or whose bead status cannot
  be determined, is also preserved (so a person can still attach). A purged session's worktree
  MUST be kept when a spared session still uses the same directory (a per-bead worktree is shared
  by every role's session for that bead). The shutdown sweep lists only the daemon's default
  `ccpool` pool: a session in a role's own dedicated pool is outside it, neither purged nor spared
  by shutdown, and is bounded only by the mechanisms below. Operator ruling (Phillip, 2026-09-30,
  bead `pg2-hwt7v`), superseding the earlier behavior of closing working sessions and
  force-removing their worktrees.
  - **What bounds a spared session after the daemon exits.** Sparing leaves a deliberate, bounded
    leak. (a) The dispatch-time reconcile of the next dispatch, run against the dispatching role's
    own pool, purges it, worktree included, once its turn has ended (`idle` or `needs_input`) and
    its bead is closed; the next shutdown sweep does too, but only for a session in the default
    pool. This is the only bound for a session parked in `needs_input` with an open bead, which
    waits for a person by design. (b) `ccpool`'s own reaper closes any live session that is not in
    `needs_input` once its last activity is older than `idle_ttl`, and evicts `idle`/`errored`
    sessions over `max_sessions` (never `starting`/`ready`/`working`); both stamp a close reason
    (`phillipgreenii-nix-agent-support` ADR 0072). (c) Spared sessions still count toward
    `max_sessions`, so `INV-CCH-6` stops the next daemon from launching past the cap while they
    run. (d) The spared session's handler dies with the old daemon, but its supervision lease
    (`INV-CCH-18`) then expires, and the next dispatch of the same role reclaims it: an idle one
    is closed and its bead claim released, a working one has its time budget enforced from its
    launch time. Until then the session runs unsupervised. (e) A worktree left with no session row at all
    is reclaimed by the worktree-keyed sweep of `INV-CCH-19`.
- **`INV-CCH-15`** — a dispatch-time worktree cleanup (after a session reaches a terminal outcome)
  MUST NOT remove a per-bead worktree while another live session in `starting`, `ready`, `working`
  or `needs_input` still uses the same directory; the last session to detach removes it. A
  per-bead worktree is shared by every role's session for that bead, so closing the first one
  (or its bead) must not delete the working directory out from under a peer. If the session list
  cannot be read the worktree is left in place. This extends the same sharing rule that
  `INV-CCH-14` and the dispatch-time reconcile apply to their own purges (bead `pg2-aqpqx`).
- **`INV-CCH-16`** — when a ccpool-backed handler session runs under `permissionMode=dontAsk`, the
  prompt the handler sends it MUST carry, ahead of the role's task text, a code-owned notice (not
  editable through any role's prompt file) that writes to the agent runtime's protected paths --
  the `.claude/` directory, except `.claude/worktrees` -- are denied automatically in that mode and
  cannot be granted by any allow-list entry. The notice MUST instruct the session that, if the task
  requires such a write, it MUST NOT attempt or retry the write, MUST stop at once, and MUST hand
  the task back through the role's normal hand-back path stating the reason and carrying the exact
  intended change (unified diff, or full content for a new file). The notice MUST NOT be sent when
  the session runs under any other permission mode. This invariant does not choose the permission
  mode; that remains a deployment decision.
- **`INV-CCH-17`** — once a dispatch reaches a terminal outcome (success, an applied failure
  action, or a budget stop), the handler MUST close, without purging, the settled session it
  launched or absorbed, so a finished session does not hold a counted slot of the pool until
  `ccpool`'s idle timeout. The close applies to that session only, never to another row, and only
  when ALL of the following hold at the moment of closing: the session is quiet (neither it nor
  its Agent-tool subagents have written for the quiet window; for every isolation type, not only
  worktrees); a fresh read shows the row present, not yet closed by anyone, and `idle` or
  `errored`. A `needs_input` session (`phillipgreenii-nix-agent-support` ADR 0037), a session still starting, ready or working, a session already closed (the budget-stop
  watchdog, an external idle-timeout, eviction or operator close), and any dispatch ended by daemon
  shutdown are exempt; the shutdown sweep owns that teardown. A session that is not yet quiet is
  left open and the deferral is logged. A failed close MUST NOT change the dispatch's outcome; it
  is logged and the session is left to `ccpool`'s idle timeout. A settled row closed this way is
  still the duplicate a crash-window redelivery of the same event absorbs (`INV-CCH-2`,
  `INV-EVT-2`) rather than a reason to launch a second session; a row closed by anyone else, or a
  non-terminal dead row, is not. The absorption is bounded by the event: the launching dispatch
  stamps its event id on the session, and a handler-closed row is a duplicate only for a dispatch
  carrying that same id. A later dispatch for the same bead and role with a different event id (a
  review reopened after a head advance), or a row with no recorded event id, launches a fresh
  session instead of being absorbed into the dead row. Operator ruling (Phillip, 2026-10-05, bead `pg2-58edz`), narrowing
  the purge-on-teardown of `phillipgreenii-nix-agent-support` ADR 0015 to a non-purge close; see
  ADR 0082. The event bound on absorption is bead `pg2-uprw5`; because the event id is identical across
  re-reviews of one bead, absorption is also bounded by the item's pinned head (`pg2-afre3`).
- **`INV-CCH-18`** — a session the handler launched MUST NOT stay unsupervised once its handler is
  gone. While a handler is alive it MUST keep a supervision lease on its session: the lease is
  stamped when the session is launched (covering the whole launch wait, so a session still
  starting is never mistaken for an orphan), refreshed on every poll for as long as the dispatch
  runs, and covers the wait, the budget watchdog, the worktree cleanup and the close of the settled
  session. A failure to refresh the lease MUST NOT fail the dispatch; it is logged, and repeated
  consecutive failures are logged at error level so a live handler whose lease is lapsing is
  visible. The lease TTL is configurable and MUST be at least ten poll intervals. A session of a
  role's own pool that carries that role's tags and an expired lease is an **orphan**; a session
  with no lease at all (launched before leases existed) is never an orphan, and neither is a
  closed one. Before checking capacity, a dispatch MUST handle every orphan of its own role, one
  at a time, while holding an exclusive per-session lock (an orphan whose lock is held is skipped)
  and only after re-reading the session to confirm the lease is still expired:
  - **`idle` or `errored`, and quiet** (neither it nor its subagents wrote within the quiet
    window): apply the role's completion rule to its bead. A role that claims its beads
    (close-only, close-or-handback) MUST release the claim, with a comment naming the session,
    only when the bead is still in progress AND claimed by that role's own actor; a bead that is
    open, closed, or claimed by someone else is not written, and a role that never claims its
    bead (the triage roles) writes nothing. If the bead cannot be read, nothing is done this
    time. Then the session MUST be closed without purging, and its worktree and anchor branch
    removed under the guards of `INV-CCH-15` (kept when a live or `needs_input` session still uses
    the directory, and kept, with the reason logged, when git refuses because the tree is dirty).
  - **`starting`, `ready` or `working`**: enforce the role's time budget, measured from the
    session's recorded launch time. Over budget, the handler MUST apply the same hard stop a
    supervised session gets (`INV-CCH-11`) and then remove the worktree under the same guards;
    under budget it MUST leave the session alone. A role with no time budget leaves such an
    orphan alone, and an over-budget orphan whose bead is already closed is left alone rather than
    reopened.
  - **`needs_input`**: left alone (`phillipgreenii-nix-agent-support` ADR 0037).
    A dispatch that absorbs an existing session as a duplicate (`INV-EVT-2`) MUST take the same
    per-session lock and refresh the lease before it starts waiting, and MUST measure that session's
    budget from its original launch time, so absorbing an under-budget orphan never grants it a fresh
    budget. A session reclaimed as an orphan is abandoned work, not a settled duplicate: a redelivery
    of the same dispatch launches a fresh session. Every reclaim and every orphan hard stop is
    recorded as an event naming the session, bead, role, pool, state, lease expiry and launch time.
    Not covered, accepted: an orphan is handled only when its OWN role next dispatches (until then
    only `ccpool`'s idle timeout bounds it); a role with no time budget leaves a `working` orphan
    alone; and only the time budget is enforced for an orphan, not tokens or cost. Operator ruling
    (Phillip, 2026-10-05, bead `pg2-g2u9m`); see ADR 0083.
- **`INV-CCH-19`** — a per-bead worktree, and its `pg-router/<bead>` anchor branch, MUST NOT stay
  behind once nothing can be using it, even when no session row leads to it: a handler killed or
  crashed after it created the worktree but before the session existed, or a session closed
  (`idle_ttl`, eviction, the operator) before anyone reclaimed its worktree, leaves one. Before
  checking capacity, a dispatch of a role with worktree isolation MUST therefore scan the worktree
  directory itself (after the orphan reconcile of `INV-CCH-18`, and bounded to a handful of removals
  per dispatch) and remove a worktree and its anchor branch only when ALL of the following hold,
  each re-checked while holding an exclusive per-bead lock:
  - it is a linked worktree directly under the worktree directory whose checked-out branch is
    exactly its own anchor branch, and git does not hold it locked;
  - it is older than a grace window (the launch wait plus the lease TTL);
  - no open or live session row of the role's pool names it, by working directory or by bead; a row
    that is closed and no longer live does not protect it;
  - no live process on the machine has it, or a directory inside it, as its working directory,
    whichever role, pool or handler owns that process; a process listing that cannot be read keeps
    every worktree;
  - its working tree is clean, and its branch holds no commit the canonical clone's `HEAD` lacks;
    an unreadable status or commit count keeps it, and so does a git refusal (the removal is never
    forced);
  - no live dispatch holds the per-bead worktree lock. Every dispatch of a role with worktree
    isolation MUST hold that lock, shared, from before it creates the worktree until it returns;
    the operating system drops it when its holder dies, so a killed handler stops protecting its
    worktree at the moment it dies.

  A session list that cannot be read, a lock that cannot be taken, or a missing lock directory
  means no action. Each removal is logged and recorded as an event naming the bead, role, worktree
  and age. A worktree that is kept for any reason is left for a later dispatch or for
  `pg-disk-reclaimer`. This closes the gap `INV-CCH-15` and the session-keyed reconciles leave: each
  of them keys on a session row. The live-process guard exists because only the dispatching role's
  own pool is visible to the row guard, and a per-bead worktree is shared by every role's session
  for the bead: a session of another role whose handler is gone (spared at a daemon restart) shows
  in no visible row and holds no lock, yet its agent process is still running in the worktree, and
  reclaiming it leaves that process in a deleted directory (bead `pg2-e5yw3`). Not covered,
  accepted: a worktree is reclaimed only when SOME worktree-isolation role next dispatches (until
  then it only costs disk); a clean, commit-free worktree whose session in another role's pool has
  no running process is removable (the same blind spot `INV-CCH-15` has, now bounded to sessions
  that are not running); and a session row
  left in `starting`, `ready`, `working` or `errored` by a killed handler is bounded by
  `INV-CCH-18`'s lease, not by this invariant (a lease-bearing row is reclaimed or budget-stopped
  there, and its worktree follows it; a leaseless one is bounded by `ccpool`'s idle timeout, after
  which this invariant reclaims its worktree). Follow-up to bead `pg2-w3usi`; bead `pg2-ganjb`;
  see ADR 0084.

- **`INV-CCH-20`** — a session's pool record MUST NOT be deleted until its per-bead worktree has
  been removed or confirmed gone; an interrupted removal MUST be retried by the next reconcile of
  the same pool; until then the record MUST NOT be treated as a duplicate to absorb. The handler
  therefore tears a session in a per-bead linked worktree down in two phases: it closes the record
  without purging, removes the worktree (from the repository root) and its anchor branch, and only
  then purges the record. A worktree directory that no longer exists, or one under the worktree
  directory that git no longer knows as a worktree, counts as gone (the husk is deleted and git's
  stale registrations pruned). A session whose working directory is not such a worktree (no
  isolation, or an unrelated path) is purged in one step, as is a session whose worktree another
  live session of the pool still uses (`INV-CCH-15`; any state, since a redispatch for the bead
  reuses the directory). A failed or interrupted removal leaves the record in place. Each
  dispatch-time reconcile of the pool MUST first retry at most one such record, whether or not its
  session is still live (a live one that is not `starting`, `ready` or `working` is closed again
  first), and that record MUST be neither closed again as a closed-bead session (`INV-CCH-14`'s
  dispatch-time reconcile) nor reclaimed as an orphan (`INV-CCH-18`) in the same pass. The
  default pool's record is also retried by the next shutdown sweep. Bound: the next same-pool
  dispatch, or for the default pool also the next shutdown; a record with no transcript can be
  pruned by the pool's own reaper first. Not covered, accepted: the retry removes one worktree
  per dispatch, so a backlog drains over several dispatches. Bead `pg2-kqegi`; closes the gap in
  which a purge-first teardown left a half-removed worktree no record led to (`pg2-me0t1`).
