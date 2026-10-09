---
name: drain-one
description: The shared per-bead protocol for draining ONE already-claimed bead — container probe, understand, isolate, delegate to an implementation subagent, validate, land via a lander subagent, finish, and route a bead that cannot complete to pb:drain-stuck. Applied by /drain-beads and by the pg-router drain workers. Do NOT use to select or claim a bead.
---

# drain-one

Apply this to ONE bead the caller has ALREADY claimed. It does not select a bead,
claim one, self-check freshness, drill down into epics, or loop: the caller does
those. It returns to the caller with an outcome (see "Outcomes"). Step numbers
continue from the caller's step 1 (CLAIM).

If the skill text you hold ends with `skill content truncated for compaction`,
or you cannot quote the step you need, Read this file's path in full (and the
`references/` file the step names) before acting.

## Inputs

The caller states these in prose when it applies the skill. Anything it does not
state takes the default, which is exactly what /drain-beads does.

- **actor** (default `<session-id>-drain`): the actor id passed as `--actor` on
  every `bd` write. This skill and its references call it `ID`. A caller that
  passes a literal actor overrides the `-drain` suffix rule (ruling `pg2-mcp1j`),
  which makes the subagent-ownership check a no-op, so only the brief ban in
  `references/delegate.md` protects subagent claims.
- **release policy** (default `release`): `release` — a container hit returns
  `container-hit` still holding the claim, and the pinned-lander case releases the
  bead to `open`. `park` — NO path may leave the bead `open`, unclaimed and ready,
  because the router would re-offer it within a minute: a container hit takes
  `pb:drain-stuck`'s CONTAINER PARK entry and the pinned-lander case PARKs.
- **session mode** (default `loop`): `loop` — an operator is reachable, and async
  subagents follow the rule in `references/rules.md` (end the turn, the task
  notification resumes you). `unattended` — no operator is present: every
  "report to the operator" line becomes a bead comment plus your final message,
  and you MUST NOT end your turn while any subagent or background task is
  outstanding — await it inside the turn (a `Monitor` until-loop, loaded first
  with `ToolSearch select:Monitor`), because a session that ends its turn reads as
  idle while an async subagent still runs.
- **tracker section** (default none): a per-tracker addendum from the caller
  giving the isolation, the work branch, the worktree (`WT`) and the push and
  draft-PR authority. With none: isolate with `pb drain isolate`, `<work-branch>`
  is `drain/<id>`, the worktree is what ISOLATE produced, and push authority is
  the default policy stated at step 6. With one, push and draft-PR authority come
  ONLY from it, and where it grants none you MUST NOT push (U-5).

`<work-branch>` below is the work branch (default `drain/<id>`). Every
`bd`/`git` command here that names a branch uses it.

**Precondition (first action).** The bead MUST be `in_progress` with the
assignee equal to the actor:

```bash
bd show <id> --json | jq -r '(if type=="object" and has("data") then .data else . end)[0] | "\(.status) \(.assignee)"'
```

If it is not, change NOTHING and return `not-mine`.

## Outcomes

The bead ends CLOSED or released; NEVER left `in_progress`, except `container-hit`.
Return exactly one of:

