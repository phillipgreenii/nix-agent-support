# pg-task-focus runs as a loopback daemon with a contract-first API, and refuses a log whose active profile is gone

**Status**: Accepted (amends 0088; resolves `pg2-t7me1.2`). One decision in it is the daemon designer's, not the operator's, and awaits the operator's confirmation: see "Decision 7" and "To reverse it".
**Date**: 2026-10-10
**Deciders**: phillipg (rulings of 2026-10-07 to 2026-10-09); the sub-project 2 designer for Decision 7

This ADR amends ADR 0088, whose last paragraph left "the daemon's loopback and no-authentication
decision" to the amendment that sub-project 2 makes. It records how the `pg-task-focus` core library is
run: the daemon and its HTTP API, the command-line client, what the service exports to be observed, and
how it is deployed. The behavior these decisions produce is in `docs/behavior/pg-task-focus/`
(`service.md`, `command-line.md`, `observability.md`); this ADR carries only the reasons. The key words
MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119.

## Context

The library (ADR 0088) is a testable core with no HTTP, no UI and no process. Four things now decide the
shape of the service around it:

- Several clients (a terminal, a browser tab, a menu-bar plugin, a connector backend) act on one log, and
  any of them may retry a request whose answer was lost.
- A loopback HTTP API that a browser can reach is exposed to every web page the operator visits, and to
  DNS rebinding.
- The service is unattended and plays a sound at the end of a cycle, so a failure that is silent is a
  failure of the product.
- The consuming machine already has an observability stack, a launchd convention and a reverse proxy,
  all owned by other repositories.

## Decision

1. **The API is spec-first and the handlers are hand-written under contract tests.** One OpenAPI 3.1
   document (`api/openapi.yaml`) is the authority on every path, body and status; its component schemas
   are JSON Schema 2020-12. The handlers are written by hand, not generated, because the engine owns every
   rule and a handler is a translation between two shapes; a generator would add a build step and a second
   place for the error table to live. The cost of "hand-written" is paid by tests: a route missing from
   the document, or a path in it with no route, fails; the reason enum equals the library's closed set plus
   the transport's; and every request and response of the end-to-end tests is validated against the
   document, with a negative control that proves the check can fail. `/stream` is the one exception, as
   OpenAPI 3.1 cannot describe its items; its format is in the document's prose and tested directly.
2. **The service is loopback-only with no authentication token, behind a Host and Origin allowlist.** It
   binds `127.0.0.1`, requires a `Host` from the allowlist on every route (the DNS-rebinding defence),
   accepts an `Origin` only from the same allowlist (`null` refused, absent permitted), requires the JSON
   content type on every mutation, and sends no CORS header. The allowlist is `127.0.0.1:<port>`,
   `localhost:<port>` and the host and origin of the configured public URL. No token is used: a token a
   browser would have to hold is no stronger than the allowlist against a page the operator visits, and a
   token on a single-user loopback service buys nothing against the one local user it could stop. This is
   the decision ADR 0088 deferred.
3. **Errors are RFC 9457 problems with one closed set of reasons.** The set is the library's catalog (one
   code per condition, no catch-all, operator rulings 36 and 43) plus the reasons only the HTTP layer can
   find: `forbidden_host` and `forbidden_origin` (403), `unsupported_media_type` (415), `not_found` (404),
   `method_not_allowed` (405), and `internal_error` (500) for a defect. These six are additions to the
   design's error table, made because a refusal of the transport has no library code and must not be
   mislabelled with one. A response also carries a `traceresponse` header and the problem's `trace_id`
   even when no collector is attached, so a bug report can name the request.
4. **One binary, a thin client.** `pg-task-focus serve` is the daemon and every other verb is a client of
   the API (cobra, which gives shell completion). A client verb never reads the log or the configuration,
   except the two offline checks (`check`, `config check`), which exist for the case when the daemon will
   not start. Every client verb has `--json`; its output validates against `schemas/cli.schema.json`,
   which is derived from the API document by a test that fails when they drift. Exit codes are the
   repository's: 1 is the generic error, and each specific condition has its own value of 2 or more (usage
   2, unreachable 3, refused 4, store did not take it 5, not ready 6, serve failed to start 7, check
   found a problem 8).
5. **Home-manager scope for the daemon, darwin scope for the registrations.** This repository's guidance
   prefers the HM-scoped launchd pattern (`phillipgreenii-nix-personal` ADR 0055, as pa-monitor does) over
   a darwin module (as pg-desk-serve does). The daemon's enable flag, its rendered configuration and its
   data are per-user, it plays sound in the user's own session, and the SwiftBar plugin of sub-project 5
   will be installed by the same HM module; so the daemon lives in `home/programs/pg-task-focus`. The
   four registrations that make it visible (`metricsTargets`, `logSources` with the smallest
   `errorAlert` threshold, `alertRuleFiles`, `dashboardProviders`) are options declared at system scope,
   so they live in `darwin/modules/pg-task-focus`, which follows the HM flag across
   `home-manager.users` and asserts that only one user runs the daemon (the registrations are one per
   machine). The rendered configuration is validated at build time by the package's own `config check`.
   The module sets `PG_TASK_FOCUS_ADDR` and `PG_TASK_FOCUS_CONFIG` for the CLI. Registering the browser
   hostname in the local reverse proxy is the consuming flake's job; the daemon works with no proxy.
