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

- Land from outside any repository, naming a worktree (its clone is used; all worktree flags MUST be in one clone):

`pg-git-lander land {{feature-branch}} --source-worktree {{path/to/worktree}}`

- Rebase a branch onto a commit:

`pg-git-lander rebase {{feature-branch}} --onto {{origin/main}}`

- Resume, skip a commit of, abort or quit a stopped operation (no worktree flags: run inside the repository or pass `--repo`):

`pg-git-lander land --continue`

`pg-git-lander land --skip`

`pg-git-lander land --abort`

`pg-git-lander rebase --quit`

- Land past a failed or flaky `pre-merge-verify` gate, ONLY when the operator said to (it skips those hooks and git's own hooks for the run, and the outcome says so):

`pg-git-lander land {{feature-branch}} --continue --no-verify`

- Show the operations in this repository, as JSON:

`pg-git-lander status --json`

- List stopped operations that have been stopped for more than 2 hours, with their `--continue`/`--abort` commands:

`pg-git-lander status --stale-after {{2h}}`

- Print the last 50 lines of an operation's log:

`pg-git-lander log {{op_id|branch}} --tail {{50}}`

- Validate the configuration (exit 7 when a configured command is not on this process's PATH):

`pg-git-lander config check --file {{path/to/config.toml}}`
