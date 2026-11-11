# pg-git-lander

> Rebase a branch onto a commit, or land it onto the primary branch, under repository locks so concurrent runs queue.
> Exit codes: 0 done, 2 usage or config error, 3 busy (retry), 4 stopped (see the printed next commands), 5 refused.
> More information: `pg-git-lander --help`.

- Land a branch onto the primary branch (the branch is deleted and its worktree removed):

`pg-git-lander land {{feature-branch}}`

- Land onto another branch, keeping the worktree parked on the placeholder branch `pg-git-lander/idle/<name>`:

`pg-git-lander land {{feature-branch}} --into {{release}} --keep-source-worktree`

- Land and keep the branch, moved to the result:

`pg-git-lander land {{feature-branch}} --keep-source-branch`

- Rebase a branch onto a commit:

`pg-git-lander rebase {{feature-branch}} --onto {{origin/main}}`

- Resume, skip a commit of, abort or quit a stopped operation:

`pg-git-lander land --continue`

`pg-git-lander land --skip`

`pg-git-lander land --abort`

`pg-git-lander rebase --quit`

- Show the operations in this repository, as JSON:

`pg-git-lander status --json`

- Print the last 50 lines of an operation's log:

`pg-git-lander log {{op_id|branch}} --tail {{50}}`

- Validate the configuration:

`pg-git-lander config check --file {{path/to/config.toml}}`
