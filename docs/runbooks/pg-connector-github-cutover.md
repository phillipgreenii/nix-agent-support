# Runbook: the GitHub daemon cutover for the PR lane, and its rollback

This page orders the operator's cutover of the PR lane onto the GitHub connector daemon and states
the rollback. The PR lane's cutover and the daemon program's cutover phase are ONE cutover, not two:
one relock and one apply carry both. What each mode of the backfill driver means is in
`docs/behavior/pg-decider/backfill.md`; how to run it is in `docs/runbooks/pg-pr-backfill.md`; the
daemon's own behavior is in `packages/pg-connector/docs/behavior/github/`. This page is the order
and the rollback, and names the deployment repository only generically (the deployment owns the
values and the apply).

## What the one apply changes

The deployment repository carries these edits together with the relock that brings in this
repository's change:

- The pg-router source registry has ONE PR source, `pr-changes`
  (`pg-router-source-pg-connector changes pr --consumer pr-changes --retry-window <window>`), in place of
  the two old-backend sources. It is an UNSCOPED consumer: it names no query, so the daemon's feed
  delivers a row for every watched PR whatever query lists it. It passes the same `--retry-window` as
  the sources it replaces. The deployment declares no watched query beyond the ones the earlier
  deployment apply already declared.
- `--fresh` is no longer passed on the PR read of a gather (`INV-GATHER-1` in
  `docs/behavior/pg-desk/gather.md`): the daemon owns the PR cache and its change detection
  (`INV-CACHE-8` in `packages/pg-connector/docs/behavior/invariants.md`). The `issue` reads and the
  head check (`pg-desk pr head-check`) keep `--fresh`. This is pg-desk code, so it travels with the
  relock, not with a deployment option. If the relock and the registry switch were NOT applied
  together, the old backend's PR reads would lose `--fresh` and could be served from its cache.
- The umbrella's cache and ledger files of the two GitHub instances are cleared once, at the apply,
  by the deployment, so neither side serves the other's state.
- The backfill driver reads the daemon's GraphQL budget gauge (`rate_source` is `daemon-metrics`)
  and its query-name file lists the daemon's watched queries.

## Cutover order

1. Check the preconditions: the daemon is running and has served the watched queries (the earlier
   deployment applies are in place), the backfill driver's plan pass has been reviewed, and the
   backfill driver is NOT running. A replay MUST NOT run while the driver runs.
2. Apply the deployment (relock first, with the change of this repository and the edits of the
   deployment together). The router now polls the single `pr-changes` source. Its first poll
   registers the consumer at the tail of the feed after every watched query has its baseline, so it
   answers nothing and sees none of the history (see "The cursor decision").
3. Run the marking step: `pg-pr-backfill mark-watched`. It marks the PRs that have an open tracker
   anchor as watched, hydrates them, and checks that the daemon's watched count equals the
   tracker's open-anchor count. Require exit `0` and a last line `DONE ... watched=N expected=N` with
   `N` non-zero (the step refuses to pass at zero equals zero). Read the `UNMAPPED` and `skip` lines
   (`docs/runbooks/pg-pr-backfill.md`, "Marking the open-anchor PRs watched").
