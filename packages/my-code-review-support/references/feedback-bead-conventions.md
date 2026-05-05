# Feedback Bead Conventions

This reference documents the bead patterns used by the `gather-pr-feedback` and `review-pr-feedback` agents.

## Title Conventions

| Bead            | Title pattern                             | Example                                           |
| --------------- | ----------------------------------------- | ------------------------------------------------- |
| PR tracker      | `PR Tracker (#NNN): <PR title>`           | `PR Tracker (#123): Add feature X`                |
| Gathering       | `gather feedback at <ISO 8601 timestamp>` | `gather feedback at 2026-03-26T14:30Z`            |
| Feedback        | `Feedback: <summary of feedback>`         | `Feedback: Fix null check in parser`              |
| Proposed change | `Proposed Change: <description>`          | `Proposed Change: Add null guard to parser.ts:42` |

## Hierarchy

All beads are children of the PR tracker epic. The tracker is the only epic; all others are tasks.

```
PR Tracker (#123): Add feature X              [epic, open, P2]
├── gather feedback at 2026-03-26T14:30Z       [task, closed, P3]
├── gather feedback at 2026-03-26T16:00Z       [task, closed, P3]
├── Feedback: Fix null check in parser         [task, closed, P2]
├── Feedback: Consider error handling          [task, closed, P2]
├── Proposed Change: Add null guard...         [task, open, P2]
└── Proposed Change: Rethink retry...          [task, open, P1]
```

## Relationship Conventions

- Feedback beads have `relates-to` links to the gathering bead that created them: `bd dep relate <feedback-bead> <gather-bead>`
- Proposed-change beads have `relates-to` links to each feedback bead that contributed: `bd dep relate <proposed-change> <feedback-bead>`

## Lifecycle

| Bead            | Type | Created state  | Closed when                          | Close reason pattern                                              |
| --------------- | ---- | -------------- | ------------------------------------ | ----------------------------------------------------------------- |
| PR tracker      | epic | open           | PR merges                            | `PR #NNN merged`                                                  |
| Gathering       | task | open (claimed) | Gathering run completes              | `Complete: N actionable, M ignored`                               |
| Feedback        | task | open           | After review by `review-pr-feedback` | `Proposed change created: beads-xxx` or `No action needed: <why>` |
| Proposed change | task | open           | After implementation                 | `Implemented in commit <sha>` or `Resolved: <why>`                |

When a PR is merged, the `check-my-pr` skill closes the tracker and all its children with reason `PR #NNN merged, closing with tracker` (children) or `PR #NNN merged` (tracker).

## Priority Conventions

- P2: normal (default for all beads)
- P3: gathering beads (low-priority bookkeeping)
- P1: elevated proposed changes requiring human review

### When to assign each priority

- **P3 = nice-to-have / style.** Bookkeeping or low-impact polish that does not need to land before merge.
- **P2 = should-fix before merge.** The default for actionable feedback and proposed changes — work that an agent can implement without additional human input.
- **P1 = blocking / requires human discussion before action.** Architectural concerns, ambiguous scope, or changes whose direction needs a human decision before any implementation work proceeds.

## Change Detection

Gathering beads store thread IDs and `updated_at` timestamps in their notes field. The format is one entry per line:

```
thread:<id>:updated_at:<ISO 8601 timestamp>
```

Subsequent gathering runs compare against the most recent closed gathering bead's notes to identify new or updated threads.

## Bead Commands Reference

Creating beads with parent:

```bash
bd create --title="<title>" --type=task --priority=2 --parent=<tracker-id>
```

Linking beads:

```bash
bd dep relate <bead-a> <bead-b>
```

Finding children:

```bash
bd children <tracker-id>
```

Closing with reason:

```bash
bd close <id> --reason="<reason>"
```
