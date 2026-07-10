# pg-pr

> Unified PR-work CLI for agents and humans.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/tree/main/packages/pg-pr>.
> How the review/work flows should behave: see the pr-pool behavior docs [`pr-pool/docs/behavior/`](../pr-pool/docs/behavior/README.md). How they're implemented today: [`docs/pr-review-flow.md`](../../docs/pr-review-flow.md) (downstream reference).

- Print version:

`pg-pr version`

- Create a git worktree for a pull request:

`pg-pr worktree add {{pr_number}}`

- List local PR worktrees:

`pg-pr worktree list`

- Remove a PR's worktree (use `--force` to discard uncommitted changes):

`pg-pr worktree remove {{pr_number}}`

- Detect the branch and PR context for the current directory:

`pg-pr branch detect`

- Daemon dashboard snapshot (JSON):

`curl http://127.0.0.1:9818/api/v1/dashboard`
