# Runbook: the Jira daemon cutover for the issue lane (fix forward, no rollback)

This page orders the operator's cutover of the `issue` lane's Jira side from the old stateless Jira
backend onto the Jira connector daemon. It states the order, the exact verbs with their exit
semantics, the checks before and after, and that there is no rollback. What each verb does is in the
behavior docs it links; this page is the order and the checks. It names the deployment repository only
generically: the deployment owns the values and the apply, and this page gives no apply command.

The backend and its daemon instance are both named `pg-connector-issue-jira` (the default of the
option `phillipgreenii.programs.pg-connector.jira.instance`). `pg-connector config show` prints the
resolved config file and the registered backends. Sample commands carry two placeholders:
`<jira issue source>` for the router query name of the Jira issue source, and `<configFile>` for the
daemon's config path.

## No shadow run, no rollback

There is NO shadow run and NO planned rollback to the old Jira instance: a failure is fixed forward.
This is the operator ruling, 2026-10-24, bead `pg2-sus2a`, verbatim: "pg2-sus2a doesn't need a
shadow, just build it as primary and we will test it that way. the current system isn't used much, so
don't waste time with shadow, just replace the existing with that we want it to be an fix forward."

`pg-desk issue changes --reset` is NOT a step of this runbook. It replays only the watched active
issues as `added` and is not a routine verb: it belongs to a step taken after a pg-desk store
move-aside, and only then (`docs/behavior/pg-desk/operator-commands.md`, "issue changes --reset").
Do not run it during this cutover.

## State directories

The new daemon's state directory is `~/.local/state/pg-connector-issue-jira-daemon`. It differs from
the old backend's `pg-connector-issue-jira`, so the two never share files: the flip leaves the old
backend's state behind and the daemon starts from its own baseline.

## What the flip changes

The flip is one option of the deployment repository:
`phillipgreenii.programs.pg-connector.jira.registry.rewrite = true`. Before it, the umbrella's
registry holds the OLD entry whose command is `pg-connector-issue-jira` with no `--config`. After it,
the registry holds the NEW entry whose command is
`[pg-connector-issue-jira, --config, <path>, --instance, pg-connector-issue-jira]`, so the umbrella
reaches the daemon. The daemon install and its watched-query declaration are already in place before
this page starts; only the flip is left.

Two consequences shape the order:

- Before the flip, `pg-connector issue capabilities` reaches the OLD backend, so it MUST NOT be used
  as the daemon check. The daemon is checked through its own status mode (step 1).
- The Jira client answers unavailable while the daemon is down (`INV-JIRA-PROC-2` stands). From the
  flip until the daemon is up and serving, an unpinned id-keyed read of a non-Jira id through the
  umbrella fails instead of reaching the beads backends. So the daemon MUST be confirmed to answer
  before the backfill or any refresh.

## Cutover order

