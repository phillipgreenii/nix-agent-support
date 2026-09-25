# pg-desk — interpret

Interpret is stage 2 of the pipeline (see [`pipeline-run.md`](pipeline-run.md)). Every
interpretation MUST be a deterministic function of store rows, computed with an injectable clock,
and MUST NOT use an LLM for any step below.

## Steps this phase implements

- **Ownership** — classifies a PR as mine, co-owned, or team, from config and the commit-author
  logins `pr commits` reported.
- **Enrichment** — derives kind, languages, and size from `pr files` and `pr commits`.
- **Urgency (base + layered Jira half)** — the base signal (labels, keywords, checks rollup, and
  bugfix commits) is always computed first, unchanged since Phase 9. Starting Phase 13 (docket
  `pg2-2j5ac.40`), it is layered with ONE additional signal read from the PR's own
  cross-referenced Jira issue(s) (gathered by [`gather.md`](gather.md)'s ticket-key scan): a
  match against the configured `jira.high_priority_values`, `jira.incident_labels`, or
  `jira.incident_issue_types` raises the level exactly as a matched urgency label would. A PR with
  no cross-referenced Jira issue, or an unconfigured `jira` section, degrades to the base signal
  unchanged — the layered signal is additive only, never a replacement that could silently score a
  PR as "no urgency." The Slack incident signal is deliberately NOT carried yet (tracked
  separately) — only the Jira half is in scope this phase.
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
- **Panel placement** — five named panels (`team_awaiting_owner`, `team_awaiting_team`,
  `team_awaiting_me`, `mine_awaiting_me`, `mine_awaiting_team`), or no panel at all for a PR that
  is not open, a draft team PR, or a team PR with zero match reasons. Operator ruling, 2026-09-25
  (superseding the earlier act-now/blocked taxonomy): "Act Now" conflated "nothing is stopping you
  from looking at this" with "this needs YOUR action."

  A PR is **blocked** when CI is not green (`failure`, `pending`, or `none` all count — only
  `success` passes), the bot verdict is disapproved, a non-bot reviewer currently carries a
  `CHANGES_REQUESTED` review, or there is a merge conflict. Blocked always wins over every
  assignment/approval check below.
  - **Team**, once not blocked: if the operator is a requested reviewer and has not yet approved
    → `team_awaiting_me`; if the operator has already approved → `team_awaiting_owner` (the ball
    is back with the PR's owner or other reviewers). If the operator is not a requested reviewer:
    any existing human approval → `team_awaiting_owner`, otherwise → `team_awaiting_team`. Blocked
    → `team_awaiting_owner` (fixing CI/conflicts/disapprovals is the PR owner's job, not the
    reviewer's).
  - **Mine**, once not blocked: any unresolved review-thread comment → `mine_awaiting_me` (no
    author qualifier — an open thread is on the operator regardless of who left it). Otherwise, an
    existing human approval → `mine_awaiting_me` (nothing left to do but merge). Otherwise →
    `mine_awaiting_team`. Blocked → `mine_awaiting_me` (it's the operator's own PR to fix).

  **Known data gap:** pg-desk has no per-review head-SHA history, so "approved" here means "a
  currently `APPROVED` review exists," not "a non-stale one" — a self- or team-approval from
  before the PR's latest push still reads as satisfied. A future gather/store change to add
  per-review staleness would change this without changing the taxonomy above.

  **Known scope gap:** the taxonomy cannot distinguish "fully approved" from "partially approved"
  (no required-approver-count signal — no CODEOWNERS/branch-protection data is gathered), so mine
  has only two panels rather than a third "waiting on more approvals" bucket.

**Hidden and WIP are explicitly NOT interpreted.** `serve` and `open` join the `annotation` table
at read time, so a `hide` or `wip` call takes effect on the very next request, never waiting for
the next pipeline run. A hidden PR MUST be excluded from the five panel arrays and exposed only in
its own `hidden` array.

## Exit codes, telemetry, and logs

Interpret has no exit code of its own; like gather, it contributes to `run`'s exit code (`0` on
success or a degraded run, `1` only on a triggering-entity fetch or store failure — interpret
itself never introduces a new failure exit). It emits nothing over OpenTelemetry or Prometheus
through Phase 13 (D24; resolved by the observability review `pg2-7kizi`). Its activity is part of
`run`'s structured JSON stderr log and, under `--verbose`, the three-stage timeline.

## Out of scope

The project-health half of layered urgency, and everything Slack/thread-shaped (the Slack
incident signal, permalink cross-references) stay deferred — the Slack incident signal is today a
nil-disabled, LLM-assessed hook with no deterministic variant, so it carries no phase assignment
yet; project health has none assigned either. Draft auto-promotion, `wip on`'s upstream draft
conversion, and pending reply posting are accepted, recorded losses for this whole window —
`ready_to_promote` and `open --promotable` exist precisely so the operator can act on them by hand
instead.
