# pg-wi-flow

> Bead-workflow CLI framework: query/list/reserve/claim/release work items, driven by a two-layer JSON config and the built-in null workflow (more verbs land in later packets).
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Print the fully built filter set for the default (unattended) query:

`pg-wi-flow query`

- Print the filter set for one or more stages:

`pg-wi-flow query --stage {{groom}}`

- List the items the default query would admit:

`pg-wi-flow list`

- List open items no query admits:

`pg-wi-flow list --unpooled`

- Reserve and print the next claimable item at a given stage:

`pg-wi-flow next --stage {{implement}}`

- Transfer a reservation to the caller's own identity:

`pg-wi-flow claim {{item-id}}`

- Release an item, clearing its assignee:

`pg-wi-flow release {{item-id}}`
