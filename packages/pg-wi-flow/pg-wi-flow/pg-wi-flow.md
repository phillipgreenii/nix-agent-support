# pg-wi-flow

> Bead-workflow CLI framework: query/list/reserve/claim/release work items, annotate/advance/create-child/merge/close them, driven by a two-layer JSON config and the built-in null workflow (more verbs land in later packets).
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

- Classify an item's kind and component:

`pg-wi-flow annotate {{item-id}} --kind {{bug}} --component {{litellm}}`

- Record a reviewer's verdict for the current round:

`pg-wi-flow record-verdict {{item-id}} --concern {{intent}} --json {{verdict.json}}`

- Merge the recorded verdicts for the current round:

`pg-wi-flow round {{item-id}}`

- Move an item to another stage:

`pg-wi-flow advance {{item-id}} --to {{implement}}`

- Create a child item, blocked on another:

`pg-wi-flow create-child {{parent-id}} --title {{"land the change"}} --kind {{land}} --stage {{implement}} --blocked-by {{item-id}}`

- Close an item:

`pg-wi-flow close {{item-id}} --reason {{"superseded"}}`

- Close an item as a duplicate of an older, still-open one:

`pg-wi-flow close-duplicate {{item-id}} --of {{canonical-id}}`
