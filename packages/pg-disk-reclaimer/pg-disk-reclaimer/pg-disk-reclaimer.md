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

- Dry-run output shows each item's reclaimable size (or `size: unknown (timed out after 60s)`) and a trailing total of the known sizes. An item's own `displayTimeoutSeconds` also raises its size ceiling. Raise the ceiling for every item with:

`PGDR_SIZE_TIMEOUT_SECONDS={{300}} pg-disk-reclaimer reclaim --aggressiveness {{3}}`

- Aggressiveness is a ceiling on a 0-5 scale (0 tidy cruft, 1 tidy, 2 caches, 3 deep, 4 costly or irreversible, 5 hardest to recover); under `--apply`, levels 4 and above ask for interactive confirmation. Full definitions: `packages/pg-disk-reclaimer/pg-disk-reclaimer/docs/aggressiveness-scale.md` in the phillipgreenii-nix-agent-support repo:

`pg-disk-reclaimer reclaim --aggressiveness {{3}}`

- Actually remove (rather than dry-run):

`pg-disk-reclaimer reclaim --aggressiveness {{2}} --apply`

- Reclaim only specific registry items (by id) instead of every qualifying item:

`pg-disk-reclaimer reclaim --aggressiveness {{2}} {{item-id}}`

- Reclaim, also showing a note when a selected item's path does not exist on this machine (hidden by default):

`pg-disk-reclaimer reclaim --aggressiveness {{2}} --verbose`
