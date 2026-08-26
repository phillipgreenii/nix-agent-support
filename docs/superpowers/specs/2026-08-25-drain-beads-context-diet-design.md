# drain-beads Context Diet — Design

Date: 2026-08-25
Status: Approved by operator (Phillip) in-session; measurements from drain session
`a7003f8a-99d9-4543-94e3-47de9dcf98fd` (2026-08-25).

## Problem

A `/drain-beads` orchestrator session reached ~325K tokens (~33% of a 1M window) after
completing only 6 beads. Measured from the session transcript JSONL (byte counts via `jq`
over message content; token totals from the API `usage` fields on each assistant turn):

- **Fixed baseline: ~104K tokens** before the first tool call, of which the
  `/drain-beads` command body itself is 56KB (~15K tokens).
- **Growth: ~220K tokens across 6 beads (~35K/bead)**, dominated by work the prompt
  deliberately keeps in the orchestrator's own context:
  - 121 orchestrator Bash calls (41KB inputs + 104KB results): claim/isolate/land/gate/close
    mechanics, 48 of them `git -C` state checks.
  - 13 Reads (68KB): the orchestrator read the same referenced plan doc
    (`docs/superpowers/plans/2026-08-25-pg-go-mutate-tui.md` in repo-base) **eight times**
    to compose subagent briefs.
  - 6 subagent briefs (38KB): 5–10KB each, largely re-transcribed plan content.
  - `integrate-branch:integrate-branch` (11KB) + `integrate-branch:ff-merge-to-main`
    (25KB) skill bodies (~37KB together) loaded into the main context for LAND.
  - 306 assistant turns of framing + 51KB of thinking.

Delegation of _implementation_ worked (subagent reports came back at 3–4KB each). The
context leak is the orchestration work itself. At ~35K/bead the session exhausts its
window around bead ~24.

## Approved changes

Four changes, approved by the operator 2026-08-25:

1. **The orchestrator MUST NOT read a bead's referenced documents.** Briefs become
   pointers (bead id + absolute repo root + worktree path); the implementation subagent
   runs `bd show` and reads referenced docs itself.
2. **LAND moves to a dedicated lander subagent** (dispatched serially, never fanned out),
   which invokes the `integrate-branch:integrate-branch` dispatcher in its own context and
   returns a structured verdict.
3. **Two new `pb` subcommands** collapse the scripted multi-call dances into single
   invocations: `pb drain isolate` (worktree + prek-config symlink) and
   `pb gate attach-verified-child` (the 5-step post-deploy gate sequence).
4. **Rare-path procedures split out of `drain-beads.md` into on-demand skills**
   (`pb:drain-stuck`, `pb:drain-absorb-pointer`), mirroring the dispatcher/handler
   pattern `integrate-branch` already uses; incident-history narratives compress to
   one-line provenance citations.

Projected effect: per-bead orchestrator cost drops from ~35K to roughly 8–12K tokens, and
the command-body baseline drops by roughly half.

## Design

### D1. Pointer briefs (change 1)

`drain-beads.md` step 2 (UNDERSTAND) keeps `bd show <id>` in the orchestrator — it needs
the target repo(s), the handoff-pointer disposition, and multi-repo detection — but adds:
the orchestrator MUST NOT Read any file, plan, spec, or doc the bead references.

Step 4 (DELEGATE) briefs become pointers. A brief MUST contain: the bead id, the absolute
repo root (per always-on rule A-3), the worktree path, and the instruction that the
subagent run `bd show <id>` itself and read any referenced docs itself from inside the
worktree. A brief MUST NOT transcribe bead descriptions or referenced-doc content. The
brief also states (per always-on L-3 and standing feedback): explicit timeouts or
`run_in_background` for builds, never `run_in_background` for git commits, and report
fully in one turn.

### D2. Lander subagent (change 2)

Step 6 (LAND) dispatches ONE subagent per bead, synchronously — never in parallel with
another land, and never fanned out. The lander brief contains: absolute canonical repo
root, worktree path, branch `drain/<id>`, bead id, and instructions to:

- invoke the `integrate-branch:integrate-branch` skill with NO handler named (for a
  workforest set: `pn-workspace-rules:land-workforest`);
- retry transient failures itself (lost ff-race, rejected non-ff push): bounded at 3
  attempts with re-rebase between;
- NEVER merge a PR, never push a primary branch, never use `run_in_background` for
  commits, and report fully in one turn;
- return a structured report: `outcome` (`landed` | `pr-opened` | `pr-updated` |
  `stopped:<reason>`), the landed/pushed SHA per changed repo, and PR number + URL when
  applicable.

