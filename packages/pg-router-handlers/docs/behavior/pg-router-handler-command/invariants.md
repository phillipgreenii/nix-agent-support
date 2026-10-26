# Invariants — pg-router-handler-command

The rules this adapter holds. They bound what the adapter does with one dispatched event; they do
not govern what a role's command does. IDs are topical and stable; each rule uses RFC 2119 language.
See the [glossary](glossary.md), [interfaces](interfaces.md), [actors](actors.md) and
[journeys](journeys.md).

## The adapter and its role

- **`INV-CMA-1`** <!-- uuid: ef64e56d-1191-4ad5-9057-ebddbfdb281e --> — **The adapter runs every role that names it, and nothing else.** It is a
  participant of its own, invoked by the core for each dispatch and lifecycle hook of a role whose
  `handler` it is (`INV-ROLE-1`, `DEC-ROLE-1`), and it holds no state between calls, so a restart of the core or of
  the adapter loses nothing. A role whose work is a session, rather than a command, names the other
  adapter instead.
- **`INV-CMA-2`** <!-- uuid: 97a60c63-5ccd-4307-a8c7-44f7636405bc --> — **The adapter reads its configuration from the role object, strictly.**
  It reads the role's `argv` from the configuration table that arrives with every request
  (`INV-ROLE-2`), and it **MUST** fail the call, naming the field, when the table has a field it does
  not know or a field of the wrong type. It **MUST** also provide `validate`, the optional generic
  subcommand of `DEC-WIRE-1` (`INV-ROLE-5`), which applies the same decoding to a rendered table
  without running a payload, so a deployment can fail its build on a mistake before it is applied.
  `validate` never runs a prefix tool the argv starts with, and verifies nothing about pg-router.

## One dispatch

- **`INV-CMA-3`** <!-- uuid: dfb4ba05-d86c-48ff-8494-1f0397734515 --> — **A dispatch runs the role's argv once, and succeeds exactly when the
  command exits zero.** Each element of the argv is rendered, one at a time, through the template
  engine over the event's item fields, so item data can fill an element but can never be parsed as
  more than that one element. The rendered argv is run once: the adapter never retries it, which is
  the core's decision (`INV-FAIL-1`). An argv that cannot be rendered is a failure of the dispatch
  before anything runs.
- **`INV-CMA-4`** <!-- uuid: 16f70908-919c-4b38-a2c9-bbdd46786f45 --> — **A busy command is a pre-accept decline, and a failed one carries its
  evidence.** A command that exits with the busy code
  makes the adapter decline the dispatch as busy (kind `busy`), which the core re-offers
  (`INV-CONC-1`, `INV-FAIL-1`); that decline carries no stderr tail. The reading is unconditional:
  the adapter cannot tell which layer of the argv exited with the code, so a prefix tool that exits
  `9` for its own failure reads as busy too (`INV-CMA-9`). Any other non-zero exit fails the dispatch, and
  the error **MUST** end with the command's **stderr tail**: its last 2 KiB at most, redacted before
  it is cut, collapsed to one line with the lines joined by `|`, and prefixed `...` when cut. A
  successful run's standard error is never read or logged.
- **`INV-CMA-5`** <!-- uuid: 9c2e6ac2-9ec2-4a95-97bd-c22c1f74ca40 --> — **A command role does not survive the core's shutdown.** Its dispatch is
  short, and cancelling a stuck one is the point of the cancel, so a role of this adapter leaves
  `survives_shutdown` unset (`INV-ROLE-3`) and the core cancels it after its bounded drain.
- **`INV-CMA-6`** <!-- uuid: d7df20d6-96e6-40c7-a921-4755bddcf9df --> — **The payload and everything it starts are one process group, and cancelling
  ends the whole group.** The adapter starts the payload in a process group of its own and cancels by
  sending SIGTERM to the group, escalating to SIGKILL after a grace period, because a prefix tool
  that does not exec would otherwise keep a payload alive after its parent is killed
  (`INV-ENV-4`).
