# Canonical-primary-branch + Worktree Discipline — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish "canonical clone always on its primary branch; agents work in a worktree/workforest by default" as global rules, and ship a standalone `integrate-branch` tool that adapts a repo's completed work to the right integration method (ff-merge / PR / custom).

**Architecture:** A bats-tested advisory bash tool (`integrate-branch-support`) reports a repo's integration facts + a `strategy` (or `null`); a Claude skill (`integrate-branch`) reads that report and delegates to a handler skill (`ff-merge-to-main` / `pull-request`). Rules go in the nix-managed global rules file; the `pn`-workspace skill is updated to land via the tool; six stale memories are deleted. Design pattern: **Strategy** (interchangeable handlers) behind an advisory detector; the tool is **not** coupled to `pn` or beads.

**Tech Stack:** Bash (`mkBashScript` builder + bats), Nix (home-manager modules, `mkClaudeMarketplace`), Claude Code skills/plugins, `bd` (beads) for the memory sweep.

**Spec:** `docs/superpowers/specs/2026-07-13-canonical-main-worktree-discipline-design.md` (v6). This plan implements that spec; section refs below (e.g. §4.4) point into it.

## Global Constraints

- **Validation gate:** `nix flake check` MUST pass; nix changes validated build-only (`darwin-rebuild check --flake .`) — NEVER `switch`/`darwin-rebuild switch` (user-only). NEVER `sudo`.
- **Bats tests MUST be isolated** — generate scenarios in a temp dir; mock external tools (`gh`, `bd`).
- **`nix flake check` does NOT auto-gate a package's bats `.check`** — the new package's `.check` MUST be explicitly merged into the flake `checks` attrset (template: `gc-dolt-maintenance` at `flake.nix:1413-1417`).
- **Package versioning is automatic** via `mkSrcDigest` per-source digest — do NOT thread a repo gitHash.
- **Custom git-config prefix:** `pgii-integrate-branch.*` (keys: `.strategy`, `.primaryBranch`).
- **The tool MUST stay decoupled** — no `pn-workspace.toml` / central-registry / required-beads dependency; git-standard sources only. Beads is an _optional_, non-fatal signal.
- **Commit style:** conventional commits; no `Refs:` line (branch is `worktree-discipline-design`, no Jira ticket). NEVER `--no-verify`.
- Work happens in the existing worktree `phillipgreenii-nix-agent-support/.worktrees/worktree-discipline-design` (branch `worktree-discipline-design`); the `repo-base` changes need a second worktree (Phase 4) — this is the cross-repo case, so use a **workforest** if landing together.

---

## File Structure

**`phillipgreenii-nix-agent-support`:**

- `packages/integrate-branch-support/` — NEW bash package (`default.nix`, `integrate-branch-support.sh`, `integrate-branch-support.bash` [logic], `integrate-branch-support.md` [tldr], `completions/`, `tests/*.bats`).
- `flake.nix` — MODIFY: overlay entry + `packages` re-export + merge `.check` into `checks`.
- `home/programs/integrate-branch-support/default.nix` — NEW home module (`home.packages = [ pkg ]`).
- `home/default.nix` — MODIFY: import the new module.
- `claude-marketplace/integrate-branch/` — NEW plugin (`.claude-plugin/plugin.json`, `skills/integrate-branch/SKILL.md`, `skills/ff-merge-to-main/SKILL.md`, `skills/pull-request/SKILL.md`, `README.md`).
- `claude-marketplace/.claude-plugin/marketplace.json` — MODIFY: register the plugin.
- `home/programs/agent-rules/pgii-agent-rules.md` — MODIFY: add Tier R.

**`phillipg-nix-repo-base`:**

- `pn-workspace-rules/skills/pn-workspace-rules/SKILL.md` — MODIFY: Tier P edits.
- `docs/worktrees.md` — MODIFY: reconcile work-around narrative.

**Workspace root:**

- `pn-workspace.toml` — MODIFY: per-repo `post-clone` hooks provisioning `pgii-integrate-branch.strategy`.
- Shared `bd` DB — 6 `bd forget` deletions.

---

## Phase 1 — `integrate-branch-support` bash tool (advisory detector)

### Task 1: Scaffold the package + gated first test

**Files** (mirror `packages/git-tools/` exactly — a two-level aggregate+leaf shape):

