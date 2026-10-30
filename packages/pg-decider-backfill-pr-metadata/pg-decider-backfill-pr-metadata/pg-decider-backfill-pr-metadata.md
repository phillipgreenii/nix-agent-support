# pg-decider-backfill-pr-metadata

> One-off backfill: copy `repo` and `pr_number` from the parent anchor onto decider-minted process-feedback children that lack them. Dry-run by default.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Show the planned changes (writes nothing):

`pg-decider-backfill-pr-metadata --bd-dir {{/path/to/tracker/root}} --actor {{session-id}}`

- Apply the changes (explicit opt-in, stops on the first failure):

`pg-decider-backfill-pr-metadata --bd-dir {{/path/to/tracker/root}} --actor {{session-id}} --apply`
