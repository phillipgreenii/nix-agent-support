# pg-router exit codes are the shared workspace codes; 3 is Unavailable and the reserved-3 rule is retired

**Status**: Accepted (operator ruling, Phillip, 2026-11-11, verbatim: "review existing apps and create beads to fix violations. it is ok to break contracts, just make sure both sides are included in the specific landing of a change."; supersedes the band statements of 0042 and of `DEC-WIRE-1`)
**Date**: 2026-11-14
**Deciders**: Phillip Green II

## Context

ADR 0042 made the low exit codes global (`0` ok, `1` unexpected, `2` usage) and left everything
at or above `3` as one app-specific band, with `9` for busy. `DEC-WIRE-1` then narrowed that band:
`3` became "core-reserved pre-flight" (`drain.go`'s `exitPrecheck`) and the app band started at `4`.

The workspace has since defined shared exit codes in the `exitcode` package of the `x` module
(inventory: `docs/exit-codes.md` in `x`): OK 0, Error 1, Usage 2, Unavailable 3, NotFound 4,
Degraded 5, Timeout 6, Config 7, Denied 8, Busy 9, ShutdownOverrun 10, Conflict 11, app-specific
20 to 63, and never 64 to 78, 124, 126, 127, 128+n or 255. Against it pg-router had three
violations: a CLI call with no running core exited `1` (Unavailable is `3`), a refused log lock
exited `1` (Busy is `9`), and the config or startup precheck exited `3` (Config is `7`). The
reserved-3 rule also made `3` mean "precheck" in pg-router and "daemon down" everywhere else.

## Decision

1. **pg-router uses the shared codes.** `conformance.ExitOK`, `ExitError`, `ExitUsage`, `ExitBusy`
   and the new `ExitUnavailable` and `ExitConfig` are declared once, in package `conformance`, as
   aliases of the `exitcode` constants.
2. **No running core is `3` (Unavailable).** Every CLI verb that cannot locate or reach the core
   (status, explain, push-inject, ingest-event, self-status, the gate verbs, log compact against a
   named core) exits `3`. A failure that is not "no core" (a schema-invalid event, an unknown
   role) stays `1`.
3. **A refused log lock is `9` (Busy).** `log compact` run offline against a log another process
   holds exits `9`; its dry run says a real run "would be REFUSED (exit 9)".
4. **The config or startup precheck is `7` (Config)**: `run`, `run-until-idle`, `run-role` and
   `run-query` when the configuration does not load.
5. **`doctor` exits** `0` when no probe fails, `2` for usage, otherwise the first that applies of
   `7` (the config did not load), `1` (any failed probe other than a missing core) and `3` (the
   only failed probe is that no core is running).
6. **The reserved-3 rule is retired.** No code is reserved to the core: a participant MAY exit
   with any shared code, and the conformance driver no longer flags `3` specially (it is an
   ordinary non-zero exit of a command role, a failure like any other, as the driver already
   treats `1`). The app-specific band is the shared one, `20` to `63`; a participant MUST NOT use
   `64` to `78`, `124`, `126`, `127`, `128+n` or `255`.
7. **`9` keeps both readings** (a participant's pre-accept decline and the core's own read
   refusal), unchanged.

## Consequences

- This is a wire change. The sides that move together in this landing: the CLI exits in
  `cmd/pg-router`, `conformance/transport.go`, the conformance driver (`exitCoreReserved` removed)
  and its test, `docs/decisions/wire.md`, `docs/behavior/interfaces.md`, `docs/decisions/role.md`,
  the help text in `args.go`, and the tests that assert exits `1` or `3`.
- Callers of the pg-router CLI that exec it (the desk focus announcer, the router probe, the
  checked-config build step) only test for non-zero, so they are unaffected. The manager-callback
  verbs (`ingest-event`, `self-status`) relay the core's coarse exit code to a manager; no external
  manager parses a value other than `0`.
- Superseded statements: ADR 0042's Decision table (`≥3` as one undifferentiated band, `9` as the
  required spelling) and `DEC-WIRE-1`'s "Reconciling against ADR 0042" bullet (`3` reserved, band
  starting at `4`). ADR 0042's decision that usage is `2` and busy is not `2` stands.
