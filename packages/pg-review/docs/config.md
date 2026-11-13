# Configuration

pg-review reads one file, `$XDG_CONFIG_HOME/pg-review/config.toml` (`$HOME/.config`
when `XDG_CONFIG_HOME` is unset). Nix MAY render it and a person MAY write it by hand;
the tool never reads a router configuration.

## Built-ins and merging

The binary embeds the built-in lenses (`agent-workflow`, `config-health`), the profiles
`health-check` and `deep-review`, the providers `ccaudit`, `hook-decisions` and
`config-inventory` (each command is the matching `pg-review-provider-*` binary on
`PATH`), the `[limits]` defaults and `[ccpool]` defaults. A missing `config.toml` is a
working configuration.

A `config.toml` entry whose id is built in is merged onto it key by key: a key it sets
replaces the built-in value, a key it omits keeps it, and an array (or a provider's
`queries` table) is replaced whole. An entry with a new id needs every required key: a
profile's `model`, a provider's `command`, and a lens's `profile`, `prompt`, `providers`
and `kinds`.

Scopes have no built-in. With no `[[scope]]`, `pg-review job doctor --config-only` exits 0
and prints one warning line.

A lens's `prompt` and `base_prompt` paths are relative to `$XDG_CONFIG_HOME/pg-review/`;
a file there wins, then the embedded copy is used. The effective `max_findings` of a lens
is the lower of its value and `[limits] items_max_per_run`.

## `[limits]` defaults

| key                       | default |
| ------------------------- | ------- |
| `items_max_per_run`       | 5       |
| `items_max_pending`       | 12      |
| `evidence_ttl_days`       | 14      |
| `field_max_chars`         | 1200    |
| `items_auto_defer_days`   | 30      |
| `interactive_ttl_minutes` | 240     |

## `[ccpool]`

`read_tools` defaults to `["Read", "Grep", "Glob"]` and `neutral_dir` to
`$XDG_STATE_HOME/pg-review/analyst-cwd`. `pool_dir` is optional: unset means ccpool's own
default pool.

## Providers

A provider accepts an optional `args_style`: `cwd-prefix` (the default, and the default for
`ccaudit` and `hook-decisions`) or `inventory` (the default for `config-inventory`).

## `pg-review job doctor --config-only`

Validates the effective configuration (built-ins merged with `config.toml`) and exits 0
when valid. Otherwise it prints one line per problem, in the form
`<table> "<id>": <message>`, and exits 7. It rejects: a lens with an unknown provider or
profile or an empty kind list; a provider whose command is missing or not found (an
absolute executable path or on `PATH`); a scope with no prefix; duplicate ids within a
table; a prompt file that does not exist; a profile with no model; an effort outside
`low`, `medium`, `high`, `xhigh`, `max`; malformed TOML; and a `schema_version` other
than 1.

## `[tracker]`

Where Adopted work beads are filed: `command`, `dir`, `default_label` and `[[tracker.repo]]`
(`prefix`, `label`). See `outbox.md`.
