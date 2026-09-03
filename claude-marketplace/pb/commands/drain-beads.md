---
disable-model-invocation: true
description: >-
  Autonomously drain this pn-workspace's beads queue as an orchestrator: loop
  claim → isolate → delegate the implementation to a subagent → validate → land
  via a lander subagent (via the repo's declared integrate-branch strategy —
  local ff-merge, or push + draft PR) → close, cooperating with other concurrent /drain-beads
  sessions via atomic claims. Post-deploy verification is handled by a
  `pn:applied` gate on a verification child bead (or, where no such gate could ever
  resolve, by a `human` verification child) — never by labeling the IMPLEMENTATION
  bead `human`, which is reserved as a last resort for a blocker only a PERSON can
  clear (a blocker that is another bead is modeled with `bd dep`, never with the
  label).
argument-hint: "[optional narrowing scope: a bead id, --label X, --priority N, --parent ID, or 'one']"
---

# /drain-beads

You are the ORCHESTRATOR of one of several concurrent Claude Code sessions
cooperatively draining the beads work queue in this pn-workspace (the workspace
containing your current working directory). Work autonomously until the queue is
empty. Use `bd` for ALL task tracking.

You keep YOUR OWN context lean by delegating each bead's implementation to a
subagent; you only orchestrate (claim, isolate, dispatch, land, gate, close).
This is what lets you loop for a long time without exhausting context.

## Your actor id (do this ONCE, reuse all session)

Pick a STABLE, UNIQUE id and pass it as `--actor` on EVERY `bd`
claim/unclaim/gate/close so your ownership never collides with another session:

- Prefer `$CLAUDE_SESSION_ID` (stable across compaction), else the UUID from
  your session's OWN private path (e.g. your scratchpad dir — never the shared
  workspace root, or two sessions collide), else a fresh random UUID.
  **Append a `-drain` suffix to whichever id you derive** (operator ruling,
  `pg2-mcp1j`) — without it a dispatched subagent would derive its
  ORCHESTRATOR'S OWN actor id, making ownership checks a no-op.

