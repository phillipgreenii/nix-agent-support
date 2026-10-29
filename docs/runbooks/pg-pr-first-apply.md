# Runbook: the PR lane's first apply (plan, flip, apply pass, verify, rollback)

The procedure that brings a deployment from the legacy PR path to the PR lane: the `pr-change` role
runs in `plan` mode first, the backfill driver plans every followed PR once, an operator reads the
plan, and only then are the role and the lane mode key flipped to `apply` and every PR is applied
once. Design: the PR lane refactor, "Adoption, backfill and the first apply"; the driver's own
behavior is in [`docs/behavior/pg-decider/backfill.md`](../behavior/pg-decider/backfill.md) and its
commands and configuration keys in [`pg-pr-backfill.md`](pg-pr-backfill.md). This page is the numbered
procedure with the commands filled in.

The key words MUST, MUST NOT, SHOULD and MAY in this page are RFC 2119 words.

**Who does what.** An agent MAY build and rehearse everything on this page against a test tracker (the
rollback drill of step 8 is an automated check). Steps 3, 5 and 6 are the operator's: an agent MUST NOT
flip the role or the lane mode key to `apply`, and MUST NOT run the apply pass.

## Before you start

- `pg-pr-backfill`, `pg-pr-change`, `pg-connector` and `bd` on `PATH`, and `PG_DECIDER_CONFIG` set as
  for the `pr-change` role. The driver configuration file (`PG_PR_BACKFILL_CONFIG`, default
  `$XDG_CONFIG_HOME/pg-pr-backfill/config.json`) names the query-name file, the tracker directory
  (`beads_dir`) and the rate source; every key is in [`pg-pr-backfill.md`](pg-pr-backfill.md).
- The machine awake for each pass. A pass that is interrupted resumes.
- **The run record is a record of the lane, not a log to rotate.** The run record
  (`$XDG_STATE_HOME/pg-pr-change/pr-change-runs.jsonl`, or the file `run_record` names) MUST NOT be
  rotated, truncated or deleted before the plan summary of step 3 has been consumed, and MUST be kept
  until the operator declares the lane stable. The plan summary, the resume of a pass, the residual
  check and the listing of writes of step 8 all read it. The generic log rotator does not list it; do
  not add it to one.
- **Plan age bound: 48 hours.** A plan-mode record older than 48 hours MUST be re-planned before the
  flip (`pg-pr-backfill run --mode plan --new-pass`, then the summary again). This is the default of
  the summary's `--max-plan-age` and of the configuration key `max_plan_age`; the summary flags an
  older record and exits `20`.

## The procedure

### 1. Deploy the role in `plan` mode

Deploy the `pr-change` role with its mode word `plan` and set the pg-desk configuration key
`pr_lane.mode` to `plan`, in one deployment apply (the deployment declares both from one option, so
they cannot disagree). The role decides and records; it writes no work item and no escalation.

Check what is live:

```bash
pg-desk status          # the "pr lane" line reads: mode plan
pg-desk doctor
```

### 2. Backfill: plan every followed PR

```bash
pg-pr-backfill enumerate                      # the set; fails closed on an empty set or query-name file
pg-pr-backfill run --mode plan                # paced; the last line is DONE, FLOOR or FAILED
```

Run `run --mode plan` again after `FLOOR` (wait for the GraphQL window) or after `FAILED` with exit
`11`; it resumes and skips finished PRs. Go on when it prints `DONE`. When the daemon's marking step
has landed, `mark-watched` ran then and is not repeated here. The meaning of each last line and exit
code is in [`pg-pr-backfill.md`](pg-pr-backfill.md).

### 3. Review the plan summary (the operator)

```bash
pg-pr-backfill summary
```

Pass conditions, all of them:

- coverage is complete: the PRs enumerated equal the PRs with a plan-mode record, and none is named as
  lacking one (a failed run, an unhydrated PR and a PR the forge reports gone are named);
- no PR would get a second anchor;
- no `create` the operator cannot explain;
- every unadoptable item has a disposition of `ADOPT` or `CLOSE` with a reason. There is no "leave": a
  keyless item left alone is duplicated by the first apply, which the no-duplicates rule forbids;
- no plan record is older than the 48-hour bound above.

The operator writes the expected number of creates per kind, from the summary, as a comment on the
epic bead. That number is the stop criterion of step 6. If a flood of review, fix-ci or conflict items
is a concern after the review, the operator MAY keep the role in `plan` mode longer.

### 4. Run the end-to-end gate and check the alerts

Run the end-to-end decider suite against real binaries, in the background through the wrapper. The
fast run (over the fake `bd`) is the check; the soak runs the same scenarios over a real `bd` and takes
tens of minutes, so it is run by hand, from the repository root, and only here, before the first apply:

```bash
bgrun lane-fake -- pg-nix-log-wrapped nix build .#checks.<system>.pg-decider-lane-fake -L
bgwait lane-fake --timeout 540       # with a Bash timeout of 600000; call it again on exit 2
bgrun lane-soak -- pg-nix-log-wrapped nix run .#pg-decider-lane-e2e-soak
bgwait lane-soak --timeout 540       # call it again on exit 2 until it is done
```

Each MUST end `DONE exit=0` with `scenarios, each with a PASS line, none skipped`; that includes the
rollback drill of step 8.

Register the lane's new alert uids with the router probe (the deployment's probe uid lists, together
with the relock of this repository, so the deployment check does not fail on an absent uid), which
empties the deferred-registration allowlist. The backfill of step 2 ended the primary alert's
expected-red window, so registering it now files no bead. Then confirm that the primary alert
(`pg-desk-pr-never-hydrated`) is green.

