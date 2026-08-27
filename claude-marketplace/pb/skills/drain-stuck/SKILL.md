---
name: drain-stuck
description: Disposition a claimed drain-beads bead that cannot complete — run the freshness probes, then park it for a human, close it as moot, or convert its blockers into bd dependencies. Invoked by /drain-beads with the bead id, session actor id, worktree/branch location, and what was tried. Do NOT use outside a drain session.
---

# drain-stuck

You were invoked from a /drain-beads session holding a claimed bead it cannot
complete. Required context from the caller: the bead id (<id>), the session
actor id (ID), the worktree/branch location, and what was tried. Every `bd`
write below passes `--actor "ID"`.

`human` means A PERSON IS THE BLOCKER — the LAST RESORT. Work through STUCK in
order; it exits by exactly one of: PARK (labeled `human`, claim released),
CLOSE-AS-MOOT, or CONVERT-TO-DEPENDENCY. Whatever the exit, the claim is
RELEASED or the bead CLOSED before you return — never return still holding a
claim in a state this skill does not define.

## STUCK — cannot complete a claimed bead (LAST RESORT: escalate to a human)

`human` means A PERSON IS THE BLOCKER. It is the LAST RESORT, for work a person
must move forward — never a generic "not workable right now" park. Do NOT use it
merely because final acceptance needs the change deployed (that is the drain
loop's POST-DEPLOY VERIFICATION GATE section's job — run pb gate
attach-verified-child there, not here), and do NOT use it because ANOTHER BEAD
must finish first (that is a DEPENDENCY — step 3).

Triggers: underspecified / needs a human decision; the pre-apply gates cannot be
made to pass; landing returns a GENUINE `stopped:<reason>` (not a transient ff-race
or rejected non-ff push); a post-deploy gate could not be attached; repeated failed
attempts.
NOT a trigger: "another bead has to land first".

Step 1 only PRESERVES the work; the park that goes in front of a human is the
COMMENT + `human` label in steps 6–7. Steps 2 and 3 stand between the two
deliberately, and each has its OWN exit that is not a park: a bead whose premise
the probes prove MOOT leaves via **CLOSE-AS-MOOT**, and a bead whose live blockers
are all OTHER BEADS leaves via **CONVERT-TO-DEPENDENCY**. Reaching step 4 means a
person really is the blocker.

1. PARK the change (do NOT discard it). KEEP the isolated worktree/branch — do NOT
   clean it up; the park IS leaving it in place. If the WIP commits cleanly, commit
   it on branch `drain/<id>` with a `WIP (parked): <id> <why>` message; if
   pre-commit hooks block the commit, leave the changes uncommitted in the retained
   worktree (do NOT use `--no-verify`).
2. FRESHNESS CHECK — re-verify the bead's PREMISE against CURRENT reality BEFORE you
   write any park comment or apply any label. The bead body is a snapshot from FILING
   time and the reason you are stuck may already be answered: in one pass over the
   parked queue, 5 of 9 beads were already resolved or void. Follow the
   `beads-lifecycle` skill's `Premise Freshness` rules (F-1..F-9) and run the NAMED PROBES from F-3 — one per
   external referent this bead names — keeping each decisive output verbatim:
   - `landed?` / `pushed?` / `patch-identical?` for commits and parked branches;
     `path-exists?` / `symbol-shape?` for the files, modules, and symbols the bead's
     steps or design edit; `ticket-open?` for external tickets; `sibling-open?` for
     referenced beads; `next-free-id?` for any "next free" number the bead recorded.
   - An earlier REVIEW of this bead's plan is NOT a freshness signal (F-6). A reviewed
     snapshot ages exactly as fast as the snapshot, so "already reviewed" / "looks
     plan-ready" MUST NOT stand in for running the probes.
   - An unresolvable probe (`exit 128`, missing repo, referent too vague to probe) reads
     as STILL LIVE, never as moot (F-4).
   - Premise STILL LIVE → continue to step 3, and carry this line into the step-6
     comment so the next reader inherits the check:
     `FRESHNESS: <ISO date> — <probe>=<decisive output>; <probe>=<decisive output> ⇒ premise LIVE`
     If the bead names no external referent, record that instead (F-5):
     `FRESHNESS: <ISO date> — no external referent named ⇒ nothing to re-verify`
   - Premise PROVABLY MOOT → this bead is answered, not blocked. Do NOT park it and do
     NOT label it `human`: go to **CLOSE-AS-MOOT** below.

