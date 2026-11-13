# pg-desk

`pg-desk serve` is the long-running pg-desk daemon: an HTTP server (a launchd user agent on darwin)
that serves the triage payload and `/metrics`.

## /metrics snapshot

`/metrics` does not collect, and performs no database query, on a scrape. `pg-desk serve` rebuilds the
whole catalog in a background goroutine every 30 seconds (`--metrics-snapshot-interval`, intended range
30 to 60 seconds), one build at a time, and publishes the result through an atomic pointer; the handler
serves the last snapshot, so scrape latency does not depend on host load or on the poller and writers
sharing the database. A failed build keeps the last good snapshot and lets its age grow. Staleness is
visible in `pg_desk_metrics_snapshot_age_seconds` (seconds since the served snapshot was built),
`pg_desk_metrics_snapshot_compute_duration_seconds` (a histogram of build times) and
`pg_desk_metrics_snapshot_errors_total`. A scrape arriving before the first snapshot exists waits at
most 5 seconds, then returns only the snapshot-state families (no age series yet).

The build reads narrow data: `Store.ListEntityFlow` (the flow columns, served from the covering index
`entity_flow`, never the facts column, which is nearly all of the table), `Store.ChangeKindCountsAll`
(one SQL aggregate instead of loading every `change_log` row) and `Store.ListHiddenWIP` (one query for
all hidden and wip annotations). `status` and `doctor` read the same `changes.Observe`, so they report the
same values.

## Profiling

`pg-desk serve` (and no other subcommand) can expose Go's runtime profiler (`net/http/pprof`) for
diagnosing CPU or memory use. It is off unless `PG_DESK_PPROF_ADDR` is set; with it unset the daemon
opens no listener. The darwin launchd agent sets it by default
(`phillipgreenii.services.pg-desk-serve.pprof.enable`, default `true`) to the unix socket
`$XDG_STATE_HOME/pg-desk/pprof.sock` (resolved to the absolute path by the Nix module), created with
mode `0600`. The implementation is the shared package `github.com/phillipgreenii/x/pprofserver`.

`go tool pprof` cannot dial a unix socket, so fetch the profile with `curl`, then read the file:

```bash
SOCK="$XDG_STATE_HOME/pg-desk/pprof.sock"
curl --unix-socket "$SOCK" 'http://pprof/debug/pprof/profile?seconds=30' -o cpu.pb.gz
go tool pprof -top cpu.pb.gz
```

Do not send SIGQUIT to get a goroutine dump: this daemon traps only SIGINT and SIGTERM, so on
SIGQUIT the Go runtime dumps goroutines and exits. Fetch `http://pprof/debug/pprof/goroutine?debug=2`
over the socket instead.

The runbook `docs/go-daemon-profiling-runbook.md` in `phillipg-nix-repo-base` is the authoritative
home of how to capture and read a profile. The listener accepts only an absolute socket path or a
loopback `host:port`; any other value is refused with a line on stderr and the daemon keeps running.
