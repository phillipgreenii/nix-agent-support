# pg-rescue

> Wrap a command and, when it fails, try an ordered chain of named failure handlers.
> Exit codes: 0 resolved (or the command succeeded), 75 deferred, 70 wrapper error, otherwise the command's own code.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/tree/main/packages/pg-rescue>.

- Run a command, trying the handlers of a configured chain if it fails:

`pg-rescue --chain {{sync}} -C {{path/to/repo}} -- {{git pull --rebase}}`

- Name the handlers explicitly instead of a chain (there is no default chain):

`pg-rescue --handlers {{fix-small,notify}} -- {{command}} {{args}}`

- Say what the step is for, so handlers can tell:

`pg-rescue --chain {{sync}} --context "{{sync-projects: rebase onto origin}}" -- {{command}}`

- Check a handler's "resolved" claim with a command instead of re-running the original:

`pg-rescue --chain {{sync}} --verify "{{git status --porcelain}}" -- {{command}}`

- Show more of what the chain did (`-v` adds details and verify output, `-vv` adds argv, meta and stderr):

`pg-rescue --chain {{sync}} -vv -- {{command}}`

- Print nothing from pg-rescue; the command's own output still passes through:

`pg-rescue --chain {{sync}} -q -- {{command}}`

- Write the machine-readable result of the run (one run-log line) to a file:

`pg-rescue --chain {{sync}} --result-file {{path/to/result.json}} -- {{command}}`

- Feed saved failure output to a chain instead of running a command:

`pg-rescue --stdin --handlers {{my-handler}} --verify true -vv < {{path/to/saved-output.log}}`

- Validate the config and list handlers and chains, with the resolved path of each handler:

`pg-rescue check`

- Print a result for a handler script to end with (resolved, deferred or declined):

`pg-rescue result {{resolved|deferred|declined}} "{{one line summary}}"`

- Show the last runs, newest first:

`tail -n {{5}} ~/.local/state/pg-rescue/runs.jsonl | jq -c '{run_id, result, chain, cmd}'`
