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
  not know or a field of the wrong type. It **MUST** also provide a check that applies the same
  decoding to a rendered table without running anything, so a deployment can fail its build on a
  dropped or misspelled field before it is applied.
- **`INV-CCA-3`** <!-- uuid: 3f48ccd2-e5a3-404f-8f15-c902b20773ed --> — **Every role of this adapter survives the core's shutdown, and a role
  cannot silently lose that.** The deployment's helper for this adapter sets the role's
  `survives_shutdown` (`INV-ROLE-3`), and the deployment **MUST** assert, when it evaluates its
  configuration, that every role using this adapter has set it. A session is supervised by this
  adapter for far longer than a shutdown budget, and the next core re-offers the event and adopts the
  session (`phillipgreenii-nix-agent-support` ADR 0085).

## The environment a session starts in

- **`INV-CCA-4`** <!-- uuid: e93252bb-3c02-41bd-a835-1c195999ca37 --> — **A role's wrap reaches the agent through one launcher per pool, and the
  session tool learns no wrapper concept.** Because the session tool builds each session's
  environment itself and passes only what it is told, wrapping the adapter or the call would not reach
  the agent. The deployment therefore renders a launcher for each role as a store path and sets it as
  the agent executable of the role's pool. The launcher resets the environment to the keep-list
  (`INV-CCA-5`), runs the role's wrap one layer per entry (`INV-ROLE-4`), always runs the marker
  stage last (`INV-CCA-7`), and then starts the agent with the arguments it received, unchanged. The
  session tool starts the launcher exactly as it would start any executable
  (`packages/ccpool/docs/behavior`'s `INV-POOL-6`).
- **`INV-CCA-5`** <!-- uuid: 3cf795a1-1777-4e5f-aa6a-e3fb6e90cc86 --> — **The launcher's keep-list names its variables, and never matches a
  prefix.** The keep-list is the base list (the system variables, the terminal variables a session's
  terminal supplies, and the variables the participants inject), each by **explicit name**. The
  injected variables are `BEADS_ACTOR`, `BEADS_DIR`, `WORKSPACE_ROOT` and `PG_ROUTER_TEST_MODE` from
  the core's smoke-test command, and `CCPOOL_EXTERNAL_ID`, `PA_MONITOR_NO_NUDGE`, `CCPOOL_POOL` and
  `CCPOOL_AUTONOMOUS` from the session tool. The variables the session tool blanks so that a session
  does not believe it is a child session **MUST NOT** be re-introduced, and a prefix match (for
  example every `CCPOOL_` or `BEADS_` variable) is forbidden, because it would drop
  `PA_MONITOR_NO_NUDGE` and `WORKSPACE_ROOT` and let others through. The keep-list **MUST** be derived
  from what the session tool injects, by a test, so a new injection fails the test instead of being
  silently dropped.
- **`INV-CCA-6`** <!-- uuid: 2d015136-1b66-42ef-9c7e-c380de11c682 --> — **A launcher is static per pool, so a wrap entry that needs a dispatch's
  item data is an error here, and item data arrives as variables.** A role of this adapter MUST NOT
  use a templated (array-form) wrap entry: the deployment **MUST** reject it when it builds the
  configuration. The role's item context reaches the agent as environment variables, which this
  adapter passes with each launch and resume (`INV-CCA-7`). Isolation (a worktree and a branch for
  the dispatch) stays this adapter's job and is the role's `isolation` setting, because the adapter
  owns the worktree's creation, prefetch, cleanup, abandoned-worktree sweeps and the working
  directory the session tool records for a resume.
- **`INV-CCA-7`** <!-- uuid: 397cf038-71f6-48fe-8623-47b91a61604a --> — **Every launch and every resume re-supplies the dispatch's environment,
  and a resume uses the current launcher.** The session tool does not persist the environment it was
  given, so each ensure, each resume and each brand-new launch the adapter issues **MUST** pass the
  per-dispatch variables again: the ready-marker path and the item-context variables. The pool's
  configuration is read afresh on every call, so a resume runs the launcher the currently applied
  configuration names. A marker stage that finds its marker variable unset **MUST** fail loudly, with
  a message on standard error and a non-zero exit before the agent starts, and **MUST NOT** skip
  the marker silently. A session that dies without the marker failed in its environment, not in its
  work.
- **`INV-CCA-8`** <!-- uuid: d78d392f-b09f-44b0-8c63-049be1a90b46 --> — **Roles on a shared pool declare identical wraps, and a role that needs
  another wrap takes a pool of its own.** Roles on a **shared pool** share its launcher, so for them the
  environment is a property of the pool, which would let it drift from each role's own declaration.
  The deployment **MUST** assert, when it evaluates its configuration, that every role sharing a pool
  declares the same wrap list, and **MUST** fail the evaluation otherwise. Moving a role to its own
  pool through the per-role pool setting is a configuration change.

## Across a cutover

- **`INV-CCA-9`** <!-- uuid: d06fdd0f-6c53-421a-b60f-adf6da7839fd --> — **A session the earlier handler started is adopted by this adapter
  across a cutover, and the contract the alerts read is preserved.** A role moves from the earlier
  combined handler to this adapter in one deployment commit. A session the earlier handler launched
  and left running **MUST** be adopted by this adapter's redelivered dispatch (its stable name, its
  external identifier format and its dispatch metadata are unchanged), so no session is launched beside
  a running one and none is orphaned. The failure-reason strings the failure-rate alert reads
  (`usage-limit`, `triager-failure`, `at-capacity`, `session-in-flight` and every `skipped-*`
  reason) and the coarse busy exit code `9` **MUST** be preserved exactly, because they are a
  handler-to-alert contract (`INV-CCH-22`, `INV-CCH-28`, `INV-CCH-30`).
