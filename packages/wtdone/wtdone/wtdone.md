# wtdone

> Guarded worktree teardown: refuses if a live process is anchored inside the worktree (`lsof`), stops its fsmonitor daemon, removes the worktree, deletes the branch with a plain `git branch -d` (never `-D`), prunes, and prints the landed sha plus the canonical clone's remaining worktrees.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Tear down the worktree and branch for `{{pg2-abcde}}`, resolved against the canonical clone `cd`'d into:

`wtdone {{pg2-abcde}}`

- Tear down against a specific canonical clone instead of the current directory's:

`wtdone {{pg2-abcde}} --cc {{/path/to/canonical/clone}}`

- A session tearing down its OWN worktree must leave it first (the liveness guard cannot protect the caller from itself):

`cd {{/path/to/canonical/clone}} && wtdone {{pg2-abcde}}`
