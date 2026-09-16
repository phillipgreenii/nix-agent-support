# pg-desk — interpret

Interpret is stage 2 of the pipeline (see [`pipeline-run.md`](pipeline-run.md)). Every
interpretation MUST be a deterministic function of store rows, computed with an injectable clock,
and MUST NOT use an LLM for any step below.

## Steps this phase implements

- **Ownership** — classifies a PR as mine, co-owned, or team, from config and the commit-author
  logins `pr commits` reported.
- **Enrichment** — derives kind, languages, and size from `pr files` and `pr commits`.
- **Urgency (base only)** — labels, keywords, checks rollup, and bugfix commits. The layered
  signals (project health, Jira priority) read cross-referenced rows and are out of scope until
  Phase 13 (see below); this phase runs base urgency alone.
- **Category** — a ranked classification over a configured vocabulary.
- **Feedback dispositions** — evaluated over every comment and thread of the PR on every run (a
  live, idempotent recompute, never cached). A disposition an operator or agent recorded through
  `feedback set` (see [`feedback.md`](feedback.md)) is an override this step MUST honor over its
  own verdict, on every later run.
- **Approvals, gate state, and waiting-on-me** — the last computed from the bead facts gather
  already collected through `issue deps --full`, so the human-facing views never read beads
  directly.
- **Match reasons** — recomputed from facts each run, never from which named query returned the
  PR: author in the team-members list, a review requested of self, a self review already exists,
  or the PR's labels intersect the configured watch labels.
- **Ready-to-promote** — a stored flag (own PR, not co-owned, draft, not WIP, checks green, no bot
  disapproval, no merge conflict). It is recorded, not acted on — see "Out of scope" below.

**Hidden and WIP are explicitly NOT interpreted.** `serve` and `open` join the `annotation` table
at read time, so a `hide` or `wip` call takes effect on the very next request, never waiting for
the next pipeline run. A hidden PR MUST be excluded from the five panel arrays and exposed only in
its own `hidden` array.

## Exit codes, telemetry, and logs

Interpret has no exit code of its own; like gather, it contributes to `run`'s exit code (`0` on
success or a degraded run, `1` only on a triggering-entity fetch or store failure — interpret
itself never introduces a new failure exit). It emits nothing over OpenTelemetry or Prometheus in
Phase 9 (D24). Its activity is part of `run`'s structured JSON stderr log and, under `--verbose`,
the three-stage timeline.

## Out of scope (Phase 9)

The layered urgency signals (project health, Jira priority) and the cross-reference step (ticket
keys and URLs found in PR, Jira, and thread text) are Phase 13; the `xref` table exists in the
schema ladder but is not populated until then. The Slack incident signal stays deferred (tracked
separately; it is today a nil-disabled, LLM-assessed hook with no deterministic variant, so it
carries no phase assignment yet). Draft auto-promotion, `wip on`'s upstream draft conversion, and
pending reply posting are accepted, recorded losses for this whole window — `ready_to_promote`
and `open --promotable` exist precisely so the operator can act on them by hand instead.
