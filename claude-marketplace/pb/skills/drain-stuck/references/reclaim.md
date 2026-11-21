# drain-stuck reference: RECLAIM

Read in full when STUCK step 7 or any exit's RECLAIM step applies. Moved verbatim from `SKILL.md`; `ID`, `<id>`, `<work-branch>` and the step numbers are defined there.

## RECLAIM (an idle isolation, before the claim is released)

Operator ruling (Phillip, 2026-10-22, bead `pg2-v3hsj`, verbatim): "if the worktree has no
changes, then remove it and add a note to each bead that a clean up pass ran and since there were
no changes, the work tree was removed. the branch can remain, but have a bunch of worktrees doing
nothing is wasteful."

This is a Guard Clause on every exit that releases the claim while leaving the bead unlanded:
ESCALATE (STUCK step 7), CONVERT-TO-DEPENDENCY (step 3), CONVERT-LANDING-TO-BEAD (step 4), DEFER-ON-EVENT (step 4), and `pb:drain-one`'s
STAMP REFUSAL. CLOSE-AS-MOOT has its own isolation check (its step 4), and CONTAINER PARK has no
isolation, so neither runs this. A coordinated workforest set is out of scope: skip RECLAIM for a
bead isolated as a set.

- The agent MUST run it BEFORE the release call, with its shell's working directory OUTSIDE the
  worktree (`wtdone`'s liveness guard refuses a worktree the calling shell stands in):

  ```bash
  pb drain reclaim --bead <id> --repo <abs-canonical-clone-path> --actor "ID" [--branch <work-branch>]
  ```

  `--branch` is needed only when `<work-branch>` is not `drain/<id>`.

- **Two paths, chosen by the tool, not the agent.** When `pg-gwm config check` passes for the repo
  and the worktree is a pg-gwm lease (`pg-gwm info --path` says managed; the output then reads
  `isolation=pg-gwm`), RECLAIM uses the pg-gwm path below; otherwise (`isolation=legacy`) the legacy
  `wtdone` path in the next bullet applies. In the pg-gwm path the tool never calls `wtdone` and
  never removes a worktree: a clean worktree with zero commits ahead of the primary branch is given
  back with `pg-gwm release` (`lease=released`; the zero-commit branch is then deleted with a plain
  `git branch -d`), and a worktree that is dirty or whose branch has commits is KEPT and its lease
  ended with `pg-gwm expire`, never `release` (`lease=expired`): the reaper then frees the slot and
  preserves the unsaved work on a `pg-gwm/dirty-reaped-*` branch after its next reap, so a parked
  bead's work is found there (`pg-gwm preserved list`) and `pb drain isolate` re-leases it on resume.
  The agent MUST NOT run `pg-gwm release` or `pg-gwm expire` itself: `pb drain reclaim` decides.

- (Legacy path.) The tool decides, never the agent: it removes the worktree (through `wtdone`, never forced) ONLY
  when `git status --porcelain` is empty AND the branch has zero commits the primary branch lacks,
  and then adds the clean-up note to the bead itself. Reading `outcome=`: `removed` (worktree gone,
  bead noted; `wtdone` also deletes the zero-commit branch, which loses nothing and which
  `pb drain isolate` recreates on resume), `kept-dirty`, `kept-commits` and `none` (all left exactly
  as found, no note).
- The agent MUST NOT add its own note, and MUST NOT remove a worktree by hand, whatever the
  outcome. A WIP commit from STUCK step 1 makes the branch `kept-commits`, so a parked bead with
  real work is never touched.
- A non-zero exit MUST NOT block the release: exit 1 means the worktree is still in place (for
  example `wtdone` refused because a process is anchored in it) and no note was written, so
  carry on and mention it in the return; exit 20 means the worktree WAS removed but the note failed,
  so post the note text from the error with `bd comment <id> "<note>" --actor "ID"`.
