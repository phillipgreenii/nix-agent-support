# pg-keep-awake

> Ask the Mac to stay awake, and see what is holding it awake.
> One keeper process holds the assertion for every request, under one battery rule; clients never run `caffeinate` themselves.
> The command line is the only supported interface; the state files are private.
> More information: `pg-keep-awake --help`, and `pg-keep-awake <command> --help`.

- Keep the Mac awake while a process lives (omit `--pid` to bind to the caller's parent process):

`pg-keep-awake acquire --owner {{myscript}} --pid {{1234}}`

- Keep the Mac awake for a set time, with no process attached:

`pg-keep-awake acquire --owner {{myscript}} --for {{30m}}`

- Withdraw the request (exits 0 even when there was nothing to release):

`pg-keep-awake release --owner {{myscript}}`

- Withdraw every request of an owner:

`pg-keep-awake release --owner {{myscript}} --all`

- Hold the Mac awake for the life of one command; its exit status passes through:

`pg-keep-awake run --owner {{myscript}} -- {{make build}}`

- Show what holds the Mac awake: the keeper, the battery, every lease and every other holder:

`pg-keep-awake status`

- Print the same as one JSON object, or check keeper health (exit 5 keeper down, 6 degraded):

`pg-keep-awake status --json`

`pg-keep-awake status --check`

- Let the Mac sleep for a while without losing any request (it returns on its own):

`pg-keep-awake pause --for {{1h}}`

- End a pause now:

`pg-keep-awake resume`

- Validate a configuration file without changing anything (exit 4 when invalid):

`pg-keep-awake config check --config {{path/to/config.toml}}`
