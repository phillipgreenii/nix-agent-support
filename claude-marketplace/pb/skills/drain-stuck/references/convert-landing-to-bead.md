# drain-stuck reference: CONVERT-LANDING-TO-BEAD

Read in full when STUCK step 3 finds finished work that cannot be landed. Moved verbatim from `SKILL.md`; `ID`, `<id>`, `<work-branch>` and the step numbers are defined there.

## CONVERT-LANDING-TO-BEAD (STUCK step 3 found finished work that cannot be landed)

Reached when the bead's implementation is DONE and COMMITTED on `<work-branch>` but landing keeps
failing for a reason that is not a defect in the diff: a lander `stopped:ff-race-retry-limit` after
the re-dispatch bound, `stopped:precommit-branch-diff-failed` or `stopped:eval-failed` that is not
a defect in the diff, a pre-land hook timeout at its cap, or host load. Operator ruling (Phillip,
2026-11-11, verbatim): "don't defer beads which need to be landed ... the alternative should be to
create a child bead which blocks the parent which describes that it needs to be landed."

Such a bead MUST NOT leave through DEFER-ON-EVENT or any other defer. A persistent landing
obstacle is NOT a "live external event" (D-10): a deferred bead holding unlanded work disappears
from `bd ready` until the date, then re-enters as ordinary work with no record in the graph that
only a landing is left. Verify first that finished work exists: `git -C <repo> cherry -v <primary> <work-branch>`
is non-empty and the bead's acceptance criteria were met (a WIP commit from an unfinished attempt is
ESCALATE, not this exit).

1. FILE ONE landing bead, a separate `task` in the same tracker, carrying the parent's repo label
   (and project label) and a priority at least as urgent as the parent's. Its body MUST name: the
   work branch and worktree path; why landing failed (the lander's `stopped:` reason and decisive
   output); the exact re-land steps (rebase onto current main; re-run any flaky-looking test alone
   several times first; land through the lander; do NOT bypass hooks and do NOT widen timeouts); and
   the done condition (landed on main, verified with `git merge-base --is-ancestor <tip> <primary>`
   or the lander's verified verdict; branch and worktree retired; parent closed citing the work's
   `git patch-id --stable`, never a commit SHA). The body MUST NOT cite a SHA as the identity of the work.

   ```bash
   bd create "land <work-branch>: finished work for <parent title>, landing failed (<reason>)" \
     -t task --labels <repo-label>[,<project-label>] -p <parent priority or higher> \
     -d "<body as above>" --actor "ID" --json
   # capture the new id as <landing-bead>
   ```

2. WIRE it as a BLOCKER of the parent while the parent is still `in_progress` and owned by you
   (**D-5**), with the flag form, and read it back:

   ```bash
   bd dep add <id> --blocked-by <landing-bead>
   bd dep list <id>    # CONFIRM <landing-bead> echoes back "(open) via blocks"
   ```

   The landing bead is a SEPARATE bead joined by a `blocks` edge, NOT a parent-child link: D-9
   forbids a parent blocked by its own child (it hides the subtree), so MUST NOT file it with
   `--parent <id>`. Do NOT pass `--no-cycle-check` (**D-4**).

3. COMMENT on the parent with the `FRESHNESS:` line, the landing bead id, and where the work is:

   ```bash
   bd comment <id> "not stuck on a person or an event: the work is finished and committed on branch <work-branch> in <repo> but could not be landed (<stopped reason>). Landing is now tracked by <landing-bead>, wired as a blocks edge. This bead returns to the drain queue by itself when that bead closes. NOT deferred.
   FRESHNESS: <ISO date> — landed?=<git cherry / merge-base output> ⇒ premise LIVE
   BLOCKED-BY-BEADS: <landing-bead>" --actor "ID"
   ```

4. RECLAIM runs ONLY for an isolation that holds nothing, and a finished-unlanded isolation holds
   commits, so `pb drain reclaim` will report `kept-commits` and leave it. Then RELEASE the parent
   in ONE call, open and unassigned, with NO `--defer` (**B-2**, **B-3**):

   ```bash
   bd update <id> --remove-label human --status open --assignee "" --actor "ID"
   ```

5. Do NOT clean up the worktree/branch: the landing bead's worker lands from it. Done, return to
   the caller with outcome `released`.
