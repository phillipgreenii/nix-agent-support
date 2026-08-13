# CLI — pr-pool decision docs

Realization decisions about pr-pool's **operator-facing command surface**: the option and subcommand
spellings, their arguments, and their per-command mechanics. The behavior side — that `INTF-CLI` is a
**driving port** whose counterparty is an actor rather than an implementer, which affordances it
offers the operator, and what must hold when one is invoked — is in pr-pool's
[behavior docs](../behavior/interfaces.md). The names below are the operator's vocabulary and the
behavior docs use them, but which flags and subcommands exist, and what each takes, is recorded here.

### `DEC-CLI-1` — the global option surface <!-- uuid: 162d5964-d3f0-4cd8-b4c3-1159fb1a0a26 -->

**Decided.** Every operator subcommand accepts these:

| Option                 | Effect                                                                   |
| ---------------------- | ------------------------------------------------------------------------ |
| `--json`               | emit JSON instead of text (any operator subcommand)                      |
| `--only <selector>`    | allow-list: restrict the **active** set of sources/handlers for this run |
| `--disable <selector>` | deny-list: exclude sources/handlers for this run                         |
| `--version`, `-v`      | print the version and exit                                               |
| `--help`, `-h`         | print help and exit                                                      |

`--only` / `--disable` (and their environment-variable equivalents) are the concrete spelling of the
**run-scoped selectors** the behavior set states: they restrict which sources and handlers a run
activates, and which a smoke test may reach, **without editing config** (`STORY-OP-3`). `--json` is
the concrete spelling of the machine-readable output form every subcommand offers.

**Why the selectors are flags rather than config.** The behavior they serve is "isolate or pause part
of the system for one run", and a value written into config is not scoped to one run — it has to be
put back. A flag expires when the process does, which is the scoping the story asks for. The
environment-variable equivalents exist so a supervisor can set them without rewriting a command line.

**Not decided here.** The selector's own grammar (whether it names participants, globs them, or takes
a list) is the implementation's, and the configuration schema it selects over is still an open
question in the behavior set (`OQ-CONFIG`).

### `DEC-CLI-2` — the operator subcommand surface <!-- uuid: 3c480e5c-a705-47a7-906f-3d59ff983117 -->

**Decided.** The operator subcommands, their arguments, and their mechanics:

| Subcommand                | Arguments                      | Behavior                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| ------------------------- | ------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `run`                     | —                              | Start the core as a long-running **daemon** (socket service); route events as sources emit them until stopped.                                                                                                                                                                                                                                                                                                                                                         |
| `run-until-idle`          | —                              | Start the socket service and dispatch from the durable queue; **exit once the queue is drained and no offer is outstanding** (every enqueued event accepted or expired, and no handler holding an offer, `INV-LIFE-1`). The default when no subcommand is given.                                                                                                                                                                                                       |
| `run-role <role> <event>` | role, event                    | **Smoke test**: dispatch **one named event** through **one handler** (its CLI-facing name is its _role_), then tear down. Runs **no discovery** — the event is explicit. Sets a **test-mode** signal (env) so the handler knows a test is in flight.                                                                                                                                                                                                                   |
| `run-query <query>`       | query                          | **Smoke test**: run **one pull source's query** once, **read-only**, and print the events it would emit. Also sets the test-mode signal.                                                                                                                                                                                                                                                                                                                               |
| `push-inject <json>`      | event JSON                     | Inject an **arbitrary operator-supplied event** into the **live** core — the same core-side enqueue as the `ingest-event` manager callback, but **operator-initiated**, locating/authenticating the core like the other operator subcommands. Durable via the queue, delivered at-least-once and deduped (`INV-EVT-*`). **Distinct** from `ingest-event` (a manager→core callback) and `run-role` (a smoke test that tears down). Primarily for manual/test injection. |
| `status`                  | —                              | Resolved-config summary **plus** live **deliveries** and per-`type` **queue depths**.                                                                                                                                                                                                                                                                                                                                                                                  |
| `config`                  | `--show` \| `--print-defaults` | `--show` prints the **resolved** configuration; `--print-defaults` prints the built-in defaults as a copy-paste starting point.                                                                                                                                                                                                                                                                                                                                        |

The same binary also carries the manager→core callback subcommand `ingest-event`, which belongs to
`INTF-SOURCE`'s manager-initiated direction and is invoked through the callback the core hands out
(`DEC-WIRE-2`), never by the operator. It follows the common transport contract (`DEC-WIRE-1`).

**Why the two run modes are separate subcommands rather than a flag.** They differ in their exit
predicate, not in their configuration: one never exits on its own, the other exits when the queue is
drained and no offer is outstanding. A flag on one command would make the predicate a property of the
invocation rather than of the mode, and the default — running until idle — could then be reached only
by remembering to pass it.

**Not decided here.** Which affordances the operator boundary offers at all — run the core, smoke-test
one handler or one source, inject an event, inspect live state, resolve config — is behavior and stays
in `INTF-CLI`, together with the requirement that every subcommand emit human-readable text by default
and a machine-readable form on request.
