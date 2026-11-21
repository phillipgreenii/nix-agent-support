# drain-stuck reference: DEFER-ON-EVENT

Read in full when STUCK step 3 finds the blocker is a live external event. Moved verbatim from `SKILL.md`; `ID`, `<id>`, `<work-branch>` and the step numbers are defined there.

## DEFER-ON-EVENT (STUCK step 3 found the blocker is a live external event, not a person or a bead)

Never reached by a bead whose finished work is committed but unlanded (that is CONVERT-LANDING-TO-BEAD).
Reached when the ONLY live blocker is something that happens on its own schedule and that
NEITHER a person NOR another bead can make happen sooner (**D-10**) — a metric that has not
yet fired naturally, a live window that has not yet occurred. The bead is not waiting on a
decision; it is waiting on the WORLD, so neither `escalated` nor `human` MUST be applied here:
either label would put a non-question in front of the triager and the operator on every single
re-park, which is the exact defect
`pg2-yanwe` traced to `pg2-b48eq` re-acquiring `human` at least three times across
2026-07-21..09-22 despite its own 2026-07-21 comment already ruling "No human decision
required".

1. NAME THE PRECONDITION exactly as STUCK step 4 — the observable event/outcome that must
   occur before this is workable — with its `PRECONDITION-KEY` and `DERIVED-FROM`.
2. DETECT A REPEAT exactly as STUCK step 5. Ordinary case (key absent, or key present but the
   event genuinely still has not occurred) → continue below. If the SAME key already appears
   from an earlier escalation or defer, that is a signal the CHECK itself — not the world — may be wrong (a
   renamed metric, a wrong PromQL, a daemon that no longer emits it): that genuinely IS worth a
   person's judgment, so escalate it via STUCK's step 6b/7b (`escalated,stale-precondition`) instead
   of continuing here. Do not let an event-gated bead re-park on the same unresolved key a third
   time with no one ever looking at it.
3. COMMENT what you tried and why the event has not yet occurred, carrying the STUCK step 2
   `FRESHNESS:` line and the step-1 PRECONDITION block:

   ```bash
   bd comment <id> "stuck (event-gated, not human-gated): <what you tried / observed>. The blocker is a live external event/observation, not a person or another bead — see beads-lifecycle D-10. Parked on branch <work-branch> in <repo>.
   FRESHNESS: <ISO date> — <probe>=<decisive output> ⇒ premise LIVE
   PRECONDITION: <observable outcome that must occur before this is workable>
   PRECONDITION-KEY: <stable-outcome-slug>
   DERIVED-FROM: <repo>@<sha> — <path(s) you read>" --actor "ID"
   ```

4. RECLAIM (below), then DEFER, with NO `human` label — remove it in the SAME call if the
   bead already carries it from an earlier, mis-applied park:

   ```bash
   bd update <id> --defer +1d --remove-label human --status open --assignee "" --actor "ID"
   ```

   The window is exactly `+1d`, also on a re-defer (beads-lifecycle D-10; operator rulings,
   Phillip, 2026-10-27: "automated deferral should be only one day"; "it is acceptable for
   agents to suggest a longer deferral"). You MAY name a longer window you suggest in the
   step-3 comment, but MUST NOT apply it: a longer defer is the operator's to set. `--defer`
   is what keeps this out of `bd ready`, not a label — it clears itself on the date with no one
   having to remember to remove anything, and it never reaches `/unblock-human-beads`'
   `--label human` queue at all.

5. Do NOT clean up a parked worktree/branch that holds work — the work resumes there once the
   event has occurred (RECLAIM, at step 4, removed it only if it held nothing). Done — return
   to the caller.
