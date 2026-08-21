# Invariants — ccpool

Rules this set's implementation MUST hold, following the behavior-docs method
(`phillipgreenii-nix-agent-support · behavior-docs/docs/behavior`). Names are namespaced by topic
(`INV-3`) since this set cites the method, and — because ccpool has no pr-pool edge by design
(D2) — every clause here is restated at ccpool's own floor: no `owner`/`role`/`item`/`claim`/
`tracker`/`Callback` vocabulary, and no pr-pool citation.

## Pool (`INV-POOL-*`)

- **`INV-POOL-1`** <!-- uuid: b9bdb506-7898-4040-98d6-c537cadf2494 --> — **A pool is a directory.**
  Its identity and its runtime substrate both derive from that one canonical location, so two
  pools MUST NOT share a substrate. Every session a pool launches MUST be marked excluded from a
  co-resident monitor's own actuation (`packages/pa-monitor/docs/behavior · INTF-NONUDGE`,
  `ACTOR-CCP-MONITOR`), and the
  guardrails a pool establishes at launch (isolation, unclassified-tool pre-denial, the
  autonomous-mode question block) MUST NOT be weakened by a session-level setting.
- **`INV-POOL-2`** <!-- uuid: c6506508-a27c-44ee-9901-f9eda11ba61c --> — Every pool MUST
  self-register in a machine-wide registry at creation, and a periodic, timer-driven sweep MUST
  reap every registered pool per its own policy — independent of whether anything is actively
  dispatching to it. A registry entry whose pool no longer exists MUST be discarded without
  touching any other pool's data.
- **`INV-POOL-3`** <!-- uuid: 422e92cd-079c-430f-98b3-2bdc76f6fb7d --> — **Reap MUST spare a
  human-awaited session** (one in `needs_input`) under both inactivity-bound and pool-cap
  eviction, ordering eviction among the rest by **least-recent activity**, never by creation
  order. The pool MAY sit above its cap when only spared sessions remain — that is accepted, not a
  defect. A session belonging to a run that no longer exists MUST NOT accumulate across restarts;
  the next sweep MUST reclaim it.

## Session lifecycle (`INV-STATE-*`, `INV-SESS-*`, `INV-CCPOOL-CWD-*`)

The two state vocabularies, the six store states, a session's dispatch/status/ingestion
obligations, and ownership of a session's working directory.

### The two state vocabularies (`INV-STATE-*`)

- **`INV-STATE-1`** <!-- uuid: c9bae1bc-6ee6-433b-a18d-3f51e6e9fc7f --> — **Store state and
  reconciled state are two named concepts and MUST NOT be conflated.** Store state is the last
  **observed turn outcome** — a fact recorded by the agent's own signals. Reconciled state is what
  the session is doing **now**, derived on read from liveness plus observation, and MUST NOT be
  cached: liveness is always re-derived, never trusted from a stored value. Any statement about "a
  session's state" MUST say which of the two it means.
- **`INV-STATE-2`** <!-- uuid: 08fcd6eb-2521-436e-960d-dce17b58f272 --> — Store state MUST be
  exactly one of six values, each obliging a consumer to a specific conclusion: **starting**
  (launch under way — MUST NOT be dispatched to); **ready** (accepted for input, nothing dispatched
  yet); **working** (a turn is in flight — a dispatch MUST be refused, `INV-SESS-2`'s busy
  reply); **needs_input** (alive, **non-terminal**, holding a question — **MUST NOT** be read as a
  failure; the work is neither done nor lost); **idle** (the last turn **ended** — means only "the
  turn is over," never "the requested work succeeded"); **errored** (the last turn **failed** —
  absent a failure reason (`INV-SESS-1`), a consumer MUST NOT infer which kind of failure).

### Sessions (`INV-SESS-*`)

- **`INV-SESS-1`** <!-- uuid: 17b28a64-7ee3-4962-ae95-f06c617f6a8a --> — The status surface's
  intended contract includes, alongside `errored`, a **failure classification** (at minimum:
  transient-infrastructure, rate-limited, terminal) and the failing turn's diagnostic text, so a
  consumer can tell retry-worthy from fatal. Until that field is realized (`## Realization gaps`),
  a consumer MUST treat `errored` as unclassified rather than guess a class. The runtime's own
  retry logic already tells a transient failure apart from the other two classes internally, so
  this gap is a persistence gap, not a classification one.
