# pg-rescue-fix-ci

> Handle one fix-ci item: claim it, request one rerun of the failed CI jobs under `pg-rescue --chain fix-ci`, and let the chain's give-up hand the item to the worker when the rerun is spent. Exit `0` for rerun requested or awaited, gave-up and peer-held; `1` for any other failure; `2` for a usage error.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Handle one item (the rerun is requested once per item; a later run with a new failing build gives up):

`pg-rescue-fix-ci --item-id {{item-id}} --repo {{owner/name}} --pr-number {{123}} --tracker-dir {{path/to/dir-containing-.beads}}`

- Pin the CI backend instance and the state directory:

`pg-rescue-fix-ci --item-id {{item-id}} --repo {{owner/name}} --pr-number {{123}} --tracker-dir {{path/to/dir-containing-.beads}} --ci-backend {{instance}} --state-dir {{path/to/state}}`
