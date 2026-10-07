# pg-desk — interpret

Interpret is stage 2 of the pipeline (see [`pipeline-run.md`](pipeline-run.md)). Every
interpretation MUST be a deterministic function of store rows, computed with an injectable clock,
and MUST NOT use an LLM for any step below.

## Steps this phase implements

- **Ownership** — classifies a PR as mine, co-owned, or team, from config and the commit-author
  logins `pr commits` reported.
- **Enrichment** — derives kind, languages, and size from `pr files` and `pr commits`, and carries
  three display facts copied verbatim from the PR facts: its title, its URL, and its `author` (the
  PR author's login, bot logins included; the empty string when the PR facts are absent). The
  `author` fact is the PR's author, not its ownership class — a PR classified as co-owned still
  reports one author. The dashboard payload served per [`serve.md`](serve.md) carries these on each
  row, so a human-facing view can show who owns a team PR.
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
- **Ready-to-promote** — a stored flag (own PR, not co-owned, draft, not WIP, checks green — judged
  by the same review-exempt rule as the owner-side "blocked" below, and NOT softened by the
  reviewer-only cancelled-run and no-CI-data rules, so an own draft whose only run was cancelled
  or that has no CI data is never ready — no bot disapproval, no merge conflict). It is recorded,
  not acted on — see "Out of scope" below.
- **Panel placement** — five named panels (`team_awaiting_owner`, `team_awaiting_team`,
  `team_awaiting_me`, `mine_awaiting_me`, `mine_awaiting_team`), or no panel at all for a PR that
  is not open, a draft team PR, or a team PR with zero match reasons. Operator ruling, 2026-09-25
  (superseding the earlier act-now/blocked taxonomy): "Act Now" conflated "nothing is stopping you
  from looking at this" with "this needs YOUR action."

  A PR is **blocked** when CI is failing or absent (`failure` or `none` — `success` and
  `pending` do not block; a PR still waiting on CI falls through to the approval checks below,
  operator ruling 2026-10-01), the bot verdict is disapproved, a non-bot reviewer currently carries a
  `CHANGES_REQUESTED` review, or there is a merge conflict. For an **own** PR, blocked always wins
  over every assignment/approval check below. For a **team** PR, the blocked test differs in three
  ways (operator ruling 2026-10-05, reversing parts of 2026-10-01): a cancelled run that masks no
  real failure does not block; no CI data (`none`) does not block (it reads as `pending`); and a
  bot disapproval does not block a PR the operator was asked to review. These reviewer-only
  softenings never apply to the own-PR panels or to ready-to-promote.
  - **Review-exempt checks.** The configuration key `review_exempt_checks` (default empty) lists
    CI job names whose failure alone does not make a PR unreviewable (operator ruling 2026-10-02).
    A failed run is _exempt_ only when pg-desk can prove it: the run's per-job results were
    gathered, at least one job failed, and every failed job's name is in the list. When every failed
    run on the head commit is exempt, the PR is NOT blocked by CI: it is treated as `pending` when
    other runs are still in flight and as `success` otherwise, so it falls through to the approval
    checks below (and, for an own draft, can be ready-to-promote). The CI rollup itself is
    unchanged: the PR's CI state, its CI display, its `ci-failing` urgency signal, and its `build`
    links (see [`links.md`](links.md)) still report the failure. Matching is exact and
    case-sensitive against a **job** name: no patterns, no substrings, and a run (workflow) name is
    never matched. Job results are optional in the gathered CI facts (only a completed, non-passing
    run on the head commit carries them, and fetching can fail), so a failed run with no job data
    is _not provably exempt_ and keeps the PR blocked; so does a failed run that reports no failed
    job. Any other failed run, or any other blocker (bot disapproval, requested changes, merge
    conflict), still blocks. With an empty list the behavior is exactly the one described above.
  - **Cancelled runs (team PRs only).** A run whose conclusion is `cancelled` is a cancellation,
    not a verdict about the code: it usually comes from a concurrency supersession or someone
    stopping the run, and the PR owner has nothing to fix. For a team PR it does not make the PR
    unreviewable, with one guard: if the run's gathered job results show a job that really failed
    (neither cancelled nor exempt) before the cancel, the run is a real failure and blocks. A
    cancelled run whose jobs were not gathered is treated as harmless — the one case where absent job
    data softens a failure, because a cancellation is a distinct category. Because only the newest
    run of each workflow counts, a newest cancelled run also supersedes an older real failure of the
    same workflow. When every failed run on the head commit is exempt or harmlessly cancelled, the
    team PR is treated as `pending` when other runs are in flight and as `success` otherwise. The CI
    rollup itself is unchanged: the CI state still reads `failure`, the `ci-failing` urgency signal
    fires, and the `build` link ([`links.md`](links.md)) is still produced.
  - **No CI data (team PRs only).** A rollup of `none` means pg-desk counted no run on the head
    commit. That conflates "this PR has no CI" with "pg-desk saw no data" (the connector can return
    nothing for a PR whose checks all pass), so it is not a failure and does not block a team PR.
    The own-PR panels keep treating it as not green.
  - **CI green** is judged only from the PR's current head commit. Workflow runs from earlier
    pushed commits are ignored — GitHub cancels a superseded commit's in-flight runs, and those
    cancellations are not failures of the current state. Within the head commit only the newest
    run of each workflow (highest attempt, then highest run id) counts. A run with no commit SHA,
    or a PR whose head commit is unknown, is counted as before.
  - **Bot disapproval** from a comment verdict requires Findings `Problems`. Clean + Approved is a
    bot approval; Clean + Withheld (e.g. "No issues found" with auto-approval blocked because an
    app is not opted in) is a policy limit, not a review finding, and contributes no verdict. The
    most recent definite verdict comment wins.
  - **Human approval** (`human_approvers` / `human_approved`) and the non-bot
    `CHANGES_REQUESTED` check never include any bot (operator ruling 2026-10-02): a bot-only
    approval leaves `human_approved` false, and when a bot and a person both approve,
    `human_approvers` counts only the person. Stored review data carries only the reviewer's
    login (no account type), so a login is a bot when it is in `approver_allowlist` (even if it
    does not look like a bot), ends in `[bot]`, or is one of a small set of known bots GitHub
    reports without that suffix on reviews (`github-actions`, `dependabot`,
    `copilot-pull-request-reviewer`). A bot's approval is carried by the bot verdict instead.
  - **Team**, evaluated in this order:
    1. **Hard-blocked** — CI failing (judged as described under the cancelled-run and no-CI-data
       rules above), a non-bot reviewer's `CHANGES_REQUESTED`, or a merge conflict →
       `team_awaiting_owner`, even when the operator is a requested reviewer: fixing those is the PR
       owner's job, not the reviewer's. (A reviewer's own `CHANGES_REQUESTED` therefore still beats a
       later re-request; that is existing behavior, not part of the 2026-10-05 ruling.)
    2. **Requested of the operator** → `team_awaiting_me`, whether or not they already approved
       (GitHub drops a reviewer from the requested list once they submit any review, so a
       requested-and-already-approved operator means the PR author re-requested a look, usually after
       new commits; a live re-request wins over the prior approval). A bot disapproval does not
       override a live request: the review bot's own comment states that its review does not satisfy
       code-owner requirements and human review is still required, so its verdict is advice to a
       human reviewer, not a gate (operator ruling 2026-10-05).
    3. **Bot disapproved** (operator not requested) → `team_awaiting_owner`.
    4. **A human approval exists** → decided by GitHub's merge state, because an approval count says
       nothing about whether the PR is ready (operator ruling 2026-10-05): `BLOCKED` →
       `team_awaiting_team` (merge requirements are still unmet, normally outstanding required
       reviews), except while any CI run is still in flight, when `BLOCKED` is plausibly just the
       pending check and the PR stays `team_awaiting_owner`; `UNKNOWN` or absent → fall back to the
       weaker proxy "a review request is still outstanding" → `team_awaiting_team`, otherwise
       `team_awaiting_owner`; any other state (`CLEAN`, `HAS_HOOKS`, `UNSTABLE`, `BEHIND`) →
       `team_awaiting_owner` (nothing is left but the owner merging or updating the branch). A
       conflict (`DIRTY`) is already a hard blocker above.
    5. **Otherwise** → `team_awaiting_team`.
  - **Mine**, once not blocked: any unresolved review-thread comment → `mine_awaiting_me` (no
    author qualifier — an open thread is on the operator regardless of who left it). Otherwise, an
    existing human approval → `mine_awaiting_me` (nothing left to do but merge). Otherwise →
    `mine_awaiting_team`. Blocked → `mine_awaiting_me` (it's the operator's own PR to fix). The
    own-PR panels are unchanged by the 2026-10-05 team rulings: they use only the review-exempt
    softening, never the cancelled-run or no-CI-data softening, never the merge-state rule, and a
    bot disapproval still blocks.

  **Known data gap:** the panels carry no staleness axis, so "approved" here means "a currently
  `APPROVED` review exists," not "a non-stale one" — a self- or team-approval from before the PR's
  latest push still reads as satisfied. The stored approvals DO record, separately, whether the
  operator's own review has gone stale behind a push (`self_review_stale`) and whether a human
  teammate's approval of the current head stands (`human_approval_standing`), derived from the
  commit each review was submitted against; only the `pr.review-stale-after-push` attention rule
  (see [`attention.md`](attention.md)) reads them, and they never move a PR between panels.

  **Known scope gap:** pg-desk gathers no required-approver count or CODEOWNERS data. The team side
  works around that with GitHub's own merge state (above); the own-PR side has no such signal, so
  it cannot distinguish "fully approved" from "partially approved" and has only two panels rather
  than a third "waiting on more approvals" bucket.

  **Merge-state limits.** The merge state is a snapshot from the PR's last full hydration, not a
  live read: it is not a change signal (the entity change flow deliberately ignores it), GitHub
  computes it lazily so it can read `UNKNOWN` right after an event, and a settled value can lag by
  up to the sweep interval. The error is benign — both `team_awaiting_owner` and
  `team_awaiting_team` are outside the default `open` view. `BLOCKED` can also come from required
  conversation resolution, which is the author's job; the taxonomy does not distinguish that from
  outstanding reviews. Whether GitHub reports `BLOCKED` or `BEHIND` when a branch is both behind and
  unreviewed is not verified; both are best-effort.

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
