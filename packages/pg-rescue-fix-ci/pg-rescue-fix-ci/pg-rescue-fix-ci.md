# pg-rescue-fix-ci

> Handle one fix-ci item: claim it, request one rerun of the failed CI jobs under `pg-rescue --chain fix-ci`, and let the chain's give-up hand the item to the worker when the rerun is spent. Exit `0` for rerun requested or awaited, gave-up, gone and peer-held; `1` for any other failure; `2` for a usage error.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Handle one item (the rerun is requested once per item; a later run with a new failing build gives up). `failing_builds` and `ci_rerun_of` are comma-separated `<run id>:<attempt>` strings compared as sets; `ci_rerun_of` is recorded before the rerun is requested:

`pg-rescue-fix-ci --item-id {{item-id}} --repo {{owner/name}} --pr-number {{123}} --tracker-dir {{path/to/dir-containing-.beads}}`

- Pin the CI backend instance and the state directory (the outcome record `<run-id>.outcome`, the run log `<run-id>.log` and the claim record `<run-id>.claim` live there):

`pg-rescue-fix-ci --item-id {{item-id}} --repo {{owner/name}} --pr-number {{123}} --tracker-dir {{path/to/dir-containing-.beads}} --ci-backend {{instance}} --state-dir {{path/to/state}}`

- Lower the failure cap (default 3 failed runs per item, after which the give-up chain hands it to the worker) and the stranded-claim sweep age in seconds (default 7200):

`SCRIPT_MAX_FAILURES={{2}} SWEEP_AGE={{3600}} pg-rescue-fix-ci --item-id {{item-id}} --repo {{owner/name}} --pr-number {{123}} --tracker-dir {{path/to/dir-containing-.beads}}`