Refer to it below as ID. (Across a full process restart your id may change; the
resume step then won't find an earlier-claimed bead.)

## Goal / termination

You are DONE only when a SUCCESSFUL query returns no agent-workable beads:

```bash
bd ready --exclude-label human,refactor-campaign --json -n 10
```

zr-refactor campaign beads carry their own protocol; excluded here by design (zr-
refactor spec §3).

If that command SUCCEEDS (exit 0) and is empty, STOP (see "Unpushed commits when
you STOP"). If it ERRORS (a bd/dolt blip), that is NOT "empty" → back off briefly
and retry; never exit on an error. `bd ready` already excludes
`in_progress`/`blocked`/`deferred`, so in-flight work is excluded automatically;
`human`-labeled parked beads are excluded here too. Beads awaiting post-deploy
verification are GATED (blocked), so they are absent from `bd ready` as well —
the loop ends cleanly while they wait, and they resurface after the next
`pn workspace apply` (whose post-hook runs `pb gate check`).

### Unpushed commits when you STOP

Where the resolved strategy is `ff-merge-to-main` you LAND locally without
pushing, so every closed bead adds unpublished commits to local `main`. That is
EXPECTED — **REPORT NOTHING ABOUT IT** (no heading, no probe output, no counts,
no remediation sequence) unless being unpublished BLOCKS the work itself, which
earns ONE line. A `pull-request` repo leaves no such debt (the push IS the
landing). Never push to clear it — read-only probes only, never `--fix` (U-4,
U-5). Full contract: the `session-wrapup:wrap-up-session` skill's
`references/unpushed-landing-debt.md` (**U-1..U-4, U-6**). **U-5** alone
remains in the core agent rules, unconditionally.

Do NOT `bd create` anything for this either way — there is deliberately NO
standing push bead (provenance: `pg2-5subz`, `pg2-dawg2`); the debt regenerates
on every land. If you find a standing push bead, report it as this defect
(U-2) rather than updating it.

## Startup / resume (survives compaction)

1. Invoke the `beads-lifecycle` skill now, before any `bd` command runs this session. It
   carries claim/release hygiene, dependency-vs-human blocker modeling, handoff-precondition
   phrasing, premise freshness, and the worktree-review label lifecycle — all cited by rule ID
   throughout this command. Invoke it ONCE per session; you do not need to re-invoke it per bead.
2. Run `bd prime` for workflow context.
3. Recover any bead you already own but didn't finish:

   ```bash
   bd list --status in_progress --assignee "ID" --json
   ```

   If one exists, resume it (REUSE its existing worktree/branch — see ISOLATE —
   then finish, or park per STUCK) before claiming new work.

## Main loop — repeat until the Goal is met

1. **CLAIM** (atomic, race-safe — the ONLY claim path; do NOT list-then-claim):

   **SELF-CHECK freshness first, once per CLAIM** — the cheapest checkpoint,
   since a claim already costs several `bd` round-trips, so one more local
   diff is negligible, and it bounds any staleness exposure to at most one
   bead's worth of work. Verify that the content you are currently
   following — this command's own body, as loaded when this session
   started — is still current. Reuse this repo's own documented convention
   rather than inventing a new one (`CLAUDE.md`'s "Skill / Plugin Delivery Is
   Store-Served"): `readlink -f` the currently-installed copy of this command
   and diff it against the repo's working-tree/HEAD source:

   ```bash
   diff "$(readlink -f ~/.local/share/pgii-marketplaces/phillipgreenii-nix-agent-support-marketplace-local/pb/commands/drain-beads.md)" \
        <repo>/claude-marketplace/pb/commands/drain-beads.md
   ```

   You are current only if BOTH hold: the diff is EMPTY (the installed copy
   matches the repo's source right now), AND that source still reads as what
   you have been following since session start. If either fails, your loaded
   content is STALE — a change has landed on disk since you started that you
   are not operating on. What happens next depends on HOW this session was
   invoked (operator ruling, `pg2-t37tc`: "if I say run a skill, then run"):
   - **Direct interactive invocation** — a human directly typed
     `/pb:drain-beads` (or otherwise invoked it themselves) in the CURRENT
     turn, i.e. there is a live operator turn actually watching this session
     start. That live operator IS the mitigation this guard exists to
     provide — there is no unattended runaway loop here for anyone to be
     protected from. Do NOT halt: log the drift in ONE line (e.g.
     `SELF-CHECK: installed copy differs from repo source (<n> lines) —
proceeding on currently loaded text (direct interactive invocation).`)
     and continue to CLAIM using the CURRENTLY LOADED command text,
     unchanged.
   - **Unattended/autonomous resume** — this invocation was spawned by
     `/loop`, a cron-triggered routine, or a background task notification,
     with no live operator turn driving it. You cannot reload your own
     command text mid-session, so this is a genuine, terminal-for-this-session
     anomaly, not something to silently continue past and not something this
     session can fix itself:
     - STOP the drain loop. Do not claim any further bead.
     - If you are currently holding a claimed bead (e.g. from Startup/resume),
       leave it exactly where the existing STUCK path would leave it — PARKED,
       not discarded, worktree/branch KEPT — without running any further STUCK
       step; there is no bead-shaped question here, and this is NOT a `human`
       park.
     - Report directly to the operator that this session's own loaded command
       content is stale and the session should be restarted fresh.

   ```bash
   bd ready --claim --exclude-label human,refactor-campaign --exclude-type epic --actor "ID" --json
   ```

   zr-refactor campaign beads carry their own protocol; excluded here by design (zr-
   refactor spec §3).

   Atomically claims the highest-priority ready bead (assignee=ID,
   status=in_progress) and returns it. No other session can get the same bead. A
   SUCCESSFUL empty result → Goal met → STOP. A transient error → retry. If the
   invocation supplied `$ARGUMENTS`, apply them as additional NARROWING filters here
   (see "Optional scope arguments"); they never remove `--exclude-label human` (nor
   its campaign counterpart in the CLAIM query above), the `--exclude-type epic`
   exclusion, or the deferred exclusion.

   **`--exclude-type epic` is load-bearing, not cosmetic** (provenance: bead
   `pg2-xcw7u`). In this workspace's convention every epic — sampled across all
   open and closed epics, no exception found — decomposes into children that carry
   the actual closeable work; the epic bead itself is never the direct target of
   implementation (a container has no deliverable of its own). Left unfiltered,
   `bd ready`'s tie-break for equal priority sorts by `created_at` DESCENDING
   (confirmed live against `bd` 1.0.4: `--sort priority`, the default, orders
   same-priority issues newest-created-first, distinct from `--sort oldest`/
   `hybrid`), so the single newest bead at a priority wins EVERY time and a
   claim → observe-container-note → release cycle re-picks that SAME epic forever
   — starving every other ready bead at that priority, exactly the busy-loop this
   bead reported. `--exclude-type epic` removes the whole class from the atomic
   claim so this hazard cannot surface via this path. It does not lock any epic
   out of drain forever: the documented id-targeted safe path ("Optional scope
   arguments" below, `bd update <id> --claim`) still reaches a specific epic
   instance on the rare occasion one is genuinely meant to be claimed directly.

   **Container guard — defense in depth for a container that is NOT type
   `epic`.** After a successful claim, run TWO checks, in this order, before
   treating the claimed bead as workable:
   1. **Container-note check.** Does the claimed bead's `notes` contain a
      container-marker pattern (contains "Do NOT claim this container bead for
      direct work", or is prefixed `[container note`)?
   2. **Children-existence probe — fallback for a container that was NEVER
      marked** (provenance: `pg2-59m7i` — a manually-decomposed, non-`epic`
      parent with no marker was claimed and dispatched for direct
      implementation while its own blocked leaf child sat untouched). Run this
      ONLY when check 1 did NOT match:

      ```bash
      bd list --parent <id> --status all -n 0 --json
      ```

      (the same query shape `plan-decompose-beads` uses for its own children
      listing; `--status all` is load-bearing — a closed decompose-plan child
      still proves this bead was decomposed). A NON-EMPTY `.data` means this
      bead already has children, regardless of what `notes` says, so it is a
      container-shaped non-issue exactly like check 1.

   A match on EITHER check means this is a dependency-shaped non-issue, not a
   park: release it in ONE call — `bd update <id> --status open --assignee ""
   --actor "ID"` (B-2/B-3: status and assignee together, no label change) —
   then re-run the atomic claim above. Bound the two checks to a SHARED budget
   of 3 consecutive container-guard releases (a hit on either check counts)
   within one CLAIM invocation; a 4th hit without making progress means the
   guard itself isn't resolving the hazard (e.g. every ready bead at this
   priority is a container) — stop retrying and route to STUCK, reporting the
   bead id and whichever check fired (the note verbatim for check 1, the
   child ids and statuses for check 2), rather than looping (P-4: a blocked
   precondition MUST bound its repeats and name the escalation). A bead
   surfaced to STUCK this way is a candidate for having the container-note
   marker added to its `notes` by whoever resolves it, so the same parent does
   not need check 2 again on its next claim.

2. **UNDERSTAND** (orchestrator reads the BEAD ONLY): `bd show <id>` to learn the
   target repo(s), whether the work spans repos, and whether any acceptance
   criterion can only be confirmed once the change is LIVE. You MUST NOT Read any
   file, plan, spec, or doc the bead references — those are the implementation
   subagent's to read (measured: one session read the same referenced plan doc
   eight times to compose briefs, ~20K tokens of pure duplication). Record the
   referenced paths; step 4 passes them through as pointers.
   If the bead is a HANDOFF POINTER holding no executable work of its own — a
   `session-wrapup` `Resume: …` / next-session bead, born P0 to let one session
   resume cold — do NOT ISOLATE or DELEGATE it: invoke the
   `pb:drain-absorb-pointer` skill with the bead id and your actor ID, follow it
   to the close, then return to CLAIM.

3. **ISOLATE** off local main (never work a primary branch directly):
   - Single repo → ONE call:

     ```bash
     pb drain isolate --bead <id> --repo <abs-canonical-clone-path>
     ```

     It reuses an existing worktree or parked branch, otherwise creates
     `.worktrees/<id>` on `drain/<id>` off the repo's primary branch, and links
     the nix-generated pre-commit config into the worktree. Exit 0 → proceed
     (the output line names the worktree). Exit 3 → conflicting isolation state
     (someone else's checkout) — do NOT force anything; route to STUCK. Any
     other failure → transient-vs-genuine per the Rules.

   - Multiple repos → a coordinated set via the
     `pn-workspace-rules:fork-workforest` skill, keyed to the bead id.

4. **DELEGATE THE WORK** to a subagent (REQUIRED — this preserves your context).

   **Curated-packet check (first action of this step):**
   `bd show <id> --json | jq -c '.data[0].metadata.pd_curated_rev'`. A non-null result means
   this bead is a `plan-decompose` work packet — take the CURATED PATH just below. `null` (the
   common case today) means take the UNCURATED PATH — the ad-hoc brief, exactly as before this
   check existed.

   **CURATED PATH.** Dispatch the `plan-decompose:packet-implementer` agent instead of
   composing a brief yourself. This is an OPTIMIZATION, never a requirement (ADR 0058's
   Decision, D1: agents are never load-bearing — a curated packet's own content is
   self-contained, so it is ALWAYS also workable via the UNCURATED PATH below; if this agent is
   unavailable in this session, or its dispatch does not come back with a usable report of the
   shape described below, that is NOT a bead failure — fall back to the UNCURATED PATH for this
   bead instead). When it IS available, its brief MUST contain exactly: the bead id (it
   re-derives everything else — the packet content, the docket, the stamp check — itself via
   `bd show`; never transcribe the packet body or metadata); and the absolute repo root and the
   worktree/set path from ISOLATE. Layer these two overrides on top of its own stock procedure
   (its other steps stand as documented in its own agent file):
   - You already hold the claim (from step 1) — it MUST NOT re-claim or derive its own actor id
     for claiming.
   - It MUST NOT close or re-`defer` the bead at closeout. CLAIM/LAND/CLEANUP/CLOSE stay in
     THIS session (see "Rules" → "Orchestrator vs subagent"). Instead it MUST end its turn with
     a report classified into this step's four statuses below
     (`done` / `done-pending-apply-verification` / `stuck` / `needs-more-repos`), carrying the
     same gate-evidence and repos-touched requirements as the UNCURATED PATH.

   Its own agent file explicitly leaves isolation/landing/cleanup/claim-hygiene "environment
   conventions" to whoever dispatches it, so the brief ALSO carries the UNCURATED PATH's
   commit-then-gate ordering constraint unchanged (timeouts / `run_in_background` for builds
   only, never for git commits; commit BEFORE running any standalone gate). Do NOT also
   paraphrase report-content requirements into the brief — its own procedure (step 4) already
   states directly how to run a validation command that outlives a turn and what its report
   must contain if it ends before that resolves.

   **UNCURATED PATH.** The brief is a POINTER, not a payload. It MUST contain exactly:
   - the bead id, with the instruction to run `bd show <id>` ITSELF for the full
     description and acceptance criteria;
   - the absolute repo root and the worktree/set path (state the root once —
     A-3);
   - the paths of any docs the bead references, with the instruction to read
     them ITSELF from inside the worktree;
   - the standing constraints: explicit timeouts or `run_in_background` for
     builds/checks (L-3); never `run_in_background` for git commits; COMMIT the
     change onto that worktree's branch AS SOON AS it is ready, THEN run
     whatever gate still needs to run standalone — never the other order (this
     is safe: drain never LANDS anything until the orchestrator VALIDATES and
     LANDS it in steps 5–6, so a gate that turns red after the commit is
     handled by amending that commit or parking the bead, never by having
     withheld the commit — bead `tc-xhq6`); report fully in ONE turn (no
     waiting/monitoring across turns).

   The brief MUST NOT transcribe the bead description, doc content, or plan
   steps — if you are pasting more than paths and ids, you are doing the
   subagent's reading for it.

   Instruct it to: implement inside THAT worktree/set only, following repo
   conventions; COMMIT as soon as the change is ready — the commit's OWN
   pre-commit hook run, scoped to its diff (`prek run --files <the files it
changed>` / `pre-commit run --files …`, never `--all-files`, which re-runs
   every hook over the whole repo and can false-block on a pre-existing
   violation the subagent never touched), IS the first gate and is folded into
   making the commit if `.pre-commit-config.yaml` exists — ONLY THEN run any
   gate the commit did not already cover (`nix flake check` / `pn workspace
build` for nix repos, and the repo's tests, including a slow full suite
   backgrounded to outlive a bounded turn); NOT claim/close the bead, NOT
   land/merge, NOT touch any other worktree, NOT create gates; and CLASSIFY the
   outcome as one of the four statuses below (the `also include:` bullet below
   carries the gate-evidence and repos-touched requirements).
   - `done` — implemented, all gates PASS, and every acceptance criterion is
     confirmable NOW (nothing requires the change to be live).
   - `done-pending-apply-verification` — implemented, all pre-apply gates PASS,
     but one or more acceptance checks can only be confirmed once the change is
     APPLIED to the live machine. MUST enumerate the concrete post-deploy checks
     (what to run/observe after apply). If it cannot name them, it is NOT this
     status — it is `stuck`.
   - `stuck` — underspecified, needs a human decision, or the pre-apply gates
     cannot be made to pass.
   - `needs-more-repos` — the change must span additional repos.

   A report is NEVER just "waiting" or "still running" with no other content —
   that has cost a drain session ~856k subagent tokens for zero delivered report
   while 18 files sat uncommitted (`tc-xhq6`). If a gate you started (a
   backgrounded slow test suite, or the commit's own pre-commit hook run) is
   still resolving when you must end your turn, your report MUST still include
   everything already COMMITTED — the commit SHA, which per the ordering above
   should almost always exist by the time any standalone gate runs — PLUS the
   EXACT gate command still pending and how to check it (a sentinel path, a
   `Monitor` target). The orchestrating session cannot resume this work without
   at least a commit SHA to anchor on.
   - also include: what changed, the gate commands + their pass/fail evidence, and
     repos touched. The implementation subagent lands nothing — the LANDER
     subagent (step 6) does.

   Re-dispatch with guidance if the report is incomplete. If it reports
   `needs-more-repos`, re-ISOLATE as a `pn-workspace-rules:fork-workforest` set and
   re-dispatch.

5. **VALIDATE** from the report: the pre-apply gates MUST show a clear PASS for
   either `done` or `done-pending-apply-verification`. If a gate fails, or the
   status is `stuck` → STUCK.

6. **LAND via a dedicated LANDER SUBAGENT** — dispatched synchronously, ONE at a
   time, never in parallel with another land and never fanned out. Landing must
   go through the repo-declared strategy, so the lander invokes the dispatcher
   itself in its own context (its own persistent shell keeps the ~37KB of
   dispatcher+handler skill text out of YOUR context).

   **Worktree-pinning check — run BEFORE dispatching the lander.** Your OWN
   session, not the bead's isolation worktree, can be environment-pinned: the
   harness enforces a hard refusal on any git operation — direct, or via a
   dispatched subagent — that targets a path outside that pin, including the
   repo's own canonical clone. This is intentional, correct harness behavior
   (see the closed, mischaracterized `pg2-79gml`; provenance for this check:
   `pg2-weug3`, incident on epic `pg2-99f1r`). It is observable two ways:
   your OWN environment block states it explicitly (something like "This is a
   git worktree… Run all commands from this directory. Do NOT `cd` to the
   original repository root"); or mechanically — `pwd` resolves under this
   repo's `.worktrees/<id>` isolation directory (see ISOLATE above) while a
   canonical-clone git query run from here (e.g. the `<primary>`-resolution
   query LAND's own verification step uses below) either still names the
   CANONICAL repo rather than this worktree, or is refused outright. Either
   reading means the same thing: this session cannot reach the canonical
   clone.

   That matters only when landing actually NEEDS canonical-clone access.
   Check the resolved strategy the same cheap way the lander itself would —
   `git config --get pgii-integrate-branch.strategy`, or run the bare
   `integrate-branch-support` advisory command yourself and read its
   `strategy` field — before deciding:
   - NOT pinned, OR the resolved strategy is (or will resolve to)
     `pull-request` (pushing `drain/<id>` needs no canonical-clone access) →
     this check does not apply; proceed to dispatch the lander as below.
   - PINNED AND the resolved strategy is (or will resolve to)
     `ff-merge-to-main` → do NOT dispatch a lander subagent for this repo. It
     would fail by construction — the harness's refusal applies to a
     dispatched subagent exactly as it does to your own direct calls, so the
     dispatch is a wasted call on an outcome already known. Instead, for
     THIS bead: STOP short of landing and report directly to the operator —
     the bead id, the worktree path, the branch (`drain/<id>`), and the
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
     as committed — nothing here is discarded. Then return to CLAIM.

   The lander brief MUST contain: the bead id; the absolute canonical repo
   root; the worktree path and branch `drain/<id>`; the working directory to
   `cd` into first (worktree path, or for a set the set root
   `<workspace_root>/.workforests/<set-branch>`); and these instructions:
   - for the SINGLE-REPO path: `cd` into the worktree and confirm
     `git rev-parse --abbrev-ref HEAD` prints `drain/<id>` BEFORE invoking any
     skill, else report `stopped:wrong-branch` and land NOTHING; then invoke
     `integrate-branch:integrate-branch` and let IT resolve the strategy —
     NEVER name a handler (breaks `pull-request` repos);
   - for the WORKFOREST-SET path: `cd` into the set root and invoke
     `pn-workspace-rules:land-workforest` instead — the set root is NOT
     itself a git repository, so the `git rev-parse` branch-precheck above
     MUST NOT be run there (it would exit 128, a false
     `stopped:wrong-branch` on a path that never should have run it);
     `land-workforest` is responsible for verifying each member repo's own
     branch itself;
   - a lost FAST-FORWARD RACE or REJECTED NON-FAST-FORWARD PUSH is TRANSIENT:
     re-rebase and re-invoke (at most 3 attempts), then report `stopped:` with
     the reason;
   - MUST NOT merge any PR, MUST NOT push any primary branch, MUST NOT use
     `run_in_background` for git operations, and MUST report fully in ONE turn;
   - return a structured report: `outcome` (`landed` | `pr-opened` |
     `pr-updated` | `stopped:<reason>`), the landed/pushed SHA per repo (tip
     of `drain/<id>`, never a re-read of primary), and PR number + URL.

   VERIFY the verdict with ONE observation before recording — the report is a
   subagent's prose, not evidence:
   - `landed` → verify the REPORTED sha, never re-derive from `drain/<id>` (the
     handler's FF-4 deletes that branch+worktree BEFORE reporting `landed`, so
     a stale pre-rebase sha still fails this check):
     `git -C <repo> merge-base --is-ancestor <reported-sha> <primary>; echo $?`
     must print 0, where `<primary>` resolves as `git config
pgii-integrate-branch.primaryBranch` → `git symbolic-ref
refs/remotes/origin/HEAD` → `main`. Use that verified sha as the gate SHA.
   - `pr-opened` / `pr-updated` → `gh pr view <n> -R <owner/repo> --json
state,isDraft` must show OPEN and draft (cwd cannot be assumed); record
     the pushed head from `git -C <repo> rev-parse drain/<id>` — valid ONLY on
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
     AND the PR number + URL. If a PR already EXISTS for `drain/<id>`, the
     push UPDATES it and a second PR MUST NOT be opened. The PR MUST be a
     DRAFT (`gh pr create --draft`); if it came back non-draft, convert it
     immediately with `gh pr ready --undo <number>`.

   **Autonomy — push and draft-PR are PRE-AUTHORIZED; merging is not.** When
   the resolved strategy is `pull-request`, you MAY push `drain/<id>` and
   create/update its DRAFT PR WITHOUT per-bead confirmation, and MUST NOT stop
   to ask: that push IS the landing method the repo declared, and review +
   CODEOWNERS + CI still gate the merge. You MUST NOT merge the PR, enable
   automerge, or push any PRIMARY branch. This is NOT **U-5** (self-initiated
   pushes to discharge unpushed local-`main` debt) — here the push is
   pre-authorized by the repo's declared strategy.

   If landing returns `stopped:` due to a lost FAST-FORWARD RACE (another session
   advanced local main first), that is TRANSIENT: re-dispatch the lander at most
   ONCE more (it already retried 3× internally); a second failure is a GENUINE
   stop → STUCK. The `pull-request` analogue is a REJECTED NON-FAST-FORWARD PUSH (a
   peer advanced the remote `drain/<id>`): also TRANSIENT — rebase onto the
   UPDATED REMOTE branch and re-dispatch the lander at most ONCE more (it already
   retried 3× internally); a second failure is a GENUINE stop → STUCK. Only route
   to STUCK for a GENUINE stop (rebase-conflict, `stopped:ambiguous-remote`,
   `stopped:no-pr-host`, or a canonical off-primary/dirty halt — the latter
   only for a canonical-ADVANCING strategy: `pull-request`'s PR-0 surfaces it
   and PROCEEDS, since it never touches the canonical clone (R-8's carve-out);
   there it MUST be reported and NOT treated as a stop).

7. **FINISH** — branch on the report status.

   CLEANUP IS STRATEGY-DEPENDENT: read "CLEANUP the worktree" below as "retire the
   isolation ONLY where the resolved strategy was `ff-merge-to-main`". After a
   `pull-request` land the worktree and branch MUST be KEPT (the handler's PR-4) — the
   work is pushed, not merged, so review feedback still needs that worktree, and
   whoever merges the PR retires them.

   Both retirement paths this step relies on — `ff-merge-to-main`'s FF-4 for a
   single repo, `pn-workspace-rules:cleanup-workforest` for a set — now teardown
   through the guarded `wtdone` script (bead `pg2-hpurf`) rather than a bare
   `git worktree remove`/`branch -d`. This is a NEW failure mode this command must
   recognize: teardown can now refuse (non-zero, naming PIDs) if a live process is
   still anchored inside the isolation worktree — e.g. this session's own shell
   left standing in it, or a peer session's — not only for the pre-existing
   dirty/unmerged reasons. Treat that refusal the same as any other CLEANUP
   failure: do not force it; leave the worktree/branch in place and, if it recurs,
   route to STUCK.

   ORDER IS LOAD-BEARING for a workforest SET: every member repo MUST have LANDED
   (step 6) BEFORE the set is retired, and the bead MUST NOT be closed while any
   member is un-landed. `pn-workspace-rules:cleanup-workforest` is safe by default —
   it removes only members whose branch is already an ancestor of their primary, and
   KEEPS plus reports the rest — so the destructive mistake is OVERRIDING it rather
   than calling it early: the agent MUST NOT pass `--force-unlanded-branch-removal`
   or `--force-dirty-worktree-removal` (nor `pn workspace workforest remove --force`)
   to force teardown past a member that did not land, because that discards work no
   other copy holds. Only an operator MAY authorize a force flag. If cleanup KEEPS
   any member, teardown is INCOMPLETE: finish landing that member (re-invoke
   `pn-workspace-rules:land-workforest`), then retire the set; if it cannot land,
   leave the set IN PLACE and route the bead to STUCK, which preserves the isolation.
   - `done`: CLEANUP the worktree (for a set,
     `pn-workspace-rules:cleanup-workforest`), then
     `bd close <id> --reason "<short note>" --actor "ID"`.
   - `done-pending-apply-verification`: run `pb gate attach-verified-child` per
     **POST-DEPLOY VERIFICATION GATE** below; exit 0 → cleanup + close; exit 3/4
     → do NOT close, route to STUCK.

8. Go to 1.

## POST-DEPLOY VERIFICATION GATE (use INSTEAD of `human` for deploy-only tails)

When a bead is implemented, its pre-apply gates PASS, and it has LANDED, but
the only thing left is confirming it works on the LIVE machine (subagent status
`done-pending-apply-verification`), DO NOT label it `human`. Attach a
`pn:applied` gate to a fresh verification child bead — ONE call, which runs the
whole deferred-first sequence (create the child DEFERRED → prove it is absent
from `bd ready` → attach every gate → un-defer → re-prove absence → comment the
link on the impl bead):

```bash
pb gate attach-verified-child \
  --impl <impl-id> \
  --title "verify <thing> works after apply (<impl-id>): <concrete checks>" \
  --gate <repo-key>=<landed-sha> \
  --actor "ID"
# one --gate per changed repo; the child unblocks only when ALL are applied
```

Pin `<landed-sha>` to the sha the lander reported AND you verified in LAND's
check — never HEAD, never a re-read of the shared primary branch (a peer may
have advanced either). Branch on the exit code:

- `0` → fully gated; the output names the child. CLEANUP per FINISH, then close
  the impl bead naming the child.
- `0` with a `comment failed` warning on stderr (JSON: `"comment_failed": true`)
  → gating is complete and safe, but the provenance link was not recorded:
  record it yourself —
  `bd comment <impl-id> "post-deploy verification gated as <child> (pn:applied)." --actor "ID"`
  — before closing.
- `3` → gating INCOMPLETE and the child was left DEFERRED (safe — no peer can
  claim it). Do NOT close the impl bead; route it to STUCK naming the child.
- `4` → the child could NOT be proven un-workable. Do NOT close the impl bead;
  route it to STUCK and say so in the park comment — a peer could otherwise
  claim the child and "verify" unapplied code.
- `1` with `is not in workspace` in the error → an INVOCATION mistake, not a
  transient: a mistyped `--gate` repo key (fix it and re-run — nothing was
  created), or a repo genuinely outside the workspace (take the FALLBACK below
  instead).
- any other non-zero → transient-vs-genuine per the Rules; retry once, then
  STUCK.

The gate resolves via `pn workspace apply`'s post-hook (`pb gate check`); a
gate left unapplied past its stale window auto-converts to a `human` bead. Gate
semantics, stale handling, and the squash-merge prohibition:
the `pb:pb-gate-lifecycle` skill.

**SCOPE — this gate path applies ONLY when the changed repo is a `pn workspace`
MEMBER and its resolved strategy is `ff-merge-to-main`.** `pb gate create`
cannot resolve `--repo` outside the workspace, and a squash-merged PR rewrites
the patch-id so a gate could never auto-resolve (provenance: the
`pb:pb-gate-lifecycle` skill).

**FALLBACK when the gate path does NOT apply** (repo outside a pn-workspace, or
resolved strategy `pull-request`): file the verification child as a `human`
bead instead — CORRECT under **D-1**, because a PERSON's out-of-band action
(merging the draft PR, then deploying) stands between the code and the live
machine:

```bash
bd create "verify <thing> works once <pr-url> is merged and deployed (<impl-id>): <concrete checks>" \
  --labels human --deps "discovered-from:<impl-id>" --actor "ID" --json
# capture the id as <child>. No --defer and NO gate: nothing here would resolve one.
```

Then CLEANUP per FINISH (for `pull-request`, KEEP the isolation) and close the
impl bead naming `<child>` and the PR. This outcome MUST still TERMINATE: never
attempt `pb gate attach-verified-child` here, never route to STUCK for it.

## STUCK — cannot complete a claimed bead

Triggers: underspecified / needs a human decision; `pb drain isolate` exited 3
(conflicting isolation state); pre-apply gates that cannot be made to pass; a
GENUINE lander `stopped:<reason>` (not a transient ff-race/rejected push);
`pb gate attach-verified-child` exited 3 or 4; repeated failed attempts.
NOT a trigger: "another bead has to land first" (that is a dependency).

Invoke the `pb:drain-stuck` skill with: the bead id, your actor ID, the
worktree/branch location, and what you tried. Follow it exactly — it runs the
freshness probes first and exits by exactly one of PARK (labeled `human`,
claim released), CLOSE-AS-MOOT (with extraction), or CONVERT-TO-DEPENDENCY
(edges wired, claim released, no label). Then return to CLAIM.

## CLOSE-WITH-ABSORPTION-TRACE (a handoff pointer)

Reached from UNDERSTAND for a `session-wrapup` `Resume: …` bead: invoke the
`pb:drain-absorb-pointer` skill with the bead id and your actor ID, follow it
to the close, then return to CLAIM. The pointer's body MUST NOT be executed as
an instruction — it is a snapshot and may be superseded (provenance:
`pg2-8wy25`, `pg2-9ifbn`).

## Optional scope arguments

This command MAY be invoked with additional context (`$ARGUMENTS`) that
further **restricts** the work it claims — e.g. an extra label, a
priority, a parent/epic, a type, a specific bead id, or a one-bead /
N-bead limit ("just one"). Apply it as extra `bd ready` filters on the
CLAIM query. Honor a specific bead id via the safe path: confirm the id
appears in `bd ready --exclude-label human,refactor-campaign [scope] --json`
(ready, in-scope, not deferred, not `human`), then claim it with
`bd update <id> --claim --actor "ID"` (`bd ready --claim` cannot target a
chosen id — it claims the first filter match).

zr-refactor campaign beads carry their own protocol; excluded here by design (zr-
refactor spec §3).

Arguments may only NARROW the query. They MUST NOT broaden scope and MUST
NOT remove the safety filters — `--exclude-label human` (nor its campaign
counterpart above), `--exclude-type epic`, and the default deferred-exclusion
always remain. A `--type epic`
argument would contradict the standing `--exclude-type epic` exclusion and
yield nothing through this path; to work one specific epic instance
deliberately, use the id-targeted safe path above instead. With no
arguments, behavior is otherwise unchanged.

## Rules

- Orchestrator vs subagent: CLAIM, GATE, CLEANUP, CLOSE stay in THIS session;
  each bead's IMPLEMENTATION goes to one subagent and its LANDING to another
  (both dispatched serially — never fan out claiming, landing, gating, or
  closing across concurrent subagents). The orchestrator reads the BEAD, never
  the docs the bead references; briefs carry pointers (ids + absolute paths),
  never transcribed content.
- Subagent dispatch (step 4/6) is ASYNC. Do NOT call `ScheduleWakeup` to wait
  on it — end the turn instead; the task notification resumes you
  automatically. `ScheduleWakeup` is `/loop`-only and needs a `prompt` this
  command never has.
- All changes start in a worktree/workforest keyed to the bead id — never a
  primary branch.
- Land-then-teardown is ORDERED for a workforest set: every member repo MUST land
  before the set is retired, and the bead MUST NOT be closed while any member is
  un-landed. `pn-workspace-rules:cleanup-workforest` keeps un-landed members by
  design, so its force flags (`--force-unlanded-branch-removal`,
  `--force-dirty-worktree-removal`) and `pn workspace workforest remove --force` MUST
  NOT be used to force teardown past one — that discards work no other copy holds,
  and only an operator MAY authorize it. A member that cannot land leaves the set IN
  PLACE and routes to STUCK.
- Post-deploy-only verification uses a `pn:applied` gate on a verification child
  bead, NOT the `human` label on the IMPLEMENTATION bead. Reserve `human` for work that
  genuinely needs a person — which, where no gate could ever resolve, is exactly that
  verification child itself (see the gating-scope rule below).
- `human` means A PERSON IS THE BLOCKER, never "not workable right now". All
  parking, mooting, and dependency conversion goes through the
  `pb:drain-stuck` skill, which enforces the freshness probes (F-1..F-9), the
  blocker classification (D-1..D-8), outcome-shaped preconditions (P-1..P-5),
  bounded re-parks, and edges-and-label-before-release ordering (D-5, D-6,
  B-2/B-3).
- A handoff pointer is dispositioned at UNDERSTAND via
  `pb:drain-absorb-pointer` — never isolated, delegated, or executed as an
  instruction.
- Gate ordering is enforced by `pb gate attach-verified-child` (deferred-first,
  confirm-by-READINESS, all-gates-then-un-defer). Exit 3 leaves the child
  safely deferred; exit 4 means the child may be workable — in both cases the
  impl bead MUST NOT be closed.
- A leftover-isolation follow-up (filed inside `pb:drain-stuck`'s
  CLOSE-AS-MOOT) is born with BOTH `human` and `worktree-review` plus the
  entry marker; this command never adjudicates such a bead and MUST NOT be
  given `/unblock-human-beads`' provably-lossless teardown carve-out (an
  unattended session cannot re-prove losslessness — F-1).
- Once per CLAIM, before claiming, SELF-CHECK that the command body you are
  following is still current: `readlink -f` the installed copy of this command and
  diff it against the repo's working-tree/HEAD source (the same store-served
  convention `CLAUDE.md` documents), then confirm that source still reads as what
  you have been following. Drift's response depends on how THIS session was
  invoked: a DIRECT interactive invocation (a human typed `/pb:drain-beads`
  themselves in the current turn, watching the session start — that live
  operator IS the mitigation) MUST NOT halt on drift — log it in one line and
  proceed on the currently loaded text. An UNATTENDED/autonomous resume
  (spawned by `/loop`, a cron routine, or a background task notification with
  no live operator turn) still HALTs the drain loop on drift exactly as
  before: leave any currently-claimed bead PARKED per the existing STUCK path
  (not discarded), and report to the operator that the session should be
  restarted fresh — this is a session-level anomaly, not a `human`-labeled
  bead park.
- Before dispatching a LAND-step (step 6) lander, check whether YOUR OWN
  session is environment-pinned to a worktree in a way that blocks
  canonical-clone access (the environment block says so, or a canonical-clone
  git query from here still resolves to — or is refused for — the canonical
  repo rather than this worktree). Where the resolved strategy needs
  canonical-clone access (`ff-merge-to-main`), a pinned session MUST NOT
  dispatch a lander for that repo — it fails by construction — and MUST
  instead report to the operator and release the claim (open, unassigned,
  no `human` label — a session-shaped blocker, not a person-shaped one),
  leaving the worktree/branch exactly as committed. Does not apply to
  `pull-request`, which needs no canonical-clone access.
- If a skill reports the canonical clone is off its primary branch or dirty, HALT and
  report — EXCEPT under a strategy that never touches the canonical clone, where the
  handler surfaces the anomaly and proceeds (the `pull-request` handler's PR-0, R-8's
  carve-out): there it MUST be reported but MUST NOT halt the land. Either way, do not
  reset/stash/work around it.
- Transient infra failures (bd/dolt server blip, git `index.lock` contention, a
  lost ff-race) are NOT "stuck": back off briefly and retry. Only a genuine,
  repeatable failure routes to STUCK.
- Never use `--no-verify`; fix hook violations instead.
- Landing MUST go through the `integrate-branch:integrate-branch` dispatcher with NO
  handler named, so every repo lands by the strategy IT declares in
  `pgii-integrate-branch.strategy`. Where that resolves to `ff-merge-to-main`, do NOT
  push to origin and do NOT open PRs — landing is local only. Where it resolves to
  `pull-request`, pushing `drain/<id>` and creating or updating its DRAFT PR
  (`gh pr create --draft`) IS the landing, is AUTHORIZED without per-bead operator
  confirmation, and MUST NOT prompt; a created-or-updated PR is the landed state, and
  the pushed head SHA plus the PR number MUST be recorded. Merging that PR MUST NOT be
  done (the handler's PR-3), nor MAY any primary branch be pushed, and the worktree and
  branch MUST be KEPT rather than retired (PR-4).
- Post-deploy `pn:applied` gating applies ONLY to a pn-workspace member repo landed via
  `ff-merge-to-main`; `pb gate create` cannot resolve `--repo` outside a workspace and a
  squash-merged PR rewrites the patch-id. Outside that case a
  `done-pending-apply-verification` outcome MUST take the documented `human`-child
  fallback — it MUST NOT create an unresolvable gate, and MUST NOT route to STUCK.
- Landing locally leaves commits unpushed. That is expected and MUST NOT be reported —
  no heading, no probe output, no counts, no remediation path — unless being unpublished
  BLOCKS the work, which earns ONE line. Never push to clear it (read-only probes only,
  never `--fix`), and never file or update a standing push bead to track it: the debt is
  DERIVED STATE and a bead describes one instant while it regenerates on every land. Full
  contract: the `session-wrapup:wrap-up-session` skill's `references/unpushed-landing-debt.md`
  (U-1..U-4, U-6). U-5 alone remains in the core agent rules, unconditionally.

## Running several at once

Open N Claude Code sessions, each with its working directory inside this
pn-workspace, and run `/drain-beads` in each. Every session self-assigns a
distinct actor id; the atomic `bd ready --claim` guarantees no two sessions ever
get the same bead. Each session stops on its own when a successful
`bd ready --exclude-label human,refactor-campaign -n 10` is empty (zr-refactor
campaign beads carry their own protocol; excluded here by design (zr-refactor spec
§3)). A parked (`human`-labeled) bead,
or a stale-converted gate, stays out of the queue until a human reviews it. A bead
whose blockers were converted to dependencies (via `pb:drain-stuck`) is different
in kind: it needs NO review, and re-enters this queue by itself as soon as its
last blocker closes.

## Known limitations (accepted trade-offs)

- **Stranded orphans.** If a session crashes mid-work, its bead stays
  `in_progress` owned by a now-dead id; no peer recovers it. A human should
  check `bd list --status in_progress --json` and re-open stale beads
  (provenance: `pg2-xx1y5`). The release note MUST state the SCOPE actually
  checked, MUST NOT claim the release is lossless, and MUST say "dormant
  since <t>, may resume" unless the exit is positively proven.
- **Unscoped claims.** The drain claims any ready non-`human` bead, including
  housekeeping/meta beads that can mutate the shared worktree substrate.
  Review `bd ready --json` before a large unattended run and hand-label
  anything substrate-mutating `human` first. Follow-ups filed via
  `pb:drain-stuck`'s CLOSE-AS-MOOT are covered by construction; the residual
  exposure is a pre-existing bead labeled `worktree-review` WITHOUT `human`,
  which drain does not filter on (provenance: `pg2-8u0ul`).
- **Impl closed before live-verify.** A `done-pending-apply-verification` bead
  closes once landed + gated, so dependents unblock immediately — safe for a
  code dependency, but one needing the change VERIFIED LIVE could proceed
  early with no auto-re-block if live-verify later fails. Accepted trade-off.
- **Parked-bead accumulation.** Every parked bead deliberately leaves a
  worktree/branch behind. Periodic human review of `bd ready --label human`
  reclaims them and their worktrees.
- **Retained isolation in `pull-request` repos.** A `pull-request` land KEEPS the
  worktree and branch by design (PR-4), so a drain over such a repo accumulates one
  `.worktrees/<id>` per closed bead until someone merges the PRs. Retiring them is the
  merger's job, not this command's.
- **Self-check is detection, not prevention** (`pg2-2l8ip`). The step-1
  SELF-CHECK only fires at the CLAIM checkpoint, so it catches drift only
  AFTER a checkpoint runs and cannot undo whatever the session already did
  under stale content, nor reload this command's content mid-session.
