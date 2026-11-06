# pg-review-provider-hook-decisions

Evidence provider for the agent-workflow lens. It reads the approval hook's
decision database (`tool_decisions` in `asks.db`) and prints a JSON array of
evidence rows (the `evidence-rows` schema) for one window and scope.

## Invocation

```text
pg-review-provider-hook-decisions --since T --until T --scope NAME
    [--query NAME]... [--cwd-prefix P]... [--exclude-cwd-prefix P]... [--db PATH]
```

- `--since` and `--until` are RFC3339 (any offset); they are normalised to UTC
  with a `Z` suffix, the form the database stores in `created_at`. The window
  is half-open: `since <= created_at < until`.
- `--scope` is required and echoed into every row.
- `--cwd-prefix` keeps decisions whose recorded `cwd` is under any prefix;
  `--exclude-cwd-prefix` drops those under any prefix (used to exclude the
  review job's own neutral directory). Prefixes must be absolute, a trailing
  slash is ignored, and matching is by whole path components, case-sensitive,
  the same rule as the pg-ccaudit scope filter.
- `--query` selects queries (repeatable); with none, every query runs.
- `--db` overrides the database path (default
  `$XDG_DATA_HOME/claude-extended-tool-approver/asks.db`); it is an addition to
  the shared flag set for tests and manual runs.
- Exit codes: 0 success, 2 usage error (missing or invalid window, unknown flag
  or query, bad prefix, missing scope), 1 runtime failure (unreadable
  database).

## Source access

The database is opened with the SQLite `file:<path>?mode=ro` URI and is never
written. Rows with `excluded = 1` (operator-excluded from evaluation) are
ignored. Note: the hook's own read-only store uses `immutable=1` instead,
because `mode=ro` against a live WAL database may need to touch the `-shm`
sidecar; if live runs show `SQLITE_CANTOPEN`, that is the place to revisit.

## Queries and row kinds

Each query selects one hook outcome; the query name is the row `kind`.

| Query      | `tool_decisions.outcome` | Meaning                                   |
| ---------- | ------------------------ | ----------------------------------------- |
| `denied`   | `denied`                 | the user or auto-mode classifier declined |
| `failed`   | `failed`                 | an approved tool call failed              |
| `rejected` | `rejected`               | the hook itself refused the call          |

`pending`, `approved` and `unresolved` rows are not evidence of friction and
are not read.

Rows are grouped by subject key within a query. Per row: `type` is
`tool-call`, `defined_in` is `claude-extended-tool-approver`, `baseline` is
empty, `metrics` are `count` (decisions) and `sessions` (distinct sessions),
`first_seen`/`last_seen` are the earliest and latest `created_at`, and `query`
is the query name.

## Subject keys

The provider owns the key; per-session fields never enter it.

- Non-Bash tool: the tool name (`Write`).
- Bash: `Bash:<program>`, where the program is the first word after any leading
  `VAR=value` assignments, reduced to its base name (`/usr/bin/curl` becomes
  `curl`). For `git`, `nix`, `bd`, `gh`, `go`, `docker`, `kubectl`, `cargo`,
  `npm`, `pn` and `pg-hooks`, a second word that looks like a subcommand
  (lowercase letters, digits, hyphens) is appended: `Bash:git push`.
- Unparseable input: `Bash:(unparsed)`; an empty command: `Bash:(empty)`.

Repeated refusals of one kind of command therefore collapse to one subject
regardless of arguments, paths or sessions.

## Ordering, ids and samples

Rows sort by (kind, subject key) and take ids `E-001` onward, so two runs over
one database are byte-identical. `samples` holds at most three references of
the form `decision:<row id> session:<session id>` (lowest row ids first); these
are the only place per-session values appear.

## Tests

The fixture is built by a Go test helper
(`internal/provider/hookdecisions/fixture`) that creates the `tool_decisions`
table with the hook's column set, filled with synthetic rows; no binary is
committed. The golden file is
`testdata/provider-hook-decisions/golden.json` (rewrite with
`go test ./internal/provider/hookdecisions/ -update`). The helper mirrors the
hook's schema by hand, so a hook migration that renames a column the provider
reads needs a matching helper change.
