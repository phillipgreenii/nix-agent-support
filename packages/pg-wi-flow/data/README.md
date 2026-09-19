# pg-wi-flow plugin data root

This directory is `pg-wi-flow`'s `paths.defaults` -- the plugin-defaults data root a machine
config's `paths.defaults` value points at (a Nix-store path,
`/nix/store/…-pg-wi-flow-data`). `pg-wi-flow`'s render engine (`lib/context.bash`) resolves every
data-overlay file against the repo overlay first
(`<repo>/<paths.repo_local>/REL`, default `.claude/wi-flow`), falling back to this root
(`<paths.defaults>/REL`) when the repo overlay doesn't carry that file.

## Data-file contract

Plain markdown, no frontmatter. `stages/<s>.md`: what the worker does and which verbs end the
stage. `stages/<s>/escalation.md`: what the resolver may decide alone, what it must bump, the
sibling-check duty. `concerns/<c>.md`: the questions the reviewer asks and the evidence it needs to
answer ready. `checklists/<k>.md`: evidence the item must carry for this kind. A README under the
data root carries this contract and one example of each.

## Execution phase 1: no stage/concern/checklist files ship here

This phase (tc-9ddu3.1, "framework only, null workflow") configures no `workflows` key at all --
only the built-in **null workflow** exists (one stage, `work`: `entry` AND `closes`, `concerns:
[]`). The null workflow needs no `stages/<s>.md`, `stages/<s>/escalation.md`, `concerns/<c>.md`, or
`checklists/<k>.md` file: it has exactly one stage that is both the entry and the closing stage,
and an empty concerns list, so nothing in this phase ever looks one up.

Because of that, this data root deliberately ships **no `stages/`, `concerns/`, or `checklists/`
files at all** -- not even a single placeholder example of each, which the contract above would
otherwise call for. Fabricating example files here would describe stages/concerns/checklists that
correspond to nothing this phase actually runs. homelab's real `stages/`, `concerns/`, and
`checklists/` files -- the ones the contract's "one example of each" describes -- land in
execution phase 2, the first phase that configures an actual `workflows` key.