- **`INV-CMA-8`** <!-- uuid: 93f99015-5677-4fa3-b6a1-fd8079fb858f --> — **A payload starts from the base environment plus the item context, and nothing else of an item.**
  The adapter **MUST** set `PG_ROUTER_ROLE`, `PG_ROUTER_ITEM_ID` and `PG_ROUTER_ITEM_TYPE` for every
  payload, from the dispatched item's id and type. The title is left out, because it is untrusted free
  text, and a role that needs it templates it into an element of its argv, which is never
  shell-parsed. The base environment of `INV-ENV-2` is, for this adapter, the system variables
  (`HOME`, `USER`, `LOGNAME`, `TMPDIR`, `LANG`, and `PATH` fixed to `/usr/bin:/bin:/usr/sbin:/sbin`),
  the terminal variables (`TERM`, and `COLORTERM` where set), the markers the adapter's participants
  inject by explicit name, never by prefix (`BEADS_ACTOR`, `BEADS_DIR`, `WORKSPACE_ROOT` and
  `PG_ROUTER_TEST_MODE`, the last set by the core's smoke-test command), and the item context. The
  list is revisable by decision. A role's `strictBaseEnv` (a boolean, **off by default**, the
  strict-env switch of `INV-ENV-2`) selects it: while off, the payload keeps the ambient environment
  it has, with the item context added. A payload that needs more names it in its own argv, or in the
  spec of the prefix tool its argv starts with. The core's `run-role` smoke command runs under the
  operator's shell, not the daemon's, so while the switch is off it is not representative of the
  payload's environment.
- **`INV-CMA-9`** <!-- uuid: 5e4ce5f8-e5fb-485c-a4b7-983334acccce --> — **A failing command, a failing prefix tool included, is an accepted dispatch that ends in a handler error.**
  The adapter never declines a failed command: the dispatch is accepted, ends with a handler error
  (exit `1`), and is re-offered only if the role opted into dispatch retries with a transient class.
  It counts as unsuccessful in the role's progress (`INV-OBS-3`). The outcome carries the redacted
  stderr tail (`INV-CMA-4`), where a `pg-wrap[<stage>]:` line, when the argv starts with `pg-wrap`,
  names the failing layer. The adapter neither knows nor checks a prefix tool: the argv's first
  element is part of the command (`INV-ROLE-4`).
- **`INV-CMA-10`** <!-- uuid: 56b4b7b4-89ba-4916-9195-d9f9484bdbfb --> — **Each accepted dispatch records what it ran and how it ended.**
  The adapter **MUST** write one `dispatch_start` event-log record per accepted dispatch carrying the
  role, the item's id and type, the rendered argv, whether `strictBaseEnv` is on, and the **names** of
  the base-environment variables the payload received, never their values and never the item-context
  values. A decline writes a `dispatch_decline` record with the role, the item id, the reason, the
  kind and the evidence it has. The `dispatch_start` fields are `role`, `item_id`, `item_type`,
  `argv`, `strict_base_env` and `base_env_names`; it is written BEFORE the payload starts, so a
  payload that hangs, is killed or cannot start still has one. Exit `9` is a decline that is only
  known after the command ran, so it leaves a `dispatch_start` and then a `dispatch_decline`. The
  `dispatch_decline` fields are `role`, `item_id`, `reason`, `decline_kind`,
  `session_env` (an empty object for this adapter), `evidence` and `failure_signature` (`INV-CCH-35`
  lists them in full). Every other accepted dispatch **MUST** write exactly one outcome record after
  its `dispatch_start`, success included: a `dispatch_end` record (`role`, `item_id`, `item_type`
  and an `outcome` of `completed` for exit `0`, or `interrupted` when the dispatch's context ended
  while the payload ran) or, when the payload exits non-zero or cannot start, the `dispatch_result`
  failure record (`role`, `pool` and `session` empty, `bead`, `failure_signature`,
  `signature_evidence`; `INV-CCH-9`), never both.