- `closed` — closed (done, a handoff bead absorbed, or STUCK's CLOSE-AS-MOOT).
- `released` — claim released with the assignee cleared, by `pb:drain-stuck`
  (PARK, CONVERT-TO-DEPENDENCY, DEFER-ON-EVENT), by step 4's STAMP REFUSAL
  (`--status deferred --assignee ""`), or by step 6's pinned-session release.
- `container-hit` — release policy `release` only: the probe fired and you STILL
  HOLD the claim. Include the evidence the probe collected. The caller owns the
  release, the consecutive-hit budget and the demotion.

## Container probe

**Container guard — defense in depth for a container that is NOT type
`epic`.** After a successful claim, run TWO checks, in this order, before
treating the claimed bead as workable:

1.  **Container-note check.** Does the claimed bead's `notes` contain a
    container-marker pattern (contains "Do NOT claim this container bead for
    direct work", or is prefixed `[container note`)?
2.  **Children-existence probe — fallback for a container that was NEVER
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

The probe is READ-ONLY. If neither check matches, continue at step 2. If the
caller says the probe is already settled for this bead (an epic drill-down
descendant, or a resumed claim), skip it. On a match, collect: which check fired,
the note verbatim (check 1) or the child ids, statuses and priorities (check 2),
and the container's own priority, then:

- release policy `release` → return `container-hit` with that evidence, still
  holding the claim.
- release policy `park` → do NOT release it plainly. Invoke `pb:drain-stuck` with
  the evidence and take its CONTAINER PARK entry, then return `released`.

## Steps

2. **UNDERSTAND** (orchestrator reads the BEAD ONLY): `bd show <id>` to learn the
   target repo(s), whether the work spans repos, and whether any acceptance
   criterion can only be confirmed once the change is LIVE. You MUST NOT Read any
   file, plan, spec, or doc the bead references — those are the implementation
   subagent's to read (measured: one session read the same referenced plan doc
   eight times to compose briefs, ~20K tokens of pure duplication). Record the
   referenced paths; step 4 passes them through as pointers.
   If the bead is a handoff bead (type `handoff`; the `beads-lifecycle:handoff-bead`
   skill is the sole definition and says what counts) — do NOT ISOLATE or
   DELEGATE it: invoke that skill, follow its unattended handling (absorb and
   close) with your actor ID, then return to the caller with outcome `closed`.

3. **ISOLATE** off local main (never work a primary branch directly):
   - Single repo → ONE call:

     ```bash
     pb drain isolate --bead <id> --repo <abs-canonical-clone-path>
     ```

     It reuses an existing worktree or parked branch, otherwise creates
     `.worktrees/<id>` on `<work-branch>` off the repo's primary branch. It asks
     `pg-hooks status --porcelain` about the hooks and writes nothing into the
     worktree: git runs the hooks from the shared common dir, and the
     `precommit=` field of the output line (`Result.Precommit` in `--json`)
     reports `bundle|stale|missing|broken` (`missing` also covers `pg-hooks`
     absent or an unrecognized state). `stale`,
     `missing` and `broken` mean the commit's own hook run will NOT happen
     (the stubs print one `pg-hooks:` notice and exit 0), so tell the
     implementation subagent the commit gate is not in force rather than let
     it report hooks as passed. Exit 0 → proceed
     (the output line names the worktree). Exit 3 → conflicting isolation state
     (someone else's checkout) — do NOT force anything; route to STUCK. Any
     other failure → transient-vs-genuine per the Rules. A git timeout
     (exit 1, message naming fsmonitor/fseventsd contention; pb already removed
     only the worktree/branch that call created) is machine-level contention,
     i.e. transient: retry per the Rules once the load clears — do NOT kill
     processes by pattern or change any git config. A
     `pb: warning: core.worktree set in canonical config …` line on stderr
     (exit still 0) means the canonical clone's `.git/config` carries a stray
     `core.worktree`: git will report a phantom dirty tree there and the lander
     will halt at FF-0a. Proceed with the work, but do NOT clear the key (R-3)
     — surface it to the operator in your report.

   - Multiple repos → a coordinated set via the
     `pn-workspace-rules:fork-workforest` skill, keyed to the bead id.

4. **DELEGATE THE WORK** to a subagent (REQUIRED — this preserves your context).
   Read `references/rules.md` and `references/delegate.md` in full first: they hold
   the per-bead rules, the curated-packet check, the curated and uncurated briefs,
   STAMP REFUSAL, the four report statuses and the stall-phrase check. In short:
   the brief is a POINTER (bead id, absolute repo root and worktree path, referenced
   doc paths), never a payload; the subagent commits before it runs standalone
   gates, MUST NOT claim or close the bead, create beads, add dependencies or gates,
   or land anything, and ends with ONE of `done`, `done-pending-apply-verification`,
   `stuck`, `needs-more-repos`. Scan every subagent report for a stall phrase before
   trusting it.

5. **VALIDATE** from the report (first applying the stall-phrase check above —
   a stalled report is never validated as-is): the pre-apply gates MUST show a clear PASS for
   either `done` or `done-pending-apply-verification`. If a gate fails, or the
   status is `stuck` → STUCK — EXCEPT a curated packet's stamp refusal, which takes
   step 4's STAMP REFUSAL, never STUCK. If the report itself claims it closed the bead,
   created a bead, or created a dependency/gate, that claim is ITSELF a
   brief-violation to flag (orchestrator-only verbs — see step 4), regardless
   of what else the report says (bead `tc-eidt`, incident `tc-6zps`).

6. **LAND via a dedicated LANDER SUBAGENT.** Read `references/land.md` in full now:
   it holds the pinned-session check, the lander brief, the verdict verification and
   the strategy-specific LANDED rules. In short:
   - Dispatch ONE lander at a time, synchronously. It invokes
     `integrate-branch:integrate-branch` with NO handler named (or
     `pn-workspace-rules:land-workforest` for a set) and reports `landed`,
     `pr-opened`, `pr-updated` or `stopped:<reason>`.
   - Before dispatching, check whether YOUR session is pinned. A pin is proven ONLY
     by an observed harness refusal or a failed probe, never by environment-block
     text alone. Pinned AND the strategy is `ff-merge-to-main` → do NOT dispatch:
     release policy `release` releases the bead to `open` with no `human` label and
     returns `released`; release policy `park` PARKs it through `pb:drain-stuck`.
   - VERIFY the reported verdict with one observation before recording it.
   - A lost fast-forward race or a rejected non-fast-forward push is TRANSIENT:
     re-dispatch at most once more. A genuine `stopped:` routes to STUCK.
   - PUSH AUTHORITY (default policy, no tracker section): under `pull-request` you
     MAY push `<work-branch>` and create or update its DRAFT PR without asking; you
     MUST NOT merge it, enable automerge, or push a primary branch. Under
     `ff-merge-to-main` landing is local only and you MUST NOT push. With a tracker
     section, authority comes ONLY from it (see Inputs).

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
   recognize: teardown can now refuse (non-zero, naming PIDs) if a process on `wtdone`'s
   blocking allow-list (`claude`, `git`, shells, `python*`, editors, `go`, `nix`;
   override via `WTDONE_BLOCKING_COMMANDS`) is still anchored inside the isolation
   worktree — e.g. this session's own shell left standing in it, or a peer
   session's — not only for the pre-existing dirty/unmerged reasons. Anchored
   processes under other names (a language server, `caffeinate`) are ignored and
   do not block. Treat that refusal the same as any other CLEANUP
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
     **POST-DEPLOY VERIFICATION GATE** (`references/post-deploy-gate.md`); exit 0 → cleanup + close; exit 3/4
     → do NOT close, route to STUCK.

8. Return to the caller with the outcome (see "Outcomes").

## STUCK — cannot complete a claimed bead

Triggers: underspecified / needs a human decision; `pb drain isolate` exited 3
(conflicting isolation state); pre-apply gates that cannot be made to pass; a
GENUINE lander `stopped:<reason>` (not a transient ff-race/rejected push);
`pb gate attach-verified-child` exited 3 or 4; repeated failed attempts.
NOT a trigger: "another bead has to land first" (that is a dependency); a
curated packet's stamp refusal (step 4's STAMP REFUSAL — deferred, claim
released, no label).

Invoke the `pb:drain-stuck` skill with: the bead id, your actor ID, the
worktree/branch location (`<work-branch>`), and what you tried. Follow it exactly — it runs the
freshness probes first and exits by exactly one of PARK (labeled `human`,
claim released), CLOSE-AS-MOOT (with extraction), CONVERT-TO-DEPENDENCY
(edges wired, claim released, no label), or DEFER-ON-EVENT (deferred, claim
released, no label — a live external event, not a person or a bead, is the
blocker). Then return to the caller with outcome `released` (`closed` after CLOSE-AS-MOOT).

## CLOSE-WITH-ABSORPTION-TRACE (a handoff bead)

Reached from UNDERSTAND for a handoff bead: invoke the `beads-lifecycle:handoff-bead` skill,
follow its unattended handling to the close with your actor ID, then return to the caller with outcome `closed`. The skill
is the sole contract; this command does not restate it.
