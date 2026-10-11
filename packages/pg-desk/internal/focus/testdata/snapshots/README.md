# Recorded snapshots of the rank gate

`TestFocusRankGate` (and the flake check `pg-desk-focus-rank-gate`, which runs only that test) loads
each file under `jira/`, `bd/` and `github-pr/` into a store, runs it through the rank and asserts the
exact rank order and the five `rank_inputs` counts. A change of a real input format (a due-date
layout, a timestamp offset, a missing status category) moves a count or the order and fails the gate,
where a hand-written synthetic fixture would keep passing.

## What these files are

Each file is a rank fixture (the format is described in the header of `rank_golden_test.go`) whose
`facts` are the shape of what a backend prints, under the keys pg-desk stores them:

| Directory    | Printed by                                                | Stored under               |
| ------------ | --------------------------------------------------------- | -------------------------- |
| `jira/`      | `pg-connector issue show --json` (Jira) and `issue deps`  | `issue_show`, `issue_deps` |
| `bd/`        | `pg-connector issue show --json` (beads) and `issue deps` | `issue_show`, `issue_deps` |
| `github-pr/` | `pg-connector pr show --json`                             | `pr_show`                  |

**Provenance.** The values are synthetic placeholders written to keep the real SHAPE: the field names
of the connector schema, the raw Jira timestamp form (`2026-09-14T09:30:00.000+0000`, an offset
without a colon), the date-only Jira `due_date`, bd's `P0` to `P4` priorities and midnight-`Z`
`due_date`, 40-character SHAs and the state spellings. They were NOT recorded from a live tracker (no
live host is read from this repository's change). To replace one with a real recording, run the named
command against a real item, then scrub it as below and keep the `expected` block honest by deriving
it from the rulings, not from the rank's output.

## Scrub rules

A recorded snapshot MUST carry no real names, logins, tracker keys, repository names or URLs. Replace
them with neutral placeholders that keep the field's shape: `PRJ-<n>` keys, `bd-<id>` ids,
`example-org/example-repo`, `https://tracker.example.test` and `https://github.example.test` URLs,
`Pat Example` and `pat@example.test` for the operator, `teammate` for another author (the identifier
guard, `TestIdentifierAllowlistGuard`, scans these directories and fails on any other login-shaped
token in a structured identity field).

## A known gap the Jira snapshot pins

The age key reads a creation time as RFC 3339. The raw Jira layout `...000+0000` is not RFC 3339, so
every Jira issue in `jira/assigned_issues.json` ranks on its first-stored time and counts toward
`age_fallback` (5 of 5). The expectation pins that current behavior. If the rank learns the Jira
layout, `age_fallback` drops to 0 and this gate fails; update the expected count in the same change.
