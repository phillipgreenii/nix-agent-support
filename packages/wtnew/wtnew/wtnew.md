# wtnew

> Create a fresh git worktree for manual (non-drain) work: adds the worktree on a new branch, guarantees the pre-commit config symlink a fresh worktree is otherwise missing, and prints the same integration-facts JSON block `integrate-branch-support` prints.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Create `.worktrees/{{pg2-abcde}}` off the repo's resolved primary branch, on a plain branch named `{{pg2-abcde}}`:

`wtnew {{pg2-abcde}}`

- Branch from a specific ref instead of the resolved primary branch:

`wtnew {{pg2-abcde}} --base {{origin/main}}`

- Use a specific branch name instead of the plain `<bead-or-name>` default (e.g. to opt into the `drain/` naming the automated `/drain-beads` flow uses):

`wtnew {{pg2-abcde}} --branch {{drain/pg2-abcde}}`

- Extract just the primary_branch facts field the new worktree resolved to:

`wtnew {{pg2-abcde}} | jq '.primary_branch'`
