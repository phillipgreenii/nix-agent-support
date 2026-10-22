# Journeys — pg-router-handler-ccpool

Stories and use cases, typed per the behavior-docs method; each carries on its own definition what it
requires and includes (`INV-22`).

## Stories

- **`STORY-CCA-RUN`** <!-- uuid: e07c24a8-add4-4335-ba53-6860749d18dc --> — As pg-router's core, I want a role to run an agent session for one
  event and tell me whether it was accepted, declined or finished, and I want a restart of me never
  to lose the session. _(→ `USECASE-CCA-DISPATCH`; `INV-CCA-1`, `INV-CCA-2`, `INV-CCA-3`.)_
- **`STORY-CCA-ENV`** <!-- uuid: 9738bd68-28f8-4537-b371-958ed721ffde --> — As an operator, I want each role's session to start with the
  environment I declared for it, and to see why a session that never became ready was declined, so a
  tool the role needs is on its path and a broken launch tells me what failed without touching the
  bead. _(→ `USECASE-CCA-LAUNCH`, `USECASE-CCA-VALIDATE`; `INV-CCA-2`, `INV-CCA-4`, `INV-CCA-5`,
  `INV-CCA-6`, `INV-CCA-7`, `INV-CCA-8`, `INV-CCA-10`.)_
- **`STORY-CCA-CUTOVER`** <!-- uuid: c0325999-4547-4f9a-ab61-11550f8d94c6 --> — As an operator, I want to move one role at a time from the
  earlier handler to this adapter without dropping a running session or changing what my alerts
  read. _(→ `USECASE-CCA-CUTOVER`; `INV-CCA-9`.)_

## Use cases

### `USECASE-CCA-DISPATCH` — run a dispatched event as a session <!-- uuid: d322ec5c-571d-4c95-b07d-a9d64bc152f1 -->

**Primary actor:** `ACTOR-CCA-CORE`.
**Level:** user-goal.
**Preconditions:** the role names this adapter as its `handler`.
_Requires:_ `INTF-HANDLER`, `INV-CCA-1`, `INV-CCA-2`, `INV-CCA-3`.
_Includes:_ `INTF-CCA-SESSION`, `USECASE-CCA-LAUNCH`.

1. The core dispatches one event, with the role object, under one tracking id.
2. The adapter decodes the role's configuration strictly (`INV-CCA-2`) and applies the lifecycle
   rules of a session-backed role (`INV-CCA-1`): it checks capacity, prepares isolation, and starts
   or adopts a session (`USECASE-CCA-LAUNCH`).
3. It supervises the session, and replies inline or defers, and the session outlives a restart of
   the core, whose next dispatch adopts it (`INV-CCA-3`).

Extensions:

- 2a. The configuration has an unknown or mistyped field: the call fails with the field named and
  nothing is launched.
- 2b. The role is at capacity, its origin is unavailable, or its precheck proves the dispatch moot:
  the adapter declines before launching, exactly as the earlier set states (`INV-CCA-1`), with the
  kind its reason maps to (`INV-CCA-6`).
- 2c. The session never becomes ready: the adapter declines, as `USECASE-CCA-LAUNCH` describes.

### `USECASE-CCA-LAUNCH` — start a session with the role's environment <!-- uuid: f08ab96e-3e53-4f99-9930-861e0dce87bd -->

**Primary actor:** `ACTOR-CCA-CORE`.
**Level:** subfunction, included by `USECASE-CCA-DISPATCH`.
**Preconditions:** the deployment has set the role's `sessionEnv` and named the pool's agent
executable.
_Requires:_ `INV-CCA-4`, `INV-CCA-5`, `INV-CCA-6`, `INV-CCA-7`, `INV-CCA-10`.
_Includes:_ `INTF-CCA-SESSION`.

1. The adapter asks the session tool to start the session, passing the role's `sessionEnv`, the
   markers and the item context explicitly (`INV-CCA-4`, `INV-CCA-5`), and writes one
   `dispatch_start` record (`INV-CCA-10`).
2. The session tool starts the pool's agent executable, which is opaque to it, and the session
   becomes ready.
3. Before any later send, the adapter checks that the session is live (`INV-CCA-7`).

Extensions:

- 2a. The launch fails or times out, the session's payload fails in its pane, or the agent dies or
  stalls before ready: the adapter purges the abandoned session, reclaims its worktree, writes no
  bead and declines `busy` with reason `launch-failed` and kind `fault`, with the redacted evidence
  as `detail`, and one `dispatch_decline` record (`INV-CCA-6`, `INV-CCA-10`). The core re-offers
  the event while it is unexpired.
- 2b. The adapter's own context is cancelled during the launch: no decline is given (`INV-CCA-6`).
- 3a. The session is not live when a reminder, a wrap-up or the nudge would be sent: the dispatch
  treats it as ended by the dead-session path and never resumes it (`INV-CCA-7`).

```mermaid
sequenceDiagram
    participant A as pg-router-handler-ccpool
    participant S as session tool
    participant G as agent
    A->>S: start, with sessionEnv, markers and item context
    S->>G: start the pool's agent executable
    alt ready
      G-->>A: the session is ready
    else fails before ready
      S-->>A: failure with status and output tail
      A->>A: purge, reclaim, decline launch-failed (fault)
    end
```

### `USECASE-CCA-VALIDATE` — check a role's configuration at build time <!-- uuid: 5873b485-f027-4242-ab5b-74aa10b92fd8 -->

**Primary actor:** `ACTOR-CCA-OP`.
**Level:** user-goal.
**Preconditions:** the role's configuration has been rendered by the deployment.
_Requires:_ `INTF-CCA-CONFIG`, `INV-CCA-2`, `INV-CCA-4`, `INV-CCA-8`.

1. The deployment's build runs the adapter's `validate` over each role's rendered table.
2. A table with an unknown or mistyped field, a `sessionEnv` key that collides with a marker, is
   empty or contains `=`, or a referenced pool directory that does not exist, fails the build, naming
   the role and the problem.
3. A `sessionEnv` key that looks like a secret is a warning, and the build finishes.
4. The core runs the same `validate` at startup and `doctor` runs it on demand, so a role that fails
   it stays registered and is unavailable (`INV-ROLE-5`). The adapter never runs `pg-wrap` there.

### `USECASE-CCA-CUTOVER` — move one role from the earlier handler to this adapter <!-- uuid: f31b9c82-83ee-4d1d-acd6-1b5947a829f1 -->

**Primary actor:** `ACTOR-CCA-OP`.
**Level:** user-goal.
**Preconditions:** the role's own configuration has been moved into its `[role.config]` table.
_Requires:_ `INV-CCA-9`, `INV-CCA-3`.
_Includes:_ `USECASE-CCA-VALIDATE`.

1. The operator sets the role's `handler`, builds (`USECASE-CCA-VALIDATE`), exercises the role once
   with the smoke-test command, and applies.
2. A session the earlier handler left running is adopted by this adapter's redelivered dispatch
   (`INV-CCA-9`).
3. The operator reads the failure-rate alert's inputs and finds the reason strings unchanged
   (`INV-CCA-9`).

Extensions:

- 2a. The adoption does not happen: a second session is launched, or one is orphaned. The move is
  reverted, which puts the role back on the earlier path with nothing else to undo.
