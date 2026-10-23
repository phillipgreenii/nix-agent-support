# pg-rescue-push-branch

> Push a rebased PR branch from a linked git worktree with `--force-with-lease` pinned to an expected sha. Always pushes to `origin`. Refuses the main working tree, a worktree outside the given root, a rebase in progress, a HEAD that does not descend from `origin/<base>`, the base branch, unsafe branch names and malformed shas; exits 3 when the remote branch moved or does not exist.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Push the worktree's `HEAD` to its PR branch, only if the remote branch still points at the expected sha:

`pg-rescue-push-branch --worktree {{path/to/worktree}} --worktree-root {{path/to/root}} --branch {{pr-branch}} --base {{main}} --expected-sha {{40-hex-sha}}`

- Act on the exit code (`0` pushed, `3` lease rejected, `2` usage error or refusal, `1` other failure):

`pg-rescue-push-branch --worktree {{path/to/worktree}} --worktree-root {{path/to/root}} --branch {{pr-branch}} --base {{main}} --expected-sha {{40-hex-sha}}; echo $?`
