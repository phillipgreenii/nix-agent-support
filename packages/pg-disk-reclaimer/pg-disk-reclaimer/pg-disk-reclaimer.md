# pg-disk-reclaimer

> Data-driven disk-space-reclaim CLI, driven by a registry of removable "areas".
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- List reclaimable registry items:

`pg-disk-reclaimer list`

- Validate a registry file:

`pg-disk-reclaimer validate {{path/to/registry.json}}`

- Reclaim disk space up to an aggressiveness ceiling (dry-run by default):

`pg-disk-reclaimer reclaim --aggressiveness {{2}}`

- Actually remove (rather than dry-run):

`pg-disk-reclaimer reclaim --aggressiveness {{2}} --apply`