The orchestrator records the verdict and SHA only — but VERIFIES before recording,
because the report is a subagent's prose, not evidence: for `landed`, the orchestrator
verifies the REPORTED sha rather than re-deriving it from `drain/<id>` (the handler's
FF-4 deletes that branch and worktree BEFORE reporting `landed`, so it is already gone)
— `git -C <repo> merge-base --is-ancestor <reported-sha> <primary>` must hold, and that
same verified sha becomes the gate SHA; for `pr-opened`/`pr-updated`, `gh pr view <n>
--json state,isDraft` must show OPEN and draft, and the pushed head comes from
`git -C <repo> rev-parse drain/<id>` (valid only on this path, since PR-4 keeps the
branch). A verdict failing its check is treated as `stopped:`, never recorded as landed.
Re-dispatch after a transient `stopped:` is bounded at ONE (the lander already retried
3× internally).

The "Rules" section's line "CLAIM, ISOLATE, LAND, GATE, CLEANUP, CLOSE stay in THIS
session" changes to: CLAIM, GATE, CLEANUP, CLOSE stay in-session; IMPLEMENTATION and
LANDING each go to a dedicated subagent, one at a time. R-9 is still honored — landing
still flows through the `integrate-branch:integrate-branch` dispatcher, just inside the
lander's context, which has its own persistent shell (the original "skills need
persistent shell/cwd state" justification conflated "a shell" with "this session's
shell").

### D3. `pb drain isolate` (change 3a)

```
pb drain isolate --bead <id> --repo <abs-path> [--json]
```

Implemented as `internal/drain` (logic) + `cmd/pb/drain.go` / `cmd/pb/drain_isolate.go`
(cobra shell), following the existing DI seam (`run.Runner`; git via shell-out, like
`internal/patchid`). Behavior:

1. Verify `--repo` is a git repo (`git -C <repo> rev-parse --show-toplevel`).
2. Resolve the primary branch: `git config pgii-integrate-branch.primaryBranch` →
   `git symbolic-ref refs/remotes/origin/HEAD` → `main` (the R-rules resolution order).
3. Target: worktree `<repo>/.worktrees/<bead>` on branch `drain/<bead>` (paths built on
   git's own `rev-parse --show-toplevel` output, so macOS `/var` → `/private/var`
   symlinking cannot desync the worktree-list comparison).
4. If the path is registered in `git worktree list --porcelain`: a different branch is a
   CONFLICT (exit 3); the right branch with the directory present on disk reports
   `reused=worktree`; the right branch with the directory DELETED (a stale registration)
   is pruned (`git worktree prune`) and recreated below.
5. If the path exists on disk but is not registered on `drain/<bead>` (a plain
   directory, or a detached-HEAD worktree) → CONFLICT (exit 3). If branch
   `drain/<bead>` is checked out in some OTHER worktree → CONFLICT (exit 3).
6. Else if branch `drain/<bead>` exists:
   `git worktree add .worktrees/<bead> drain/<bead>` → `reused=branch`; else
   `git worktree add .worktrees/<bead> -b drain/<bead> <primary>` → `reused=none`.
7. prek config: if `<repo>/.pre-commit-config.yaml` exists (a gitignored symlink in
   canonical clones — ADR 0016 in repo-base) and the worktree lacks it, symlink the
   CANONICAL CONFIG PATH into the worktree (symlink-to-symlink — deliberately not the
   resolved store target, so a later `nix run .#install-pre-commit-hooks` propagates to
   long-lived worktrees; this deviates from the workspace CLAUDE.md's manual
   resolved-target recipe because it is strictly fresher). A dangling worktree link is
   re-pointed, never reported as `present`. Report `precommit=linked|present|none`.
8. Print one line: `worktree=<abs> branch=drain/<bead> reused=<none|worktree|branch>
precommit=<linked|present|none>` (or JSON).

Exit codes (per the always-on exit-code rule — 1 stays generic): 0 = success;
1 = generic/unexpected failure; 3 = conflicting isolation state (worktree on wrong
branch, or branch checked out elsewhere) — the orchestrator routes to STUCK, never
forces.

### D4. `pb gate attach-verified-child` (change 3b)

```
pb gate attach-verified-child --impl <impl-id> --title <child-title> \
  --gate <repo-key>=<sha> [--gate <repo-key>=<sha> ...] \
  --actor <id> [--reason <text>] [--json]
```

Implemented as `internal/gate/attach.go` (reusing `gate.Create` per `--gate` pair, so
patch-id computation, DB co-location, and `applied_baseline` metadata stay single-sourced)
plus `cmd/pb/gate_attach_verified_child.go` registered under the existing `gate` parent.
New thin methods on `internal/bd.Client`: `CreateBead`, `ReadyIDs`, `UpdateDefer`,
`Comment`.

Sequence (deferred-first is mandatory — it closes the fleet-claim race):

1. Validate the invocation BEFORE any write: at least one `--gate` is required (a
   zero-gate call would eventually un-defer a completely ungated child), and every
   `--gate` repo key must resolve in `pn workspace info` — a typo fails plain (exit 1)
   here instead of stranding a deferred child at exit 3. Then resolve the impl bead's
   DB (`resolveBeadDB`, existing).
