# my-code-review-support (deprecated)

> [!WARNING]
> This package is deprecated in favour of `pg-pr-plugin`. The Claude
> skills, commands, and agents shipped here have been adapted to drive
> the `pg-pr` Go CLI and now live under `packages/pg-pr-plugin/`.
>
> See `docs/superpowers/specs/2026-05-19-pg-pr-design.md` for the
> migration plan. Removal is scheduled for Phase 4.

## Migration table

| Old (this package)               | New (pg-pr-plugin)                                                        |
| -------------------------------- | ------------------------------------------------------------------------- |
| `skills/check-my-pr`             | `skills/pg-pr-watch-my-prs` + `commands/check-my-pr`                      |
| `skills/perform-draft-review-pr` | `commands/perform-draft-review-pr`                                        |
| `agents/review-orchestrator`     | `agents/pg-pr-review-orchestrator`                                        |
| `agents/review-code-changes`     | `agents/pg-pr-review-code-changes`                                        |
| `agents/review-pr-structure`     | `agents/pg-pr-review-pr-structure`                                        |
| `agents/review-jira-alignment`   | `agents/pg-pr-review-jira-alignment`                                      |
| `agents/gather-pr-feedback`      | Phase 3 — folded into `pg-pr sync` and the `pg-pr-process-feedback` skill |
| `agents/review-pr-feedback`      | Phase 3 — folded into the `pg-pr-process-feedback` skill                  |

The adapted content invokes the `pg-pr` verbs (e.g.,
`pg-pr review draft`, `pg-pr pr files`) in place of the legacy
`my-code-review-support-cli` invocations.
