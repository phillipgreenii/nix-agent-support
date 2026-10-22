# pg-rescue-resolve-conflict

> Resolve one resolve-conflict item: claim it, rebase its PR branch in a throwaway linked worktree under `pg-rescue --chain conflict`, push through `pg-rescue-push-branch`, close the item. Exit `0` for resolved, gave-up, lease-rejected and peer-held; `1` for any other failure; `2` for a usage error.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Resolve one item (the repo name is looked up in the repo-dir map):

`pg-rescue-resolve-conflict --item-id {{item-id}} --repo {{owner/name}} --branch {{pr-branch}} --base {{main}} --head-sha {{40-hex-sha}} --repo-dir-map {{name}}={{path/to/clone}} --worktree-root {{path/to/worktrees}} --tracker-dir {{path/to/dir-containing-.beads}}`
