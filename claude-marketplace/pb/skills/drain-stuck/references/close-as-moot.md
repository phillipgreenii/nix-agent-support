# drain-stuck reference: CLOSE-AS-MOOT

Read in full when STUCK step 2 proves the premise moot. Moved verbatim from `SKILL.md`; `ID`, `<id>`, `<work-branch>` and the step numbers are defined there.

## CLOSE-AS-MOOT (STUCK step 2 disproved the premise)

Reached ONLY from a FRESHNESS CHECK whose probes decisively answered the bead's own
question. The bead is not blocked — it is ANSWERED, so escalating it would put a
non-question in front of the triager and the operator. Close it instead. But a moot bead is not
worthless: stale work often contains a PREDICTION about the code, and a blind close
throws that away (F-7). EXTRACT first, close second.

1. READ the stale work before discarding it — the bead's description/design, its
   comments, and any WIP commit on `<work-branch>`. You are looking for a claim it makes
   that CURRENT source VIOLATES: a defect it predicted, or a decision it called
   load-bearing that the shipped version skipped. Blind-closing is forbidden.

2. EXTRACT any such claim as its own bead BEFORE closing, so the link survives:

   ```bash
   bd create "<the prediction, restated as the defect it predicts>" \
     -d "Extracted from <id> while closing it as moot. The stale work claimed <X>; CURRENT source violates it: <probe>=<decisive output> / <path:line>." \
     --deps "discovered-from:<id>" --actor "ID" --json
   # capture the new id as <extracted>
   ```

3. RECORD the check on the bead, then CLOSE — the recorded probe output IS the
   justification, so it MUST be verbatim, not paraphrased:

   ```bash
   bd comment <id> "FRESHNESS: <ISO date> — <probe>=<decisive output verbatim> ⇒ premise MOOT. Superseded by <what superseded it>. Extracted: <extracted> (or: nothing extractable)." --actor "ID"
   bd close <id> --reason "moot on re-verification: <probe>=<decisive output>; superseded by <what>; extracted <extracted>" --actor "ID"
   ```

4. The isolation — do NOT delete unlanded work. Check whether anything would be lost (`<primary>` is the repo's primary branch: `git config pgii-integrate-branch.primaryBranch`, else `git symbolic-ref refs/remotes/origin/HEAD`, else `main`):

   ```bash
   git -C <worktree-path> status --porcelain; git -C <repo> cherry -v <primary> <work-branch>
   ```

   BOTH empty → nothing to lose → CLEANUP as in the `done` path. EITHER non-empty →
   LEAVE the worktree/branch in place and file the follow-up rather than orphan it. This is
   the ENTRY point for the `worktree-review` label, so it MUST carry that label ALONGSIDE
   `human` — `/unblock-human-beads` triages on the label, and a `worktree-review`-only bead
   would not carry the human label the person queue selects on (W-1). This is one of two places
   this skill writes `human` directly, and it is deliberate: the follow-up is NEW planned
   operator work (a keep-or-discard ruling on an isolation artifact; LV-2), not a stuck bead,
   so it carries `human` ONLY, never `attention`, and does not go through the triager.
   Record the entry marker at birth (W-2); `bd create` defaults to P2 and
   promotes nothing, so use the no-promotion form:

   ```bash
   bd create "worktree-review: reconcile leftover isolation for <id> (closed as moot)" \
     --labels human,worktree-review --deps "discovered-from:<id>" \
     --notes "[worktree-review $(date +%F)] Leftover isolation from <id>: branch <work-branch> in <repo>. Unlanded: <git cherry output>. Dirty: <git status --porcelain output>. A person must rule on keep vs discard. No promotion (priority left at P2)." \
     --actor "ID"
   ```

   The follow-up is NOT deferred (no `--defer`): it is planned operator work, and a defer would
   hide it from `bd ready --label human` (operator ruling, Phillip, 2026-10-27: "DEFER is for the
   situation that nothing can be done right now, regardless of my availability"; this matches
   `/unblock-human-beads`' own follow-up, which "is not deferred").

   Whoever later adjudicates that isolation MUST remove the label and restore the recorded
   priority in the same update that releases or closes the bead — the
   `beads-lifecycle` skill's `Worktree-Review Label Lifecycle` rules (W-1..W-8) are the label's full contract, and
   `/unblock-human-beads`' RELEASE / CLOSE steps are where it is carried out. Drain itself
   never adjudicates isolation: such a bead is substrate-class and never enters drain's queue.

   **The asymmetry with `/unblock-human-beads` is DELIBERATE, not an oversight.** That command has
   a class-1a carve-out: it MAY tear down an isolation autonomously, with no operator prompt, when
   losslessness is MECHANICALLY PROVEN in that same session (clean `git status --porcelain`, and
   every commit on the branch landed or patch-identical to one that is). **This command gets NO
   such carve-out and MUST NOT be given one.** Drain runs UNATTENDED, so a proof cannot be
   inherited — a reading is valid only for the instant it was taken (**F-1**), a peer session may
   commit into that worktree between the probe and the teardown, and there is no operator to fall
   back to when a leg reads ambiguously; `/unblock-human-beads` runs with a person in the session.
   The only isolation drain ever retires is the one IT created for the bead it currently holds,
   after that bead's own work LANDED (FINISH step 7) — never an adjudication of someone else's.

5. Done — return to the caller.
