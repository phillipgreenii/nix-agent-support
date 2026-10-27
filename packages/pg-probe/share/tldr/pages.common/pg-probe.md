# pg-probe

> Read-only probes for unattended sessions: GET a daemon's pprof endpoint over its unix socket, or wait for a log line.
> It is not a generic curl or unix-socket client: only a fixed set of `/debug/pprof` paths is reachable, and only with GET.
> More information: `pg-probe --help`.

- List the pprof paths it will fetch (every other path, and any method but GET, is refused with exit 3):

`pg-probe pprof list`

- Fetch a profile from the pa-monitor pprof socket (default `$XDG_STATE_HOME/pa-monitor/pprof.sock`):

`pg-probe pprof {{heap|goroutine|allocs|block|mutex|threadcreate|cmdline|index}}`

- Print a goroutine dump as text:

`pg-probe pprof goroutine --debug {{2}}`

- Capture a CPU profile for N seconds (1 to 300) into a file you name, then read it:

`pg-probe pprof profile --seconds {{30}} > {{/tmp/cpu.pb.gz}}`

- Print the first line of a log that matches a regex, waiting up to 60 seconds (exit 124 on timeout):

`pg-probe wait-log {{/tmp/job.log}} '{{listening on}}'`

- Wait up to two minutes, testing the whole existing file first, or only lines written from now on:

`pg-probe wait-log --timeout {{2m}} --from-start {{/tmp/job.log}} '{{done}}'`

`pg-probe wait-log --timeout {{2m}} --lines 0 {{/tmp/job.log}} '{{done}}'`