- **`INV-SESS-2`** <!-- uuid: 86a26bc7-1018-44d0-8ed7-cfb7ccf1d01a --> — **A session's status is a
  surface a caller reads; ccpool MUST NOT push it anywhere.** The caller polls `INTF-CALLER`'s
  status read whenever it wants an answer. A dispatch aimed at a `working` session MUST be
  refused, telling the caller busy, rather than queued or silently dropped.
- **`INV-SESS-3`** <!-- uuid: dc315663-564a-481c-a3e0-a911bb3d8aaf --> — A fire-and-forget dispatch
  MAY demand ingestion confirmation (the prompt was actually taken up), and an unconfirmed
  delivery MUST be reported as its own distinct outcome — never conflated with busy or with a
  plain timeout.

- **`INV-SESS-4`** <!-- uuid: 7994abf0-a943-479a-856b-99526f13a5f2 --> — Session metadata is an
  **opaque, caller-owned** key/value set. ccpool MUST NOT interpret a key's meaning; it MUST only
  store what a caller sets, return it uninterpreted, and let a caller filter sessions by it.

### Working directory (`INV-CCPOOL-CWD-*`)

- **`INV-CCPOOL-CWD-1`** <!-- uuid: 549deacd-8a8e-430b-b4d8-a29c17dce903 --> — A session's
  **working directory** is **caller-owned**. The dispatching caller MUST be able to state it
  explicitly per session, and ccpool MUST honour that value rather than substituting one of its
  own. Absent an explicit value, ccpool MUST fall back to a **pool-scoped configured default**,
  and failing that to the **invoking process's** working directory. The working directory is
  **decided once, when the session is created**, is **recorded on the session**, and is
  **immutable for the session's life**: resuming a cold session MUST relaunch it in its
  **recorded** directory and MUST NOT re-derive one from the resuming caller's environment. The
  pool's own **state location MUST NOT** be used as a session's working directory. (`ADR 0038`)

## Autonomous mode (`INV-AUTON-*`)

- **`INV-AUTON-1`** <!-- uuid: fbd37bcd-8c37-43ad-a2d4-91f23ebb43ba --> — A session dispatched
  **autonomous** MUST have its human-question channel **structurally removed**: an attempted
  question MUST be denied before it is ever posed, and the denial MUST carry a reason the agent
  inside the session receives, so it can adapt rather than conclude it has been abandoned
  (`ACTOR-CCP-AGENT`, `INTF-DENY`). The mode removes the **channel**, not the **state** — a
  session denied a question MAY still separately enter `needs_input` for an unrelated reason, and
  MUST still be notified per the ordinary rule (`INV-NOTIF-1`). Autonomous mode is set per dispatch
  by the caller and MUST NOT be assumed to persist across a relaunch that does not restate it.

## Budget observation (`INV-METER-*`)

- **`INV-METER-1`** <!-- uuid: 94cd77f1-9ff3-4cda-a65d-53ace679e48d --> — ccpool owns the
  **meter**: a caller SHOULD be able to read a session's own resource consumption from ccpool.
  Policy over that reading — ceilings, wind-down, stopping — is the caller's, out of this set's
  extent. Until the meter is realized as a declared reading (`## Realization gaps`), the
  transcript anchor is the only carrier, and it is not itself a consumption reading. This
  per-session reading is distinct from the co-resident monitor's own **account-level** usage
  surface (`packages/pa-monitor/docs/behavior · INTF-STATE`, declared, not yet consumable
  either) — neither substitutes for the other.

## Notifications (`INV-NOTIF-*`)

- **`INV-NOTIF-1`** <!-- uuid: 3a56b9af-4209-4afb-9726-67a643535075 --> — ccpool MUST emit a
  notification on the **transition into** a notifying state, computed where every transition is
  observed (never sampled), and MUST NOT itself own delivery to the operator — the configured
  notifier owns that, and an absent or misconfigured notifier MUST NOT be read back as a defect
  in the transition itself.

## Trust and consent (`INV-TRUST-*`)

- **`INV-TRUST-1`** <!-- uuid: 80c7bc5c-0302-4c61-a2d5-31e6d0ba0973 --> — A session MUST NOT hang
  on an interactive trust or tool-consent gate the caller cannot see. ccpool MUST pre-establish
  folder trust for a session's directory before launch, and MUST pre-deny any external tool
  server that directory declares but the deployment has not classified — deny-by-default, never
  left to an interactive prompt.
