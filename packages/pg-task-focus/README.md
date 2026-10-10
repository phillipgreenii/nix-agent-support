# pg-task-focus

A local, single-user daemon and command-line client that keep the operator on a written daily
routine: day, week and sprint checklists, and timed work cycles with an overtime sound. Everything is
recorded in an append-only event log; every view is derived from it.

The behavior is specified in `docs/behavior/pg-task-focus/` (start at its `README.md`), and the
reasons for the main decisions are in `docs/adr/0088-pg-task-focus-event-log-and-projection.md` and
`docs/adr/0091-pg-task-focus-daemon-api-and-observability.md`.

## Layout

| Path                              | What it is                                                                                |
| --------------------------------- | ----------------------------------------------------------------------------------------- |
| `cmd/pg-task-focus`               | The binary: `serve` (the daemon) and the client verbs                                     |
| `api/openapi.yaml`                | The HTTP API contract (OpenAPI 3.1), the authority on every path, body and status         |
| `schemas/`                        | JSON Schemas: the event line, the configuration, and the command line's `--json` output   |
| `internal/engine`, `command`, ... | The core library (sub-project 1): event store, projection, validation by candidate replay |
| `internal/server`, `wire`         | The HTTP handlers and the wire shapes                                                     |
| `internal/daemon`                 | Startup, readiness, SIGHUP reload, the alert runner, shutdown                             |
| `internal/obs`                    | The `pg_task_focus_` metrics catalog, logs, tracing                                       |
| `internal/cli`, `client`          | The command line and its HTTP client                                                      |
| `grafana/`                        | The dashboard and the alert rules                                                         |

## Running it

The Nix modules run it as a launchd user agent (`home/programs/pg-task-focus`, with its
observability registrations in `darwin/modules/pg-task-focus`). By hand:

```bash
pg-task-focus serve --config /path/to/config.json   # data under $XDG_DATA_HOME/pg-task-focus
pg-task-focus status                                # the CLI finds the daemon at 127.0.0.1:49210
```

`PG_TASK_FOCUS_ADDR`, `PG_TASK_FOCUS_CONFIG` and `PG_TASK_FOCUS_DATA_DIR` set the address, the
configuration file and the data directory. `SIGHUP` reloads the configuration. Check a log or a
configuration without the daemon: `pg-task-focus check`, `pg-task-focus config check`.

The root path serves a static placeholder page until the web UI lands. Logs are JSON lines on stdout. The API is loopback-only (`127.0.0.1`), requires a `Host` and
`Origin` from the allowlist (the loopback names with the port, and the host of `public_url`), and uses
no token. A scrape of `127.0.0.1:<port>/metrics` is in the allowlist.

## The reverse proxy contract

The web UI is reached in a browser through a local reverse proxy that this repository does not own.
The consuming flake sets `phillipgreenii.programs.pg-task-focus.publicUrl` to the proxy's URL for it
(for example `https://focus.<proxy domain>`); the daemon then admits that host and origin and builds
deep links from it (`<public_url>/#/tasks/<task_id>`, `<public_url>/#/cycles/<cycle_id>`). The proxy
MUST forward the request's `Host` header unchanged (or rewrite it to `127.0.0.1:<listen_port>`), MUST
forward to `127.0.0.1:<listen_port>`, and MUST NOT buffer `/api/v1/stream` (server-sent events). The
daemon works with no proxy.

## Tests

`go test ./...` runs everything, including the daemon's end-to-end tests: a real daemon on an
ephemeral loopback port with a fake clock and a fake sound player, driven over HTTP and through the
command line, every request and response validated against `api/openapi.yaml`. The nix gate is
`checks.<system>.pg-task-focus-go-tests`.

Regenerate `schemas/cli.schema.json` after changing the API document with
`UPDATE_CLI_SCHEMA=1 go test ./internal/cli -run TestCLISchemaIsDerivedFromTheAPI`.
