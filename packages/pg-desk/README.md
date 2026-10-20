# pg-desk

`pg-desk serve` is the long-running pg-desk daemon: an HTTP server (a launchd user agent on darwin)
that serves the triage payload and `/metrics`.

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
