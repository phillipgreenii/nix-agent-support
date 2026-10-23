# pg-rescue-resolve-conflict

> Resolve one resolve-conflict item: claim it, rebase its PR branch in a throwaway linked worktree under `pg-rescue --chain conflict`, push through `pg-rescue-push-branch`, close the item. Exit `0` for resolved, gave-up, stale (the head moved or the push lease was rejected: the item is closed), gone (already closed) and peer-held; `1` for any other failure; `2` for a usage error.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Resolve one item (the repo name is looked up in the repo-dir map):

`pg-rescue-resolve-conflict --item-id {{item-id}} --repo {{owner/name}} --branch {{pr-branch}} --base {{main}} --head-sha {{40-hex-sha}} --repo-dir-map {{name}}={{path/to/clone}} --worktree-root {{path/to/worktrees}} --tracker-dir {{path/to/dir-containing-.beads}}`

- Do not push a rebase an agent handler resolved (it goes to the give-up chain instead; the default is `true`, which pushes it):

`PUSH_AGENT_RESOLVED=false pg-rescue-resolve-conflict {{options}}`

- Send an item to the give-up chain after fewer failed runs (default 3; the count is the item's `script_failures` metadata):

`SCRIPT_MAX_FAILURES={{1}} pg-rescue-resolve-conflict {{options}}`

- Read why a run failed (outcome line and the run's stderr):

`cat ${XDG_STATE_HOME:-~/.local/state}/pg-rescue-resolve-conflict/{{run-id}}.{outcome,log}`
