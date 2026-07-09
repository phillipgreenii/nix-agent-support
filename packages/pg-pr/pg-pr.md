# pg-pr

> Unified PR-work CLI for agents and humans.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/tree/main/packages/pg-pr>.
> Review flow (pg-pr = PR-data interface, pr-pool = review-workflow owner): see the living doc `docs/pr-review-flow.md`.

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