2. Create the child deferred:
   `bd -C <db> create <title> --defer 2126-01-01 --deps discovered-from:<impl>
--actor <actor> --json`.
3. Confirm the child is NOT workable: `bd -C <db> ready --json -n 0` must return an
   envelope whose `data` KEY IS PRESENT and non-null, with the child id absent from it.
   **Divergence from the prose check:** the markdown procedure required a non-empty
   `bd ready` as a positive control because a text agent cannot distinguish "query ran
   and returned empty" from "query broke". The Go client keys the control on the
   `data` key's presence instead (a `*[]…` unmarshal target): `{}`, `{"data":null}`,
   and error envelopes all FAIL the control, while a legitimately empty queue
   (`{"data":[]}` — normal when draining the last bead) passes. A present child
   triggers ONE defer re-apply + re-check; still present → exit 4.
4. Attach one gate per `--gate` pair via `gate.Create` (child as `--blocks`, pinned sha as
   `--commit`). Any failure → leave the child deferred, report gates created so far,
   exit 3.
5. Un-defer: `bd -C <db> update <child> --defer "" --actor <actor>`.
6. Re-confirm the child is absent from `bd ready` (now held by the gates). Failure →
   exit 4.
7. Comment on the impl bead:
   `post-deploy verification gated as <child> (pn:applied).` A comment failure AFTER
   complete gating is not fatal: exit 0 with a stderr warning and
   `"comment_failed": true` in the JSON — the caller records the link manually before
   closing.
8. Print `child=<id> gates=<n>` (or JSON).

Exit codes: 0 = fully gated; 1 = generic failure before/at child creation; 3 = gating
incomplete, child left DEFERRED (safe: route the impl bead to STUCK); 4 = child may be
WORKABLE (dangerous: do NOT close the impl bead; route to STUCK and say so).

`drain-beads.md`'s POST-DEPLOY VERIFICATION GATE section shrinks to: the scope rule
(pn-workspace member + `ff-merge-to-main` only), one `pb gate attach-verified-child`
invocation with exit-code handling, and the existing `human`-child fallback.

### D5. Rare-path skills (change 4)

Two new auto-discovered skills under `claude-marketplace/pb/skills/`:

- **`drain-stuck`**: the STUCK procedure (steps 1–9) plus its two non-park exits,
  CLOSE-AS-MOOT and CONVERT-TO-DEPENDENCY, moved whole (the branch points between them
  are internal to the procedure). Invoked with: bead id, actor ID, worktree path, and
  what was tried.
- **`drain-absorb-pointer`**: CLOSE-WITH-ABSORPTION-TRACE, invoked from UNDERSTAND when
  the bead is a handoff pointer.

`drain-beads.md` keeps a ~10-line trigger stub per path that names the skill to invoke
and what context to pass. Skill bodies load only when the path fires (this session:
never), and — bonus — a Skill body is read at invoke time from the installed marketplace,
so rare-path content is fresher than the session-start-loaded command body; the existing
per-CLAIM SELF-CHECK stays scoped to `drain-beads.md` alone.

Incident-history narratives in the remaining command body compress to one-line
provenance citations (`(provenance: pg2-xxxxx)`) — the bead records hold the full
stories. RFC 2119 rule lines stay verbatim.

Cost acknowledged: each new skill adds its name+description to EVERY session's skill
listing, so both descriptions MUST stay tight (2–3 sentences).

`plugin.json` version bumps 1.0.0 → 1.1.0 (content-stamping appends the digest either
way).

## Non-goals

- No new ADR (the spec + bead + git history carry provenance; pb's external contract,
  `phillipgreenii-nix-agent-support` ADR 0018, is unchanged — these are additive verbs).
- No `pb drain cleanup`/retire verb (cleanup is already one call in the happy path).
- No change to the CLAIM path, the actor-id scheme, the SELF-CHECK, termination, or
  `/unblock-human-beads`.
- No change to `stop-draining-beads.md` / `unblock-human-beads.md`.
- No new flake checks for skill content (prettier/treefmt + existing hooks gate the
  markdown).

## Risks

- **Lander autonomy — the risk changed in kind, not just place**: previously the
  orchestrator observed every landing command; now the verdict arrives as a subagent's
  self-report, which could misstate the outcome (a violated MUST NOT, a wrong SHA that
  would pin a never-resolving gate). Mitigation is D2's mandatory orchestrator-side
  verification: one `merge-base --is-ancestor` (against the REPORTED sha, never a
  re-derive from `drain/<id>` — FF-4 deletes that branch before `landed` is reported) or
  `gh pr view` observation per land. The brief carries the same MUST NOTs the
  orchestrator had; the handler skills enforce PR-3/PR-4.
- **`bd ready` scope**: `bd ready` run via `-C <db>` sees that DB only — same behavior as
  the prose procedure, which ran from the workspace. Attach runs all its bd calls
  against the impl bead's own DB, which is strictly more consistent than the prose
  version (cwd-dependent).
- **Skill-listing growth**: two new always-on descriptions (~100 tokens per session for
  every session on the machine). Accepted; descriptions kept minimal.
