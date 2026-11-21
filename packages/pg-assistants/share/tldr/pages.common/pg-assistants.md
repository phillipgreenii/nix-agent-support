# pg-assistants

> Keep permanent Claude Code assistants running in tmux and recycle each session before its context fills.
> launchd runs the daemon; these commands look at it and talk to it.
> More information: `pg-assistants --help`, and `pg-assistants <command> --help`.

- Show each assistant: running or not, context fill, ladder rung, why it is waiting, and any problem (IDLE: it ended its turn and went quiet; the daemon nudges, then restarts it):

`pg-assistants status`

- Check the daemon's health (exit 3 down, 5 degraded, 0 healthy), printing one line:

`pg-assistants status --check`

- Print the status file as JSON:

`pg-assistants status --json`

- Attach to an assistant's session (detach with `C-b d`; the assistant keeps running):

`pg-assistants attach {{assistant_id}}`

- Inside an assistant's session, ask the daemon to stop it (run it on its own, then end the turn):

`pg-assistants exit`

- Validate a configuration file without changing anything (exit 7 when invalid):

`pg-assistants config check --config {{path/to/config.toml}}`