### 5. Flip (the operator)

First record the tracker's before-image. The rollback of step 8 needs it, because an audit comment
names the rule and the time of a write but not the value an update replaced:

```bash
pg-pr-backfill snapshot                       # refuses once a flip marker or an apply record exists
```

Then write the flip marker, just BEFORE the deployment apply that changes the modes, with the epic
bead's id:

```bash
pg-pr-backfill flip-marker --time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --epic-bead <epic-id>
```

The marker is a row in the run record (the authoritative copy) and a comment on the epic bead. It
bounds the listing of step 8: the bound is the EARLIER of the marker's time and the first apply-mode
run record, so a write that a live `apply` run made between the deployment apply and the marker is
still found. It is NOT the lane-level flip time that pg-desk shows on its `pr lane` line (a display
value that follows the configured mode).

Now change the `pr-change` role's mode word and `pr_lane.mode` to `apply` in one deployment apply, and
check `pg-desk status` reads mode apply.

### 6. Apply pass (the operator)

```bash
pg-pr-backfill run --mode apply --max-creates <kind>=<n>,<kind>=<n>
```

with the numbers recorded in step 3 (for example `anchor=12,review-pr=9`; a kind not named has the
limit 0). The limits count only the creates this pass makes, not those of live router runs. Run it again after `FLOOR` or after `FAILED` with exit `11` until `DONE`; a run that ends
`transient` or `degraded` is not a stop. A PR that is merged or closed is handled by the normal closure
rules in this pass.

**Stop at once, and go to step 8, when** the driver ends `FAILED` with exit `12` (`stop=` says why: a
create limit exceeded, a run that ended `unauthenticated` or `decide_failed`, an unreadable rate
signal). Do not run it again.

### 7. Verify

```bash
pg-pr-backfill summary --mode apply                          # a residual of zero for every enumerated PR
pg-pr-backfill run --mode apply --new-pass --max-creates anchor=0   # the second pass writes nothing (router creates do not count)
pg-pr-backfill writes                                        # no write after the first pass's
pg-pr-change apply <one real open anchor's PR id>            # re-run by hand: it wrote nothing
```

Also confirm the primary alert (`pg-desk-pr-never-hydrated`) and the residual alert
(`pg-desk-pr-residual-after-apply`) are green. The first apply is verified when all of this holds.

### 8. Rollback

This replaces the earlier rollback ("restore the store backup"), which fix-forward rules out, and it
refines "closing the beads created since the flip", which describes the creates only: a close or reopen
the first pass made is not undone by closing created beads, so the listing is by timestamp across
every kind of write.

First stop further writes: set the `pr-change` role's mode word and `pr_lane.mode` back to `plan`, in
one deployment apply. Then:

```bash
pg-pr-backfill rollback                       # a dry run: the by-timestamp listing, then the reverts it would make
pg-pr-backfill rollback --apply               # makes them, then verifies the tracker; the last line is ROLLBACK DONE or FAILED
```

The listing is read from the decider's audit comments on the work items (each names the rule, the
operation and the time), from the flip bound of step 5 onward, so it covers creates, closes, reopens
and head or key updates alike. Each write is reverted by its inverse:

| Write    | Revert                                                                                                              |
| -------- | ------------------------------------------------------------------------------------------------------------------- |
| `create` | close the created item (a tracker has no delete)                                                                    |
| `close`  | reopen it, unless it is no longer closed                                                                            |
| `reopen` | close it, unless it is no longer open                                                                               |
| `update` | put back the title, priority, description, labels and metadata of the before-image taken in step 5 (not the status) |

The revert is by write, not a restore of the whole item, so a change a person made since the flip is
not undone: a status a person changed is left (the listing says `left as it is`), and an item whose
status a person set is not reopened. Every item the rollback changed gets one comment starting
`pg-pr-backfill rollback`, and a second `rollback --apply` lists nothing (it is a no-op). It refuses,
exit `33` and nothing written, when the snapshot is missing, was taken after the flip bound, or does
not agree with the audit comments (an item updated but absent from it, or created but already in it).

After a rollback and before a second flip, take the before-image again with `pg-pr-backfill snapshot
--force` and write a new flip marker.

The rollback is rehearsed against a test tracker by the end-to-end gate of step 4, scenario
`a-rollback-drill-lists-every-write-since-the-flip-marker-by-timestamp-and-reverts-each`, with the real
tracker and the real composed command. Run it before any production flip.

## Operating notes

### The lookup ceiling

Every `pr` run lists the work items of all kinds in every status and fails closed (exit 4, nothing
written) when the listing holds more than `work_items_ceiling`. Closed items only accumulate, so the
listing only grows and the lane WILL reach any fixed ceiling. The decider reports the item count and
the ceiling on every run, and the alert on the count over a fraction of the ceiling is the warning.
The remedy, in this order:

1. Raise `work_items_ceiling` in the decider configuration (`PG_DECIDER_CONFIG`; the deployment sets
   it at about twice the listing size measured against the real tracker).
2. Or compact closed items with the tracker's own cleanup (`bd admin cleanup --older-than <days>
--dry-run` first, then `--force`). Deleting a closed review item of a PR that is still open lets
   the lane create a review item for the same head again: only items of PRs that are terminal on the
   forge MAY be deleted.

### The run record

Retention is stated above (not rotated away before the plan summary is consumed, kept until the
operator declares the lane stable). The pg-desk log source registers the file but sets no rotation,
and the file is on no rotation list. When the operator declares the lane stable, the file MAY be
rotated by an ordinary tool; until then it MUST NOT.