4. Reset the new consumer with a replay, AFTER step 3 and only when the driver is not running:

   ```bash
   pg-connector pr changes --consumer pr-changes --reset --output json
   ```

   The answer lists every watched PR as `added`. Compare its set with the watched count of step 3.
   The call acknowledges what it printed, so keep its output: it is the record of the replay. The
   replay goes to the operator's call and NOT to the router's `pr.changed` queue (see "The replay
   does not reach the router"): the backfill driver's passes bring every followed PR through the
   composed command whatever the router saw.

5. Observe the live feed: the next poll of `pr-changes` answers only what changed since the replay;
   `pg-connector pr inspect <id>...` shows a PR as tracked or watched; the daemon's budget stays above
   the driver's floor (`graphql_remaining` gauge).
6. The post-apply verification closes the cutover. Two verify beads, gated on the apply, carry the
   checks (run by an agent): `pg2-g6stl` for the daemon enablement (daemon up under launchd with the
   4000 points/h budget, the registry on the daemon's argv, a live pg-router exercise with a
   non-trivial outcome, the ccpool precheck treating an unavailable connector as retryable, and the
   drain evidence recorded on the already-closed `pg2-xg2k8`) and `pg2-accquj` for the PR lane
   (registry, marker file, marking, replay). Only after both, and after the lane is declared stable,
   is the old backend removed.

## The cursor decision

The old backend's cursor is a position in its own ledger. It cannot carry into the daemon's sequence
numbers, so the cutover does not carry it: the new consumer `pr-changes` is a fresh consumer, reset
with a replay after the marking step.

- A consumer's first poll starts at the tail of the feed, taken only after every configured watched
  query has its baseline. Before that, the poll answers empty and creates no cursor (and the
  acknowledgement creates it), so a poll that races the first complete run neither starts at a
  premature tail nor skips rows.
- The replay lists only WATCHED PRs. A replay before the marking step therefore lacks every PR
  that only the marking step reaches; that is why the marking step comes first.
- The replay is a hydration burst: the daemon reports every watched PR as `added` once, to the
  caller of the reset. A re-run of the composed command on an unchanged view writes nothing, so a
  second replay of an unchanged view is the same set and writes nothing new.
- The replay MUST NOT run while the backfill driver is running: both read and write the same
  per-PR lane state.

Both properties are shown against the real daemon, client, umbrella and adapter by
`TestCursorDecision_MarkingThenReplayDeliversEveryWatchedPRAsAdded` and
`TestCursorDecision_AFirstUnscopedPollBeforeEveryBaselineAnswersEmptyAndCreatesNoCursor`
(`packages/pg-connector/cmd/pg-connector-github/cursor_decision_e2e_test.go`), each with a negative
case (a replay before the marking step lacks the directly marked PR; a poll before the baselines
creates no consumer row).

## The replay does not reach the router

Decision (2026-10-27): the router does NOT need to see the cutover replay on `pr.changed`, and
`pg-router-source-pg-connector` does not gain a `--reset` option. Evidence:

- The replay cannot reach the router by that route. The reset is run by the operator and its answer
  and acknowledgement belong to that call; the reset moves the consumer to the tail of the feed, so
  the router's next poll of `pr-changes` answers only what changed after it. The adapter is polled
  by the router on a schedule, so a `--reset` in the registry stanza would replay every watched PR on
  every poll, and a hand-run adapter call prints to the operator, not to the queue.
- Every PR the replay lists is also reached by the backfill driver, which does the same work through
  the composed command. `pg-pr-backfill enumerate` takes the tracker's open anchors plus the open
  members of the deployment's PR queries, which covers the watched PRs: the open-anchor PRs are the
  ones `mark-watched` marks, and the queries the daemon watches are the ones the driver lists
  (`docs/behavior/pg-decider/backfill.md`). The first apply pass over that whole set follows the
  cutover (the flip is plan, then apply: `docs/runbooks/pg-pr-backfill.md`), so it also covers a
  change that landed between the last old-backend poll and the consumer's first poll, the only
  rows the tail start skips.
- A PR watched only through a direct watch-option read, outside every anchor and query, is not in
  the backfill set; it is reached by the first change to it, as any watched PR is, and it was not
  followed before the cutover either.

Cost of the decision: the lane learns of a quiet PR at the apply pass, not at the replay. If a future
cutover needs the router to see a replay, inject it with the router's own operator command
(`push-inject`), not through the adapter.

## Rollback

Rolling back is a deployment edit plus an apply, not a runtime toggle. In this order:

1. Put the registry back to the two old-backend sources (`pr-mine` and `pr-team`) in place of
   `pr-changes`, and set the backfill driver's `rate_source` back to `old-backend-log`.
2. Restore `--fresh` on the PR gather read (a revert of the single change in
   `packages/pg-desk/internal/gather/gather.go`), in the SAME relock and apply as step 1. Only the
   `pr` read changes: the `issue` reads and the head check keep `--fresh` in both directions.
3. Clear the umbrella cache and ledger files of the two instances again, so neither side serves the
   other's state: `pg-connector ledger clear --backend <pr instance>` and
   `pg-connector ledger clear --backend <ci instance>` (each also drops the cache files of that backend).
   The deployment clears once, at the first apply, and records that in a marker file in the
   umbrella's state directory; remove the marker before a second cutover so it clears again.
4. Leave the daemon's watched flags in place: they are sticky and harmless with no consumer.
5. With its ledger cleared, the old backend's query-diff feed reports every member as `added` once, a
   hydration burst that the lane absorbs.

The lane mode is independent of the cutover: the `pr-change` role stays in whatever mode the flip
left it. `pr-sweep` and `pr-sweep-full` (and the `pr.reconcile` event type) are NOT restored by a
rollback: the PR lane refactor removed them for good, so a rollback has no sweep to bring back. The
posted-review sidecar is untouched.

## Shadow criteria

The cutover went ahead with one unmet shadow criterion (daemon uptime, 36.4 percent of the slots,
host reboot downtime with the daemon healthy on every sampled tick). The operator ruled the
criterion waived (2026-10-23), so no follow-up bead exists for it; the decision is recorded on the
daemon program's cutover bead. Every other criterion of the shadow report was met
(`docs/runbooks/pg-desk-shadow-compare.md`). The ruling also supersedes the earlier plan for a
follow-up bead on the uptime criterion: there is none. The post-apply checks are the verify beads
named in step 6 of "Cutover order".

## Plugin documentation check

The cutover needs no edit to the plugin documentation. The check is a search for any file under
`claude-marketplace` that calls `pg-connector pr` or `pg-connector ci`:

```bash
rg -n 'pg-connector (pr|ci)\b' claude-marketplace
```

Its result at the time of writing was no output and exit status `1`: no plugin file makes that call,
and the retired PR plugin is gone and is not reintroduced. Had a file made the call, the only edits
would be docs-only: an unavailable connector is retryable, and `pg-connector pr refresh <id>` (or
`--fresh`) gives read-your-own-write. Re-run the search before a cutover if plugins changed since.
