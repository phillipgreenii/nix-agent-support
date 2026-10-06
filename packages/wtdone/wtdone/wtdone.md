# wtdone

> Guarded worktree teardown: refuses if a process on a blocking allow-list (claude, git, shells, python, editors, go, nix) is anchored inside the worktree (`lsof`), stops its fsmonitor daemon, removes the worktree, deletes the branch with a plain `git branch -d` (never `-D`), prunes, and prints the landed sha plus the canonical clone's remaining worktrees. Anchored processes under any other name (a language server, `caffeinate`, ...) are reported and ignored.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Tear down the worktree and branch for `{{pg2-abcde}}`, resolved against the canonical clone `cd`'d into:

`wtdone {{pg2-abcde}}`

- Tear down against a specific canonical clone instead of the current directory's:

`wtdone {{pg2-abcde}} --cc {{/path/to/canonical/clone}}`

- A session tearing down its OWN worktree must leave it first (the liveness guard cannot protect the caller from itself):

`cd {{/path/to/canonical/clone}} && wtdone {{pg2-abcde}}`

- Override which process names block removal (space-separated; `*` suffix is a prefix match; replaces the default `claude git bash zsh sh python* vim nvim emacs go nix`; there is no flag for this):

`WTDONE_BLOCKING_COMMANDS="{{claude git}}" wtdone {{pg2-abcde}}`
