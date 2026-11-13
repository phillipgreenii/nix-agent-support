# pg-review

Self-review tool. It reviews how Claude Code is used on this machine, periodically and unattended
or on demand, and turns what the evidence shows into decisions for the operator. Each decision is
a review item written for a person with no memory of the surrounding tooling. The job never
applies a change: an adopted item files one ordinary work bead and nothing else.

The full design, including the contracts and the resolved review findings (which override earlier
text), is in [docs/design.md](docs/design.md). Per-component notes live next to it in `docs/`.

## Overview

The CLI is grouped by audience:

- `pg-review human` (default output: text): `pending`, `show`, `rule`.
- `pg-review agent` (default output: JSON): `q`, `file`, `context`.
- `pg-review job` (default output: text): `run`, `maintain`, `doctor`, `status`, `show-run`,
  `resume`.

`--output text|json` overrides the default; there is no terminal detection.

Exit codes (shared table `github.com/phillipgreenii/x/exitcode`, plus the app codes in `internal/appcode`): 0 ok, 1 unexpected failure, 2 usage,
4 not found (an unknown or expired id, an unknown lens), 7 configuration rejected, 9 run lock busy, 20 skipped or already
done, 21 validation rejected (findings, a batch, a broker request), 22 ledger error or state conflict, 23 provider
failure or timeout. A command that is not implemented yet prints `not implemented` and exits

1.

Pinned contracts live in `schemas/` (JSON Schema 2020-12, every payload carries `schema_version`

1. and are validated through `internal/schema`.

## Routine

The operator's whole routine is three commands. Everything else happens without them.

```text
# 1. What is waiting for me?
$ pg-review human pending

# 2. Read one in full (context, options, recommendation, glossary)
$ pg-review human show 17

# 3. Decide. One or several at once.
$ pg-review human rule 17 adopted
$ pg-review human rule 18 deferred --reconsider-when "in 60 days or if refusals double"
$ pg-review human rule 19 deferred --note "Need to know which references are in scope" --reconsider-when "after the scope list is known"
$ pg-review human rule 3 4 5 rejected --note "known and accepted"
```

- `pending` starts with a banner (counts, oldest age, held lenses, stale lenses), then one line per
  item with its impact.
- `show` records which revision of the item you saw. `rule` needs that record: ruling an item you
  have not shown exits 22 with "show it first", and it also exits 22 if the item changed since.
  Show every id you intend to rule, or run `show 3 4 5` with several ids.
- Rulings are `adopted`, `deferred`, `rejected` and `reopen`. A missing fact is a `deferred`
  ruling whose `--note` names it and whose `--reconsider-when` is concrete. `adopted` files one
  work bead in the workspace tracker; if filing fails the ruling stands and `job maintain` retries.

Optional extras:

```text
$ pg-review human show 17 --evidence      # the raw evidence rows behind the item
$ pg-review human pending --decided       # earlier rulings, one line each
$ pg-review human rule 12 reopen          # bring an old decision back
$ pg-review job status                    # last runs, backlog, paused lenses
```

An item that stays pending for more than `items_auto_defer_days` (default 30) is deferred
automatically and returns under a 30, 60, 120 day backoff, so a long absence is visible in the
`pending` banner and costs nothing to clear.

## Running by hand

A review is a stateless job over an explicit window. No router, adapter or schedule is needed:

```text
$ pg-review job run agent-workflow --scope workspace --duration 24h        # the previous 24 hours, now
$ pg-review job run config-health  --scope workspace --duration 7d
$ pg-review job run agent-workflow --scope workspace --duration 24h --evidence-only   # gather, do not analyse
$ pg-review job run agent-workflow --scope workspace --duration 24h --analyst interactive
$ pg-review job doctor                    # is everything wired (recorder, ccpool, hook, config)
```

`--analyst interactive` prints the run id, the prompt and the staging path so a Claude session you
already have open acts as the analyst. Start that session in the neutral directory the command
prints; a session started anywhere else is not excluded from later reviews. The analyst files its
findings with `pg-review agent file --run <id> <findings.json>`; an interactive run that is not
filed within `interactive_ttl_minutes` (default 240) is marked Failed.

### Catching up a missed window

A daily schedule reviews exactly 24 hours. A night the router skipped (the router was not running,
the host was asleep, or a run lock was held) is not reviewed later by itself. Catch up by hand
with the missed window, or with one longer window that covers several nights (overlapping windows
dedupe by topic):

```text
$ pg-review job run agent-workflow --scope workspace \
    --from 2026-02-04T02:00:00-05:00 --to 2026-02-05T02:00:00-05:00
```

`job status` and the `pending` banner show a stale lens. A manual run does not clear the "lens not
running" alert, which counts scheduled runs only.

## Configuration

The tool reads only `config.toml` under `$XDG_CONFIG_HOME/pg-review/`. Nix or home-manager may
render it and a person may write it by hand; the tool works the same either way.

- The built-in lenses, profiles, evidence providers, base prompt, lens prompts and glossary
  vocabulary are embedded in the binary, so a missing `config.toml` is valid. A `config.toml` entry
  with the same id as a built-in is merged onto it key by key.
- Scopes have no built-in, because their paths are private to a deployment: define each with
  `[[scope]]` (a name, path prefixes, and the layers that load there).
- A `prompt` or `base_prompt` path is relative to `$XDG_CONFIG_HOME/pg-review/`.
- Limits (`items_max_per_run`, `items_max_pending`, `items_auto_defer_days`,
  `interactive_ttl_minutes`, `evidence_ttl_days`, `field_max_chars`) live in `[limits]`.
- `pg-review job doctor --config-only` validates the effective configuration (built-ins merged
  with `config.toml`) and nothing else; it reads no router configuration.
- State lives under `$XDG_STATE_HOME/pg-review/` (the ledger, mode 0600) and evidence under
  `$XDG_CACHE_HOME/pg-review/`. Tests point the tool at a hermetic setup with `XDG_CONFIG_HOME`,
  `XDG_STATE_HOME` and `XDG_CACHE_HOME`.

The configuration reference with worked examples is section 5.13 of [docs/design.md](docs/design.md).

## Scheduling (adapter)

The tool has no router, event or schedule concepts. Recurring reviews run through
[pg-review-router-adapter](../pg-review-router-adapter/README.md), which a pg-router command role
calls with the item timestamp and the schedule's static metadata. The adapter computes the
explicit window, builds the run key `<lens>/<scope>/<window>/<window end>`, passes `--run-key`,
and maps exit codes to router semantics (20 skipped and 9 busy become 0; it never exits 9). The schedule
entries themselves live in the router configuration, not in `config.toml`. See section 5.10 of
[docs/design.md](docs/design.md).

## Implementation notes

- Evidence row `baseline` is an array of `{from, to, metrics}` objects; `window` bounds are RFC 3339.
- Finding sums are `{label, addends: [number], total: number}`; all finding strings are capped at
  1200 characters.
- Schemas are embedded from `schemas/embed.go` and validated with format assertion on.
- Run `./update-deps.sh` after changing dependencies.
