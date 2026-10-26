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
   The call acknowledges what it printed, so keep its output: it is the record of the replay, and the
   backfill driver's passes bring every followed PR through the composed command whatever the
   router saw.

5. Observe the live feed: the next poll of `pr-changes` answers only what changed since the replay;
   `pg-connector pr inspect <id>...` shows a PR as tracked or watched; the daemon's budget stays above
   the driver's floor (`graphql_remaining` gauge).
6. The post-apply verification of the deployment's verify bead (the same checks, run by an agent)
   closes the cutover. Only after it, and after the lane is declared stable, is the old backend
   removed.

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
- The replay is a hydration burst: the old backend's replacement reports every watched PR as
  `added` once. That burst is accepted, and the lane absorbs it because a re-run of the composed
  command on an unchanged view writes nothing. A second replay of an unchanged view is the same set
  and writes nothing new.
- The replay MUST NOT run while the backfill driver is running: both read and write the same
  per-PR lane state.

Both properties are shown against the real daemon, client, umbrella and adapter by
`TestCursorDecision_MarkingThenReplayDeliversEveryWatchedPRAsAdded` and
`TestCursorDecision_AFirstUnscopedPollBeforeEveryBaselineAnswersEmptyAndCreatesNoCursor`
(`packages/pg-connector/cmd/pg-connector-github/cursor_decision_e2e_test.go`), each with a negative
case (a replay before the marking step lacks the directly marked PR; a poll before the baselines
creates no consumer row).

## Rollback

Rolling back is a deployment edit plus an apply, not a runtime toggle. In this order:

1. Put the registry back to the two old-backend sources (`pr-mine` and `pr-team`) in place of
   `pr-changes`, and set the backfill driver's `rate_source` back to `old-backend-log`.
2. Restore `--fresh` on the PR gather read (a revert of the single change in
   `packages/pg-desk/internal/gather/gather.go`), in the SAME relock and apply as step 1.
3. Clear the umbrella cache and ledger files of the two instances again, so neither side serves the
   other's state: `pg-connector ledger clear --backend <pr instance>` and
   `pg-connector ledger clear --backend <ci instance>` (each also drops the cache files of that backend).
   The deployment clears once, at the first apply, and records that in a marker file in the
   umbrella's state directory; remove the marker before a second cutover so it clears again.
4. Leave the daemon's watched flags in place: they are sticky and harmless with no consumer.
5. With its ledger cleared, the old backend's query-diff feed reports every member as `added` once, a
   hydration burst that the lane absorbs.

The lane mode is independent of the cutover: the `pr-change` role stays in whatever mode the flip
left it. The retired sweep roles are NOT restored by a rollback; they were removed for good with the
lane refactor. The posted-review sidecar is untouched.

## Shadow criteria

The cutover went ahead with one unmet shadow criterion (daemon uptime, 36.4 percent of the slots,
host reboot downtime with the daemon healthy on every sampled tick). The operator ruled the
criterion waived (2026-10-23), so no follow-up bead exists for it; the decision is recorded on the
daemon program's cutover bead. Every other criterion of the shadow report was met
(`docs/runbooks/pg-desk-shadow-compare.md`).
