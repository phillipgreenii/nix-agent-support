# pg-probe

A read-only probe for unattended (permission mode `dontAsk`) agent sessions. It exists because two
read-only checks a pool session needs have no safe, grantable shell spelling:

- `curl --unix-socket SOCK http://pprof/debug/pprof/...` is a generic unix-socket client: the same
  grant could reach a container engine's API socket, so it cannot be allowed.
- `timeout N tail -f FILE | grep -m1 RE` is a pipeline of three commands.

`pg-probe` is a deliberately narrow replacement for each, so a single prefix grant
(`Bash(pg-probe:*)`) is safe. Origin: operator ruling on `pg2-r9kv7` (W8, 2026-10-27), bead
`pg2-pqztiu`. The ruling approved it NARROWLY: the pprof socket case and `wait-log` only. It MUST NOT
grow into a general curl, a Prometheus or Grafana client, or any writer.

## Commands

```text
pg-probe pprof list
pg-probe pprof TARGET [--seconds N] [--debug N] [-X GET]
pg-probe wait-log [--timeout DUR] [--lines N | --from-start] [--poll DUR] FILE REGEX
pg-probe --version | --help
```

### `pprof`

GETs one `/debug/pprof` path over the daemon's pprof unix socket and writes the body to stdout. It
never writes a file: redirect the output yourself (`pg-probe pprof profile --seconds 30 > /tmp/cpu.pb.gz`),
so the redirect stays subject to the session's normal write rules.

`TARGET` is a name (`heap`, `goroutine`, `profile`, `index`, ...) or a full allowed path. The allowed
paths are exactly the ones `pg-probe pprof list` prints: the index, `allocs`, `block`, `cmdline`,
`goroutine`, `heap`, `mutex`, `profile`, `symbol`, `threadcreate` and `trace`.

The restrictions are enforced in code before any network access, and each has a test with a real
socket:

| Rule                                                                                                         | Refusal                                   |
| ------------------------------------------------------------------------------------------------------------ | ----------------------------------------- |
| The path MUST equal an allowlisted path (no cleaning, no prefix match, no query or fragment in it).          | exit 3                                    |
| The method MUST be GET (`-X` exists only so a `curl` habit gets a clear refusal).                            | exit 3                                    |
| `--seconds` (1 to 300) applies to `profile` and `trace` only; `--debug` (0 to 2) to the named profiles only. | exit 3                                    |
| The socket MUST be an absolute path named `pprof.sock` that is a unix socket.                                | exit 3 (a `docker.sock` is never dialled) |
| A redirect is never followed.                                                                                | exit 1 (HTTP 3xx reported as an error)    |

The socket is `$PG_PROBE_PPROF_SOCKET` when set (tests use it), else
`$XDG_STATE_HOME/pa-monitor/pprof.sock` (`~/.local/state/pa-monitor/pprof.sock`). There is deliberately
no `--socket` flag: a prefix grant of `pg-probe` cannot set an environment variable, so the socket
cannot be redirected by the command line a session runs.

### `wait-log`

Prints the first line of `FILE` that matches `REGEX` (an RE2 expression, so no catastrophic
backtracking) and exits 0. By default it first tests the last 10 lines, as `tail -f` shows them, then
follows the file; `--lines N` changes that (`0` means only lines written from now on) and
`--from-start` tests the whole file. A line is tested only once its newline has been written. It
survives truncation and rotation (a new file under the same name is read from its start).

`--timeout` defaults to 60 seconds, MUST be positive and is capped at one hour. On timeout it exits
**124**, the same code as `timeout(1)`, which no other path of `pg-probe` uses.

`FILE` MUST resolve (through symlinks) to a regular file under an allowed log root, or the command exits 3
without opening it. The roots are `$PG_PROBE_WAIT_LOG_ROOTS` (a colon-separated list; tests use it)
when set, else: `/tmp`, `/private/tmp`, `/var/log`, `/private/var/log`, `$TMPDIR`,
`$XDG_STATE_HOME/pa-monitor` and `$XDG_STATE_HOME/pg-router*`. `~/.claude`, `~/.ssh` and the rest of
the home directory are not roots.

## Exit codes

| Code | Meaning                                                                                   |
| ---- | ----------------------------------------------------------------------------------------- |
| 0    | success                                                                                   |
| 1    | runtime error (no socket, HTTP error status, missing file, interrupted)                   |
| 2    | usage error                                                                               |
| 3    | refused by policy (path, method, query, socket name, file outside the roots, bad timeout) |
| 124  | `wait-log` timed out                                                                      |

## Granting it to pool roles

Installing the package (the `phillipgreenii.programs.pg-probe` home module, on by default wherever
Claude is enabled) puts it on `PATH` and registers the tldr page; it grants nothing. A pool role runs
it only when its `--allowed-tools` list carries `Bash(pg-probe:*)`. Per the `pg2-r9kv7` ruling the
handler-wide list (`baseAllowedTools`) gets NO additions, so the grant is per role in the consuming
machine flake, next to the `bgrun`/`bgcheck`/`bgwait` trio, and its pinning check (`pg2-b1lir` Work
4, `checks.<system>.pg-router-pool-grants`) lists it there.

## Tests

`checks.<system>.pg-probe-go-tests` runs the whole module: a real unix-socket HTTP server for every
allowlisted path, the refusal of every other path, every non-GET method, bad queries and a
wrongly named socket (each with an assertion that the server saw zero requests, plus a negative
control proving the recorder sees a direct request), and real files for `wait-log` (match, later
append, timeout, truncation, rotation, symlink and `..` escapes, FIFO and directory refusal).
