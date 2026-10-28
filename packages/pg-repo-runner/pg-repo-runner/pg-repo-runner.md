# pg-repo-runner

> Kernel and facade of the pg-router-managed async repo runners. Each role resolves the repo named by `PG_ROUTER_ITEM_ID`, checks the allow-list and the path, honors the kill switch and the load check, takes a single-flight lease, then acts. Exit `0` for any verdict, `9` for "not now", `2` for a refusal or usage error, `1` for a malfunction.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Print the verdict store, the lease, the kill switch and the open failure transitions (red tip, diverged, push blocked):

`pg-repo-runner status`

- Print the verdicts of one repo as JSON:

`pg-repo-runner status --json --repo {{repo-key}}`

- Print the repo tips that have no check verdict yet, as JSONL:

`pg-repo-runner tips`

- Check one repo tip by hand (an immutable `nix flake check`, then a verdict; a red check still exits `0`):

`PG_ROUTER_ITEM_ID={{repo-key}}@{{sha}} pg-repo-runner verify`

- Use another config file:

`PG_REPO_RUNNER_CONFIG={{path/to/config.toml}} pg-repo-runner status`

- Sync one repo once (fetch, fast-forward a behind clone, record an ahead one, surface a diverged one; never pushes or rebases):

`PG_ROUTER_ITEM_ID={{repo-key}} pg-repo-runner sync`
