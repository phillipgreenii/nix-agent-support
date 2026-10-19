# pg-keep-awake integration suite runbook

The suite in this directory builds the real `pg-keep-awake` and `pg-caffeinate-shim` and runs the
real keeper, anchor, `/usr/bin/caffeinate`, CLI and shim hermetically: every child gets a temp
`XDG_STATE_HOME` and `XDG_CONFIG_HOME`, a file-backed battery
(`PG_KEEP_AWAKE_BATTERY_SOURCE=file:<path>`) and a 200 ms heartbeat. It observes the assertion with
`/usr/bin/pmset -g assertions`. Scenarios S1 to S16 are the tests named `TestS<n>...`; S17 is the
skip-line unit test (`TestSkipReasonFor`, `TestSkipLineFormat`).

## Manual run on the Mac

1. Confirm `/usr/bin/caffeinate` exists, then from `packages/pg-keep-awake` run:

   ```bash
   go test -v -count=1 ./internal/integration/...
   ```

2. Confirm the output has no `SKIPPED integration:` line and no `--- SKIP`. A `SKIPPED integration:`
   line means the host cannot run the suite (not darwin, no caffeinate, `pmset -g assertions`
   fails, or a probe caffeinate never appeared in the listing within 10 seconds); the reason follows
   the colon.
3. While a test holds a lease (a scenario takes tens of seconds), run
   `/usr/bin/pmset -g assertions` in another shell. The keeper's `caffeinate -i -w <anchor pid>`
   is listed as `pid <n>(caffeinate)`; that is the same listing the tests parse.

## Proving the skip path

Run `PG_KEEP_AWAKE_ITEST_SKIP=1 go test -v -count=1 ./internal/integration/...`. Exactly one line
starting `SKIPPED integration: forced by PG_KEEP_AWAKE_ITEST_SKIP` is printed and every scenario
reports `--- SKIP`. Piping the output through `grep -c '^SKIPPED integration: '` prints `1`.

## Cautions

- Tests signal only pids they spawned, plus the anchor and caffeinate named by their own
  `assertion.json`. They never touch `~/.local/state/pg-keep-awake` or the real keeper.
- A test run that is killed (not interrupted cleanly) can leave a test-owned `caffeinate` held for
  up to the anchor's watchdog timeout (30 s), after which the anchor and its `caffeinate` exit on
  their own. They carry a temp `--state-dir`, so they are never the real keeper's pair.
- The suite uses absolute paths (`/bin/sleep`, `/bin/sh`, `/usr/bin/pmset`, `/usr/bin/caffeinate`)
  so it also runs in the nix sandbox, where `PATH` is empty. Where the sandbox cannot run
  `caffeinate` or list its assertion, the check log carries the `SKIPPED integration:` line instead
  (`checks.<system>.pg-keep-awake-go-tests` runs `go test -v`).
