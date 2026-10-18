# pg-wi-flow plugin data root

This directory is `pg-wi-flow`'s `paths.defaults` -- the plugin-defaults data root a machine
config's `paths.defaults` value points at (a Nix-store path,
`/nix/store/…-pg-wi-flow-data`). `pg-wi-flow`'s render engine (`lib/context.bash`) resolves every
data-overlay file against the repo overlay first
(`<repo>/<paths.repo_local>/REL`, default `.claude/wi-flow`), falling back to this root
(`<paths.defaults>/REL`) when the repo overlay doesn't carry that file. Concerns and checklists are
the union of both trees (the repo wins on a collision); stage instruction files are single-file
overrides. A missing checklist or concern file is normal, never an error.

`default.nix` copies this whole tree into the store path, so any file added under `stages/`,
`concerns/` or `checklists/` ships with no further edit to it.

## Data-file contract

Plain markdown, no frontmatter. `stages/<s>.md`: what the worker does and which verbs end the
stage. `stages/<s>/escalation.md`: what the resolver may decide alone, what it must bump, the
sibling-check duty. `concerns/<c>.md`: the questions the reviewer asks and the evidence it needs to
answer ready. `checklists/<k>.md`: evidence the item must carry for this kind.

## What ships here

- `stages/groom.md` and `stages/groom/escalation.md`: the groom stage (the entry stage).
- Concern files under `concerns/` as later packets add them.
- No checklists: checklists are homelab-specific and ship in the homelab repo overlay, not here.

## Examples

A stage file (`stages/<s>.md`):

```markdown
# Stage: example

1. Read the item and classify it.
2. Run the reviewers, then `pg-wi-flow round <id>`.
3. End with exactly one verb: `pg-wi-flow advance <id> --to <next>` or `pg-wi-flow escalate`.
```

An escalation file (`stages/<s>/escalation.md`):

```markdown
# Escalation: example

Decide alone when the answer follows from the item or the repo's docs: `resolve --decision ...
--rationale ...`. Bump with `escalate` on the question when only the operator can answer. Check each
sibling in `WI_SIBLINGS` for the same stale assumption after settling a conflict.
```

A concern file (`concerns/<c>.md`):

```markdown
# Concern: example

- Is the item single-concept and internally consistent?
- Evidence needed to answer ready: a cited doc path and a stated acceptance.
```

A checklist (illustrative only; not shipped, since checklists live in the repo overlay):

```markdown
# Checklist: upgrade

- The chosen target version is named.
- The rollback step is stated.
- A verification command and its expected output are given.
- Dependents of the upgraded component are listed.
```
