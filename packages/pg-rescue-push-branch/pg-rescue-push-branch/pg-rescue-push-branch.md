# pg-rescue-push-branch

> Push a rebased PR branch from a linked git worktree with `--force-with-lease` pinned to an expected sha. Refuses the main working tree, the base branch, unsafe branch names and malformed shas; exits 3 when the remote branch moved or does not exist.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Push the worktree's `HEAD` to its PR branch, only if the remote branch still points at the expected sha:

`pg-rescue-push-branch --worktree {{path/to/worktree}} --branch {{pr-branch}} --base {{main}} --expected-sha {{40-hex-sha}}`

- Push to a remote other than `origin`:

`pg-rescue-push-branch --worktree {{path/to/worktree}} --branch {{pr-branch}} --base {{main}} --expected-sha {{40-hex-sha}} --remote {{upstream}}`

- Act on the exit code (`0` pushed, `3` lease rejected, `2` usage error or refusal, `1` other failure):

`pg-rescue-push-branch --worktree {{path/to/worktree}} --branch {{pr-branch}} --base {{main}} --expected-sha {{40-hex-sha}}; echo $?`
