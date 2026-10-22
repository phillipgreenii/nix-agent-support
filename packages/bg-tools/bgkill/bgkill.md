# bgkill

> Stop a background job launched with `bgrun`, by name. Sends SIGTERM (never SIGKILL) to the job's recorded launcher and the processes beneath it.
> Refuses a pid that is no longer the launcher bgrun started. The exit record then holds the payload's true exit code (143).
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Stop a running job:

`bgkill {{flake-check}}`

- Give the job longer to stop before reporting it still running (exit status 6):

`bgkill --grace {{15}} {{flake-check}}`

- Use a custom state directory:

`bgkill --dir {{/tmp/my-jobs}} {{build}}`

- Confirm what happened (the exit record, never the log tail):

`bgcheck {{flake-check}}`
