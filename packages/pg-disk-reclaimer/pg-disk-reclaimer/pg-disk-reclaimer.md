# pg-disk-reclaimer

> Data-driven disk-space-reclaim CLI, driven by a registry of removable "areas".
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- List reclaimable registry items:

`pg-disk-reclaimer list`

- List reclaimable registry items up to an aggressiveness ceiling:

`pg-disk-reclaimer list --aggressiveness {{2}}`

- List, also showing items whose path does not exist on this machine (hidden by default):

`pg-disk-reclaimer list --verbose`

- Validate a registry file (schema checks plus a best-effort check that each command's leading token exists; it cannot validate pipes, subshells, or later commands in a `&&` chain):

`pg-disk-reclaimer validate {{path/to/registry.json}}`

- Reclaim disk space up to an aggressiveness ceiling (dry-run by default):

`pg-disk-reclaimer reclaim --aggressiveness {{2}}`

- Actually remove (rather than dry-run):

`pg-disk-reclaimer reclaim --aggressiveness {{2}} --apply`

- Reclaim only specific registry items (by id) instead of every qualifying item:

`pg-disk-reclaimer reclaim --aggressiveness {{2}} {{item-id}}`
