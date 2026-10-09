# drain-one reference: per-bead rules

Read in full BEFORE step 4 (DELEGATE) of `SKILL.md`. These are the per-bead rules moved verbatim from /drain-beads; the loop-only rules (claim sourcing, epic drill-down, the freshness SELF-CHECK, `--monitor-if-empty`) stay in that command. `ID`, `<work-branch>`, the release policy, session mode and tracker section are defined in `SKILL.md`.

- Orchestrator vs subagent: CLAIM, GATE, CLEANUP, CLOSE stay in THIS session;
  each bead's IMPLEMENTATION goes to one subagent and its LANDING to another
  (both dispatched serially — never fan out claiming, landing, gating, or
  closing across concurrent subagents). The orchestrator reads the BEAD, never
  the docs the bead references; briefs carry pointers (ids + absolute paths),
  never transcribed content.
- Subagent dispatch (step 4/6) is ASYNC. Do NOT call `ScheduleWakeup` to wait
  on it — end the turn instead; the task notification resumes you
  automatically. `ScheduleWakeup` is `/loop`-only and needs a `prompt` this
  command never has. This is orthogonal to, and MUST NOT be confused with, a
  DISPATCHED subagent's own backgrounded Bash/Monitor calls (e.g. an
  implementer's backgrounded `nix build .#checks.<system>.<name>`) — the
  notification-resumes-you mechanism applies only to YOU, the top-level orchestrator, waiting on your own Agent-tool dispatch;
  a subagent gets no such notification for its own child and MUST block on it
  itself via `Monitor` in the same turn (see step 4's worked example).
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
  `pb:drain-stuck` skill, which enforces the freshness probes (F-1..F-10), the
  blocker classification (D-1..D-10), outcome-shaped preconditions (P-1..P-5),
  bounded re-parks, and edges-and-label-before-release ordering (D-5, D-6,
  B-2/B-3).
- A curated packet's stamp refusal is released WITHOUT `pb:drain-stuck`: the packet is not
  implemented by any path that pass (the UNCURATED fallback covers only an
  unavailable agent or an unusable report), is released DEFERRED with the assignee cleared in
  the same call (`bd update <id> --status deferred --assignee "" --actor "ID"`, B-2/B-3/B-4 —
  never `--status open`), and gets no `human` label: a `plan-decompose` reconcile (or its
  stamp catch-up) clears it. See step 4's STAMP REFUSAL (`pg2-wceuh`).
- A handoff bead is dispositioned at UNDERSTAND via
  `beads-lifecycle:handoff-bead` — never isolated, delegated, or executed as an
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
- The orchestrator MUST NOT persistent-`cd` into a bead worktree
  (`.worktrees/<id>`, `.claude/worktrees/<name>`) or a workforest set root: the
  harness rewrites the environment block to pin the session there
  (`pg2-u4r7t`). Use `git -C <abs>`, absolute paths, or a `( cd ... )`
  subshell; the orchestrator's own cwd stays the canonical root. Brief
  subagents the same way.
- Before dispatching a LAND-step (step 6) lander, check whether YOUR OWN
  session is pinned in a way that blocks canonical-clone access. A pin is
  proven ONLY by an OBSERVED harness refusal or a failed probe of the needed
  operation — environment-block text or a worktree `pwd` alone MUST NOT cause
  an abort. If you self-pinned, recover per step 6's "Self-pinned recovery"
  (plain `cd <abs-canonical>`, re-probe) and proceed. Where the resolved
  strategy needs canonical-clone access (`ff-merge-to-main`) and the pin IS
  proven, a pinned session MUST NOT
  dispatch a lander for that repo — it fails by construction — and MUST
  instead report to the operator and release the claim (open, unassigned,
  no `human` label — a session-shaped blocker, not a person-shaped one),
  leaving the worktree/branch exactly as committed. Does not apply to
  `pull-request`, which needs no canonical-clone access. Under release policy `park`, PARK the bead through `pb:drain-stuck` instead of releasing it plainly.
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
  `pull-request`, pushing `<work-branch>` and creating or updating its DRAFT PR
  (`gh pr create --draft`) IS the landing, is AUTHORIZED without per-bead operator
  confirmation, and MUST NOT prompt; a created-or-updated PR is the landed state, and
  the pushed head SHA plus the PR number MUST be recorded. Merging that PR MUST NOT be
  done (the handler's PR-3), nor MAY any primary branch be pushed, and the worktree and
  branch MUST be KEPT rather than retired (PR-4). With a tracker section this
  permission is the DEFAULT policy only: push and draft-PR authority come ONLY from that
  section, and where it grants none you MUST NOT push (U-5).
- Post-deploy `pn:applied` gating applies ONLY to a pn-workspace member repo landed via
  `ff-merge-to-main` WHOSE CHANGED FILES are actually applied by the terminal host's own
  `pn workspace apply` (nixos-rebuild) — repo/strategy alone is necessary but NOT
  sufficient (see the POST-DEPLOY VERIFICATION GATE SCOPE note above). `pb gate create`
  cannot resolve `--repo` outside a workspace and a squash-merged PR rewrites the
  patch-id. Outside either case — including a same-repo change whose real deployment
  mechanism is a k8s cluster `just deploy <cluster>` or a `just deploy-remote` to a
  non-terminal machine — a `done-pending-apply-verification` outcome MUST take the
  documented `human`-child fallback — it MUST NOT create an unresolvable gate (or, worse,
  one that resolves on an unrelated apply and proves nothing about the real deployment),
  and MUST NOT route to STUCK.
- Landing locally leaves commits unpushed. That is expected and MUST NOT be reported —
  no heading, no probe output, no counts, no remediation path. In a `pn` workspace it never
  blocks a bead (build/apply use local-clone overrides) and MUST NOT park, defer, or
  `human`-label one. Never push to clear it (read-only probes only,
  never `--fix`), and never file or update a standing push bead to track it: the debt is
  DERIVED STATE and a bead describes one instant while it regenerates on every land. Full
  contract: the `session-wrapup:wrap-up-session` skill's `references/unpushed-landing-debt.md`
  (U-1..U-4, U-6). U-5 alone remains in the core agent rules, unconditionally.