0. Record the measurement, BEFORE anything changes. Three numbers and one saved output (see "Step 0:
   the measurement" below): the active Jira issue count, the watched-set size, the real-change rate
   under `fields_changed`, and `pg-desk doctor` saved as the before-list. Record them as the
   `MEASURED (Phase 4, before the flip)` comment (see "Recording a result").
1. Confirm the daemon answers, BEFORE the flip. Find `<configFile>` and run its status mode:

   ```bash
   launchctl list | grep pg-connector-issue-jira-daemon
   launchctl print gui/$(id -u)/<label>
   pg-connector-issue-jira --config <configFile> status --json
   ```

   The label is the last column of the first command. The second prints an `arguments = { ... }`
   block with one argv word per line; the config path is the word after `--config`. The third prints
   a JSON report with `health` and `queries[]` (a query carries `baseline_at` once its baseline is
   stored). Pass: exit `0` (healthy), or exit `2` (degraded) with every watched query holding a
   `baseline_at`. Exit `3` (down) stops the cutover: fix the daemon first.

2. Run `pg-desk doctor` and save the output as the before-list. It prints a `change_flow:` section;
   each issue change-flow gate prints one line `<gate>: ok`, `<gate>: FAIL (<what tripped it>)` or
   `<gate>: UNREACHABLE (<error>)`, and a `FAIL` or `UNREACHABLE` fails doctor. Doctor is EXPECTED to
   report the dispatch-check mismatch for the Jira instance before the flip: pg-desk's configured
   `owns_changes` is true for it, and the old stateless backend does not own its changes, so the
   start-up dispatch comparison logs a warning, stores it under
   `change_flow.dispatch_check.<instance>`, and a doctor gate fails. Keep the list of `FAIL` and
   `UNREACHABLE` lines.
3. The flip, which is the operator's apply. The operator applies the deployment change
   (`phillipgreenii.programs.pg-connector.jira.registry.rewrite = true`) by the deployment
   repository's own apply procedure; this page gives no apply command. The flip precedes the backfill
   because the backfill reads the daemon.
4. Clear the umbrella cache and ledger files of the Jira instance, at the switch, BEFORE the first
   backfill or refresh read goes through the daemon:

   ```bash
   pg-connector ledger clear --backend pg-connector-issue-jira
   ```

   It also drops that backend's cache files, so neither side serves the other's state. Pass: exit
   `0`. The clear is repeated at any registry re-switch (flipping `registry.rewrite` again, in either
   direction). The GitHub cutover (`docs/runbooks/pg-connector-github-cutover.md`) clears the same way
   and uses a marker file in the umbrella's state directory so the deployment clears once; remove the
   marker before a re-switch so it clears again.

5. Confirm the daemon answers through the umbrella, now that the registry reaches it:

   ```bash
   pg-connector issue capabilities
   pg-connector issue health --backend pg-connector-issue-jira
   pg-connector issue list --query <name> --ids-only --output json
   ```

   Pass: the capabilities call and the health call exit `0`. The list call, for each watched query
   name taken from `<configFile>` (step 0), returns the same ids as the daemon's list op
   (`packages/pg-connector/docs/behavior/jira/interfaces.md`). Do NOT go on to step 6 until this
   passes.

6. Backfill the two columns that existing pg-desk entity rows lack:

   ```bash
   pg-desk issue backfill-backend --json
   ```

   It reads the daemon (it lists each watched query's members and probes the configured owning
   backend instances), which is why it runs AFTER the flip. It takes no other flag and needs no id,
   appends no change row, bumps no version, and is safe to re-run. It exits `1` when a list read or a
   probe failed, so a partial backfill is never reported complete. Its `--json` payload is
   `{"updated": n, "failed": [...]}`. Pass: exit `0` with an empty `failed` array. On exit `1`, read
   the cause in `failed`, fix it, and re-run. See `docs/behavior/pg-desk/operator-commands.md`,
   "issue backfill-backend".

7. Refresh each active Jira issue once, AFTER the backfill:

   ```bash
   pg-desk issue refresh <id>
   ```

   once per active Jira issue (the enumeration is in "Step 0: the measurement", count (1)). Each
   refresh resolves the backend from the stored `backend` column. Pass: exit `0` for each. `--watch`
   is the one-off watch and is NOT part of this runbook. The number of calls is the active count of
   step 0, not the 3 to 4 entities an older sampling suggested.

8. Run `pg-desk doctor` again, and compare against the before-list of step 2. Pass: no `FAIL` or
   `UNREACHABLE` line names the Jira instance, and none appears that the before-list lacked. The
   dispatch-check mismatch of step 2 MUST be clear now.
9. The live check T-18, last. Its four legs, with the sample commands in placeholders:
   1. `pg-router run-query --json query:<jira issue source>` shows zero items in steady state.
   2. One edit to a WATCHED Jira issue yields exactly one `change_log` row and one decider run.
   3. An edit to an UNWATCHED Jira issue yields none.
   4. A change to a pull request linked to an unwatched Jira issue yields none.

   Record the result as a bead comment (see "Recording a result"). T-2 and T-19 pass for their Jira
   cases as the done condition below states.

## Step 0: the measurement

Three numbers are taken before the flip, each by the command that follows. All times are UTC, RFC
3339 in the record.

1. The active Jira issue count. Open the pg-desk store read-only (`pg-desk status` prints its path):

   ```bash
   sqlite3 -readonly <store path> "select entity_id from entity where entity_type='issue' and active=1;"
   ```

   The table `entity` has `entity_type`, `entity_id`, `active`, `backend` and `watched`. Keep the ids
   that match, anchored as `^<pattern>$`, one of the `ticket_patterns` of the pg-desk configuration
   (`^[A-Z][A-Z0-9]+-[0-9]+$` is a placeholder shape; the real list is the `ticket_patterns` key of the
   config file `pg-desk status` reports, see `docs/behavior/pg-desk/config.md` and
   `docs/behavior/pg-desk/run-issue.md`). A beads id matches none. Before the backfill `backend` is
   NULL, so the Jira test is the id shape. The count of kept ids is the number.

2. The watched-set size before the flip. List the daemon's watched queries from its config file:

   ```bash
   jq -r '.queries | to_entries[] | select(.value | type == "object" and .watched == true) | .key' <configFile>
   ```

   For each such query, the number is how many issue keys `pjira search --all --jql "<expr>"` returns
   for that query's `expr`; the watched-set size is the distinct union over all of them. A query's
   members are the ids the daemon's list op returns for it, so after the flip
   `pg-connector issue list --query <name> --ids-only --output json` returns the same.

3. The real-change rate. Count the rows of the DAEMON's `change_log` table (`store.sqlite` in the
   config file's `state_dir`; `at` is in epoch milliseconds and `kinds` is a JSON array) whose
   `kinds` includes `fields_changed`, for the watched ids, per day over the window from the earliest
   `baseline_at` (from step 1's status report) to now:

   ```bash
   sqlite3 -readonly <state_dir>/store.sqlite \
     "select count(*) from change_log c where exists (select 1 from json_each(c.kinds) where value='fields_changed') and c.at >= <earliest baseline_at in epoch ms>;"
   ```

   Divide by the window in days. If the window is under 24 hours, state the rate as PROVISIONAL.

Then save `pg-desk doctor` (step 2) as the before-list.

## Recording a result

Every live result is recorded as a bead comment on the phase bead `pg2-2qyyh.4`, written from a file
with `bd comment pg2-2qyyh.4 --file <file>` (text from a quoted heredoc or an editor, never
shell-substituted, so backticks and `$` survive). Each comment file is a fresh
`$HOME/.cache/p4-<NN>-<UTC yyyymmddThhmmssZ>.md`, `<NN>` being the two digits of the packet's own
title. Put every number the next step needs on its own line in the form `key: value`.

This repository is public, so a comment states counts and causes, never the deployment's own issue
keys, pull request numbers or query names. Where a difference must be named, name it by its row
position in a local file under `$HOME/.cache/` that the comment cites by path.

## Done when

Every Jira entity pg-desk holds has `backend` set and `watched` set exactly for those the daemon
reports as watched; an edit to a watched Jira issue yields one event and one decider run, and neither
an edit to an unwatched Jira issue nor a change to a PR linked to one yields any (T-18); T-2 and T-19
pass for their Jira cases; the old Jira instance is no longer in any registry list.

A Jira entity that no owning backend holds (one stored only because pull-request text named it) has
its `backend` set to the non-owning backend by the backfill's probe
(`docs/behavior/pg-desk/operator-commands.md`, "issue backfill-backend"); the done condition requires
only that `backend` is set (not NULL).
