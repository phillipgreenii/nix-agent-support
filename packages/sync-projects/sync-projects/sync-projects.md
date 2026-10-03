# sync-projects

> Rebase every workspace repo onto its origin under `pg-rescue --chain sync`, then run `pn workspace push`. Stops before the push, leaving the working copy as-is, when a repo's rebase is deferred (exit 75) or fails unhandled.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Sync the whole workspace (run from inside it, or with `PN_WORKSPACE_ROOT` set):

`sync-projects`

- Find out why a run stopped, newest run first (the exit code is pg-rescue's: `75` means a follow-up item was filed):

`tail -n {{5}} ~/.local/state/pg-rescue/runs.jsonl | jq -c '{run_id, result, chain, cmd}'`

- Check that the `sync` chain's handlers all resolve before relying on it:

`pg-rescue check --chain sync`
