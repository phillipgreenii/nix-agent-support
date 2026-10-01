# CLI — pg-router decision docs

Realization decisions about pg-router's **operator-facing command surface**: the option and subcommand
spellings, their arguments, and their per-command mechanics. The behavior side — that `INTF-CLI` is a
**driving port** whose counterparty is an actor rather than an implementer, which affordances it
offers the operator, and what must hold when one is invoked — is in pg-router's
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

**Selector grammar (realized, bead `pg2-z3qh3`).** A selector is `<kind>:<name>`, where `<kind>` is
`role` or `query` and `<name>` is that participant's own configured name — `roles.Role.Name` (a
configured `[[role]]`, the operator-facing **handler**) or `query.Source.Name` (a configured
`[[query]]`, the operator-facing **source**). `role:`/`query:` are the implementation's own nouns
rather than the behavior set's narrative "handler"/"source" terms, chosen because `run-role`/
`run-query` already spell the same two participant kinds that way on this CLI — a selector reuses
vocabulary an operator already has, rather than introducing a second pair of names for the same two
things. Both `--only` and `--disable` are **repeatable**: each occurrence adds one selector, so
`--only role:foo --only query:bar` builds a two-element allow-list. A selector naming a role/query the
resolved configuration does not declare is a **usage error** (the run exits without starting) rather
than a silent no-op — an operator who mistypes a name is told immediately, instead of getting an
allow-list that quietly excludes everything.

**Environment-variable equivalents (realized).** `PG_ROUTER_ONLY` and `PG_ROUTER_DISABLE` each hold a
comma-separated list of selectors, in the same `<kind>:<name>` grammar (e.g.
`PG_ROUTER_DISABLE=role:worker,query:feedback-ready`). They are **combined with**, not overridden by,
any `--only`/`--disable` flags on the same invocation: unlike `--socket`/`--token` (a single scalar
identifying one target, where the flag wins over the environment), these are repeatable, cumulative
lists, so the effective allow-list/deny-list is the **union** of whatever the flags and the
environment each name. This differs from the `--socket`/`--token` precedent deliberately, not by
oversight.

**Combination semantics (realized).** When both an allow-list and a deny-list are in effect for one
run: `--only` (its flag occurrences unioned with `PG_ROUTER_ONLY`), if non-empty, first narrows the
candidate set to just the named participants of that kind — an **empty** `--only` leaves every
configured participant of that kind a candidate. `--disable` (unioned with `PG_ROUTER_DISABLE`) is then
applied to whatever `--only` left, removing any participant it names. A participant excluded either
way is the `INV-DISP-3` "declared but inactive this run" case, never a config error.

**Realized scope.** `--only`/`--disable` **flags** are wired onto `run` and `run-until-idle`
only — the two subcommands with a "run" spanning multiple sources+handlers to restrict.
`run-role`/`run-query` already select one participant explicitly by argument, so a repeatable
flag naming many participants has nothing to add there, and `push-inject`/etc. are not
"runs" in the `STORY-OP-3` sense, so none of them takes these flags. **`run-role`/`run-query` do
however respect the same restriction (Task 1.5c)**, reading only its environment-variable form
(`PG_ROUTER_ONLY`/`PG_ROUTER_DISABLE`): a role/source the operator has excluded stays unreachable by
the matching smoke command too, realizing `interfaces.md`'s "Run-scoped selectors" statement that
the restriction scopes "which participants that run activates **and which a smoke test may
reach**". Because a smoke command has no `--only`/`--disable` flags of its own to union the
environment into, this makes the environment form's per-invocation scope matter more, not
less — see the warning below.

**The environment form is per-invocation and MUST NOT be exported persistently.** `PG_ROUTER_ONLY`/
`PG_ROUTER_DISABLE` set in a shell profile (rather than for one command) silently narrow or exclude
participants on **every** subsequent `run`/`run-until-idle`/`run-role`/`run-query` invocation in
that shell, not just the one they were meant for — contradicting `STORY-OP-3`'s "without editing
the configuration" framing, which presumes the restriction expires with the invocation. `helpText`
(`cmd/pg-router/args.go`) carries this warning for the operator.

