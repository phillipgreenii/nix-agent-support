# drain-stuck reference: CONTAINER PARK

Read in full when a container hit under release policy `park`. Moved verbatim from `SKILL.md`; `ID`, `<id>`, `<work-branch>` and the step numbers are defined there.

## CONTAINER PARK (a container hit under release policy `park`)

The heading keeps its historic name; the exit is an ESCALATION (step 7a), because under
release policy `park` no path may leave the bead `open`, unclaimed and ready, and
`escalated` is what keeps it out of the router's drain-ready query.

Reached ONLY when `pb:drain-one`'s container probe fired and the caller's release policy
is `park`: the caller passes the probe evidence (which check fired, the note verbatim or the
child ids, statuses and priorities, and the container's own priority). A container-parent has
no work of its own, so the other exits do not fit. CONVERT-TO-DEPENDENCY would read the open
children as "every live blocker is a bead" and wire the D-9 deadlock (a parent blocked by its
own child). CONVERT-LANDING-TO-BEAD, DEFER-ON-EVENT and any other defer hide the whole subtree (**D-9**). CLOSE-AS-MOOT's
isolation steps do not apply. So:

1. Run step 2 FRESHNESS with the children-state probe only
   (`bd list --parent <id> --status all -n 0 --json`, plus the `sibling-open?` reading per
   child). SKIP steps 1, 3 and 4, and the CONVERT-TO-DEPENDENCY, DEFER-ON-EVENT and
   CLOSE-AS-MOOT exits. Run step 5's repeat detection against
   `PRECONDITION-KEY: container-not-retyped`.
2. Write the 6a comment with the probe evidence and the `FRESHNESS:` line. Do NOT include the
   branch or worktree sentence, and do NOT name a pg-router branch or a worktree path. End it
   with what removes the bead from the drain-ready query (`bd update <id> -t epic`, close it, or
   drop `has-acceptance-criteria`) and carry `PRECONDITION-KEY: container-not-retyped`, so a
   SECOND escalation takes step 5's repeat-key path (6b/7b: `escalated,stale-precondition`)
   instead of ping-ponging with the triager's re-queue or `/unblock-human-beads`' RELEASE.
3. Then 7a (`--add-label escalated`, released in the same call) and 8 (READ BACK). Apply NO
   defer and NO `bd dep` edge (**D-9**). Done — return to the caller.
