# Role — pg-router decision docs

Realization decisions about **how a role is declared**: which participant runs it, what the core
carries for it without reading it, and which of the core's two dispatch paths it takes while a
migration is in progress. The behavior side — that a role names its own handler, that the core never
reads a role's own configuration, and that survival of a shutdown is a generic role property — is
`INV-ROLE-1` to `INV-ROLE-4` in pg-router's [behavior docs](../behavior/invariants.md).

### `DEC-ROLE-1` — a role names its own handler; the core carries `wrap`, an opaque `config` and `survives_shutdown`, and chooses the dispatch path per role <!-- uuid: ab21b9c1-ca8d-4f13-a791-ea7c5a8b203b -->

**Decided** (bead `pg2-12gr4.5`, recording the handler-split decisions the operator made in the
`pg2-12gr4.1` design review of 2026-10-20: rulings 23 and 24; no separate approval, ruling 26).

**The keys.** A `[[role]]` entry gains four keys beside `name`, `enabled`, `binds`,
`non_blocking_gates` and `max_dispatch_retries`:

| Key                 | Type                     | Meaning                                                                                                                                                                                           |
| ------------------- | ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `handler`           | absolute path            | the adapter command the core runs for this role, in place of any shared command                                                                                                                   |
| `wrap`              | ordered list             | the command prefixes that run the role's payload, outermost first; the entry forms, the base environment and the wrapper contract are the wrap decision's (bead `pg2-12gr4.2`), not repeated here |
| `[role.config]`     | table                    | the role's own configuration, **opaque to the core**: carried, never read                                                                                                                         |
| `survives_shutdown` | boolean, default `false` | the role's dispatch is not cancelled when the core stops (`INV-ROLE-3`)                                                                                                                           |

A `[[query]]` entry gains `wrap` only. The core's TOML decode ignores unknown keys today. It
**MUST decode these new keys strictly**: a value of the wrong type, or one the key does not allow (a
`handler` that is not an absolute path, a `[role.config]` that is not a table), fails the
configuration load as a pre-flight failure (exit `3`, `DEC-WIRE-1`). Whether the file's other keys
become strict is not decided here.

**The role object (S1).** For a role that sets `handler`, every core→handler request carries
`role: { name, config }` on stdin, so the role's own configuration travels as the request payload
does and no per-role file or argument is needed. The wire shape, its validation and the rejected
alternatives are `DEC-WIRE-3`'s amendment.

**`survives_shutdown` (S3).** The core reads the key directly from its own configuration while it
registers roles during boot, because that is where it chooses between a cancellable and a
non-cancellable listener context for the role. It replaces the per-role JSON `survivesShutdown` flag
that the core used to read from a file the deployment rendered. The deployment's helper for a
session-backed adapter sets the key to `true`, and the deployment's module **MUST assert, at
evaluation time, that every role using that adapter has set it**, so a role cannot silently lose
survival. **Rejected:** the adapter declaring survival in its `postStartup` reply, because
`postStartup` runs only after boot, so the choice would have to move after listener registration (a
reordering of boot); and deciding from the handler's name, which the core must not know
(`GOAL-MIN-1`).

**The path per role (S5).** The core chooses between two dispatch paths **per role**, by whether the
role sets `handler`:

- **`handler` set** — the per-role path: run `<handler> <subcommand>`, send the role object on stdin,
  read `survives_shutdown` from the role's own TOML.
- **`handler` absent** — the earlier path: run the shared handler command (`PG_ROUTER_HANDLER_COMMAND`)
  with `--role-config <dir>/<role>.json` (`PG_ROUTER_HANDLER_COMMAND_DIR`) and read survival from the
  JSON file's `survivesShutdown`, exactly as before.

The compat window **ends on an event, not on a date: when no role is left on the earlier path.** One
cleanup then removes the fallback, the shared command and directory settings and the JSON file. The
order in which role families move, and each move's rollback, are in `ADR 0065`'s Addendum (handler
split); a move is one change in the deployment, and reverting it moves the family back.

**How the operator surfaces resolve a role's adapter.** `config --show`, `config --print-defaults`
and `run-role` reach a role's handler through the one per-role resolution that the daemon uses
(today's `handlerCommandFor`), and change with it:

- `config --show` (text and `--json`) lists, for each role, which path it is on and the command it
  resolves to (its `handler`, or the shared command on the earlier path), its `wrap`, its
  `survives_shutdown`, and the `config` table exactly as it would be sent on stdin; it also lists
  each query's `wrap`. It prints each role's and each query's composed command.
- `config --print-defaults` prints the schema with the four keys documented and an example role that
  names a `handler`, and prints no per-kind configuration block.
- `run-role` resolves the role through the same function as the daemon, so the smoke test exercises
  the path the daemon would take: for a role that sets `handler` it sends the role object on stdin,
  and it brackets the single dispatch with `preShutdown` as it does today.
