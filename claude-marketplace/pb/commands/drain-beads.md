---
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
argument-hint: "[optional narrowing scope: a bead id, --label X, --priority N, --parent ID, or 'one'; optionally --monitor-if-empty]"
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

## Template/formula exclusion (claim-sourcing safety — DO NOT REGRESS)

`bd ready` does NOT exclude `is_template=true` issues (a molecule TEMPLATE, e.g.
`merge-request.pr` — title `PR: {{title}}`, unrendered `{{base_branch}}` placeholders), and a
template's OWN labels need not include `human` — its label set can be anything, or empty — so
neither `--exclude-label human,human-focus-required,refactor-campaign` nor `--exclude-type epic`
reliably excludes one. Once claimed, a template is READ-ONLY: every `bd` write path
(`bd update`, `bd comment`) refuses it with
`cannot modify template : templates are read-only; use bd mol pour to create a work item` —
including the release write — so a template claimed via the bare atomic `bd ready --claim` is
PERMANENTLY STRANDED; there is no `--force`/override on any `bd update` or `bd mol` subcommand
to release it (observed live, 2026-09-15: `merge-request.pr`, provenance bead `tc-vr4ad`).

Therefore EVERY claim in this command (Main loop step 1, the Epic drill-down's descendant
claim, and the id-targeted safe path) is NEVER the bare atomic `bd ready --claim` form — it is
always a two-step preview-then-claim:

1. **List candidates** with the SAME filters the claim call would have used, but WITHOUT
   `--claim`, and WITH `--json` so `is_template` is visible.
2. **Filter client-side**: walk the result array (`.data[]` under the `{"data":[…]}` envelope, the bare top-level `[]` when `BD_JSON_ENVELOPE` is unset — J-1) in the returned (priority) order and skip any entry
   whose `is_template` field is `true`.
3. **Claim the first surviving candidate**: `bd update <id> --claim --actor "ID" --json`.

This introduces a small, ACCEPTED race window between steps 1 and 3 (a peer session could
claim the same candidate first) — strictly safer than the atomic form's failure mode, since a
lost race here just means retrying, never an unrecoverable stranded claim. Treat a step-3
failure (the candidate was claimed, closed, or deferred out from under you between steps 1 and 3) as a transient error: back off briefly and restart from step 1, never re-issue step 3 on the
same id unchanged. If EVERY candidate step 1 returns is a template, that reads as an EMPTY
result for whichever step invoked this sequence — never fall back to claiming a template as a
last resort.

## Goal / termination

You are DONE only when a SUCCESSFUL query returns no agent-workable, non-template bead:

<!-- pb-queue:drain-termination -->

```bash
bd ready --exclude-label human,human-focus-required,refactor-campaign --json -n 10
```

zr-refactor campaign beads carry their own protocol; excluded here by design (zr-
refactor spec §3). Filter out any `is_template=true` entry client-side before judging whether
the result is empty (see "Template/formula exclusion" above) — a template left in the result is
not agent-workable and MUST NOT be claimed.