- Create aggregate: `packages/integrate-branch-support/default.nix` (`{ pkgs, bashBuilders }` module)
- Create leaf: `packages/integrate-branch-support/integrate-branch-support/{default.nix, integrate-branch-support.sh, integrate-branch-support.bash, tests/test-integrate-branch-support.bats, tests/test_helper.bash}` (copy `test_helper.bash` from `claude-marketplace/bash-scripting/skills/bash-scripting/assets/test_helper.bash`)
- Modify: `flake.nix` (overlay entry ~line 178; `packages` re-export ~line 1434; `checks` merge ~line 1413)

Note: `SCRIPTS_DIR` in the bats `setup()` = `dirname(BATS_TEST_FILENAME)/..` → the leaf dir; `BIN="${SCRIPTS_DIR}/integrate-branch-support.sh"`.

**Interfaces:**

- Produces: a CLI `integrate-branch-support` that prints a JSON verdict to stdout and exits 0 on success, nonzero on hard error (§4.3–§4.4 of the spec).

- [ ] **Step 1: Write a failing smoke test**

```bash
# tests/test-integrate-branch-support.bats
setup() {
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  BIN="${SCRIPTS_DIR}/integrate-branch-support.sh"
  TEST_DIR="$(mktemp -d)"; cd "$TEST_DIR"
  git init -q --initial-branch=main
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m init
}
teardown() { rm -rf "$TEST_DIR"; }

@test "prints valid JSON with a strategy field" {
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e 'has("strategy")'
}
```

- [ ] **Step 2: Run it, verify it fails** — `bats tests/test-integrate-branch-support.bats` → FAIL (script missing).

- [ ] **Step 3: Minimal script**

```bash
# integrate-branch-support.sh   (NO shebang — mkBashScript adds it)
set -euo pipefail
jq -n '{strategy: null, reason: "stub", primary_branch: "main",
        canonical: {branch: "main", dirty: false}, remote: null, open_pr: null, mr_bead: null}'
```

- [ ] **Step 4: `default.nix` (two files, matching `packages/git-tools`)**

Leaf `packages/integrate-branch-support/integrate-branch-support/default.nix`:

```nix
{ mkBashScript, pkgs }:
mkBashScript {
  name = "integrate-branch-support";
  src = ./.;
  description = "Advisory: report a repo's integration facts + recommended strategy";
  runtimeDeps = [ pkgs.git pkgs.jq ];
  testDeps = [ pkgs.git pkgs.jq ];
}
```

Aggregate `packages/integrate-branch-support/default.nix`:

```nix
{ pkgs, bashBuilders }:
let
  integrate-branch-support = pkgs.callPackage ./integrate-branch-support {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };
in
{
  inherit integrate-branch-support;
  packages = integrate-branch-support.packages;
  tldr = integrate-branch-support.tldr;
  checks = { test-integrate-branch-support = integrate-branch-support.check; };
}
```

- [ ] **Step 5: Wire into flake.nix (three edits, mirroring `git-tools` + `gc-dolt-maintenance`)**

Overlay entry (~line 178, exactly like `git-tools`):

```nix
integrate-branch-support =
  let
    result = import ./packages/integrate-branch-support { pkgs = final; inherit bashBuilders; };
  in
  final.symlinkJoin {
    name = "integrate-branch-support-0.0.0-${phillipgreenii-nix-base.lib.mkSrcDigest result.packages}";
    paths = result.packages;
  };
```

`packages` re-export (~line 1434) — add `integrate-branch-support` to the existing `inherit (pkgs) …;` list.
`checks` merge (~line 1413, note the **plural** `.checks` via `//`, like `gc-dolt-maintenance`):

```nix
# ... existing checks ...
// (import ./packages/integrate-branch-support {
  inherit pkgs;
  bashBuilders = pkgs._agentSupportBashBuilders;
}).checks
```

- [ ] **Step 6: Verify** — `nix build .#integrate-branch-support` builds; `nix flake check` runs the bats check and passes.

- [ ] **Step 7: Commit** — `git add packages/integrate-branch-support flake.nix && git commit -m "feat(integrate-branch-support): scaffold advisory detector package with gated bats check"`

### Task 2: Primary-branch resolution

**Interfaces:** Produces `resolve_primary_branch()` → prints branch. Order: `git config --get pgii-integrate-branch.primaryBranch` → `git symbolic-ref --short refs/remotes/origin/HEAD` (strip `origin/`) → `main`.

