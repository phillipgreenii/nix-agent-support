# bgwait

> Block until a background job launched with `bgrun` finishes, then print its status and log tail once.
> Replaces repeated `bgcheck` / `sleep` / `date` polling. Default wait 480 s, capped at 540 s.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Wait for a job, then show `DONE exit=<code>` plus the last 20 log lines:

`bgwait {{flake-check}}`

- Wait at most two minutes (exit status 2 means still running; call it again):

`bgwait --timeout {{120}} {{flake-check}}`

- Show more log context:

`bgwait --lines {{80}} {{flake-check}}`

- Use a custom state directory:

`bgwait --dir {{/tmp/my-jobs}} {{build}}`
