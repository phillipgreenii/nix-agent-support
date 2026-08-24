# ccpool reap spares a human-paused session — TTL and cap eviction alike

**Status**: Accepted
**Date**: 2026-07-29
**Deciders**: Phillip Green II

## Context

ccpool's reaper (`packages/ccpool/internal/session/reap.go`) closes sessions in two passes over the
live rows, after a Pass 0 that prunes phantom rows (ADR 0015):

- **Pass 1 — TTL**: close every live session whose `last_activity_at` is older than `idle_ttl`.
- **Pass 2 — cap**: while still over `max_sessions` _after_ the TTL closures, close more sessions
  oldest-activity-first.

Neither closure pass carried **any state predicate**. A `needs_input` row is **idle by
construction** — the session is parked mid-turn waiting for a person — so it is simultaneously the
**first** session Pass 1 reaps and the **first** Pass 2 evicts. The human's in-flight context is gone
before they can `ccpool attach`.

pr-pool's orchestrator honours the **opposite** rule, and has since bead `pg2-th35`:
`closeUnlessNeedsInput` preserves a `needs_input` session explicitly, shared by `teardownAll` and
run-role's single-session teardown so those two paths cannot drift.

The ZR deployment set states the rule normatively as `INV-CCPOOL-6`
(`your-private-flake/modules/zm/pr-pool/docs/behavior/invariants.md`):

> A session projected as **`paused`** for a **human decision** (awaiting input) **MUST** be
> **preserved**, not reaped, so a person can attend it; it is **non-terminal**, resumes with context
> intact, and does not itself release the claim. […] Teardown of the pool's sessions **MUST** spare a
> human-awaited `paused` session.

`INV-CCPOOL-7` leans on the same carve-out for stray reaping ("sparing any human-awaited `paused`
session (`INV-CCPOOL-6`)").

So one carve-out was honoured by exactly one of two mechanisms. `pg2-th35` delivered the **teardown**
half only, and the orchestrator's package doc recorded the gap in the open: _"A reaper TTL for
preserved sessions was considered and deferred (pg2-th35)."_ Under pr-pool's precedence rule
`INV-PREC-1` (mirrored as ZR's `INV-GOV-3`) a newly-discovered conflict like this **MUST** be
resolved by a decision rather than patched ad hoc, which is why this ADR exists rather than a bare
code change. Found and re-verified by bead `pg2-z3aya`.

One fact about the pool cap is **load-bearing for the decision below**, and was verified against the
code rather than assumed:

- `max_sessions` is enforced **only** by the reaper. `session.Service.Ensure` never consults it —
  `MaxSessions` appears in `cmd/ccpool/reap.go`, `cmd/ccpool/reap_all.go`, and the config default,
  and nowhere else.
- pr-pool's per-role `Cap` bounds **items worked per drain pass** (`orchestrator.drain`'s `worked`
  counter), not live sessions.

The cap is therefore a **lazy, best-effort ceiling**, not an **admission gate**. Nothing in either
component refuses to start work because the pool is full.

## Decision

**TTL reap and cap eviction MUST both spare a live session whose last observed state is
`needs_input`.** Concretely:

1. Reap's TTL pass **MUST NOT** close a live row in `needs_input`, however far past `idle_ttl` its
   `last_activity_at` is.
2. Reap's cap-eviction pass **MUST NOT** close a live row in `needs_input` either — **including** the
   case where sparing it leaves the pool **above** `max_sessions`. Cap eviction gets **no last-resort
   override**.
3. Preservation is **unbounded**: there is **no** preservation TTL, and no new config key. A
   preserved session survives every subsequent reap until it leaves `needs_input` or an operator
   closes it.
4. Preservation governs **closure only**. Pass 0's phantom prune is **unchanged**: a row that is not
   live and whose Claude session is gone from disk holds no attachable context, so there is nothing
   to preserve. Preservation is meaningful only for **live** rows — which matches the projection, where
   liveness precedes state (`internal/state`, precedence 1: not live ⇒ `not-live`).
5. The carve-out **MUST** be expressed as **one predicate per component**, each citing this ADR:
   `preservedForHuman` in ccpool's reaper (shared by Pass 1 and Pass 2 so the two passes cannot
   drift) and `closeUnlessNeedsInput` in pr-pool's orchestrator (already shared by its two teardown
   paths).
6. The only way a `needs_input` session leaves the pool is **explicit operator action** — attend it
   (`ccpool attend`, `ccpool attach`) or close it (`ccpool close <external-id>`).

Rationale:

- **Precedence decides it, not taste.** `INV-PREC-1` orders **safety/isolation > continuity
  (never drop work) > efficiency**. Preserving a human's in-flight context is _continuity_; holding
  the pool at `max_sessions` is _efficiency_. Continuity wins. That ordering applies to the cap pass
  exactly as it does to the TTL pass, which is why the cap gets no override.
- **This is conformance, not a new rule.** `INV-CCPOOL-6` already says "**MUST** be preserved, not
  reaped", unqualified, and pr-pool already implements it. The reaper was the one mechanism out of
  step. The reconciliation is therefore **one-directional**: the code moves to the doc, and
  `INV-CCPOOL-6`'s wording needs **no** change.
- **The deadlock a last-resort override would prevent does not exist.** Sparing every slot's
  occupant would be indefensible if the cap were an admission gate — a pool of `max_sessions`
  paused sessions could then never admit new work. It is not a gate (see Context): `Ensure` starts a
  session regardless of pool size, and pr-pool's `Cap` counts dispatches per pass. So the cost of
  sparing is **growth above the cap**, not **starvation**. Growth is recoverable by an operator;
  a discarded conversation is not.