- [ ] **Step 1: Failing tests**

```bash
@test "primary branch: honors pgii-integrate-branch.primaryBranch" {
  git config pgii-integrate-branch.primaryBranch trunk
  run bash "$BIN"; echo "$output" | jq -e '.primary_branch == "trunk"'
}
@test "primary branch: defaults to main when unset and no origin" {
  run bash "$BIN"; echo "$output" | jq -e '.primary_branch == "main"'
}
```

- [ ] **Step 2: Run → FAIL** (`trunk` case fails; script hardcodes `main`).
- [ ] **Step 3: Implement** in `integrate-branch-support.bash` (sourced by the `.sh`):

```bash
resolve_primary_branch() {
  local b
  b="$(git config --get pgii-integrate-branch.primaryBranch 2>/dev/null || true)"
  [ -n "$b" ] && { printf '%s' "$b"; return; }
  b="$(git symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null || true)"
  [ -n "$b" ] && { printf '%s' "${b#origin/}"; return; }
  printf 'main'
}
```

- [ ] **Step 4: Run → PASS.**
- [ ] **Step 5: Commit** — `git commit -am "feat(integrate-branch-support): resolve primary branch (config→origin/HEAD→main)"`

### Task 3: Canonical clone state (branch + dirty) via git-common-dir

**Interfaces:** Produces `canonical_root()` (main working tree from `git rev-parse --git-common-dir`), `canonical_branch()`, `canonical_dirty()` → JSON `canonical.{branch,dirty}`.

- [ ] **Step 1: Failing tests** — from a linked worktree on a feature branch, `.canonical.branch == "main"` and `.canonical.dirty == false`; after `echo x > f` in the canonical clone, `.canonical.dirty == true`. (Test builds a real worktree with `git worktree add`.)

```bash
@test "canonical: reports the main worktree's branch/dirty from inside a worktree" {
  git -c user.email=t@t -c user.name=t commit -q --allow-empty -m base
  git worktree add -q wt -b feat >/dev/null
  cd wt
  run bash "$BIN"
  [ "$status" -eq 0 ]
  echo "$output" | jq -e '.canonical.branch=="main" and .canonical.dirty==false'
}
```

Implementation note: `canonical_root()` uses `git rev-parse --git-common-dir` (verified to return the _shared_ `.git` in both the main tree and a linked worktree; `dirname` → `git -C … rev-parse --show-toplevel` yields the main working tree). Add a one-line comment noting the more-robust alternative `git worktree list --porcelain | head` for the rare `--separate-git-dir` layout.

- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement** — `canonical_root() { local c; c="$(git rev-parse --git-common-dir)"; git -C "$(dirname "$c")" rev-parse --show-toplevel; }` (the common dir's parent is the main worktree; guard when already in the main tree). `canonical_branch() { git -C "$(canonical_root)" symbolic-ref --short -q HEAD || echo "(detached)"; }`. `canonical_dirty() { [ -n "$(git -C "$(canonical_root)" status --porcelain)" ] && echo true || echo false; }`.
- [ ] **Step 4: Run → PASS.**
- [ ] **Step 5: Commit** — `git commit -am "feat(integrate-branch-support): report canonical clone branch/dirty via git-common-dir"`

### Task 4: Signals — remote, open PR, MR bead (graceful degradation)

**Interfaces:** Produces `detect_remote()` (the branch's **upstream** remote via `git rev-parse --abbrev-ref --symbolic-full-name @{upstream}` → its remote; else if exactly one `git remote`, use it; else emit null + an `ambiguous` note in `reason`), `detect_open_pr()` (`gh pr view` when `gh` present, filtering **`state == OPEN`** — a merged/closed PR yields null; never fails the tool), `detect_mr_bead()` (`bd list --type=merge-request` when `bd` present and reachable, else null). Each optional source failure is swallowed (§4.2 graceful degradation).

- [ ] **Step 1: Failing tests** — (a) no remote → `.remote == null`; (b) stubbed `gh` returning an **open** PR → `.open_pr.number`; (c) stubbed `gh` returning a **merged** PR → `.open_pr == null` (spec §4.2 "merged PR ⇒ already integrated"); (d) **`gh`/`bd` absent** → those fields `null` and `status -eq 0`; (e) two remotes, no upstream set → `.remote == null` and `reason` mentions ambiguity. Use a **scrubbed** `PATH` (`PATH="$TEST_DIR/bin:/usr/bin:/bin"`, only the shims you place under `$TEST_DIR/bin`) so the developer's real `gh`/`bd` cannot leak into the absent-case tests; place executable `gh`/`bd` mock scripts in `$TEST_DIR/bin` for the present-cases.
- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement** — guard every optional call: `command -v gh >/dev/null 2>&1 && gh pr view --json number,state 2>/dev/null || true`; parse with `jq`, keep only `state=="OPEN"`, else null; on any error emit null. Same guarded shape for `bd`. `detect_remote` resolves the upstream remote first, then the single-remote fallback, then null+ambiguity.
- [ ] **Step 4: Run → PASS.**
- [ ] **Step 5: Commit** — `git commit -am "feat(integrate-branch-support): remote/open-PR/mr-bead signals with graceful degradation"`

### Task 5: Strategy determination + verdict assembly + fail-safe

**Interfaces:** Produces the full verdict (§4.3 schema). Resolution (spec §4.2/§4.3): declared `pgii-integrate-branch.strategy` → that (flag infeasible if `pull-request` + no remote, via `reason`); else no remote → `ff-merge-to-main`; else open PR or MR bead → `pull-request`; else `null`. On not-a-git-repo → exit nonzero.

- [ ] **Step 1: Failing tests** — declared `ff-merge-to-main` → `.strategy=="ff-merge-to-main"`, `.reason` mentions "declared"; no remote (undeclared) → `ff-merge-to-main` + reason "no remote"; open PR (undeclared) → `pull-request`; remote+no-PR+undeclared → `.strategy==null`; declared `pull-request` + no remote → `.strategy=="pull-request"` with `.reason` flagging infeasibility (the agent, not the tool, decides — §4.4); run outside a git repo → `status -ne 0`. The not-a-git-repo test MUST use its own fresh non-git temp dir (the shared `setup()` `git init`s `TEST_DIR`, so `cd` to a separate `mktemp -d` for this case). Sourcing convention (applies from Task 2 on): the `.sh` sources its sibling `.bash` via a `BASH_SOURCE`-relative path — `source "$(dirname "${BASH_SOURCE[0]}")/integrate-branch-support.bash"` — so both the raw-source bats run (executing the leaf `.sh`) and the built store binary resolve the lib.
- [ ] **Step 2: Run → FAIL.**
- [ ] **Step 3: Implement** the ordered resolution + `jq -n` assembly of all fields; `git rev-parse --is-inside-work-tree` guard → nonzero exit with a stderr message on failure.
- [ ] **Step 4: Run → PASS** (all Phase-1 tests green).
- [ ] **Step 5: Add tldr `integrate-branch-support.md` + `completions/`** (copy asset templates; document the JSON contract).
- [ ] **Step 6: Verify** — `nix flake check` passes.
- [ ] **Step 7: Commit** — `git commit -am "feat(integrate-branch-support): strategy resolution, verdict JSON, fail-safe + tldr"`

### Task 6: Put it on PATH (home module)

**Files:** Create `home/programs/integrate-branch-support/default.nix` (mirror `home/programs/pg-pr/default.nix`: `options...enable`, `package`; `config.home.packages = [ cfg.package ]` + tldr custom page). Modify `home/default.nix` (add to `imports`). The machine flake that enables it sets `...programs.integrate-branch-support.enable = true` (note in the plan; done at apply time, not here).

- [ ] **Step 1:** Write the module (copy `pg-pr` module shape; swap names/package).
- [ ] **Step 2:** Add the import to `home/default.nix`.
- [ ] **Step 3: Verify build-only** — `nix flake check && darwin-rebuild check --flake .` (NEVER `switch`).
- [ ] **Step 4: Commit** — `git commit -am "feat(home): expose integrate-branch-support on PATH via home module"`

---

## Phase 2 — the `integrate-branch` plugin (skills)

### Task 7: Plugin scaffold + marketplace registration

**Files:** Create `claude-marketplace/integrate-branch/.claude-plugin/plugin.json` (`{"name":"integrate-branch","description":"Integrate the current branch by the repo's method (ff-merge / PR / custom)","version":"0.1.0","defaultEnabled":true}`); modify `claude-marketplace/.claude-plugin/marketplace.json` (add `{"name":"integrate-branch","source":"./integrate-branch","description":"..."}` to `plugins`).

- [ ] **Step 1:** Create the manifest + registry entry.
- [ ] **Step 2: Verify** — `nix build .#phillipgreenii-nix-agent-support-marketplace` succeeds; the marketplace lib check (`nix flake check`) passes.
- [ ] **Step 3: Commit** — `git commit -m "feat(integrate-branch): scaffold plugin + register in marketplace"`

### Task 8: `integrate-branch` dispatcher skill

**Files:** Create `claude-marketplace/integrate-branch/skills/integrate-branch/SKILL.md`.

Front-matter:

```yaml
---
name: integrate-branch
description: Use when integrating/landing completed work on the current branch — runs integrate-branch-support, then delegates to the right handler (ff-merge-to-main / pull-request / a declared custom handler). Adapts to the repo automatically.
---
```

Body MUST encode the §4.4 agent decision logic verbatim as steps: run `integrate-branch-support`; then (1) nothing-to-integrate [on primary / detached / 0 commits ahead of `primary_branch`] → report & stop; (2) surface canonical anomalies (`canonical.branch ≠ primary_branch` or `canonical.dirty`) — method-dependent blocking; (3) `strategy` set & feasible → invoke that handler skill (confirm it is an installed handler; infeasible declared, e.g. PR + `remote:null` → surface & ask); (4) `strategy` null → decide from facts + context, else ask (offer to persist via `git config pgii-integrate-branch.strategy`). Include the §4 dispatcher mermaid.

- [ ] **Step 1:** Write SKILL.md with the full decision procedure (source of truth: spec §4.4).
- [ ] **Step 2: Verify** — marketplace rebuilds; skill front-matter valid.
- [ ] **Step 3: Commit** — `git commit -m "feat(integrate-branch): dispatcher skill with advisory decision logic"`

### Task 9: `ff-merge-to-main` handler skill

**Files:** Create `claude-marketplace/integrate-branch/skills/ff-merge-to-main/SKILL.md`. Body = the FF-0..FF-4 flow (spec §4.5) verbatim: re-derive context from git (cwd→`<WT>`, `git rev-parse --abbrev-ref HEAD`→`<FB>`, detached→halt; git-common-dir→`<CC>`); FF-0 precondition (CC on primary & clean else halt R-3/R-8); FF-1 `git -C <WT> rebase <primary>` + conflict policy (resolve-and-summarize / abort-if-not-confident); FF-2 `git -C <CC> merge --ff-only <FB>`; FF-3 retry loop (`attempts`, stop after 2nd non-ff); FF-4 cleanup from `<CC>` with shell relocated out of `<WT>`. Include the FF mermaid.

- [ ] **Step 1:** Write SKILL.md.
- [ ] **Step 2: Commit** — `git commit -m "feat(integrate-branch): ff-merge-to-main handler skill (FF-0..FF-4)"`

### Task 10: `pull-request` handler skill

**Files:** Create `claude-marketplace/integrate-branch/skills/pull-request/SKILL.md`. Body: push the feature branch, open/update the PR, **never auto-merge** (explicit human permission required), keep branch/worktree, surface `canonical.dirty`/off-primary as an R-3 note (do not halt), report the PR URL. Add `README.md` for the plugin summarizing the Strategy design + the handler contract.

- [ ] **Step 1:** Write SKILL.md + README.md.
- [ ] **Step 2: Verify** — `nix build .#phillipgreenii-nix-agent-support-marketplace`; `nix flake check`.
- [ ] **Step 3: Commit** — `git commit -m "feat(integrate-branch): pull-request handler skill + plugin README"`

---

## Phase 3 — Tier R global rules

### Task 11: Add Git Worktree / Integration Discipline to pgii-agent-rules.md

**Files:** Modify `home/programs/agent-rules/pgii-agent-rules.md` — insert a new `### Git Worktree / Integration Discipline` subsection under `## Always-Apply Rules`, immediately after the existing `### Git Workflow` block.

Content = R-1…R-9 (spec §3 Tier R), RFC 2119, matching the file's existing `-` bullet style. Include the "primary branch (default `main`)" definition and R-9 ("use `integrate-branch`; do NOT use `superpowers:finishing-a-development-branch`").

- [ ] **Step 1:** Insert the subsection (copy R-1..R-9 text from spec §3).
- [ ] **Step 2: Verify build-only** — `nix flake check && darwin-rebuild check --flake .` (NEVER `switch`); confirm the built `home-manager-files/.claude/CLAUDE.md` contains Tier R.
- [ ] **Step 3: Commit** — `git commit -m "feat(agent-rules): add Tier R worktree/integration discipline (R-1..R-9)"`

---

## Phase 4 — Tier P (`phillipg-nix-repo-base`) + workspace provisioning

> These changes are in a **different repo** (`phillipg-nix-repo-base`) + the workspace-root `pn-workspace.toml`. Per the new rules, do them in a worktree of `repo-base` (and, if landing together with agent-support, a workforest).

### Task 12: pn-workspace-rules SKILL.md edits

**Files:** Modify `phillipg-nix-repo-base/pn-workspace-rules/skills/pn-workspace-rules/SKILL.md`.

> Locate every section by its **heading**, not by line number (numbers drift; the recon's line refs are approximate).

- [ ] **Step 1:** Rewrite the `### Landing a set onto main locally (manual merge-back recipe)` section → describe landing via the `integrate-branch` **skill** (not a `pn workspace` verb — do NOT add it to the `pn workspace` command cheat-sheet) run per repo in dependency (topo) order as a best-effort ordered transaction (spec P-2): rebase the set, run `integrate-branch` in each repo, stop-and-report on a blocked repo, keep the set, remove only when all landed (P-3). **Preserve** the P-1 (workforest-for-multi-repo) and P-4 (`--in-place` escape hatch) text that sits nearby — do not disturb it.
- [ ] **Step 2 (scope carefully — M2):** Reconcile _agent-facing off-`main` framing_ only. In `### Asymmetric-defer recovery` and `### Resuming a left-behind worktree`, the `git reset --hard origin/main` / `git branch -f main origin/main` steps document **`pn workspace update`'s own worktree-recovery flow** (they reference `.pn-update` worktrees, `workforest prune`, ADR 0009) — spec §10 keeps that pn safety net and "changing pn tooling" is a non-goal, so **do NOT delete these sections**. Only adjust any wording that presents off-`main` manipulation as a _general agent recipe_ (contradicting R-3), framing it explicitly as "pn's automated recovery," not "what you should do by hand." **KEEP** the "Dirty-repo behavior differs by mode" description verbatim.
- [ ] **Step 3:** Add a short note (near `## Coordinated Workforest Sets`) that `bd` works from a worktree/workforest (git-common-dir discovery); no "bd only from canonical root" restriction (spec P-5). (Pure addition — no existing `bd` text to remove.)
- [ ] **Step 4: Verify** — `cd phillipg-nix-repo-base && nix build .#phillipg-nix-repo-base-marketplace`; `nix flake check`.
- [ ] **Step 5: Commit** (in repo-base) — `git commit -m "feat(pn-workspace-rules): land via integrate-branch; drop off-main work-arounds; bd-from-worktree note"`

### Task 13: Reconcile docs/worktrees.md

**Files:** Modify `phillipg-nix-repo-base/docs/worktrees.md` (plain doc, no rebuild). Reconcile `### Asymmetric-defer recovery` (58-74) and `### Leave-on-failure and resuming a left-behind worktree` (34-56) with the new rules — keep the description of `pn`'s internal behavior; remove/reword the "do it by hand off-main" framing to match R-1/R-3. If any SKILL.md heading was renamed in Task 12, fix the cross-link at `docs/worktrees.md:181`.

- [ ] **Step 1:** Edit the sections.
- [ ] **Step 2: Commit** — `git commit -m "docs(worktrees): reconcile off-main narrative with canonical-primary rules"`

### Task 14: Provision `pgii-integrate-branch.strategy` for the pn repos

**Files:** Modify workspace-root `/Users/phillipg/phillipg_mbp/pn-workspace.toml` — for each of the six nix-\* repos (ff-merge-to-main), add to its existing `[[repos.<key>.hooks]]` (or a new entry) a `post-clone` `run` that sets the git config, e.g.:

```toml
[[repos.<key>.hooks]]
when = ['post-clone']
run  = [
  'git config pgii-integrate-branch.strategy ff-merge-to-main',
  'git config pgii-integrate-branch.primaryBranch main',
]
```

Also set it directly on the already-cloned canonical checkouts (the hook only fires on future clones): `git -C <repo> config pgii-integrate-branch.strategy ff-merge-to-main` for the six repos.

- [ ] **Step 1:** First **verify the hook `run` grammar** — existing entries use template tokens (`run = ['{nix_run install-pre-commit-hooks}']`); confirm pn executes a plain `run = ['git config …']` as shell (check pn's hook runner / ADR 0019), and use the token form if raw shell isn't supported. Then add the `post-clone` hook per repo **and** set the config on the already-cloned canonical checkouts (the hook only fires on _future_ clones): `for r in <the six nix-* repos>; do git -C "$r" config pgii-integrate-branch.strategy ff-merge-to-main; git -C "$r" config pgii-integrate-branch.primaryBranch main; done` (git config is shared via `$GIT_COMMON_DIR/config`, so it propagates to all worktrees).
- [ ] **Step 2: Verify** — from a repo worktree, `integrate-branch-support | jq .strategy` → `"ff-merge-to-main"`.
- [ ] **Step 3:** (pn-workspace.toml is not git-tracked at the workspace root; no commit — note it in the session summary.)

---

## Phase 5 — Memory sweep

### Task 15: Delete the six stale memories

**Files:** shared `bd` DB (run from the canonical workspace root `/Users/phillipg/phillipg_mbp`, NOT from a worktree).

- [ ] **Step 1:** `for k in worktree-isolation-for-agent-work sdd-isolate-in-worktree shared-checkout-branch-switches-during-merge workforest-landing-vs-parallel-agents workforest-bd-shared-server-corruption pn-coordinated-worktrees-status; do bd forget "$k"; done`
- [ ] **Step 2: Verify** — `bd memories worktree` and `bd memories workforest` no longer list the deleted keys.
- [ ] **Step 3:** (No git commit; bd sync handled per project bd workflow.)

---

## Delivery / apply handoff (user action — not part of task execution)

The tasks build and **build-only-validate** everything (`nix flake check`, `darwin-rebuild check`) but deliberately never activate. These changes are **inert for agents until the user applies them**:

- **Tier R** (Task 11), the **`integrate-branch` plugin** (Tasks 7-10), and the **`pn-workspace-rules`** edits (Task 12) reach agents only after the user runs the apply path (`darwin-rebuild switch` / `pn workspace apply` — **user-only**, per Global Constraints).
- The **`integrate-branch-support` home module** (Task 6) defaults `enable = false` (matching the `pg-pr`/`git-tools` precedent). A **consuming machine/host flake** (a separate repo — identify at apply time) MUST set `phillipgreenii.programs.integrate-branch-support.enable = true`, else the tool isn't on PATH and `integrate-branch` cannot run its detector.
- The `pn-workspace.toml` git config on existing clones (Task 14) takes effect immediately; the `post-clone` hook only affects _future_ clones.

Flag this handoff in the session summary so the user knows the explicit apply + enable step remains.

## Self-Review

**Spec coverage:** Tier R (§3) → Task 11; Tier P (§3 P-1..P-6) → Tasks 12/14 (P-1/P-4/P-5 are rule text in Task 12; P-2/P-3 in Task 12; P-6 in Task 14); `integrate-branch` tool (§4) → Tasks 1-10; verdict schema (§4.3) → Task 5; decision logic (§4.4) → Task 8; handlers (§4.5) → Tasks 9-10; examples (§4.6) → covered by the detector tests (Tasks 2-5) + handler skills; superpowers R-9 (§6) → Task 11; memory sweep (§7) → Task 15. `wrap-up-session` refocus is explicitly out of scope (bead `pg2-8r1rp`); the `pn` defensive path is intentionally left (spec §10). No spec section is unmapped.

**Placeholder scan:** No "TBD/TODO"; bash steps carry real code; skill/rule tasks point to specific spec sections whose text is transcribed (the executor has the spec in-repo). The one judgment call left to the executor is matching `flake.nix`'s exact `callPackage`/overlay form — noted with the `git-tools`/`gc-dolt-maintenance` templates to copy.

**Type/name consistency:** `integrate-branch` (skill), `integrate-branch-support` (tool), `ff-merge-to-main`/`pull-request` (handlers), `pgii-integrate-branch.{strategy,primaryBranch}` (git config) are used identically across tasks and match the spec. Verdict fields (`strategy, reason, primary_branch, canonical.{branch,dirty}, remote, open_pr, mr_bead`) match §4.3.

**Dependency order:** Phase 1 (tool) → Phase 2 (skills call the tool) → Phase 3 (Tier R references `integrate-branch`) → Phase 4 (Tier P references it) → Phase 5 (independent). Phase 5 may run any time.
