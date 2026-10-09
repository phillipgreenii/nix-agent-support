# drain-one reference: LAND (step 6)

Read in full when step 6 of `SKILL.md` applies. Moved verbatim from /drain-beads step 6; `ID`, `<work-branch>`, the release policy and the tracker section are defined in `SKILL.md`.

6. **LAND via a dedicated LANDER SUBAGENT** — dispatched synchronously, ONE at a
   time, never in parallel with another land and never fanned out. Landing must
   go through the repo-declared strategy, so the lander invokes the dispatcher
   itself in its own context (its own persistent shell keeps the ~37KB of
   dispatcher+handler skill text out of YOUR context).

   **Worktree-pinning check — run BEFORE dispatching the lander.** Your OWN
   session, not the bead's isolation worktree, can be environment-pinned: the
   harness can refuse a git operation — direct, or via a dispatched subagent —
   that targets a path outside that pin, including the repo's own canonical
   clone. This is intentional, correct harness behavior (see the closed,
   mischaracterized `pg2-79gml`; provenance for this check: `pg2-weug3`,
   incident on epic `pg2-99f1r`; the false-abort correction below:
   `pg2-u4r7t`). The pin is **proven ONLY by an OBSERVED refusal**: either a
   harness pre-execution refusal message of the shape "This session is
   isolated in the worktree `<path>`, but this command redirects git to the
   shared checkout via -C. Refusing to run it" (observed in a session LAUNCHED
   pinned to a `.claude/worktrees/<name>` worktree — the shape `pg2-79gml`
   recorded), or a FAILED PROBE of the operation a land actually needs (e.g.
   a read-only `git -C <abs-canonical> rev-parse --abbrev-ref HEAD` that is
   refused or errors). Environment-block text alone — "This is a git
   worktree… Do NOT `cd` to the original repository root" — and a `pwd` that
   resolves under `.worktrees/<id>` or `.claude/worktrees/<name>` are
   ADVISORY: they MUST NOT, alone, be treated as a pin, and an abort MUST NOT
   rest on them. The harness rewrites that block whenever the persistent Bash
   cwd lands in a worktree, including when YOU put it there (`pg2-u4r7t`,
   2026-09-30: an orchestrator that had `cd`'d into `.worktrees/<id>` read the
   rewritten block as a pin and falsely aborted a land, although a read-only
   `git -C <canonical>` probe from that same cwd succeeded). So: PROBE
   empirically before concluding anything, and if still unsure, dispatch the
   lander and let it report `stopped:` — a wasted dispatch costs less than a
   false abort.

   **Self-pinned recovery.** If the block says worktree-pinned but YOU
   persistent-`cd`'d there (the cwd is under `.worktrees/` or
   `.claude/worktrees/` and you did not launch pinned), you pinned yourself
   (see the Rules: the orchestrator MUST NOT persistent-`cd` into a worktree
   or set root). Recover by running a plain `cd <abs-canonical-clone-root>` as
   its OWN Bash call — NO `git` in the same command — then re-run the probe
   above. Probe passes → NOT pinned; proceed to land. Do NOT abort the land on
   a self-pin. Only a probe that STILL fails after the recovery `cd` counts as
   an observed refusal.

   That matters only when landing actually NEEDS canonical-clone access.
   Check the resolved strategy the same cheap way the lander itself would —
   `git config --get pgii-integrate-branch.strategy`, or run the bare
   `integrate-branch-support` advisory command yourself and read its
   `strategy` field — before deciding:
   - NOT pinned (no observed refusal, probes pass), OR the resolved strategy is (or will resolve to)
     `pull-request` (pushing `<work-branch>` needs no canonical-clone access) →
     this check does not apply; proceed to dispatch the lander as below.
   - PINNED (per an observed refusal or failed probe, as above) AND the
     resolved strategy is (or will resolve to)
     `ff-merge-to-main` → do NOT dispatch a lander subagent for this repo. It
     would fail by construction — the harness's refusal applies to a
     dispatched subagent exactly as it does to your own direct calls, so the
     dispatch is a wasted call on an outcome already known. Instead, for
     THIS bead: STOP short of landing and report directly to the operator —
     the bead id, the worktree path, the branch (`<work-branch>`), and the
     commit state already known from the implementation report (fully
     committed, gates green; only landing is blocked) — the same shape of
     report a genuine `stopped:` lander outcome produces today, reached
     without spending a subagent dispatch on a call that cannot succeed.
     Release the claim in that SAME call, per B-2/B-3
     (`bd update <id> --status open --assignee "" --actor "ID"`) — do NOT
     leave it `in_progress` under an actor id that can never come back to
     finish it (B-1). Do NOT add the `human` label either — no PERSON needs to decide
     anything about this bead; it only needs a session that is not pinned to
     this worktree, same reasoning as the CLAIM-step SELF-CHECK's unattended
     halt above being NOT a `human` park. Leave the worktree/branch exactly
     as committed — nothing here is discarded. Then return to the caller with outcome `released`. Under release policy `park` (see `SKILL.md`), do NOT release to `open` as described above: PARK the bead through `pb:drain-stuck` instead (a comment with the evidence plus `human`), because an open, unclaimed bead is re-offered within a minute, and return `released`.

   The lander brief MUST contain: the bead id; the absolute canonical repo
   root; the worktree path and branch `<work-branch>` (for a set, the set root
   `<workspace_root>/.workforests/<set-branch>`); and these instructions. The
   lander MUST NOT persistent-`cd` into the worktree or set root (that can
   self-pin it exactly as it can the orchestrator, `pg2-u4r7t`): it MUST use
   `git -C "$WT"` (with `WT` set to the absolute worktree path), absolute
   paths, or a `( cd "$WT" && ... )` subshell, and its cwd stays where it
   started:
   - for the SINGLE-REPO path: BEFORE invoking any skill, confirm
     `git -C "$WT" rev-parse --abbrev-ref HEAD` prints `<work-branch>`, else
     report `stopped:wrong-branch` and land NOTHING; then invoke
     `integrate-branch:integrate-branch` and let IT resolve the strategy —
     NEVER name a handler (breaks `pull-request` repos);
   - for the WORKFOREST-SET path: invoke
     `pn-workspace-rules:land-workforest` against the set root (by absolute
     path, no persistent `cd`) instead — the set root is NOT
     itself a git repository, so the `git -C <wt> rev-parse` branch-precheck
     above MUST NOT be run there (it would exit 128, a false
     `stopped:wrong-branch` on a path that never should have run it);
     `land-workforest` is responsible for verifying each member repo's own
     branch itself;
   - a lost FAST-FORWARD RACE or REJECTED NON-FAST-FORWARD PUSH is TRANSIENT:
     re-rebase and re-invoke per the handler's OWN retry bound (FF-3 /
     PR-1: stop at the second consecutive failure; do NOT substitute a larger
     count of your own), then report `stopped:` with the reason;
   - the FF-1b pre-land hooks are a HARD GATE on `ff-merge-to-main` (`pg2-1rlme`):
     the lander MUST run them, MUST check their exit code, and MUST NOT run
     `merge --ff-only` (or otherwise advance main) unless that exit code was 0
     (or `pg-hooks`'s no-bundle / not-installed notice, exit 13 / 127, per the
     handler's FF-1b table). Exit 10 (a hook failed) or any other non-zero exit
     means report `stopped:precommit-branch-diff-failed` with the failing hook
     and land NOTHING. It MUST NOT chain rebase + hooks + merge into one
     unguarded script: check each step's exit status before the next, with `if`
     or an explicit `$?` test, never a bare `;` chain. FF-1b can take several
     minutes on a loaded host, so a lost ff race is expected; the handler's own
     retry bound above applies (do not widen it);
   - MUST NOT merge any PR, MUST NOT push any primary branch, MUST NOT use
     `run_in_background` for git operations, and MUST report fully in ONE turn;
   - the lander is itself a dispatched (non-top-level) subagent, so if any step
     it invokes backgrounds a command (e.g. a long hook run in
     `ff-merge-to-main`'s FF-1b), IT must block on that command itself in
     THIS SAME turn via a `Monitor` until-loop call, exactly the pattern given in the
     DELEGATE step above — there is no external notification for its own
     backgrounded work either;
   - return a structured report: `outcome` (`landed` | `pr-opened` |
     `pr-updated` | `stopped:<reason>`), the landed/pushed SHA per repo (tip
     of `<work-branch>`, never a re-read of primary), and PR number + URL.

   Apply the DELEGATE step's stall-phrase check (step 4) to the lander's report
   before anything below — a lander is itself a dispatched subagent and is
   exactly as prone to this stall as an implementation subagent (bead
   `tc-33p4`). A match means resend the correction and wait for a subsequent
   report; do not verify or record a stalled one.

   VERIFY the verdict with ONE observation before recording — the report is a
   subagent's prose, not evidence:
   - `landed` → verify the REPORTED sha, never re-derive from `<work-branch>` (the
     handler's FF-4 deletes that branch+worktree BEFORE reporting `landed`, so
     a stale pre-rebase sha still fails this check):
     `git -C <repo> merge-base --is-ancestor <reported-sha> <primary>; echo $?`
     must print 0, where `<primary>` resolves as `git config
pgii-integrate-branch.primaryBranch` → `git symbolic-ref
refs/remotes/origin/HEAD` → `main`. Use that verified sha as the gate SHA.
   - `pr-opened` / `pr-updated` → `gh pr view <n> -R <owner/repo> --json
state,isDraft` must show OPEN and draft (cwd cannot be assumed); record
     the pushed head from `git -C <repo> rev-parse <work-branch>` — valid ONLY on
     this path, since PR-4 KEEPS the branch.

   A verdict failing its check is `stopped:<unverified>`, never recorded as
   landed. Strategy-specific LANDED/push/draft-PR requirements are below.

   **What "LANDED" means depends on the resolved strategy.** Record whichever the
   handler reports:
   - `ff-merge-to-main` → outcome `landed`: the rebase-then-`--ff-only` merge
     succeeded. RECORD the landed commit SHA per changed repo.
   - `pull-request` → outcome `pr-opened` or `pr-updated`: the branch was
     pushed and a PR created/refreshed by that push. **THAT IS THE LANDED
     STATE** — this command MUST NOT merge the PR or wait for a merge (PR-3;
     merging is a human action). RECORD the pushed head SHA per changed repo
     AND the PR number + URL. If a PR already EXISTS for `<work-branch>`, the
     push UPDATES it and a second PR MUST NOT be opened. The PR MUST be a
     DRAFT (`gh pr create --draft`); if it came back non-draft, convert it
     immediately with `gh pr ready --undo <number>`.

   **Autonomy — push and draft-PR are PRE-AUTHORIZED; merging is not.** When
   the resolved strategy is `pull-request`, you MAY push `<work-branch>` and
   create/update its DRAFT PR WITHOUT per-bead confirmation, and MUST NOT stop
   to ask: that push IS the landing method the repo declared, and review +
   CODEOWNERS + CI still gate the merge. You MUST NOT merge the PR, enable
   automerge, or push any PRIMARY branch. This is NOT **U-5** (self-initiated
   pushes to discharge unpushed local-`main` debt) — here the push is
   pre-authorized by the repo's declared strategy.

   This is the DEFAULT policy. When the caller passed a tracker section, push and
   draft-PR authority come ONLY from that section; where it grants none, you MUST NOT
   push (U-5) — PARK the bead through `pb:drain-stuck` instead of landing it.

   If landing returns `stopped:` due to a lost FAST-FORWARD RACE (another session
   advanced local main first), that is TRANSIENT: re-dispatch the lander at most
   ONCE more (the handler already made its own 2 attempts internally, per
   FF-3); a second failure is a GENUINE
   stop → STUCK. The `pull-request` analogue is a REJECTED NON-FAST-FORWARD PUSH (a
   peer advanced the remote `<work-branch>`): also TRANSIENT — rebase onto the
   UPDATED REMOTE branch and re-dispatch the lander at most ONCE more (it already
   made its own 2 attempts internally, per PR-1); a second failure is a GENUINE stop → STUCK. Only route
   to STUCK for a GENUINE stop (rebase-conflict, `stopped:ambiguous-remote`,
   `stopped:no-pr-host`, or a canonical off-primary/dirty halt — the latter
   only for a canonical-ADVANCING strategy: `pull-request`'s PR-0 surfaces it
   and PROCEEDS, since it never touches the canonical clone (R-8's carve-out);
   there it MUST be reported and NOT treated as a stop).
