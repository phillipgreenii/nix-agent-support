# Invariants — pg-router-handler-ccpool

The rules this adapter holds. They bound what the adapter does with one dispatched event and with the
environment its sessions start in; the rules for a session's own lifecycle are the earlier combined
handler's and are cited, not restated. IDs are topical and stable; each rule uses RFC 2119 language.
See the [glossary](glossary.md), [interfaces](interfaces.md), [actors](actors.md) and
[journeys](journeys.md).

## The adapter and its role

- **`INV-CCA-1`** <!-- uuid: 1c9e4008-f2d1-461f-99f4-bcbe441c2e10 --> — **The adapter is a full participant, and a session-backed role's
  lifecycle rules hold for it unchanged.** It answers dispatch, capacity, `preShutdown`,
  `postStartup` reconcile and the orphan sweeps, because sessions outlive daemon restarts
  (`phillipgreenii-nix-agent-support` ADR 0085). The split from the earlier combined handler is a
  split, not a rewrite: duplicate tolerance (`INV-CCH-2`), capacity before isolation (`INV-CCH-6`),
  failure signatures (`INV-CCH-9`), origin unavailability (`INV-CCH-10`), the shutdown sweep that
  spares working sessions (`INV-CCH-14`), closing a settled session (`INV-CCH-17`), the supervision
  lease (`INV-CCH-18`), the pre-launch precheck (`INV-CCH-22`), the no-strike usage limit
  (`INV-CCH-28`) and a fresh session per dispatch (`INV-CCH-30`) apply to a role of this adapter
  exactly as they did before, and this set restates none of them.
- **`INV-CCA-2`** <!-- uuid: 78592e2a-b769-4e72-86ed-5c22e9b93645 --> — **The adapter reads its configuration from the role object, strictly.**
  It reads the role's own settings from the configuration table that arrives with every request
  (`INV-ROLE-2`), and it **MUST** fail the call, naming the field, when the table has a field it does
  not know or a field of the wrong type. It **MUST** also provide `validate` (`INV-CCA-8`), which
  applies the same decoding to a rendered table without running anything, so a deployment can fail
  its build on a dropped or misspelled field before it is applied.
- **`INV-CCA-3`** <!-- uuid: 3f48ccd2-e5a3-404f-8f15-c902b20773ed --> — **Every role of this adapter survives the core's shutdown, and a role
  cannot silently lose that.** The deployment's helper for this adapter sets the role's
  shutdown-survival property (`INV-ROLE-3`), and the deployment **MUST** assert, when it evaluates its
  configuration, that every role using this adapter has set it. A session is supervised by this
  adapter for far longer than a shutdown budget, and the next core re-offers the event and adopts the
  session (`phillipgreenii-nix-agent-support` ADR 0085).
  - **The property's name.** The role setting is spelled `survives_shutdown` in the deployment's
    configuration.

## The environment a session starts in

- **`INV-CCA-4`** <!-- uuid: 7e42c99d-b13a-4fff-92f9-5b6316c68bea --> — **A role's `sessionEnv` reaches the session uninterpreted, and holds no secret.**
  A role MAY carry `sessionEnv`, a generic string-to-string map in its own configuration. The adapter
  **MUST** pass every entry to the session tool when it starts a session (`ccpool new --env`), as it
  passes `BEADS_ACTOR`, and **MUST NOT** read, order or act on any entry. The role's configuration
  is in error when a key collides with a marker the adapter sets (`BEADS_ACTOR`, `BEADS_DIR`,
  `WORKSPACE_ROOT`, or any name beginning `PG_ROUTER_`), is empty, or contains `=`. `sessionEnv`
  **MUST NOT** carry a secret, because it is visible in the session's environment and in the
  adapter's event records (`INV-CCA-10`); a secret belongs inside a prefix tool's own spec, where it
  is applied to the payload alone. `validate` (`INV-CCA-8`) warns on a key matching `TOKEN`,
  `SECRET`, `KEY` or `PASSWORD`. A deployment uses the map to hand the agent's launch what a prefix
  tool needs, such as the one entry `PG_WRAP_SPEC` that names the spec file of `pg-wrap`, an
  external tool. The pool's agent executable stays opaque to the session tool (`INV-POOL-6`), so the
  session tool learns no prefix-tool concept, and neither does the adapter (`INV-CCH-33`).
- **`INV-CCA-5`** <!-- uuid: 5c144a64-19a4-4a60-b334-3d2f169d19a4 --> — **Every start of a session passes the whole environment, and the item context never carries the title.**
  A role is a command plus the minimal base environment and the item context, and pg-router declares
  nothing else about how it is run (`INV-ROLE-4`). The adapter is not the agent's parent: the
  terminal multiplexer starts the agent with its own environment plus what the session tool is told. So each start the adapter issues (a brand-new launch or an
  ensure) **MUST** pass again the role's `sessionEnv`, the markers `BEADS_ACTOR`, `BEADS_DIR` and
  `WORKSPACE_ROOT`, and the item context `PG_ROUTER_ROLE`, `PG_ROUTER_ITEM_ID` and
  `PG_ROUTER_ITEM_TYPE`, from the dispatched item's id and type (`INV-CCH-32`). The title is left
  out, because it is untrusted free text. Narrowing the agent's environment is the prefix tool's, from
  its own spec: the markers that spec keeps are the adapter's markers, the item context and the
  markers the session tool injects, never the variables the session tool blanks so that a session
  does not believe it is a child session.