3. CLASSIFY THE BLOCKER — is a PERSON the blocker, or is it ANOTHER BEAD? This is the
   branch BEFORE the escalation, not a check inside it: drain claims with
   `--exclude-label human` and `/unblock-human-beads` claims with `--label human`, so
   the label simultaneously hides the bead from the queue that would work it AND puts
   it in front of the operator. If you are waiting on another bead, the operator has
   nothing to answer and the tracker can express the wait exactly. Full contract: the
   `beads-lifecycle` skill's `Blocker Modeling` rules (**D-1..D-8**).
   - Name every live blocker, then ask of each: could a PERSON clear this now with a
     decision, an input, an approval, or an out-of-band action? Or must ANOTHER BEAD
     finish first? Step 2's `sibling-open?` probe already answers the second half for
     every bead this one names — REUSE those readings rather than inventing a parallel
     check: `bd show <sib> --json | jq -r '.data[0].status'`. `open` / `in_progress` /
     `blocked` ⇒ a live blocker. `closed` ⇒ NOT a blocker at all, so it MUST NOT get an
     edge; if every named bead reads `closed`, the reason you were stuck has already
     died — go back to step 2 and re-read the premise.
   - A bead you WISH existed is not a dependency. If the blocking work has no bead, the
     bead you hold is underspecified or needs a decision — that is a HUMAN blocker. MUST
     NOT invent a placeholder bead to depend on (**D-1**).
   - EVERY live blocker is a bead → go to **CONVERT-TO-DEPENDENCY**. Do NOT write a
     PRECONDITION block, do NOT touch the repeat counter, and do NOT apply `human`:
     steps 4–9 are the human-escalation path and you are not on it. A prose
     PRECONDITION is for a condition the tracker CANNOT express; this one it can, and
     prose about it would rot exactly as **P-1** describes while the graph would not.
   - ANY live blocker needs a PERSON → continue to step 4. If some blockers are ALSO
     beads, this is the MIXED case: do CONVERT-TO-DEPENDENCY's step 1 for the bead half
     first, then come back here and finish the escalation (**D-7**).

4. NAME THE PRECONDITION — only when the park is blocked on something that must
   become TRUE before the bead is workable (skip it for an underspecified /
   decision-needed park). What you write here becomes an INSTRUCTION to a later
   agent and keeps being obeyed long after the implementation it describes has been
   refactored, so it MUST be drift-detectable:
   - `PRECONDITION:` MUST state an OBSERVABLE OUTCOME — something a reader can run
     and see ("`nb` run from a subdirectory opens the Gradle ROOT project"). It MUST
     NOT state a MECHANISM ("`nb` is a function defined in `~/.zshrc`"): the next
     refactor makes a mechanism claim permanently false, and every later reader then
     concludes "not applied yet" and re-parks forever.
   - `PRECONDITION-KEY:` MUST be a short kebab slug naming that OUTCOME, not this
     attempt — `nb-opens-gradle-root`, never `nb-check-2` — so a later park blocked
     on the SAME thing produces the SAME key. Step 5 counts these.
   - `DERIVED-FROM:` MUST cite the commit and the file(s) you actually read to write
     the line (`<repo>@<sha> — <path>`), so a later reader can re-derive it and see
     the drift.
   - The failure branch MUST be bounded. MUST NOT write "if not yet applied, re-park
     or wait" with no limit — step 5 IS the limit.

5. DETECT A REPEAT — before commenting, check whether this bead was already parked
   on the same precondition:

   ```bash
   bd show <id> --json | jq -r '(.data[0].comments // [])[].text' | rg -o 'PRECONDITION-KEY: .*'
   ```

   (`comments` is `null` on a bead with none, hence the `// []`; empty output — `rg`
   exit 1 — means no prior key, NOT a failure.)
   - The key you are about to write is ABSENT → ordinary park (step 6a).
   - The key is ALREADY PRESENT — this would be the SECOND park on the same unmet
     precondition → the precondition itself is the suspect, not the world: escalate
     it as stale (step 6b + step 7b). There is NO third park on one key.
   - The bead ALREADY carries the `stale-precondition` label → an earlier staleness
     escalation was released without resolving it. Write NO precondition block at
     all: comment plainly what you observed, re-apply `human` (step 7a), and leave
     `stale-precondition` in place so it stays visible as unresolved.

