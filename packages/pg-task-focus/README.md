# pg-task-focus

A local, single-user daemon and command-line client that keep the operator on a written daily
routine: day, week and sprint checklists, and timed work cycles with an overtime sound. Everything is
recorded in an append-only event log; every view is derived from it.

The behavior is specified in `docs/behavior/pg-task-focus/` (start at its `README.md`), and the
reasons for the main decisions are in `docs/adr/0088-pg-task-focus-event-log-and-projection.md` and
`docs/adr/0091-pg-task-focus-daemon-api-and-observability.md`; the web UI's are in
`docs/adr/0092-pg-task-focus-web-ui-is-dependency-free-modules-embedded-in-the-daemon.md`.

## Layout

| Path                              | What it is                                                                                                                    |
| --------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `cmd/pg-task-focus`               | The binary: `serve` (the daemon) and the client verbs                                                                         |
| `api/openapi.yaml`                | The HTTP API contract (OpenAPI 3.1), the authority on every path, body and status                                             |
| `schemas/`                        | JSON Schemas: the event line, the configuration, and the command line's `--json` output                                       |
| `internal/engine`, `command`, ... | The core library (sub-project 1): event store, projection, validation by candidate replay                                     |
| `internal/server`, `wire`         | The HTTP handlers and the wire shapes                                                                                         |
| `internal/daemon`                 | Startup, readiness, SIGHUP reload, the alert runner, shutdown                                                                 |
| `internal/obs`                    | The `pg_task_focus_` metrics catalog, logs, tracing                                                                           |
| `internal/cli`, `client`          | The command line and its HTTP client                                                                                          |
| `web/`                            | The web UI: `index.html`, native ES modules and a stylesheet under `assets/`, embedded in the binary; its tests under `test/` |
| `grafana/`                        | The dashboard and the alert rules                                                                                             |

The SwiftBar menu-bar plugin is a separate package, `packages/pg-task-focus-swiftbar`: a streaming
renderer over `status --watch` and the CLI, with a nix-generated plugin wrapper.

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

`--client NAME` (or `PG_TASK_FOCUS_CLIENT`) sets the `X-Client` the verb sends: `cli` (the default), `web`,
`connector` or `swiftbar`, anything else is a usage error. The SwiftBar plugin
(`packages/pg-task-focus-swiftbar`, installed by `phillipgreenii.programs.pg-task-focus.swiftbar`) runs
this binary as `--client swiftbar`, so the daemon's per-client metrics show a silent plugin.

The root path serves the web UI (see below). Logs are JSON lines on stdout. The API is loopback-only (`127.0.0.1`), requires a `Host` and
`Origin` from the allowlist (the loopback names with the port, and the host of `public_url`), and uses
no token. A scrape of `127.0.0.1:<port>/metrics` is in the allowlist.

## The web UI

The page at `/` is a client of the API and nothing else. It is plain ES modules (no framework, no
bundler, no npm) that the binary embeds from `web/assets/` and serves at `/assets/{name}` under a
content security policy that allows only its own origin. Open it at `http://127.0.0.1:49210/` (or the
`public_url`). The areas are Today (checklists, the cycle panel and the period and profile modals) and
the Editor (`#/events`); `#/tasks/<id>` and `#/cycles/<id>` are the deep links.

There is nothing to build: edit a file under `web/assets/` and rebuild the binary. The page keeps its
logic out of the DOM so `node` can test it: views are pure functions from the model to a tree of plain
objects (`vdom.mjs`), the controller is a table of named intents (`intents.mjs`), and `app.mjs` is the one
module that touches the browser. A control is built by `view-common.mjs`'s `control`/`button`, which
refuses to build one that does not say whether it mutates, and disables every mutating control while
the store is read-only. The client emits no telemetry of its own: it makes the daemon's API calls with
`X-Client: web`, writes nothing to the console or to browser storage, and its free text leaves it only
as the body of a request.

## The reverse proxy contract

The web UI is reached in a browser through a local reverse proxy that this repository does not own.
The consuming flake sets `phillipgreenii.programs.pg-task-focus.publicUrl` to the proxy's URL for it
(for example `https://focus.<proxy domain>`); the daemon then admits that host and origin and builds
deep links from it (`<public_url>/#/tasks/<task_id>`, `<public_url>/#/cycles/<cycle_id>`). The proxy
MUST forward the request's `Host` header unchanged (or rewrite it to `127.0.0.1:<listen_port>`), MUST
forward to `127.0.0.1:<listen_port>`, and MUST NOT buffer `/api/v1/stream` (server-sent events). The
daemon works with no proxy.

## The connector backend

The calendar and attention reads (`GET /api/v1/calendar`, `GET /api/v1/attention`) are consumed by
`pg-connector-calendar-task-focus`, a Tier-2 backend that lives with the other backends in
`packages/pg-connector/cmd/pg-connector-calendar-task-focus` (its own Go module is `pg-connector`'s,
so it depends on `api/openapi.yaml` and never on this module's packages). It is launched once per
request. Set `phillipgreenii.programs.pg-task-focus.connector.enable` to register it with the
`pg-connector` home module: the registration derives its address from `listenPort`. The behavior is
in `docs/behavior/pg-task-focus/connector.md`, and the flake check
`test-pg-connector-calendar-task-focus-contract` fails when the OpenAPI schemas it decodes change.

## Tests

`go test ./...` runs everything, including the daemon's end-to-end tests: a real daemon on an
ephemeral loopback port with a fake clock and a fake sound player, driven over HTTP and through the
command line, every request and response validated against `api/openapi.yaml`. The nix gate is
`checks.<system>.pg-task-focus-go-tests`.

A race guard covers the health reads: `internal/daemon/health_concurrency_test.go` reads `/readyz`,
`/healthz` and `/metrics` from before the listener is bound, through several starts, forty reloads
and a stop. It can fail only under `go test -race`, which the commit-time `run-unit-tests` hook
runs. A daemon field that is written once the HTTP server is serving (the alerter, `Daemon.alerter`)
MUST be published through an atomic or a lock, never read as a plain field.

The web UI's logic is tested with `node --test` (no dependencies), run from inside the Go tests:
`web/web_test.go` runs the unit tests of the page's modules (`web/test/*.test.mjs`) and scans the
sources for what the page must never do, and `internal/daemon/webui_e2e_test.go` runs the page's own
store and API client against a real daemon. A host without `node` skips them with the reason;
`PG_TASK_FOCUS_REQUIRE_NODE=1` (set by the nix check, which supplies `nodejs`) makes that a failure.
What a browser would show (layout, focus order as a screen reader announces it, contrast, a real
`EventSource` reconnecting) is not covered by any automated test here.

Regenerate `schemas/cli.schema.json` after changing the API document with
`UPDATE_CLI_SCHEMA=1 go test ./internal/cli -run TestCLISchemaIsDerivedFromTheAPI`.
