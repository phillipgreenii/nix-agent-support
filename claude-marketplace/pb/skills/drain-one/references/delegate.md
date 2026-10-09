# drain-one reference: DELEGATE THE WORK (step 4)

Read in full when step 4 of `SKILL.md` applies. Moved verbatim from /drain-beads step 4; `ID`, `<work-branch>` and the inputs are defined in `SKILL.md`.

4. **DELEGATE THE WORK** to a subagent (REQUIRED — this preserves your context).

   **Curated-packet check (first action of this step):**
   `bd show <id> --json | jq -c '.data[0].metadata.pd_curated_rev'`. A non-null result means
   this bead is a `plan-decompose` work packet — take the CURATED PATH just below. `null` (the
   common case today) means take the UNCURATED PATH — the ad-hoc brief, exactly as before this
   check existed.

   **CURATED PATH.** Dispatch the `plan-decompose:packet-implementer` agent instead of
   composing a brief yourself. This is an OPTIMIZATION, never a requirement (ADR 0058's
   Decision, D1: agents are never load-bearing — a curated packet's own content is
   self-contained, so a packet whose stamp is CURRENT is also workable via the UNCURATED PATH
   below; if this agent is unavailable in this session, or its dispatch does not come back with
   a usable report of the shape described below, that is NOT a bead failure — fall back to the
   UNCURATED PATH for this bead instead, after first running the stamp check yourself per
   STAMP REFUSAL below, since the UNCURATED PATH has none of its own). That fallback covers an
   unavailable agent or an unusable report ONLY — it MUST NOT be used to override a stamp
   refusal. When it IS available, its brief MUST contain exactly: the bead id (it
   re-derives everything else — the packet content, the docket, the stamp check — itself via
   `bd show`; never transcribe the packet body or metadata); and the absolute repo root and the
   worktree/set path from ISOLATE. Layer these two overrides on top of its own stock procedure
   (its other steps stand as documented in its own agent file):
   - You already hold the claim (from the caller) — it MUST NOT re-claim or derive its own actor id
     for claiming.
   - It MUST NOT close or re-`defer` the bead at closeout (its step-2 stamp-refusal release is
     not closeout and stands — see STAMP REFUSAL below). CLAIM/LAND/CLEANUP/CLOSE stay in
     THIS session (see "Rules" → "Orchestrator vs subagent"). Instead it MUST end its turn with
     a report classified into this step's four statuses below
     (`done` / `done-pending-apply-verification` / `stuck` / `needs-more-repos`), carrying the
     same gate-evidence and repos-touched requirements as the UNCURATED PATH.

   Its own agent file explicitly leaves isolation/landing/cleanup/claim-hygiene "environment
   conventions" to whoever dispatches it, so the brief ALSO carries the UNCURATED PATH's
   commit-then-gate ordering constraint unchanged (timeouts / `run_in_background` for builds
   only, never for git commits; commit BEFORE running any standalone gate) AND its
   no-unauthorized-network-contact constraint unchanged (MUST NOT open network connections to
   hosts outside the isolated worktree/repo on its own initiative). Do NOT also
   paraphrase report-content requirements into the brief — its own procedure (step 4) already
   states directly how to run a validation command that outlives a turn and what its report
   must contain if it ends before that resolves.

   **STAMP REFUSAL (curated packets only).** A stamp refusal is either the implementer's report
   saying its stamp check refused the packet, or — before any UNCURATED fallback — your own two
   metadata reads (`bd show <id> --json | jq -c '.data[0] | {parent, metadata}'`, then the same
   `.metadata` read on that parent, the docket; never a design read) finding `pd_curated_rev` ≠
   the docket's `pd_rev` (compare as numbers), `pd_stale` set, or a malformed stamp. On a stamp
   refusal you MUST NOT implement the packet by ANY path this pass — not via the UNCURATED
   PATH, and not by re-dispatching with a brief that waives the check — and MUST NOT
   second-guess the stamp from the docket's reconcile reports or its design: a mismatch means a
   reconcile is unfinished or owed, never that the packet was merely untouched by an amendment.
   Nor is it a STUCK trigger: each `pb:drain-stuck` exit either closes the bead or releases it
   with `--status open` (DEFER-ON-EVENT's `--defer +7d` is only a timer), which UNDOES the defer
   and returns the packet to the open pool, where every queue consumer claims, checks, and
   releases it in an endless cross-session spin (the `plan-decompose` skill's "Stamp-mismatch
   releases"). Instead:
   1. Release it DEFERRED, in ONE call that also clears the assignee (B-2/B-3, and B-4: a move
      out of `in_progress` to `deferred` is a release too, so it MUST clear the assignee):

      ```bash
      bd update <id> --status deferred --assignee "" --actor "ID"
      ```

      Run it even when the implementer already deferred the packet: it is idempotent, and it
      clears the assignee a bare `bd defer` leaves behind (`pg2-bbiag`). If the packet's
      `pd_stale` is still unset, add `--set-metadata pd_stale=<the docket pd_rev you found>` to
      that SAME call; never overwrite a `pd_stale` already set (`reconcile-pending` and
      reconcile's HOLD marker are what reconcile reads). NEVER `--status open` here.

   2. Unless the report confirms the implementer already posted it, comment one line on the
      docket that a reconcile is owed — the `plan-decompose` skill's mode `reconcile`, whose
      "Stamp catch-up (no amendment)" is the remedy when the docket's last reconcile completed:

      ```bash
      bd comment <docket> "stamp check refused <id> (pd_curated_rev=<n>, pd_rev=<R>): reconcile owed (stamp catch-up if the last reconcile completed)" --actor "ID"
      ```

   3. Add NO `human` label and wire NO `bd dep` edge: a reconcile run — not a person, not
      another bead — clears it (its step 5 clears `pd_stale` and undefers the packet). Leave
      ISOLATE's worktree as it is (it holds no commits; a later claim reuses it) and return to
      the caller with outcome `released`.

   (Provenance: `pg2-wceuh`. Incident `pg2-om899.3`, 2026-09-30: an orchestrator judged a
   `3 ≠ 7` stamp refusal a false positive from the docket's reconcile reports and re-dispatched
   the packet via the UNCURATED PATH.)

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
     withheld the commit — bead `tc-xhq6`); MUST NOT open network connections
     (SSH, HTTP/curl to internal infra, etc.) to hosts OUTSIDE the isolated
     worktree/repo on its own initiative — if verifying a live-host fact seems
     necessary, say so in the report and let the orchestrator decide whether to
     authorize it, rather than doing it unilaterally (bead `tc-h6ty`); report
     fully in ONE turn (no waiting/monitoring across turns).
   - **if it backgrounds a command, IT must stay in its own execution — keep
     calling tools — until that command resolves, using a `Monitor` call with
     an until-loop to detect completion; it MUST NOT send a final response
     that stops short of that and says something like "I'll wait for the
     Monitor notification to arrive."** A dispatched (non-top-level) agent's
     own invocation is a bounded request/response: the moment it stops calling
     tools and returns final text, that invocation is OVER, permanently — there
     is no later resumption, unlike the top-level orchestrating session, whose
     own turn genuinely does end and get resumed by a task-notification. A
     `Monitor` call with an until-loop returns an immediate "started, you'll be
     notified" acknowledgment (it does NOT hand back the result inline) — the
     notification lands as a later event WITHIN this same still-running
     invocation, so the fix is to keep the invocation alive (do not send a
     terminal response) rather than expect anything to reach it afterward.
     Ending the turn early this way is a NO-OP that leaves the work unfinished
     — observed 3× in one session, each requiring the orchestrator to notice
     the stall and resend this exact correction (bead `tc-wklt`). A
     restatement of the rule alone has already failed to prevent recurrence,
     so use this pattern verbatim:

     ```
     Bash({ command: "pg-hooks run pre-commit a.go b.go > /tmp/hooks.log 2>&1; echo DONE >> /tmp/hooks.log",
            run_in_background: true })
     Monitor({ command: "until grep -q '^DONE' /tmp/hooks.log; do sleep 2; done; tail -c 4000 /tmp/hooks.log",
               description: "wait for hooks", timeout_ms: 600000 })
     # Monitor's tool result comes back immediately as "started" — that is NOT
     # completion. Do not send a final response yet. The completion event
     # (with the tailed log) arrives later as a notification INTO this same
     # invocation, as long as you keep it open — never end your turn here.
     ```

   The brief MUST NOT transcribe the bead description, doc content, or plan
   steps — if you are pasting more than paths and ids, you are doing the
   subagent's reading for it.

   Instruct it to: implement inside THAT worktree/set only, following repo
   conventions; COMMIT as soon as the change is ready — the commit's OWN
   pre-commit hook run, scoped to its diff (`git add` the files, then
   `pg-hooks run pre-commit <the files it changed>`; `prek run --files …` only
   if `pg-hooks` is absent; never `--all-files`, which re-runs every hook over
   the whole repo and can false-block on a pre-existing violation the subagent
   never touched), IS the first gate and is folded into making the commit when
   the repo has hooks (`pg-hooks status` says so; do NOT probe with
   `test -f .pre-commit-config.yaml`, which a bundle repo fails while its hooks
   are live) — in a bundle repo it MAY run `pg-hooks fix` after `git add` to
   autofix the staged files — ONLY THEN run any
   gate the commit did not already cover (the targeted
   `nix build .#checks.<system>.<name>` checks relevant to its change and
   `pn workspace build` for nix repos, and the repo's tests, including a slow
   full suite backgrounded to outlive a bounded turn — a full
   `nix flake check` is NOT a per-change or land-time gate); and NOT do ANY of
   the following —
   these bd/pb verbs are orchestrator-only, exhaustively, no exceptions: NOT
   `bd claim` or `bd close` the bead itself, NOT `bd create` any new bead
   (ordinary issue, follow-up, or otherwise — filing ANY bead is out of
   scope), NOT `bd dep` or `pb gate create` any dependency or gate, NOT
   land/merge anything, NOT touch any other worktree. (A DELEGATE-step
   subagent, briefed only with "do NOT create beads/gates," nonetheless ran
   `bd close` on its own bead and `bd create` ×2 for ordinary follow-up beads,
   reasoning that a vague "gates" phrase didn't cover them — bead `tc-eidt`,
   incident `tc-6zps`; the list above is deliberately exhaustive so there is
   no gap left for a subagent's own judgment to route around.) CLASSIFY the
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

   **Stall-phrase check — run on EVERY report from a dispatched subagent, this
   step's or LAND's (step 6), BEFORE trusting its content** (bead `tc-33p4`,
   following `tc-wklt`'s brief-wording fix above, which alone proved
   insufficient: the exact stall — a subagent ending its turn instead of
   continuing to block on its own backgrounded work — recurred TWICE in one
   drain session even with that wording live, each time requiring the
   orchestrator to notice the stall itself by reading the prose closely and
   manually resend a correction via `SendMessage`, after which the agent
   completed correctly). Scan the report text for a stall-indicating phrase — a
   heuristic pattern-match, not an exhaustive enumeration — e.g. "I'll wait",
   "once it resolves", "when the notification arrives", "I'll resume once",
   "I'll continue once", or similar phrasing suggesting the agent ended its turn
   expecting an external notification rather than continuing to call tools. A
   match MUST NOT be treated as final: immediately (same turn, before doing
   anything else with the report) resend a correction via `SendMessage` to that
   SAME agent, addressed by its id/name, telling it there is no notification
   mechanism for a dispatched subagent and it MUST keep calling tools — a
   `Monitor` until-loop — until the work genuinely resolves (the identical
   correction the DELEGATE brief above already gives it up front). MUST NOT
   proceed to VALIDATE (step 5) below, or record a LAND verdict (step 6), on a
   stalled report — wait for a SUBSEQUENT report, and that report is only
   final once it carries concrete evidence (exit codes, command output, a
   verified SHA) backing its classification, with no stall phrasing of its own.
