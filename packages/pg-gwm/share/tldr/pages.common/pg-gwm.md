# pg-gwm

> Lease git worktrees of a repository: bounded, reusable, and reaped when idle.
> Every command prints JSON when piped and text on a terminal (`--format=text|json`).
> Failures exit non-zero (1 internal, 2 usage, 4 not found, 9 busy, 20 max-reached, 21 name-held, 22 branch-in-use, 23 dirty, 24 lease-released, 25 lease-lost, 26 ref-unresolvable, 28 broken, 29 low-disk).
> More information: `pg-gwm --help`.

- Lease a worktree on a new branch and enter it (the path is the only output):

`p=$(pg-gwm lease --name {{x}} -b {{feature/x}} --print=path) || exit; cd "$p"`

- Lease a detached worktree at a commit-ish, waiting up to a while for one:

`pg-gwm lease --detach {{origin/main}} --wait {{2m}}`

- Renew a lease by name, or take it again if it is already held:

`pg-gwm renew --name {{x}} --ttl {{30m}}`

`pg-gwm lease --name {{x}} --or-renew`

- Give a worktree back, or mark its lease expired so the reaper reclaims it:

`pg-gwm release --name {{x}}`

`pg-gwm expire --name {{x}}`

- Show one worktree, or list every managed one:

`pg-gwm info --name {{x}}`

`pg-gwm list`

- See what a reap would do, then reap this repository, or every configured and registered one with a deadline:

`pg-gwm reap --dry-run`

`pg-gwm reap --all-repos --deadline {{5m}}`

- List the branches kept from dirty expired worktrees, and delete old ones:

`pg-gwm preserved list`

`pg-gwm preserved prune --older-than {{30d}} --dry-run`

- Check the configuration file (alone with `--config`; with repository lookups without it):

`pg-gwm config check --config {{path/to/config.toml}}`

- Serve the Prometheus metrics on a loopback port:

`pg-gwm serve --addr {{127.0.0.1:9148}}`

- Print the shell completion:

`pg-gwm completion {{bash|zsh}}`