If that command SUCCEEDS (exit 0) and has no non-template entry, STOP (see "Unpushed commits when
you STOP") — UNLESS this session was invoked with `--monitor-if-empty` (see
"--monitor-if-empty" below), in which case an empty result ARMS a recurring
check instead of stopping. If it ERRORS (a bd/dolt blip), that is NOT "empty" →
back off briefly and retry; never exit on an error — EXCEPT a persistent condition that
is not a blip (see "Persistent bd failure is not a blip" under "Transient infra failures"
in `pb:drain-one`'s `references/rules.md`), which MUST stop the loop. `bd ready` already excludes
`in_progress`/`blocked`/`deferred`, so in-flight work is excluded automatically;
`human`-labeled parked beads are excluded here too. Beads awaiting post-deploy
verification are GATED (blocked), so they are absent from `bd ready` as well —
the loop ends cleanly while they wait, and they resurface after the next
`pn workspace apply` (whose post-hook runs `pb gate check`).

### Unpushed commits when you STOP

Where the resolved strategy is `ff-merge-to-main` you LAND locally without
pushing, so every closed bead adds unpublished commits to local `main`. That is
EXPECTED — **REPORT NOTHING ABOUT IT** (no heading, no probe output, no counts,
no remediation sequence), with NO exception in a `pn` workspace: build/apply use
local-clone overrides, so unpublished or un-relocked state never blocks a bead
and MUST NOT be a reason to park, defer, `human`-label, or block one (U-6). A `pull-request` repo leaves no such debt (the push IS the
landing). Never push to clear it — read-only probes only, never `--fix` (U-4,
U-5). Full contract: the `session-wrapup:wrap-up-session` skill's
`references/unpushed-landing-debt.md` (**U-1..U-4, U-6**). **U-5** alone
remains in the core agent rules, unconditionally.

Do NOT `bd create` anything for this either way — there is deliberately NO
standing push bead (provenance: `pg2-5subz`, `pg2-dawg2`); the debt regenerates
on every land. If you find a standing push bead, report it as this defect
(U-2) rather than updating it.

### --monitor-if-empty

Accepted as one of this command's `$ARGUMENTS` (composes normally with the
narrowing arguments in "Optional scope arguments" below). It changes ONLY what
happens when the Main loop's atomic CLAIM query (step 1) comes back a
SUCCESSFUL empty result — no agent-workable bead was claimed, the exact
condition that otherwise means Goal met and STOP.

- **Flag ABSENT (default):** unchanged, byte-for-byte — an empty CLAIM result
  still means STOP, exactly as documented above.
- **Flag PRESENT:** instead of stopping, ARM a recurring self-paced check and
  end the turn:

  ```
  Skill({ skill: "loop", args: "/pb:drain-beads --monitor-if-empty <carry
    forward any other $ARGUMENTS this session was invoked with>" })
  ```

  This is a deliberate DELEGATION, never a direct `ScheduleWakeup` call.
  `ScheduleWakeup`'s own tool description scopes it to `/loop` dynamic mode and
  explicitly says not to call it from a drain-beads/orchestrator session — this
  command MUST NOT call `ScheduleWakeup` directly, with or without this flag.
  Only the `loop` skill's own dynamic (self-paced) mode owns that mechanism: it
  arms the recurring `ScheduleWakeup` (a fallback-heartbeat-style delay) and
  re-invokes the SAME command text (`/pb:drain-beads --monitor-if-empty …`) on
  each wake, exactly as any other `/loop` dynamic-mode target.

  **On each wake** (this command running again, invoked by `loop`, from
  "Startup / resume" below): the CLAIM query in Main loop step 1 runs exactly
  as always.
  - Bead found → resume the normal Main loop on it. The flag has no further
    effect once work exists — it only changes what happens at an EMPTY
    result, and this wake's CLAIM already wasn't empty.
  - Still empty → take this SAME arm step again (re-invoke the identical
    `Skill({ skill: "loop", ... })` call) and end the turn. From `loop`'s own
    perspective this is just its next self-paced re-invocation finding
    nothing new; there is no bead work and nothing else to report this turn
    (a quiet re-arm — noop: true).

  A monitor armed this way MUST NOT survive past this session:
  `/pb:stop-draining-beads` and the `session-wrapup:wrap-up-session` skill each
  cancel it (`ScheduleWakeup({stop: true})`) as part of their own stop/close-out
  sequences, independently of each other — see those documents.

## Startup / resume (survives compaction)

1. Mark this session's mode (best-effort — `|| true`; a missing/broken `session-mode` tool
   must never block the actual drain work): if `$ARGUMENTS` is 24 characters or fewer AND has
   at most one `--flag`, run `session-mode start drain-beads --force --detail "$ARGUMENTS"`;
   otherwise first write a brief few-word summary of what it restricts to (e.g. `P1, db label`
   instead of the full `--label db --priority 1 --parent pg2-xyz` clause) and pass THAT as
   `--detail` instead.
2. Invoke the `beads-lifecycle` skill now, before any `bd` command runs this session. It
   carries claim/release hygiene, dependency-vs-human blocker modeling, handoff-precondition
   phrasing, premise freshness, and the worktree-review label lifecycle — all cited by rule ID
   throughout this command. Invoke it ONCE per session; you do not need to re-invoke it per bead.
