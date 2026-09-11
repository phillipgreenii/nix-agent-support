# Journeys — prpool-ccpool-handler

Stories, use cases, and journeys, typed and leveled per the behavior-docs method's vocabulary
rules: **user-goal** and **subfunction** level elements are `USECASE-`; **summary**-level
multi-actor arcs stay `JOURNEY-`. Each element carries, on its own definition, what it requires and
what it includes (`INV-22`). None of the code below exists yet — see [README](README.md)'s
Realization gaps; these are the stories Task 5.2 onward realizes.

## Stories

- **`STORY-CCH-RUN`** <!-- uuid: dfd8305b-a52a-441c-a1d8-218337d7c81f --> — As pr-pool's core, I
  want a registered handler participant to run an agent session on my behalf through `ccpool` (or
  a bare configured command), so pr-pool itself never needs to know how to drive an agent.
  _(→ `USECASE-CCH-DISPATCH`; `INV-CCH-2`, `INV-CCH-3`.)_
- **`STORY-CCH-QUERY`** <!-- uuid: 661a1b2c-4243-42b3-8fcc-607e8ec7e4af --> — As pr-pool's core, I
  want a registered source to query beads for events on my behalf, so pr-pool itself never needs
  to know beads' query language. _(→ `USECASE-CCH-QUERY`; `INV-CCH-1`.)_

## Use cases

### `USECASE-CCH-DISPATCH` — run a dispatched event as a handler session <!-- uuid: 3c471620-e5bc-4a7d-b81a-6100cc862f74 -->

**Primary actor:** `ACTOR-CCH-CORE`.
**Level:** user-goal.
**Preconditions:** this module is registered with a reachable core (`phillipgreenii-nix-agent-support`
ADR 0036 — this module never starts a core).
_Requires:_ `INTF-HANDLER`, `INV-CCH-2`, `INV-CCH-3`.
_Includes:_ `INTF-CCH-CCPOOL` or a configured command, per the role's own backing kind.

1. The core dispatches one event under one tracking id to a bound role.
2. This module starts (or continues) a handler session: a ccpool-backed role drives `ccpool`; a
   command-backed role execs its configured argv.
3. This module replies inline with a completion outcome, or defers with an ack and finishes the
   session on its own, later.

Extensions:

- 2a. The role is already at capacity: this module declines `busy` before starting a session; the
  core re-offers per `INTF-HANDLER`'s pre-accept decline rule.
- 3a. The session hits a post-accept failure (`retryable`, `resource-limit`, `critical`): this
  module surfaces it on its own logs/metrics or as a new event, never as anything but the opaque
  completion outcome the core already stores (`INV-CCH-3`).

### `USECASE-CCH-QUERY` — answer a source query from beads <!-- uuid: c73f0cd4-5c9b-4862-84da-1b80716ad937 -->

**Primary actor:** `ACTOR-CCH-CORE`.
**Level:** user-goal.
**Preconditions:** this module's beads-backed source is registered and `bd` is reachable.
_Requires:_ `INTF-SOURCE`.
_Includes:_ `INTF-CCH-BEADS`.

1. The core queries this module's beads-backed source, on its own query trigger.
2. This module runs its configured `bd` query and answers inline with events, or defers and
   delivers them later on the callback.

Extensions:

- 2a. The query itself fails (e.g. `bd` unreachable): reported as the source's own failure, not a
  core-side condition.

## Open questions

- **`OQ-CCH-ROLEMODEL`** <!-- uuid: 0ec48c8a-1d82-45b9-b94b-589056323ada --> — whether this
  module's internal role model keeps a `kind` field distinguishing ccpool-backed from
  command-backed roles (mirroring the enum pr-pool's core drops per
  `phillipgreenii-nix-agent-support` ADR 0065's "Open question resolved" section), or dispatches by
  some other mechanism (e.g. one registered executor per configured role name). _Gap_: ADR 0065
  explicitly leaves this module's own internal layout to the implementer — "that distinction is
  now the handler's concern" — and no packet has settled it yet. _Owner_: whichever task folds the
  command executor into this module's own handler-dispatch entrypoint. _Path_: settle when that
  entrypoint is built (Task 5.3), as an internal implementation choice, not a behavior-docs
  decision. _Blocks_: nothing today — this module carries no code yet.
