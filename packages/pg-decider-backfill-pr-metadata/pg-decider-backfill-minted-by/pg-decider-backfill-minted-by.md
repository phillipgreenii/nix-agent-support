# pg-decider-backfill-minted-by

> One-off backfill: add the `minted-by:pg-decider` label to the decider's existing beads that provably are its own. Dry-run by default.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Show the planned changes and the residual look-alikes (writes nothing):

`pg-decider-backfill-minted-by --bd-dir {{/path/to/tracker/root}} --actor {{session-id}}`

- Apply the changes (explicit opt-in, stops on the first failure):

`pg-decider-backfill-minted-by --bd-dir {{/path/to/tracker/root}} --actor {{session-id}} --apply`

- Trial run: write only the first bead of each kind, still checking the full plan size:

`pg-decider-backfill-minted-by --bd-dir {{/path/to/tracker/root}} --actor {{session-id}} --expect {{539}} --limit-per-kind {{1}} --apply`

- Also label the open keyless look-alikes (the earlier tool's open beads the decider will adopt):

`pg-decider-backfill-minted-by --bd-dir {{/path/to/tracker/root}} --actor {{session-id}} --include-open-keyless --expect {{18}} --apply`

- Refuse to continue unless exactly N beads are planned:

`pg-decider-backfill-minted-by --bd-dir {{/path/to/tracker/root}} --actor {{session-id}} --expect {{539}}`
