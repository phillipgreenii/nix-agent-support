# git-branch-maintenance

> Maintain git branches with fast-forward, rebase, and cleanup operations.
> Supports worktrees and protects specified branches from deletion.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-support-apps>.

- Show status of all branches:

`git-branch-maintenance`

- Fast-forward all branches to origin/main:

`git-branch-maintenance --ff`

- Rebase all branches onto origin/main:

`git-branch-maintenance --rebase`

- Delete branches that are merged into main:

`git-branch-maintenance --delete-merged`

- Delete merged branches AND their worktrees:

`git-branch-maintenance --delete-merged --delete-merged-worktrees`

- Preview changes without making them:

`git-branch-maintenance --ff --rebase --delete-merged --dry-run`

- Protect a branch from deletion for this run:

`git-branch-maintenance --delete-merged --protect-branch {{branch_name}}`
