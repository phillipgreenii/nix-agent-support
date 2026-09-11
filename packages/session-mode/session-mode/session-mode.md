# session-mode

> Track which named mode (drain-beads, unblock-human-beads, wrap-up-session, ...) is running in
> this Claude Code session, so the status line can show it.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Start (or refresh) this session's mode record:

`session-mode start {{drain-beads}} --force --detail "{{P1 only}}"`

- Mark it stopping (before a graceful hand-off to wrap-up):

`session-mode set-status {{stopping}}`

- Mark it finished (at the loop's own termination line):

`session-mode set-status {{finished}}`

- Print the raw record for this session:

`session-mode show`