3. Run `bd prime` for workflow context.
4. Recover any bead you already own but didn't finish:

   ```bash
   bd list --status in_progress --assignee "ID" --json
   ```

   If one exists, resume it: invoke `pb:drain-one` (if it is not yet loaded this
   session) and apply it to that bead with the container probe settled — a resumed
   bead skips the probe — then it finishes the bead or parks it per STUCK, before
   you claim new work.

## Main loop — repeat until the Goal is met

1. **CLAIM** — the preview-then-claim sequence from "Template/formula exclusion" above (the
   ONLY claim path; NEVER the bare atomic `bd ready --claim`, which can claim an unreleasable
   template):

   **SELF-CHECK freshness first, once per CLAIM** — the cheapest checkpoint,
   since a claim already costs several `bd` round-trips, so one more local
   diff is negligible, and it bounds any staleness exposure to at most one
   bead's worth of work. Verify that the content you are currently
   following — this command's own body, as loaded when this session
   started — is still current. Reuse this repo's own documented convention
   rather than inventing a new one (`CLAUDE.md`'s "Skill / Plugin Delivery Is
   Store-Served"): `readlink -f` the currently-installed copy of this command
   and diff it against the repo's working-tree/HEAD source. Run this as TWO
   separate commands rather than one `diff "$(readlink -f ...)"` — CETA's
   `safecmds` rule deliberately never clears a `$(...)` command substitution
   (operator ruling `pg2-kxmpe`), so a one-liner abstains every time:

   ```bash
   readlink -f ~/.local/share/pgii-marketplaces/phillipgreenii-nix-agent-support-marketplace-local/pb/commands/drain-beads.md
   ```

   then diff the resolved path this printed against the repo source:

   ```bash
   diff <resolved-path-from-above> <repo>/claude-marketplace/pb/commands/drain-beads.md
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

      <!-- pb-queue:drain-claim -->

   ```bash
   bd ready --exclude-label human,human-focus-required,refactor-campaign --exclude-type epic --json
   ```

   zr-refactor campaign beads carry their own protocol; excluded here by design (zr-
   refactor spec §3). If the invocation supplied `$ARGUMENTS`, apply them as additional
   NARROWING filters here (see "Optional scope arguments"); they never remove
   `--exclude-label human,human-focus-required` (nor its campaign counterpart above), the
   `--exclude-type epic` exclusion, or the deferred exclusion.

   Filter the result array (`.data[]` under the `{"data":[…]}` envelope, the bare top-level `[]` when `BD_JSON_ENVELOPE` is unset — J-1) client-side, in the returned (priority) order, skipping any entry whose
   `is_template` is `true`. Then claim the first surviving candidate:

   ```bash
   bd update <id> --claim --actor "ID" --json
   ```

   This claims the highest-priority NON-TEMPLATE ready bead (assignee=ID, status=in_progress)
   and returns it. A SUCCESSFUL list with no non-template candidate → Goal met → STOP (also run
   `session-mode set-status finished`, best-effort — this is the loop's own natural,
   deterministic termination point; no hook is used or needed for this) — UNLESS this session
   was invoked with `--monitor-if-empty`, in which case take the ARM path in
   "--monitor-if-empty" above instead of stopping. A transient error on either call, OR the
   claim call failing because a peer claimed the same candidate first (the accepted
   preview-then-claim race window) → back off briefly and retry the WHOLE sequence from the
   list step, never re-issue the same claim call unchanged.

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

   **Container guard — loop response.** The probe itself (the container-note
   check and the children-existence probe, run on a claimed bead that is NOT type
   `epic`) is the first step of the `pb:drain-one` skill, which you apply at step 2
   below. It returns the outcome `container-hit` while you STILL HOLD the claim;
   this section is what the loop does about it. The consecutive-hit budget below is
   counted HERE: it counts `container-hit` outcomes within one CLAIM invocation and
   resets on any other outcome. Where this response says "route to STUCK", follow
   "STUCK routing" below.

   A match on EITHER check means this is a dependency-shaped non-issue, not a
   park: release it in ONE call — `bd update <id> --status open --assignee ""
   --actor "ID"` (B-2/B-3: status and assignee together, no label change) —
   then re-run the CLAIM sequence above. Bound the two checks to a SHARED budget
   of 3 consecutive container-guard releases (a hit on either check counts)
   within one CLAIM invocation; a 4th hit without making progress means the
   guard itself isn't resolving the hazard — most often a non-`epic` container
   that OUTRANKS every other ready bead on priority, so it re-wins the list
   step's tie-break every single pass (provenance: `tc-ipgw`, live instance
   `tc-w5rib.1` — a P1 container whose only live children were `human`-labeled
   sat above every P2 ready bead and re-won the claim 4 times running). **D-9**
   forbids the two mechanisms that would normally pull a bead out of this race
   (a `--blocked-by` edge onto its own child, a `--defer`), so before routing
   to STUCK, take the one D-9-compliant lever left: demote the container's own
   priority to match its children's — but check BEFORE spending the write,
   not after: read the child priority `<n>` off the check-2 listing (or run
   that query now if only check 1 fired) and compare it to the container's
   OWN current priority. If they already MATCH, the write is a known no-op —
   it would read `<n>` and write that same `<n>` straight back, changing
   nothing and guaranteeing the container re-wins the very next claim's
   tie-break exactly as before — so skip it and route directly to STUCK now,
   with a comment noting demotion was a no-op (same-priority) rather than
   performing a self-priority write that changes nothing and waiting for a
   4th hit to discover that. Only when the priorities DIFFER is the write
   worth spending: `bd update <id> --priority <n> --actor "ID"`. This is
   neither a blocking edge nor a defer, is non-destructive and reversible,
   and stops the container from dominating the same priority tie next pass.
   Release it (as above) and return to CLAIM — this alone resolves the common
   case without ever reaching STUCK. Do NOT loop a third round of the
   budget-of-3 on the same container waiting for it to recur that many times
   again — a SINGLE post-demotion hit (or the same-priority no-op detected
   above) is what routes it to STUCK, not a fresh budget of 3: report the
   bead id, whichever check fired (the note verbatim for check 1, the child
   ids and statuses for check 2), and either the priority already applied or
   the same-priority no-op finding, rather than looping (P-4: a blocked
   precondition MUST bound its repeats and name the escalation). A bead
   surfaced to STUCK this way — whether after a genuine post-demotion hit or
   via the same-priority no-op shortcut — is a candidate for having the
   container-note marker added to its `notes` by whoever resolves it, so the
   same parent does not need check 2 again on its next claim.

   **Epic drill-down — when the CLAIMED bead genuinely IS type `epic` with
   open children** (provenance: `tc-b02v`, live instance `tc-soml9`). The
   CLAIM sequence above already carries `--exclude-type epic`, so this path is
   reached only when an epic ends up claimed on purpose — the id-targeted safe
   path in "Optional scope arguments" below, or Startup/resume recovering a
   bead this actor id already held `in_progress` from an earlier turn. Every
   epic in this workspace is a container with no deliverable of its own (see
   "`--exclude-type epic` is load-bearing" above), and **D-9** (`beads-lifecycle`
   skill) forbids expressing "this epic still has open work" as a
   `--blocked-by` edge onto its own child or as a `--defer` on the epic — both
   would hide the whole subtree from `bd ready`, the epic and its descendants
   alike. So a claimed epic MUST NOT be dispatched for direct implementation,
   and it MUST NOT simply be released and re-claimed forever either — a
   container epic that outranks its own children on priority would just win
   that race again next pass, starving them exactly as this bead reported:
   1. Run the children-existence probe (the query `pb:drain-one`'s container probe
      also uses; this command keeps its own copy of it) to see
      whether this epic has ever been decomposed at all:
      `bd list --parent <id> --status all -n 0 --json`. An EMPTY result array (`.data` or the bare `[]`, J-1) means
      this epic was never decomposed — it is the rare, deliberately-reached
      exception the id-targeted safe path exists for (see
      "`--exclude-type epic` is load-bearing" above), not a container instance
      — apply `pb:drain-one` (step 2) to it directly, with the container
      probe settled, exactly like any other claimed bead. A NON-EMPTY result array (`.data` or the bare `[]`, J-1) means it genuinely is a container with
      decomposed children: continue to step 2.
   2. Find the first claimable descendant via the SAME preview-then-claim sequence as the
      top-level CLAIM step ("Template/formula exclusion" above) — this reuses `bd ready`'s own
      priority-sorted, blocker-aware descendant search instead of hand-rolling a walk.
      `--parent` is TRANSITIVE (verified against `bd` 1.0.4: it returns descendants at every
      depth, not only direct children), so a NESTED epic-with-no-deliverable is skipped over
      on its own — `bd ready` recurses past it to whichever of ITS OWN descendants is
      actually workable, never surfacing the nested epic itself for direct claim
      (`--exclude-type epic` again). List WITHOUT `--claim`:

      ```bash
      bd ready --parent <id> --exclude-type epic --exclude-label human,human-focus-required,refactor-campaign --json
      ```

      Apply the SAME label filters this session's own top-level CLAIM step above uses (drain's
      `--exclude-label human,human-focus-required,refactor-campaign`; a sibling command
      sourcing work the same way, e.g. `/unblock-human-beads`, substitutes its own mirrored
      filters here instead — see the bead's DESIRED BEHAVIOR for the mapping). Filter the result array (`.data[]` under the `{"data":[…]}` envelope, the bare top-level `[]` when `BD_JSON_ENVELOPE` is unset — J-1)
      client-side, skipping any `is_template=true` entry, then claim the first surviving
      candidate:

      ```bash
      bd update <id> --claim --actor "ID" --json
      ```

   3. Claim SUCCEEDED (a descendant is now claimed under ID). Release the
      epic in the SAME call shape the Container guard uses
      (`bd update <id> --status open --assignee "" --actor "ID"`, B-2/B-3 —
      status and assignee together, no label change), then apply `pb:drain-one`
      (step 2) to the NEWLY claimed descendant, telling it the container probe is
      already settled — do NOT re-run the top-level CLAIM sequence, which would just pull
      whatever else is next in queue and abandon this one.
   4. The list came back EMPTY, or every candidate was a template → every
      descendant under this epic is blocked/deferred/`in_progress`/closed/
      template-only: there is nothing claimable here right now. Release the
      epic plainly — the SAME
      `bd update <id> --status open --assignee "" --actor "ID"` call — and
      move to the next ready item. Do NOT loop on the same epic again within
      this pass; a re-claim of the SAME epic id belongs to a later pass, once
      something under it has changed.

   This is a PROCEDURAL fix to the CLAIM step only, never a graph edge:
   nothing here wires the epic `--blocked-by` its own child or defers it
   (D-9), and the epic's own status/assignee never leaves open/unassigned for
   longer than this one drill-down attempt.

2. **APPLY `pb:drain-one`** to the claimed bead: UNDERSTAND, ISOLATE, DELEGATE,
   VALIDATE, LAND, FINISH, and STUCK routing all live in that skill. If the claimed
   bead is type `epic`, run the Epic drill-down above first and apply the skill to
   the descendant it claims instead.

   Invoke the `pb:drain-one` skill at your FIRST successful claim (or at a resume,
   see Startup) and apply it to every later bead WITHOUT invoking it again —
   re-invoking re-injects its body per bead. State no inputs: its defaults are this
   command's own behavior (actor ID, release policy `release`, session mode `loop`,
   no tracker section). If the skill text you hold ends with
   `skill content truncated for compaction`, or you cannot quote the step you need,
   Read
   `~/.local/share/pgii-marketplaces/phillipgreenii-nix-agent-support-marketplace-local/pb/skills/drain-one/SKILL.md`
   and the `references/` file the step names.

   Its outcome decides what you do next:
   - `closed` or `released` → go to step 1 (CLAIM).
   - `container-hit` → the Container guard's loop response above, then step 1.
   - `not-mine` → the claim is not yours, which should not happen: report it,
     change nothing, go to step 1.

## STUCK routing

The STUCK protocol lives in `pb:drain-one`. Where this command says "route to
STUCK" (the Container guard above, a resumed bead), invoke the `pb:drain-stuck`
skill with the bead id, your actor ID, the worktree/branch location and what you
tried, follow it exactly, then return to CLAIM.

## Optional scope arguments

This command MAY be invoked with additional context (`$ARGUMENTS`) that
further **restricts** the work it claims — e.g. an extra label, a
priority, a parent/epic, a type, a specific bead id, or a one-bead /
N-bead limit ("just one"). Apply it as extra `bd ready` filters on the
list step of the CLAIM sequence (see "Template/formula exclusion" above). Honor a specific bead
id via the safe path: confirm the id
appears in `bd ready --exclude-label human,human-focus-required,refactor-campaign [scope] --json`
(ready, in-scope, not deferred, not `human`) AND that its `is_template` field is NOT `true` (a
template MUST NOT be targeted even by an explicit id, for the same permanently-stranded-claim
reason as the ordinary claim path), then claim it with
`bd update <id> --claim --actor "ID"` (`bd ready --claim` cannot target a
chosen id — it claims the first filter match).

zr-refactor campaign beads carry their own protocol; excluded here by design (zr-
refactor spec §3).

Arguments may only NARROW the query. They MUST NOT broaden scope and MUST
NOT remove the safety filters — `--exclude-label human,human-focus-required` (nor its campaign
counterpart above), `--exclude-type epic`, and the default deferred-exclusion
always remain. A `--type epic`
argument would contradict the standing `--exclude-type epic` exclusion and
yield nothing through this path; to work one specific epic instance
deliberately, use the id-targeted safe path above instead. With no
arguments, behavior is otherwise unchanged.

## Rules

- **Sourcing.** Work MUST be claimed only via the preview-then-claim sequence in
  "Template/formula exclusion" above — list with `bd ready` (no `--claim`), skip any
  `is_template=true` entry client-side, then `bd update <id> --claim --actor "ID"` on the first
  surviving candidate — and MUST NOT use the bare atomic `bd ready --claim` form anywhere (Main
  loop step 1, the Epic drill-down's descendant claim, or the id-targeted safe path): it cannot
  exclude templates, and a claimed template's write paths are all refused, permanently
  stranding the claim (observed live, 2026-09-15: `merge-request.pr`).
- Per-bead rules (orchestrator vs subagent, worktree discipline, landing, post-deploy
  gating, parking) live in `pb:drain-one`'s `references/rules.md` and apply to every
  bead this command drains.
- The orchestrator's top-level turn handling: dispatched subagent work is ASYNC.
  Do NOT call `ScheduleWakeup` to wait on it — end the turn instead; the task
  notification resumes you automatically. `ScheduleWakeup` is `/loop`-only and needs
  a `prompt` this command never has. It is
  also orthogonal to `--monitor-if-empty` (see that section above): arming
  that flag's recurring check hands the `loop` skill a prompt to re-run and
  lets `loop` own the `ScheduleWakeup` call, so THIS command still never calls
  `ScheduleWakeup` directly, even then.
- A claimed bead that genuinely IS type `epic` with decomposed children — reachable
  only via the id-targeted safe path or a resumed `in_progress` claim, since the
  CLAIM sequence already carries `--exclude-type epic` — is never dispatched for
  direct implementation and never just released-and-reclaimed forever. CLAIM's "Epic
  drill-down" step finds and claims its first ready non-`epic` descendant (via
  `bd ready --parent`, which is transitive and skips past any nested container
  epic on its own), releases the epic in the same call shape the Container guard
  uses (B-2/B-3), and continues the loop on that descendant; no claimable
  descendant releases the epic plainly and moves on. This is a PROCEDURAL
  claim-step fix only — **D-9** still forbids wiring the epic `--blocked-by` its
  own child or deferring it while children remain open.
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

## Running several at once

Open N Claude Code sessions, each with its working directory inside this
pn-workspace, and run `/drain-beads` in each. Every session self-assigns a
distinct actor id; the preview-then-claim sequence's per-id `bd update --claim` is atomic per
bead, so a lost race just means one session's list step picked a candidate a peer claimed
first — a transient failure to retry, never two sessions holding the same bead. Each session
stops on its own when a successful
`bd ready --exclude-label human,human-focus-required,refactor-campaign -n 10` is empty of
non-template entries (zr-refactor
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
  The self-check diffs this command only: a stale `pb:drain-one` skill or reference
  file, once loaded, goes undetected the same way.