- **A bounded preservation TTL is a doc change, not just a code change.** Adding one would put a
  bound on an invariant that currently states an unqualified MUST, so it would require amending
  `INV-CCPOOL-6` in the ZR deployment set. That is a human call, and it is the same option
  `pg2-th35` already considered and deferred. Deliberately not taken here; see Alternatives.

## Consequences

### Positive

- Two mechanisms, **one** rule: ccpool's reaper and pr-pool's teardown now agree, so a session that
  survives a teardown pass is no longer killed minutes later by the timer-driven reaper.
- Conformance with `INV-CCPOOL-6` and `INV-CCPOOL-7` is restored **without** touching the deployment
  set — the invariant becomes true of the implementation rather than aspirational.
- No config surface, no store schema change, no change to the reaper's cap arithmetic.
- `orchestrator.go`'s "considered and deferred" note is replaced by a pointer to this ADR, so the
  known-open gap stops being folklore.

### Negative

- **The accepted failure mode: a pool MAY sit above `max_sessions` indefinitely.** With enough
  unattended `needs_input` sessions, reap converges to "close nothing" and the pool grows —
  one tmux session and one `claude` process per preserved session. `idle_ttl`/`max_sessions` are no
  longer a hard ceiling on live sessions; they are a ceiling on **reapable** ones.
  - This is **not** a deadlock and cannot starve new work: there is no admission gate (see Context).
  - It is bounded in practice by the operator being told: the notifier fires on the
    `working → needs_input` edge (`Deps.Notify`/`notify_on`), and `ccpool attend` / `ccpool list`
    enumerate exactly these sessions.
  - The remedy is explicit and always available: `ccpool close <external-id>`.
- Reap no longer guarantees that a pool converges to `≤ max_sessions`. Anything that assumed the
  timer eventually drains the pool to the cap must instead treat the cap as advisory.
- A session wedged in `needs_input` by a bad hook edge (rather than a real question) is now
  immortal until an operator notices it. The pre-existing Pass 0 prune still collects it once its
  pane dies and its transcript is gone.

### Neutral

- Preserved sessions still **count** toward the cap (`len(live)` is unchanged in the `capClosures`
  computation), so eviction pressure on the non-paused sessions is still computed against the real
  pool size — sparing a paused session does not buy a non-paused one a reprieve.
- `auto_reap = false` (ADR 0014) is orthogonal and unchanged: it skips a pool entirely in the
  `reap-all` sweep, preserved sessions or not.

## Alternatives Considered

### Cap eviction as a last resort (spare under TTL, evict under cap pressure)

Spare `needs_input` while any non-paused candidate remains, then evict paused sessions
oldest-first — logging the reason — once they are the only candidates left. **Rejected.** It
contradicts `INV-CCPOOL-6`'s unqualified MUST, so it could not be adopted without amending the
deployment set; it inverts `INV-PREC-1` by letting efficiency beat continuity; and the deadlock it
buys protection against does not exist, because the cap is not an admission gate. It also makes the
worst outcome (losing a human's context) depend on unrelated pool load, which is exactly the
non-determinism a human would have to debug.

### Bounded preservation TTL (`preserved_ttl`)

Spare a paused session from `idle_ttl` but reap it after a longer, separately-configured window —
the option `orchestrator.go` recorded as "considered and deferred (`pg2-th35`)". **Rejected for
now**, not dropped: it adds a config key and a second TTL to reason about, and it needs a bound
clause added to `INV-CCPOOL-6` in the ZR deployment set — a human decision, not an implementer's.
Revisit if over-cap growth is actually observed; the predicate this ADR introduces is the natural
place to hang it.

### One predicate shared across both Go modules

Extract a single exported helper and have both components call it. **Rejected.** `sessionmeta` is
deliberately "the ONLY exported ccpool Go API" and the rest of ccpool is internal, so this would
widen ccpool's public surface for a one-line predicate. pr-pool already **mirrors** ccpool's state
vocabulary on purpose (`internal/ccpool.SessionState`, "mirrors ccpool's store states") rather than
importing it — the duplication across that seam is an existing, intentional choice. Both predicates
citing this ADR is the cheaper coupling.

### Leave reap alone and weaken `INV-CCPOOL-6`

Declare the reaper's behavior correct and add a reap carve-out to the invariant. **Rejected.** The
invariant's whole purpose is that a human's in-flight decision survives; a reap exemption is the one
exemption that guts it. It would also require a deployment-set edit to legitimize the exact bug
`pg2-z3aya` reports.

## Related Decisions

- [ADR 0014](0014-ccpool-reap-all-pool-registry.md) — `reap-all` over the pool registry and
  `auto_reap`; this ADR narrows what a reap pass may close, not which pools get reaped.
- [ADR 0015](0015-ccpool-session-facts-not-work-judgments.md) — ccpool tracks session FACTS.
  `needs_input` is such a fact (a hook-observed edge), which is why the reaper may key on it without
  making a work judgment.
- `INV-CCPOOL-6`, `INV-CCPOOL-7`, `INV-GOV-3` in the ZR deployment set
  (`your-private-flake/modules/zm/pr-pool/docs/behavior/invariants.md`) — the normative
  statement this ADR conforms to, unchanged by it.
- `INV-PREC-1` in `packages/pr-pool/docs/behavior/invariants.md` — the precedence ordering that
  decides the cap case.
- Beads `pg2-th35` (teardown half, closed) and `pg2-z3aya` (this reap half).
