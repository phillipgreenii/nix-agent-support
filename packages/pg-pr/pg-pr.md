# pg-pr

> Unified PR-work CLI for agents and humans.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/tree/main/packages/pg-pr>.
> How pr-pool orchestrates: see the [pr-pool behavior docs](../pr-pool/docs/behavior/README.md). The review/work **workflows** that use `pg-pr` are deployment-specific (defined in a deployment's own behavior docs). How they're implemented today: [`docs/pr-review-flow.md`](../../docs/pr-review-flow.md) (downstream reference).

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

- Report beads duplicated per PR, in all statuses, excluding those already adjudicated against a canonical bead via a `supersedes` edge (read-only audit; changes nothing):

`pg-pr sync duplicates`

- Open the pull requests needing your review in one new browser window, a tab per PR:

`pg-pr open`

- List that selection instead of opening a browser (titles are clickable hyperlinks on a terminal; piped output prints a bare URL column instead):

`pg-pr open --print`

- Open the whole review set, skipping the ones a human already approved:

`pg-pr open --all --unapproved`

- Open all of your own pull requests (`--mine` defaults to all of them, not just the actionable ones):

`pg-pr open --mine`

- Narrow either set to just what is waiting on you:

`pg-pr open --mine --needs-attention`

- Daemon dashboard snapshot (JSON):

`curl http://127.0.0.1:9818/api/v1/dashboard`
