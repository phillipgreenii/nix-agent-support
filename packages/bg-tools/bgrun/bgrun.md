# bgrun

> Launch a command in the background with a deterministic log path and a truthful exit record.
> The payload's true exit code lands in the state dir when it finishes; check with `bgcheck`.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Run a long build in the background (prints `NAME PID LOGPATH`):

`bgrun {{flake-check}} -- nix flake check`

- Run a pipeline or shell syntax (wrap it explicitly):

`bgrun {{tests}} -- bash -c '{{go test ./... | tail -40}}'`

- Use a custom state directory:

`bgrun --dir {{/tmp/my-jobs}} {{build}} -- {{nix build .#pkg}}`

- Check on it later (from any session):

`bgcheck {{flake-check}}`
