# pg-repo-runner

> Kernel and facade of the pg-router-managed async repo runners. Each role resolves the repo named by `PG_ROUTER_ITEM_ID`, checks the allow-list and the path, honors the kill switch and the load check, takes a single-flight lease, then acts. Exit `0` for any verdict, `9` for "not now", `2` for a refusal or usage error, `1` for a malfunction.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Print the verdict store, the lease, the kill switch and the open failure transitions (red tip, diverged, push blocked):

`pg-repo-runner status`

- Print the verdicts of one repo as JSON:

`pg-repo-runner status --json --repo {{repo-key}}`

- Print the repo tips that have no check verdict yet, as JSONL:

`pg-repo-runner tips`

- Print the tip of one repo only (exit `2` for a key outside the allow-list, nothing and exit `0` when its verify is off, exit `1` when the repo cannot be read; the no-argument form also exits `1` if any repo cannot be read, after printing the healthy ones):

`pg-repo-runner tips --repo {{repo-key}}`

- Check one repo tip by hand (an immutable `nix flake check`, then a verdict; a red check still exits `0`):

`PG_ROUTER_ITEM_ID={{repo-key}}@{{sha}} pg-repo-runner verify`

- Emit the monitoring gauges (best effort, through `ccpool emit-gauge`; catalog in the package README, nothing is emitted without a ccpool and an `OTEL_EXPORTER_OTLP_ENDPOINT`):

`PG_REPO_RUNNER_CCPOOL_BIN={{path/to/ccpool}} pg-repo-runner tips --repo {{repo-key}}`

- Use another config file:

`PG_REPO_RUNNER_CONFIG={{path/to/config.toml}} pg-repo-runner status`

- Sync one repo once (fetch, fast-forward a behind clone, record an ahead one, surface a diverged one; pushes only a repo whose config has push = true, dry run first; never rebases or forces):

`PG_ROUTER_ITEM_ID={{repo-key}} pg-repo-runner sync`

- Print the debounced workspace tip tuple as JSONL (one `workspace@<hash>` item once the tuple has been quiet for the quiet window and has no build verdict):

`pg-repo-runner workspace-tips`

- Build the whole workspace at one tip tuple by hand (a throwaway `runner-build-*` workforest set and `pn workspace build`, then a verdict; a red build still exits `0`):

`PG_ROUTER_ITEM_ID=workspace@{{hash}} pg-repo-runner build`
