# bgcheck

> One-call status + recent output for a background job launched with `bgrun`.
> Read-only. Trust the `DONE exit=<code>` line — never the log tail — to judge success.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Check one job (status line, then the last 20 log lines):

`bgcheck {{flake-check}}`

- Show more log context:

`bgcheck --lines {{80}} {{flake-check}}`

- List every recorded job with its status:

`bgcheck`

- Use a custom state directory:

`bgcheck --dir {{/tmp/my-jobs}} {{build}}`