6. **Observability follows pg-desk for logs and metrics and pg-pr for traces, and none of it holds free
   text.** Logs are JSON lines on stdout, which launchd captures into `*.jsonl` under the state
   directory (the log source's default glob) and which an OTLP bridge also receives at Warn and above;
   metrics use the Prometheus client directly, with the names of the design and zero-initialised alerted
   counters; spans are made by a small middleware (not `otelhttp`, which no module of this workspace
   uses) with child spans for validate, append, fsync and project taken from the engine's own stage
   timings; the exporters refuse a non-loopback endpoint. A canary test pushes a sentinel through every
   `x-free-text` field of the event schema and fails if it reaches the log, the OTLP logs, a span
   attribute, `/metrics` or `/healthz`, and fails when the schema gains a field it does not push.
7. **A log whose active profile the configuration no longer defines is refused at start.** (The open
   question the core library left to sub-project 2, "engine.Open does not verify the active profile
   against the configuration".) The daemon checks it after replay, before it serves, and exits with the
   profile it found, the profiles the configuration does define and the way out. This is the
   conservative option, and it is consistent with the rest of the design: a reload that removes the
   active profile is already a bad reload (`INV-CONF-12`), so the active profile is always one the
   configuration defines, at every reload; refusing the start keeps a restart from entering a state the
   reload forbids. It also matches "a bad configuration at first start is a hard failure" and the
   restart-only recovery of read-only mode (ruling 7 of 2026-10-08): the operator restores the profile
   in the configuration and restarts, the log untouched. The alternatives were rejected: **falling
   back** to the configured default profile would silently change the profile the operator last chose
   and the tasks every later period change materializes, which is a state change nobody asked for;
   **surfacing it in health while serving** would leave the service answering with a profile it cannot
   resolve, every period change that names no profile failing as `unknown_profile`, and the cause visible
   only to someone who reads `/healthz`. The check is one function in the daemon
   (`CheckActiveProfile`), not a library rule, so reversing it changes no event, no code and no
   interface.
8. **The overtime sound and notification are the daemon's, through two interfaces.** `SoundPlayer` and
   `Notifier` are injected so no test needs audio. The system implementation plays `<name>.aiff` from the
   system, machine and user sound folders with `afplay` (a name is `[A-Za-z0-9 _-]{1,64}`, so it cannot
   name a path) and notifies with `osascript`, the text passed as arguments and never spliced into the
   script. The scheduler is polled after every commit and every reload (operator ruling, 2026-10-09: a
   missed or an extra sound under rare circumstances is not a problem), at the instant it names, and at
   least every half minute, because a sleeping laptop's timer does not advance and the scheduler plays at
   most one catch-up alert. Callbacks the engine runs while it serializes writes (the observers,
   `OnCommit`, `OnHealthChange`) recover their own panics, so a defect in a metric can never skip the push
   to open clients for that commit.
9. **Small additions to the library, each additive.** `Engine.Do` reports its stages with wall-clock
   instants (for spans), the store reports the fsync's share of an append (for the fsync histogram, which
   an observer records only when it is non-zero, since the engine reports an append's duration once per
   batch), and `config` lists its profiles, tasks, cycle types and group order (for `GET /config`).

## Consequences

### Positive

- One contract for every client, checked in both directions by tests, and a closed reason set a client can
  switch on.
- A page the operator visits cannot reach the service; a refusal of the defences is distinguishable from a
  defect.
- A failure to start, to write, to sound the alert or to reload is visible in the log, the metrics, the
  health page and an alert, without the operator's own words in any of them.
- A restart never lands in a state a reload refuses, and the log is never touched to recover.

### Negative

- Hand-written handlers can drift from the document between tests; the contract tests are the only
  guard, and a field the tests never exercise is unchecked until it is.
- The error table gained six transport reasons the design's table did not list.
- Refusing to start on a lost active profile makes a configuration edit that removes a profile fatal for
  the daemon on its next restart even though the running daemon had rejected the same edit as a reload;
  the operator must restore the profile before anything works, including `profile change`, which is a
  client of the daemon that is down. (The way out is a configuration that still defines the profile,
  then the change, then the removal.)
- `go.mod` moved from Go 1.25 to 1.26 to share the dependency set of the other daemons (Prometheus
  client, OpenTelemetry, cobra, yaml for tests); `gomod2nix.toml` was generated with the same hashing as
  the tool but not by it (no network), and `./update-deps.sh` SHOULD be run once to confirm it is identical.

### Neutral

- The default port 49210 and the `PG_TASK_FOCUS_*` variables are literals here; a consuming flake with a
  port ledger MUST check the port.
- The proxy registration (service list, hosts entry and its dashboard test) for the `public_url` host is
  a separate, small change in the private flake that owns the proxy.

## To reverse Decision 7

Delete the call to `CheckActiveProfile` in `internal/daemon/daemon.go` (and, to keep the service honest,
make the daemon report an unresolved active profile in `/healthz`), and rewrite `INV-SVC-10` in
`docs/behavior/pg-task-focus/service.md` and the test `TestStartRefusesALogWhoseActiveProfileIsNotConfigured`.
Nothing in the log, the API or the library changes.

See also: `phillipgreenii-nix-agent-support` ADR 0088 (the library this runs), ADR 0062 (the
process-boundary adapter the connector backend follows), and `phillipgreenii-nix-personal` ADR 0055
(the HM-scoped launchd registry).
