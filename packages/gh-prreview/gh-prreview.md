# gh-prreview

> GitHub Pull Request review workflow extension for gh CLI.
> Manage PR reviews using git worktrees for isolated review environments.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-support-apps>.

- Checkout a PR as a git worktree for review:

`gh prreview checkout {{pr_number}}`

- List all locally checked out PRs:

`gh prreview list-local`

- List PRs awaiting your review:

`gh prreview list-awaiting`

- List PRs with deep search (older PRs, up to 500):

`gh prreview list-awaiting --deep`

- Remove a PR review worktree and its local branch:

`gh prreview remove {{pr_number}}`

- Remove all closed/merged PR worktrees:

`gh prreview remove --closed`
