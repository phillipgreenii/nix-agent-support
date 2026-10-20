# Invariants — pg-router-handler-command

The rules this adapter holds. They bound what the adapter does with one dispatched event; they do
not govern what a role's command does. IDs are topical and stable; each rule uses RFC 2119 language.
See the [glossary](glossary.md), [interfaces](interfaces.md), [actors](actors.md) and
[journeys](journeys.md).

## The adapter and its role

- **`INV-CMA-1`** <!-- uuid: ef64e56d-1191-4ad5-9057-ebddbfdb281e --> — **The adapter runs every role that names it, and nothing else.** It is a
  participant of its own, invoked by the core for each dispatch and lifecycle hook of a role whose
  `handler` it is (`INV-ROLE-1`), and it holds no state between calls, so a restart of the core or of
  the adapter loses nothing. A role whose work is a session, rather than a command, names the other
  adapter instead.
- **`INV-CMA-2`** <!-- uuid: 97a60c63-5ccd-4307-a8c7-44f7636405bc --> — **The adapter reads its configuration from the role object, strictly.**
  It reads the role's `argv` from the configuration table that arrives with every request
  (`INV-ROLE-2`), and it **MUST** fail the call, naming the field, when the table has a field it does
  not know or a field of the wrong type. It **MUST** also provide a check that applies the same
  decoding to a rendered table without running a payload, so a deployment can fail its build on a
  mistake before it is applied.

## One dispatch

- **`INV-CMA-3`** <!-- uuid: dfb4ba05-d86c-48ff-8494-1f0397734515 --> — **A dispatch runs the role's argv once, and succeeds exactly when the
  command exits zero.** Each element of the argv is rendered, one at a time, through the template
  engine over the event's item fields, so item data can fill an element but can never be parsed as
  more than that one element. The rendered argv is run once: the adapter never retries it, which is
  the core's decision (`INV-FAIL-1`). An argv that cannot be rendered is a failure of the dispatch
  before anything runs.
- **`INV-CMA-4`** <!-- uuid: 16f70908-919c-4b38-a2c9-bbdd46786f45 --> — **A busy command is a pre-accept decline, and a failed one carries its
  evidence.** A command that exits with the busy code, after the ready marker exists (`INV-CMA-7`),
  makes the adapter decline the dispatch as busy, which the core re-offers (`INV-CONC-1`,
  `INV-FAIL-1`); that decline carries no stderr tail. Any other non-zero exit fails the dispatch, and
  the error **MUST** end with the command's **stderr tail**: its last 2 KiB at most, redacted before
  it is cut, collapsed to one line with the lines joined by `|`, and prefixed `...` when cut. A
  successful run's standard error is never read or logged.
- **`INV-CMA-5`** <!-- uuid: 9c2e6ac2-9ec2-4a95-97bd-c22c1f74ca40 --> — **A command role does not survive the core's shutdown.** Its dispatch is
  short, and cancelling a stuck one is the point of the cancel, so a role of this adapter leaves
  `survives_shutdown` unset (`INV-ROLE-3`) and the core cancels it after its bounded drain.
- **`INV-CMA-6`** <!-- uuid: d7df20d6-96e6-40c7-a921-4755bddcf9df --> — **The payload and its wrap chain are one process group, and cancelling
  ends the whole group.** The adapter starts the chain in a process group of its own and cancels by
  sending SIGTERM to the group, escalating to SIGKILL after a grace period, because a wrapper that
  does not exec would otherwise keep a payload alive after its parent is killed.
- **`INV-CMA-7`** <!-- uuid: 6b591bff-4a16-47cd-a377-1e6b5db11acc --> — **The adapter owns the ready marker, and a failure without it is not the
  payload's.** The adapter **MUST** add the marker stage as the innermost layer of every chain,
  including a role with an empty wrap, and set the variable that names the marker file for the
  dispatch. A non-zero exit **without** the marker means the environment failed before the payload
  started, and **MUST NOT** be read as the payload's own outcome, in particular never as a busy exit.
  A marker stage whose variable is unset **MUST** fail loudly, on standard error, before the payload
  starts. The payload's base environment and wrap are `INV-ROLE-4`'s; the variable's name and the
  failure reason this produces are the wrap decision's.