- **`INV-CCA-6`** <!-- uuid: 465c3f3d-1403-4f3d-9dc6-0636744ac061 --> — **A failure before the session is ready is a pre-accept decline, with no bead written.**
  When the launch fails or times out, the session's payload fails in its pane, or the agent dies or
  stalls before it is ready, the adapter **MUST** reply with the pre-accept busy decline (exit `9`,
  `DEC-WIRE-1`), reason `launch-failed`, kind `fault`, with the redacted evidence as `detail`, and the
  failure signature `launch-failed` (`INV-CCH-9`). It purges the abandoned session and reclaims its
  worktree, mutates no bead, and gives no decline when its own context is cancelled. A non-low-disk
  isolation failure is not a decline: it is an accepted failure with no label. The `pool-launch-fail`
  stamp and the second-failure `human` escalation are retired, and so is the removal of the stamp
  after a good launch; a stamp an earlier handler left is ignored (`INV-CCH-34`). The adapter maps
  its own reasons to kinds:

  | kind      | reasons                                                              |
  | --------- | -------------------------------------------------------------------- |
  | `busy`    | `at-capacity` (a usage-limit refusal maps here), `session-in-flight` |
  | `fault`   | `capacity-unknown`, `pool-unreadable`, `low-disk`, `launch-failed`   |
  | `neutral` | `origin-unavailable` and every `skipped-*` reason                    |

  The core counts a role's progress by kind alone and never reads a reason (`INV-OBS-3`,
  `INV-CCH-37`).

- **`INV-CCA-7`** <!-- uuid: a29ee16a-e9c9-4671-8378-781e5565f49a --> — **One launch path: a send never resumes a session, so every send first checks it is live.**
  The adapter starts or resumes a session only through the ensure that passes the whole environment
  (`INV-CCA-5`). Every send goes through the session tool's reply, which resumes a cold session with
  an EMPTY environment, so the watchdog's reminder, its wrap-up and the nudge right after ensure
  **MUST** each check first that the session is live, and treat "not live" as ended, by the
  dead-session path the adapter already has (`INV-CCH-38`). The race between the check and the send
  is accepted, and a resume into a session that has just died fails at once.
- **`INV-CCA-8`** <!-- uuid: 0a8a22e4-414a-4d9a-97b4-d694914aab5b --> — **The adapter provides `validate`, which checks the role's configuration and runs nothing.**
  It is the optional generic subcommand of `DEC-WIRE-1`, called by the build, by the core at startup
  and by `doctor` (`INV-ROLE-5`). It checks the role's configuration strictly (an unknown or
  mistyped field is a problem naming the field), the `sessionEnv` keys against the load errors of
  `INV-CCA-4`, and that each referenced pool directory exists, and it warns on a secret-looking
  `sessionEnv` key. It never runs `pg-wrap` or any other prefix tool, whose own check is the tool's,
  and it verifies nothing about pg-router (`INV-CCH-36`).
- **`INV-CCA-10`** <!-- uuid: ee3073fd-9313-40d7-8a64-6a5868008f5d --> — **The adapter records what each dispatch ran and why each one declined.**
  For every accepted dispatch it **MUST** write a `dispatch_start` event-log record carrying the
  `ccpool new` route and the `sessionEnv` entries, values included (non-secret by `INV-CCA-4`); the
  agent's own environment is the prefix tool's, which an operator reaches by following the
  `PG_WRAP_SPEC` entry to `pg-wrap show`. For every decline it **MUST** write a `dispatch_decline`
  record carrying the role, the item id, the reason, the kind, the `sessionEnv`, the redacted evidence
  tail and the failure signature (`INV-CCH-35`).

## Across a cutover

- **`INV-CCA-9`** <!-- uuid: d06fdd0f-6c53-421a-b60f-adf6da7839fd --> — **A session the earlier handler started is adopted by this adapter
  across a cutover, and the contract the alerts read is preserved.** A role moves from the earlier
  combined handler to this adapter in one deployment commit. A session the earlier handler launched
  and left running **MUST** be adopted by this adapter's redelivered dispatch (its stable name, its
  external identifier format and its dispatch metadata are unchanged), so no session is launched beside
  a running one and none is orphaned. The rules the earlier handler's set states for this are
  `INV-CCH-22`, `INV-CCH-28` and `INV-CCH-30`.
  - **The alert contract.** The failure-reason strings the failure-rate alert reads (`usage-limit`,
    `triager-failure`, `at-capacity`, `session-in-flight` and every `skipped-*` reason) and the coarse
    busy exit code `9` **MUST** be preserved exactly, because they are a handler-to-alert contract.
