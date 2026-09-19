# pg-desk — gather

Gather is stage 1 of the pipeline (see [`pipeline-run.md`](pipeline-run.md)). It MUST call only
`pg-connector` — never `gh`, `bd`, `pjira`, or any other system client, per the composition rule
(D10) — and MUST read only the ids it is handed plus what the store already links; it MUST NOT
widen its scope to survey. Each fact lands in the `entity` table stamped with the `AsOf` time
`pg-connector` reported.

## Phase 9 inputs

Of the design's full input set, this phase gathers: `pr show`, `pr files`, `pr commits`, `ci
list` for the PR, `issue list --query work-beads` matched to the PR by metadata or title key, and
`issue deps --full` (for waiting-on-me). Every `pg-connector issue` exec — in gather, and later in
sync — MUST carry the beads backend's workspace variable (`PG_CONNECTOR_ISSUE_BEADS_DIR`,
falling back to `BEADS_DIR`) for the PR's own repository, because that backend refuses to run
without one.

**Per-run budget.** `pr files` and `pr commits` are keyed by `head_sha` in the store and are not
re-fetched when the head is unchanged. A `sweep` run skips stage 1 entirely for an entity whose
content hash is unchanged since its last run.

**Degradation.** A gather failure on any input other than the triggering entity itself degrades
that run: interpret proceeds with whatever it has, the interpretation is marked `degraded` naming
the failing input, and the run still exits `0` to pr-pool. A backend answering `unavailable` is
such a degradation. Only a failure to fetch the triggering entity itself is a hard failure (see
[`pipeline-run.md`](pipeline-run.md) for the resulting exit code).

**`--change removed` handling.** Gather re-reads the triggering entity with `pr show`, and the
result decides what "removed" meant: `open` means the PR merely left the named query (only query
membership and match reasons update); `merged` or `closed` is a confirmed closure; `not_found` is
treated as a closure with reason `gone`, and the run exits `0`. An id the store does not know
about is a no-op.

## Phase 13 input: the Jira ticket-key scan

Phase 13 (docket `pg2-2j5ac.40`) adds a seventh input to the same PR-gather call above, for the
`pr`-triggered path only: every Jira ticket key found in the triggering PR's branch name, title,
and body — recognized by the configured `ticket_patterns` list, never a hardcoded pattern — is
looked up with `issue show <ticket-key>` (fanned out across every registered issue backend by
`pg-connector` itself, exactly like any other `issue show` call; gather does not pin `--backend`).
Each recognized key is recorded two ways:

- As a cross-reference row (`repo`, `pr`, `<pr-id>`, `issue`, `<ticket-key>`, evidence), written
  immediately as the key is recognized — one write per `(key, field)` pair, so a key found in more
  than one of branch/title/body is written once per field it appears in. This happens regardless
  of whether the paired `issue show` call below succeeded: the row records that the PR's own text
  references the key, independent of whether the tracker currently answers for it.
- Into the gathered facts, keyed by ticket key, when `issue show` succeeds — consumed by
  interpret's layered urgency signal (see [`interpret.md`](interpret.md)) without a second gather,
  including when the SAME stored facts are later re-interpreted by `run issue <jira-ticket-key>`
  (see [`run-issue.md`](run-issue.md)), which never re-gathers.

An unconfigured (empty) `ticket_patterns` recognizes no key at all — this input then makes no
calls, exactly its pre-Phase-13 behavior; this is a safe default, not a regression. A `not_found`
answer from `issue show` is a well-formed negative answer (the cross-reference row is still
written), not a degradation; any other failure degrades this run exactly like every other
non-triggering-entity input, naming `issue show`.

## Phase 13 input: linked threads (eighth input)

Phase 13's Slack-half sibling packet ("pg-desk run thread") adds an eighth input, in the same
place as the seventh (the `pr`-triggered path only; never on a `--change removed` re-read or a
sweep-unchanged skip): a passive STORE READ of every thread already cross-referenced to the
triggering PR (the `xref` table's forward lookup, `from_type="pr"`/`to_type="thread"` — the reverse
of [`run-thread.md`](run-thread.md)'s own xref writes). This is a store read ONLY — gather never
calls `pg-connector-thread-slack` itself; `run thread`'s own active cross-referencing, triggered by
the thread-me feed, is what populates these links in the first place.

Populated into the gathered facts as, at minimum, each linked thread's own id. A fresh fetch of the
full `Thread` entity (for its permalink) is a known, documented gap in this phase: no packet
through Phase 13 persists a thread's own facts anywhere this read could recover a permalink from
without a live `pg-connector-thread-slack` call, which this input is deliberately forbidden from
making. A read failure degrades this run exactly like every other non-triggering-entity input,
naming `linked threads`; zero linked threads is a normal, expected outcome (most PRs have none),
not a degradation.

## Exit codes, telemetry, and logs

Gather has no exit code of its own; it contributes to `run`'s exit code (`0` on success or a
degraded fetch, `1` only when the failing fetch is the triggering entity itself — see
[`pipeline-run.md`](pipeline-run.md)).

Gather emits nothing over OpenTelemetry or Prometheus through Phase 13 (D24; OpenTelemetry export
is a later observability item, resolved by the observability review `pg2-7kizi`). Its activity is
part of `run`'s structured JSON log line to stderr; a degraded input (including a Jira `issue
show` failure, named `issue show`, or a linked-threads read failure, named `linked threads`) is
reported there, and `--verbose` includes it in the three-stage timeline.

## Out of scope

The ACTIVE half of thread cross-referencing — scanning a thread's own permalink/text for PR/issue
references and writing the resulting xref rows — is [`run-thread.md`](run-thread.md), not gather;
gather's own eighth input above is a passive read of links that command already wrote. Gather
targets exactly one configured repository; multi-repository gather is out of scope.