**`--json`'s realized scope (Task 1.5b).** Wired so far: `push-inject` (an earlier task), and
`config --show`, `run-query`, and `run-role`. Not yet wired: `run`/`run-until-idle`
(no task has added it yet), and `pause`/`resume`/`status`/`tui` (the latter two are not
yet built). `config --print-defaults` deliberately does **not** take `--json` — its output is the
built-in `config.toml` as **text**, and no JSON encoding is defined for it.

**`--json`'s versioning (Task 0.4).** A subcommand's `--json` output is **unversioned** by
default — it carries no `schemaVersion` field and is not one of the `schemas/`-registered,
conformance-gated wire messages `DEC-WIRE-1` governs (`INV-INTF-2`) — **unless this document says
otherwise** for that subcommand. It says otherwise for none of them: `config --show`'s,
`run-query`'s, and `run-role`'s `--json` reports are plain operator-facing CLI reports that MAY
change shape freely, same as `push-inject`'s. (`push-inject`'s own report happens to carry a
`schemaVersion` field, echoing the wire messages' convention for readability, but that field is not
itself a conformance gate — no `schemas/*.schema.json` entry backs it, and its shape is free to
evolve like every other subcommand's `--json` report. The three reports landed by Task 1.5b
deliberately omit that field rather than propagate the same accidental echo.)

**Not decided here.** The configuration schema the selectors select over is still an open question in
the behavior set (`OQ-CONFIG`). Whether a selector may ever glob or pattern-match a name, rather than
naming exactly one, is likewise left for a future decision.

### `DEC-CLI-2` — the operator subcommand surface <!-- uuid: 3c480e5c-a705-47a7-906f-3d59ff983117 -->

**Decided.** The operator subcommands, their arguments, and their mechanics:

| Subcommand                   | Arguments                                            | Behavior                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| ---------------------------- | ---------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `run`                        | —                                                    | Start the core as a long-running **daemon** (socket service); route events as sources emit them until stopped.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `run-until-idle`             | —                                                    | Start the socket service and dispatch from the durable queue; **exit once the queue is drained and no offer is outstanding** (every enqueued event accepted or expired, and no handler holding an offer, `INV-LIFE-1`). A subcommand is always **required** (bead `pg2-f3mcb.2`): bare `pg-router` is a usage error, and this mode is no longer a bare-invocation default.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `run-role <role> <json>`     | role, event JSON                                     | **Smoke test**: dispatch **one caller-supplied event** through **one handler** (its CLI-facing name is its _role_), then dispatch that handler's `preShutdown` hook. Runs **no discovery** — the event is explicit, supplied as a full JSON blob (the SAME `<json>` convention `push-inject <json>` already uses), never resolved from a bead id (pg2-oju6w.15 removed that resolution — pg-router no longer holds a beads client of its own to resolve one through; an accepted, intentional loss of the old "just type a bead id" convenience, with no replacement convenience tool). Sets the **test-mode** signal (env `PG_ROUTER_TEST_MODE=1`, Task 1.5c) so the handler knows a test is in flight — advisory only (`interfaces.md`'s "Test-mode signal"). Respects `--only`/`--disable`'s environment form (Task 1.5c): an excluded role is refused, not smoke-tested. `--json` (Task 1.5b) emits a small JSON report of the outcome instead of nothing on success. |
| `run-query query:<name>`     | the target `[[query]]`'s configured `name`           | **Smoke test**: run **one named pull source's** query once, **read-only**, and print the events it would emit. Sets `PG_ROUTER_TEST_MODE=1` (Task 1.5c) and respects `--only`/`--disable`'s environment form the same way `run-role` does. A token with no `query:` prefix (including the pre-Task-1.5c bare-role form) is an ordinary usage error — no live consumer to carry a mapping diagnostic for (operator ruling, 2026-09-02). `--json` (Task 1.5b) emits the matches as one JSON object instead of tab-separated lines.                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| `push-inject <json>`         | event JSON                                           | Inject an **arbitrary operator-supplied event** into the **live** core — the same core-side enqueue as the `ingest-event` manager callback, but **operator-initiated**, locating/authenticating the core like the other operator subcommands. Durable via the queue, delivered at-least-once and deduped (`INV-EVT-*`). **Distinct** from `ingest-event` (a manager→core callback) and `run-role` (a smoke test that tears down). Primarily for manual/test injection.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `pause`                      | `--description`, `--owner` (optional)                | Set the `SYSTEM_PAUSE` **gate** (`INV-LIFE-2`) on the **running** core, recording the operator (or `--owner`) as its owner. A socket client like `status`: **a running core is required** — with none it fails `no running core` (exit `1`) and never starts one (`DEC-WIRE-2`). Sugar for `gate set SYSTEM_PAUSE`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| `resume [--all]`             | `--all` (optional), `--by`                           | Clear `SYSTEM_PAUSE`, or — with `--all` — **every** active gate; a bare `resume` clears only `SYSTEM_PAUSE`. Same socket-client mechanics as `pause`. Sugar for `gate clear SYSTEM_PAUSE`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| `gate set <TYPE>`            | TYPE (ALL CAPS), `--description`, `--owner`, `--ttl` | Set (or renew, or overwrite) the gate of one TYPE; last writer wins. `--ttl` is a lease the owner renews by setting the gate again. Owner is **debug only**.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| `gate clear <TYPE> \| --all` | TYPE, or `--all`; `--by`                             | Clear one gate or every gate. **Any** caller may clear any gate; clearing a gate that is not set is a no-op success.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `gate list [--json]`         | —                                                    | List the active gates (TYPE, owner, set-at, TTL remaining, description), read from `status`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| `status`                     | —                                                    | **Read-only.** Resolved-config summary **plus** live **deliveries** and per-`type` **queue depths** — never a state-mutating call. Long-poll/tailing clients (the `tui` subcommand below) read deliveries incrementally as the **activity ring** (`interfaces.md`'s "Inspecting a running core") instead of re-reading the whole snapshot each time.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `config`                     | `--show` \| `--print-defaults`                       | `--show` prints the **resolved** configuration; `--print-defaults` prints the built-in defaults as a copy-paste starting point. `--show`'s output is also available as one JSON object with `--json` (Task 1.5b); `--print-defaults` does not take `--json`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| `tui`                        | —                                                    | **Continuous-interactive** view: polls `status`'s activity ring and offers `pause`/`resume` (and resume-all in the Gates modal) from the same screen — the realization of exactly those affordances (`interfaces.md`, "`tui` is not a sixth affordance"), never a new one.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |

The same binary also carries the manager→core callback subcommands `ingest-event` and `self-status`:
`ingest-event` belongs to `INTF-SOURCE`'s manager-initiated direction, while `self-status` is common
to every participant kind (the common manager contract's "Self-status," `INTF-CLI`). Both are invoked
through the callback the core hands out (`DEC-WIRE-2`), never by the operator, and both follow the
common transport contract (`DEC-WIRE-1`).

**The socket verbs.** The gate commands above are ordinary socket clients (`DEC-WIRE-1`, reached
exactly like any other socket verb, `DEC-WIRE-2`); an external system calls the same verbs directly:

| Verb         | Request                                                 | Reply                                                       |
| ------------ | ------------------------------------------------------- | ----------------------------------------------------------- |
| `gate-set`   | `{ schemaVersion, type, description?, owner?, ttlMs? }` | `{ schemaVersion, type, set, setAt, expiresAt?, renewal? }` |
| `gate-clear` | `{ schemaVersion, type? \| all?, by? }`                 | `{ schemaVersion, cleared: [type] }`                        |
| `pause`      | `{ schemaVersion, owner?, description? }`               | `{ schemaVersion, gate, set, setAt, renewal? }`             |
| `resume`     | `{ schemaVersion, all?, by? }`                          | `{ schemaVersion, cleared: [type] }`                        |

`type` is an ALL-CAPS gate TYPE (`^[A-Z][A-Z0-9_]{0,63}$`). `gate-clear` takes exactly one of `type`
or `all`. `pause`/`resume` are `gate-set`/`gate-clear` of `SYSTEM_PAUSE` with the default
description/owner filled in; a bare `resume` clears only `SYSTEM_PAUSE`. Clearing a TYPE with no
active gate is a no-op success (`cleared: []`). Registration carries an optional
`nonBlockingGates: [type]` listing the gate TYPEs a participant does not block on. The `status`
reply's `gates[]` lists **active** gates only, each `{ type, description?, owner?, setAt,
expiresAt?, ttlRemainingMs? }`.

**Not decided here.** Which affordances the operator boundary offers at all — run the core, pause and
resume it as a gate, smoke-test one handler or one source, inject an event, inspect live state,
resolve config — is behavior and stays
in `INTF-CLI`, together with the requirement that every subcommand emit human-readable text by default
and a machine-readable form on request.