6. COMMENT what you tried, why you couldn't finish, and where the work is parked so
   a human can resume. Either form MUST carry the step-2 `FRESHNESS:` line.

   **6a — ordinary park**, carrying the step-4 block when there is a precondition:

   ```bash
   bd comment <id> "stuck: <what you tried / why>. Parked on branch drain/<id> in <repo> at <worktree-path>.
   FRESHNESS: <ISO date> — <probe>=<decisive output> ⇒ premise LIVE
   PRECONDITION: <observable outcome that must hold before this is workable>
   PRECONDITION-KEY: <stable-outcome-slug>
   DERIVED-FROM: <repo>@<sha> — <path(s) you read>" --actor "ID"
   ```

   **6b — staleness escalation** (step 5 found the key). Say it is a repeat, point at
   the provenance to re-derive, and record what you ACTUALLY observed — do NOT
   restate the old precondition as though it were fresh:

   ```bash
   bd comment <id> "stuck (SUSPECTED STALE PRECONDITION): SECOND park on PRECONDITION-KEY <slug>, so the precondition may be unsatisfiable rather than merely unmet. Re-derive it from its provenance (<repo>@<sha> — <path>) against CURRENT source before acting on it. Observed now: <what you ran and saw>. FRESHNESS: <ISO date> — <probe>=<decisive output> ⇒ premise LIVE. Do NOT re-park on this key. Parked on branch drain/<id> in <repo> at <worktree-path>." --actor "ID"
   ```

7. ESCALATE by labeling for a human (hides the bead from BOTH the claim and the
   termination query, which use `--exclude-label human`). Reaching here means step 3
   found a PERSON in the way — if it did not, you are on the wrong path:

   **7a — ordinary park:**

   ```bash
   bd update <id> --add-label human --actor "ID"
   ```

   **7b — after a 6b staleness escalation** — both labels in ONE call, so the
   unblocker recognizes the class mechanically instead of re-reading the churn:

   ```bash
   bd update <id> --add-label human,stale-precondition --actor "ID"
   ```

8. UNCLAIM — do this LAST, only after the label (and any step-3 dependency edge) is
   applied, so no other session can grab it in an unlabeled, unblocked `open` window:

   ```bash
   bd update <id> --assignee "" --status open --actor "ID"
   ```

9. Do NOT clean up the parked worktree/branch. Done — return to the drain loop's
   CLAIM step.

## CLOSE-AS-MOOT (STUCK step 2 disproved the premise)

Reached ONLY from a FRESHNESS CHECK whose probes decisively answered the bead's own
question. The bead is not blocked — it is ANSWERED, so parking it would put a
non-question in front of the operator. Close it instead. But a moot bead is not
worthless: stale work often contains a PREDICTION about the code, and a blind close
throws that away (F-7). EXTRACT first, close second.

1. READ the stale work before discarding it — the bead's description/design, its
   comments, and any WIP commit on `drain/<id>`. You are looking for a claim it makes
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

4. The isolation — do NOT delete unlanded work. Check whether anything would be lost:

   ```bash
   git -C <worktree-path> status --porcelain; git -C <repo> cherry -v main drain/<id>
   ```

   BOTH empty → nothing to lose → CLEANUP as in the `done` path. EITHER non-empty →
   LEAVE the worktree/branch in place and file the follow-up rather than orphan it. This is
   the ENTRY point for the `worktree-review` label, so it MUST carry that label ALONGSIDE
   `human` — `/unblock-human-beads` triages on the label, and drain's own claim query excludes
   only `human`, so a `worktree-review`-only bead would be drain-claimable and never reach the
   operator (W-1). Record the entry marker at birth (W-2); `bd create` defaults to P2 and
   promotes nothing, so use the no-promotion form:

   ```bash
   bd create "worktree-review: reconcile leftover isolation for <id> (closed as moot)" \
     --labels human,worktree-review --defer +7d --deps "discovered-from:<id>" \
     --notes "[worktree-review $(date +%F)] Leftover isolation from <id>: worktree <worktree-path>, branch drain/<id> in <repo>. Unlanded: <git cherry output>. Dirty: <git status --porcelain output>. A person must rule on keep vs discard. No promotion (priority left at P2)." \
     --actor "ID"
   ```

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

5. Done — return to the drain loop's CLAIM step.

