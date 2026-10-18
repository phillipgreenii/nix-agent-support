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

- Escalate a blocked item, recording the concern whose verdict was `blocked`:

`pg-wi-flow escalate {{item-id}} --question {{"may we drop the table?"}} --trigger {{q:intent}} --concern {{destructive-actions}}`

- Approve a question; for a `destructive-actions` question this files a `kind:docs` follow-up unless the approval cites an authorizing doc:

`pg-wi-flow resolve {{question-id}} --decision {{"yes"}} --rationale {{"why"}} --authorizing-doc {{docs/runbooks/drop-table.md}}`

- Show the facts for an item, including `WI_LESSONS` (the component's lessons) and `WI_REVIEW_MODE` (`parallel` or `batched`, the current stage's `review_mode`):

`pg-wi-flow context {{item-id}}`

## Conventions

Lesson memory key. The learn stage records a lesson with
`bd remember "<what was ruled out>" --key lesson-<component>-<topic-slug>`,
where `<component>` is the item's `component:<name>` label value and
`<topic-slug>` is a short lowercase `[a-z0-9-]` slug. `context` reads
them back with `bd memories lesson-<component>-`, keeps keys that start
with that prefix, and prints one line per memory joined by `;` as
`WI_LESSONS=`. An item with no component, or a component with no
memories, prints an empty `WI_LESSONS=`; a failed `bd memories` read
prints one warning on stderr and an empty value (exit 0). `context
--render` shows it only when `render.include` lists `lessons`.

Destructive-actions follow-up. `escalate --concern <name>` records the
originating concern in the question's `originating_concern` metadata
(set at creation, or adopted by a reused open question that has none).
`resolve <question> --decision ... --rationale ...` on a question whose
`originating_concern` is `destructive-actions` and that was given no
`--authorizing-doc <repo-path>` files one `kind:docs` child (entry stage,
no inherited labels) under each parent the question blocked, to write the
doc that authorizes the action; with `--authorizing-doc` it files none.
A question escalated before this change has no recorded concern and gets
no follow-up. The approval itself is never refused.
