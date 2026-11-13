# job doctor

`pg-review job doctor` checks that everything the tool depends on is wired.
It prints one line per check, `PASS <name>`, `FAIL <name>: <reason>` or
`SKIP <name>`, and exits 0 only when no check failed. `--config-only` runs the
configuration check alone and nothing else.

| Check       | What it proves                                                                                          | Exit on failure |
| ----------- | ------------------------------------------------------------------------------------------------------- | --------------- |
| `config`    | The effective configuration (built-ins merged with `config.toml`) has no validation problem.            | 7               |
| `ledger`    | The ledger opens and `PRAGMA integrity_check` returns `ok`.                                             | 22              |
| `providers` | Every provider command of the effective configuration is on PATH and answers `--help` (10 s).           | 23              |
| `recorder`  | `pg-ccaudit status --json` succeeds and reports at least one indexed file.                              | 23              |
| `ccpool`    | `ccpool` is on PATH.                                                                                    | 23              |
| `hook`      | The approval hook approves `pg-review agent q ccaudit error-rate-by-tool`.                              | 23              |
| `session`   | `PG_REVIEW_RUN` and `pg-review` are both visible inside a session started the way the runner starts it. | 23              |

The exit codes are the shared config code (7) and the pg-review ledger (22) and provider (23) codes. When several checks fail the highest exit code wins. A failing check never
stops the others.

The config and providers checks use the effective configuration, so with no
`config.toml` the three built-in providers are still probed, and a built-in
provider whose command is absent fails the `providers` check (and, because
configuration validation resolves provider commands too, the `config` check).

## The recorder check never uses the coverage query

The coverage query reads zero-filled columns under feed ingest, so it would
report a healthy recorder as empty or an empty one as healthy. The recorder
check runs `pg-ccaudit status --json` and requires `files_indexed` greater than
zero: that is "the recorder is healthy and the index returns data".

## The hook check

The check runs the approval hook binary (`claude-extended-tool-approver`) in
hook mode with a PreToolUse event for the sample command, in a throwaway HOME
and XDG tree so the machine's decision log is not written, and requires
`permissionDecision` of `allow`.

## The in-session check

Mechanism chosen: start one minimal ccpool session, then ask it for two shell
commands' output and compare.

1. `ccpool capacity --json` must show a free slot and no usage limit, else the
   check fails with a "rerun later or skip" reason.
2. `ccpool new` with the options the analyst session gets: the neutral working
   directory, permission mode `dontAsk`, the first profile's model and effort,
   and the same `PG_REVIEW_RUN` and `XDG_*` environment (the run id is a unique
   probe token). Only two tools are allowed: `Bash(printenv PG_REVIEW_RUN)` and
   `Bash(command -v pg-review)`.
3. `ccpool reply --no-wait` sends a prompt asking for exactly those two
   commands and a reply holding only their output.
4. The check polls `ccpool result` (3 s interval, 3 minutes total) and passes
   when the reply contains the probe token and `pg-review`.
5. The session is always closed, even on failure.

Cost: one ccpool slot for a few seconds and one short model turn on the first
profile. It runs no analysis, files nothing and touches no ledger row. The
`--skip-session-check` flag skips it (`SKIP session`, no effect on the exit
code), so a routine doctor run costs no session. The alternative of inspecting
the tmux server environment was rejected: PATH and HOME of a ccpool session
come from `--env` or from the tmux server, which is not observable without
starting a session.

`PATH` and `HOME` inside the session are not asserted separately: resolving
`pg-review` is the end-to-end proof that they are sufficient.

## Live check (on demand, not in the unit gate)

```text
go test -tags live -run TestLiveDoctor -timeout 300s ./internal/cli/
```

It runs all checks against the real machine and fails unless the in-session
check passes.

### Result of the live run

Run on 2026-11-10 on the real machine with `pg-review job doctor` (one run, one
real ccpool session; `ccpool capacity` beforehand: `free=6 counted=0
preserved=0 live=0 max=6`, no usage limit active).

```text
PASS config
PASS ledger
PASS providers
PASS recorder
PASS ccpool
PASS hook
PASS session
```

Every check passed, including `session`: `pg-review` resolved on PATH inside
the analyst session and `PG_REVIEW_RUN` was visible there.

Model names: the doctor session does not exercise the built-in profile models,
so `claude-sonnet-5-5` and `claude-opus-5-5` were not separately launched
through ccpool. Nothing showed them to be wrong, so the profile defaults are
unchanged; a rejected model would surface as a failed analyst session on the
first real run of that profile.