## CONVERT-TO-DEPENDENCY (STUCK step 3 found the blocker is another bead)

Reached when EVERY live blocker is another bead. The bead is not waiting on a person, so
it MUST NOT be labeled `human`: the tracker can express this wait exactly, and unlike a
label a dependency edge clears ITSELF. Full contract: the `beads-lifecycle` skill's
`Blocker Modeling` rules (**D-1..D-8**).

1. WIRE one edge per live blocker, FIRST — while the bead is still `in_progress` and
   owned by you, so `bd ready` excludes it and the write lands in a window no peer can
   observe (**D-5**). Prefer the FLAG form: the first id is the BLOCKED bead, the second
   is the BLOCKER, and the bare positional form reads identically written either way
   round, so it is where a reversal hides:

   ```bash
   bd dep add <id> --blocked-by <blocker-id>   # once per blocker; <id> depends on <blocker-id>
   bd dep list <id>                            # CONFIRM each blocker echoes back "(open) via blocks"
   ```

   Leave the type at its default `blocks`. `discovered-from` does NOT gate readiness
   (**D-3**), so the `--deps "discovered-from:<id>"` form used elsewhere in the drain
   loop is the WRONG tool here — it would leave the bead drain-claimable while genuinely
   blocked. Do NOT pass `--no-cycle-check`: a cycle makes BOTH beads permanently unready
   (**D-4**).

2. COMMENT what you found, carrying the step-2 `FRESHNESS:` line. Write NO `PRECONDITION`
   block — the graph IS the precondition, and prose restating it would rot exactly as
   **P-1** describes:

   ```bash
   bd comment <id> "not stuck on a person: blocked on <blocker-id>[, <blocker-id>…], now wired as bd dependencies instead of a human park. No human input is needed to move this — it returns to the drain queue by itself when the last blocker closes. Work parked on branch drain/<id> in <repo> at <worktree-path>.
   FRESHNESS: <ISO date> — sibling-open?=<status per blocker> ⇒ premise LIVE
   BLOCKED-BY-BEADS: <blocker-id>[, <blocker-id>…]" --actor "ID"
   ```

3. RELEASE in ONE call — no `human` label, `status=open`, assignee cleared (**B-2**,
   **B-3**, **D-6**). `--remove-label human` belongs in this same call whenever the bead
   already carries it (from an earlier park) — a safe no-op otherwise:

   ```bash
   bd update <id> --remove-label human --status open --assignee "" --actor "ID"
   ```

   `open`, NOT `blocked`: readiness is DERIVED from the graph, so the bead is already
   absent from `bd ready` with no stored flag needed — and when the last blocker closes it
   re-enters drain's queue on its own, with nobody having to remember to re-open it. A
   stored `blocked` status is a value nothing recomputes, so it would strand the bead
   after the dependency resolved.

4. Do NOT clean up the parked worktree/branch — the work resumes there once the blockers
   clear. Done — return to the drain loop's CLAIM step.

**MIXED blocker (a bead AND a person) — both apply; do not let either fall through**
(**D-7**). Do step 1 above for the bead half, then go BACK to STUCK step 4 and finish the
human escalation, because a person genuinely holds part of the answer. Which of two shapes
you have decides where the `human` label goes, and the label MUST sit on the bead that
actually HOLDS the question:

- **The question is only answerable AFTER the blocker lands** → keep `human` on THIS bead
  (steps 4–8). State the consequence in the step-6 comment, because it is not obvious:
  `bd ready` excludes blocked beads, so the bead is now absent from BOTH queues until its
  blockers clear, and only then resurfaces in `bd ready --label human`. That is CORRECT —
  the question was not yet answerable — and it is why the comment must name the question,
  so the unblocker inherits it rather than re-deriving it.
- **The question is answerable NOW and independent of the blocker** → do NOT bury it
  behind the edge. File it as its OWN `human` bead with no blockers and depend on THAT, so
  the operator sees it immediately; THIS bead then takes the pure conversion path above and
  carries no label of its own (this is the `pg2-l3vdz` shape — the driver held the deps, the
  sub-beads held the questions):

  ```bash
  bd create "<the decision or input a person must supply>" --labels human \
    --deps "discovered-from:<id>" --actor "ID" --json
  # capture the new id as <question>, then wire it as a blocker like any other:
  bd dep add <id> --blocked-by <question>
  ```
