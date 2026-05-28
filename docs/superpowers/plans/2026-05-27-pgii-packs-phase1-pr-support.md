# pgii-pack-pr-support Implementation Plan (Phase 1)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the nix-packaged `pgii-pack-pr-support` pack — the migration target for the legacy `~/gc/assets/imports/zr/` pack and the half-done `phillipg-nix-ziprecruiter/modules/pg-pr-zr/pack-src/`. This plan covers the BUILD sub-phase only; cutover (deleting legacy sources) is gated on Phase 5 (pgii-bead-importer) and tracked in a follow-up plan.

**Architecture:** Use `mkPgiiPack` (from Phase 0, `lib/mkPgiiPack.nix`) to build a derivation from `packages/pgii-pack-pr-support/pack-src/`. Source content is `pg-pr-zr/pack-src/` (already nix-shaped) renamed `pg-pr-zr` → `pgii-pr-support` throughout. Doctor checks come from `~/gc/assets/imports/zr/doctor/` (PR-related subset), with `zr.pr-*` agent-prefix matchers rewritten to `pgii-pr-support.pr-*`. Order templates switch from `@SCRIPTS_DIR@` (pg-pr-zr's custom sed) to `${SCRIPTS_DIR}` (mkPgiiPack's envsubst). Wire `phillipgreenii.programs.pgii.packs.pr-support.enable` into the existing HM module with an assertion that `programs.pg-pr.enable = true` (pack scripts call `pg-pr`). Enable in `machines/phillipg-mbp-02/default.nix` so the new pack runs in parallel with the legacy `zr` pack.

**Tech Stack:** Nix flakes, home-manager, bash, bats-core, mkPgiiPack lib (Phase 0).

**Spec:** `docs/superpowers/specs/2026-05-26-pgii-packs-migration-design.md` (Phase 1 row)

**Repo root for all paths in this plan:** `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`

**Companion repo paths:**

- Source of pack body: `/Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr/pack-src/`
- Source of doctor checks: `/Users/phillipg/gc/assets/imports/zr/doctor/`
- Machine config to enable in: `/Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix`

**Phase 0 fix dependency:** Requires `phillipgreenii-nix-agent-support@d292536` (activation runs when `cities != []`). Already on `main`.

---

## File structure

**Files to create (under `packages/pgii-pack-pr-support/`):**

| File                                                                                                 | Purpose                                                                                                   |
| ---------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `packages/pgii-pack-pr-support/default.nix`                                                          | callPackage entry; calls `mkPgiiPack` with `name = "pgii-pr-support"`.                                    |
| `packages/pgii-pack-pr-support/pack-src/pack.toml`                                                   | Renders the gascity manifest. Three `[[named_session]]` entries for pr-\* agents.                         |
| `packages/pgii-pack-pr-support/pack-src/agents/pr-reviewer/{agent.toml,prompt.md}`                   | Copied verbatim from pg-pr-zr.                                                                            |
| `packages/pgii-pack-pr-support/pack-src/agents/pr-self-fixer/{agent.toml,prompt.md}`                 | Copied verbatim from pg-pr-zr.                                                                            |
| `packages/pgii-pack-pr-support/pack-src/agents/pr-triage/{agent.toml,prompt.md}`                     | Copied verbatim from pg-pr-zr.                                                                            |
| `packages/pgii-pack-pr-support/pack-src/orders/pr-watcher.toml.template`                             | Copied + `@SCRIPTS_DIR@` → `${SCRIPTS_DIR}`.                                                              |
| `packages/pgii-pack-pr-support/pack-src/orders/wake-on-work.toml.template`                           | Copied + `@SCRIPTS_DIR@` → `${SCRIPTS_DIR}`.                                                              |
| `packages/pgii-pack-pr-support/pack-src/scripts/pr-watcher.sh`                                       | Cache-path rename: `.cache/pg-pr-zr` → `.cache/pgii-pr-support`.                                          |
| `packages/pgii-pack-pr-support/pack-src/scripts/wake-on-work.sh`                                     | Header-comment rename: `pg-pr-zr pack` → `pgii-pr-support pack`.                                          |
| `packages/pgii-pack-pr-support/pack-src/doctor/check-pr-watcher-recent-runs/{doctor.toml,run.sh}`    | From legacy zr; no agent-prefix matcher present, but `pr-watcher.toml` path-ref in error message updated. |
| `packages/pgii-pack-pr-support/pack-src/doctor/check-pr-agent-woke-no-progress/{doctor.toml,run.sh}` | From legacy zr; rewrite `zr.pr-*` → `pgii-pr-support.pr-*` throughout `run.sh`.                           |
| `packages/pgii-pack-pr-support/pack-src/doctor/check-pr-feedback-backlog/{doctor.toml,run.sh}`       | From legacy zr; rewrite `zr.pr-self-fixer` → `pgii-pr-support.pr-self-fixer`.                             |
| `packages/pgii-pack-pr-support/pack-src/doctor/check-pr-feedback-throughput/{doctor.toml,run.sh}`    | From legacy zr; rewrite `zr.pr-*` → `pgii-pr-support.pr-*`.                                               |
| `packages/pgii-pack-pr-support/pack-src/doctor/check-pr-orphan-beads/{doctor.toml,run.sh}`           | From legacy zr; rewrite `zr.pr-*` → `pgii-pr-support.pr-*` (if present).                                  |
| `packages/pgii-pack-pr-support/pack-src/doctor/check-hack-1-still-needed/{doctor.toml,run.sh}`       | From legacy zr; rewrite as needed.                                                                        |

**Files to modify:**

| File                                                             | Change                                                                                                                                          |
| ---------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `flake.nix`                                                      | Register `pgii-pack-pr-support` in the overlay (alongside `pgii-pack-test-fixture`).                                                            |
| `home/programs/pgii-packs/default.nix`                           | Add `packs.pr-support.enable` option; assertion `pr-support.enable → pg-pr.enable`; add `pgii-pr-support` to the `enabledPacks` derivation map. |
| `phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix` | Add `pgii.packs.pr-support.enable = true` for parallel-run. Update flake.lock to pull in the new pack.                                          |

**Files explicitly NOT carried over (per spec):**

- `~/gc/assets/imports/zr/scripts/bead-importer.{sh,toml}` — moves to Phase 5 (`pgii-pack-bead-importer`).
- `~/gc/assets/imports/zr/scripts/notify-terminal-notifier.sh` — drop (pgii-pr-support routes through `pg-pr`, not terminal-notifier).
- `~/gc/assets/imports/zr/scripts/backfill-triage.sh`, `bead-upsert.sh`, `gh-pr-search.sh` — replaced by `pg-pr` core; no longer needed.
- `~/gc/assets/imports/zr/scripts/tests/` — bash tests for legacy scripts that no longer exist.
- `~/gc/assets/imports/zr/doctor/check-misplaced-beads/`, `check-stale-beads/`, `check-formulas-dir/` — `check-misplaced-beads` and `check-stale-beads` move to Phase 4 (`pgii-pack-gastown`); `check-formulas-dir` is generic and stays with legacy zr until cutover (then drops; gascity validates dir layout natively now).

---

## Conventions used in this plan

- All `git commit` commands assume current directory is `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support` unless stated otherwise. Each task begins implicitly with `cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`.
- `nix build` commands use `.#<attr>`.
- Pre-commit hooks (treefmt, statix, deadnix) run automatically on commit — let them.
- After each task that touches a file in `phillipg-nix-ziprecruiter`, switch to that repo's root with explicit `cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter`.

---

### Task 1: Stub the pack source directory and derivation

Create the empty pack-src skeleton and a minimal `default.nix` that callsmkPgiiPack. This proves the wiring works before we copy real content.

**Files:**

- Create: `packages/pgii-pack-pr-support/default.nix`
- Create: `packages/pgii-pack-pr-support/pack-src/pack.toml`

- [ ] **Step 1: Create the pack-src/pack.toml stub**

File: `packages/pgii-pack-pr-support/pack-src/pack.toml`

```toml
# pgii-pr-support — PR review / triage / self-fix agents + pr-watcher /
# wake-on-work orders + scripts + PR-related doctor checks.
#
# Migrated from phillipg-nix-ziprecruiter/modules/pg-pr-zr/pack-src/ (nix-built
# half-migration) and ~/gc/assets/imports/zr/doctor/ (legacy hand-built pack).
# All ZR-specific naming retired; functionality is repo/team-agnostic. Pack
# scripts depend on `pg-pr` (in PATH via phillipgreenii-nix-agent-support).

[pack]
name = "pgii-pr-support"
schema = 2

[[named_session]]
template = "pr-self-fixer"
mode = "on_demand"

[[named_session]]
template = "pr-reviewer"
mode = "on_demand"

[[named_session]]
template = "pr-triage"
mode = "on_demand"
```

- [ ] **Step 2: Create the default.nix**

File: `packages/pgii-pack-pr-support/default.nix`

```nix
{ lib, pkgs }:
let
  mkPgiiPack = import ../../lib/mkPgiiPack.nix { inherit lib pkgs; };
in
mkPgiiPack {
  name = "pgii-pr-support";
  src = ./pack-src;
  meta = with lib; {
    description = "PR review / triage / self-fix agents + pr-watcher / wake-on-work orders.";
    license = licenses.mit;
    platforms = platforms.unix;
  };
}
```

- [ ] **Step 3: Register in the flake overlay**

Modify `flake.nix`. Locate the overlay block where `pgii-pack-test-fixture` is registered:

Find the line:

```nix
          pgii-pack-test-fixture = final.callPackage ./packages/pgii-pack-test-fixture { };
```

Add immediately after it:

```nix
          pgii-pack-pr-support = final.callPackage ./packages/pgii-pack-pr-support { };
```

- [ ] **Step 4: Build the stub**

Run:

```bash
nix build .#pgii-pack-pr-support --print-out-paths
```

Expected: prints a `/nix/store/...-pgii-pr-support-0.1.0` path, exit 0.

- [ ] **Step 5: Verify minimal layout**

Run:

```bash
out=$(nix build .#pgii-pack-pr-support --print-out-paths --no-link)
test -f "$out/pack.toml"                                              && echo "ok: pack.toml"
test -d "$out/agents"                                                  && echo "ok: agents/"
test -d "$out/orders"                                                  && echo "ok: orders/"
test -d "$out/scripts"                                                 && echo "ok: scripts/"
test -d "$out/formulas"                                                && echo "ok: formulas/"
test -d "$out/doctor"                                                  && echo "ok: doctor/"
grep -q 'name = "pgii-pr-support"' "$out/pack.toml"                    && echo "ok: pack name"
grep -q '"name": "pgii-pr-support"' "$out/.pack-meta.json"             && echo "ok: meta sidecar"
```

Expected: eight `ok:` lines.

- [ ] **Step 6: Commit**

```bash
git add packages/pgii-pack-pr-support/default.nix packages/pgii-pack-pr-support/pack-src/pack.toml flake.nix
git commit -m "feat(pgii-packs): stub pgii-pack-pr-support derivation + overlay entry"
```

---

### Task 2: Port the three agents (verbatim)

Agents in `pg-pr-zr/pack-src/agents/` have no `pg-pr-zr` or `zr` references in their TOML or prompts (verified). Copy them as-is.

**Files:**

- Create: `packages/pgii-pack-pr-support/pack-src/agents/pr-reviewer/{agent.toml,prompt.md}`
- Create: `packages/pgii-pack-pr-support/pack-src/agents/pr-self-fixer/{agent.toml,prompt.md}`
- Create: `packages/pgii-pack-pr-support/pack-src/agents/pr-triage/{agent.toml,prompt.md}`

- [ ] **Step 1: Copy the agents tree verbatim**

Run:

```bash
cp -R \
  /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr/pack-src/agents/. \
  packages/pgii-pack-pr-support/pack-src/agents/
```

- [ ] **Step 2: Verify no `pg-pr-zr` / `zr.` / `ziprecruiter` references slipped in**

Run:

```bash
grep -rE "pg-pr-zr|zr\.|ziprecruiter|ZipRecruiter" \
  packages/pgii-pack-pr-support/pack-src/agents/ \
  && echo "FAIL: found references that need rewriting" \
  || echo "ok: no rewrites needed"
```

Expected: `ok: no rewrites needed`.

- [ ] **Step 3: Rebuild and verify agents land in $out**

Run:

```bash
out=$(nix build .#pgii-pack-pr-support --print-out-paths --no-link)
test -f "$out/agents/pr-reviewer/agent.toml"     && echo "ok: pr-reviewer/agent.toml"
test -f "$out/agents/pr-reviewer/prompt.md"      && echo "ok: pr-reviewer/prompt.md"
test -f "$out/agents/pr-self-fixer/agent.toml"   && echo "ok: pr-self-fixer/agent.toml"
test -f "$out/agents/pr-self-fixer/prompt.md"    && echo "ok: pr-self-fixer/prompt.md"
test -f "$out/agents/pr-triage/agent.toml"       && echo "ok: pr-triage/agent.toml"
test -f "$out/agents/pr-triage/prompt.md"        && echo "ok: pr-triage/prompt.md"
```

Expected: six `ok:` lines.

- [ ] **Step 4: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/agents
git commit -m "feat(pgii-packs): port pr-reviewer/pr-self-fixer/pr-triage agents to pgii-pack-pr-support"
```

---

### Task 3: Port + convert order templates

`pg-pr-zr/pack-src/orders/*.toml.template` use `@SCRIPTS_DIR@` (its custom sed-based substitution). `mkPgiiPack` uses `${SCRIPTS_DIR}` (envsubst-native). Convert markers during the copy.

**Files:**

- Create: `packages/pgii-pack-pr-support/pack-src/orders/pr-watcher.toml.template`
- Create: `packages/pgii-pack-pr-support/pack-src/orders/wake-on-work.toml.template`

- [ ] **Step 1: Port pr-watcher.toml.template**

Run:

```bash
sed 's|@SCRIPTS_DIR@|${SCRIPTS_DIR}|g' \
  /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr/pack-src/orders/pr-watcher.toml.template \
  > packages/pgii-pack-pr-support/pack-src/orders/pr-watcher.toml.template
```

Also rewrite the in-comment reference to `modules/pg-pr-zr/default.nix` (it's stale guidance). Open the file and replace:

OLD:

```
# @SCRIPTS_DIR@ is replaced by modules/pg-pr-zr/default.nix with the pack's
# scripts/ directory inside the nix store.
```

NEW:

```
# ${SCRIPTS_DIR} is substituted by mkPgiiPack (envsubst) with the pack's
# scripts/ directory inside the nix store.
```

- [ ] **Step 2: Port wake-on-work.toml.template**

Run:

```bash
sed 's|@SCRIPTS_DIR@|${SCRIPTS_DIR}|g' \
  /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr/pack-src/orders/wake-on-work.toml.template \
  > packages/pgii-pack-pr-support/pack-src/orders/wake-on-work.toml.template
```

No in-comment edits needed (no module ref).

- [ ] **Step 3: Verify substitution markers**

Run:

```bash
grep -n '@SCRIPTS_DIR@' packages/pgii-pack-pr-support/pack-src/orders/*.toml.template \
  && echo "FAIL: stale @SCRIPTS_DIR@ markers" \
  || echo "ok: only \${SCRIPTS_DIR} markers"

grep -nE '\$\{SCRIPTS_DIR\}/.*\.sh' packages/pgii-pack-pr-support/pack-src/orders/*.toml.template \
  || echo "FAIL: expected \${SCRIPTS_DIR}/<name>.sh exec line"
```

Expected: `ok: only ${SCRIPTS_DIR} markers` + two lines (one per order) showing the exec= entries.

- [ ] **Step 4: Rebuild and verify substitution happened**

Run:

```bash
out=$(nix build .#pgii-pack-pr-support --print-out-paths --no-link)
test -f "$out/orders/pr-watcher.toml"          && echo "ok: pr-watcher.toml present"
test -f "$out/orders/wake-on-work.toml"        && echo "ok: wake-on-work.toml present"
! test -f "$out/orders/pr-watcher.toml.template"   && echo "ok: pr-watcher template removed"
! test -f "$out/orders/wake-on-work.toml.template" && echo "ok: wake-on-work template removed"
grep -q "$out/scripts/pr-watcher.sh"   "$out/orders/pr-watcher.toml"   && echo "ok: pr-watcher SCRIPTS_DIR substituted"
grep -q "$out/scripts/wake-on-work.sh" "$out/orders/wake-on-work.toml" && echo "ok: wake-on-work SCRIPTS_DIR substituted"
```

Expected: six `ok:` lines.

- [ ] **Step 5: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/orders
git commit -m "feat(pgii-packs): port pr-watcher / wake-on-work order templates (@SCRIPTS_DIR@ → \${SCRIPTS_DIR})"
```

---

### Task 4: Port scripts with cache-path rename

`pr-watcher.sh` writes runtime state to `~/gc/.cache/pg-pr-zr/`. Rename the cache-dir literal to `~/gc/.cache/pgii-pr-support/`. `wake-on-work.sh` has only a comment to update.

**Files:**

- Create: `packages/pgii-pack-pr-support/pack-src/scripts/pr-watcher.sh`
- Create: `packages/pgii-pack-pr-support/pack-src/scripts/wake-on-work.sh`

- [ ] **Step 1: Port pr-watcher.sh with rename**

Run:

```bash
sed -e 's|\.cache/pg-pr-zr|\.cache/pgii-pr-support|g' \
    -e 's|pr-watcher\.sh — pg-pr-zr pack|pr-watcher.sh — pgii-pr-support pack|' \
    -e 's|legacy /Users/phillipg/gc/assets/imports/zr/scripts/pr-watcher\.sh|legacy ~/gc/assets/imports/zr/scripts/pr-watcher.sh|' \
  /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr/pack-src/scripts/pr-watcher.sh \
  > packages/pgii-pack-pr-support/pack-src/scripts/pr-watcher.sh
```

(The third sed normalizes the absolute path in the source-of-truth comment to a `~/...` form, which won't go stale even after we delete `zr/`.)

- [ ] **Step 2: Port wake-on-work.sh with rename**

Run:

```bash
sed -e 's|wake-on-work\.sh — pg-pr-zr pack|wake-on-work.sh — pgii-pr-support pack|' \
  /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr/pack-src/scripts/wake-on-work.sh \
  > packages/pgii-pack-pr-support/pack-src/scripts/wake-on-work.sh
```

- [ ] **Step 3: Verify no stale references**

Run:

```bash
grep -nE "pg-pr-zr|\\bzr\\b" packages/pgii-pack-pr-support/pack-src/scripts/*.sh \
  && echo "FAIL: stale references" \
  || echo "ok: scripts clean"
```

Expected: `ok: scripts clean`.

Note: `\bzr\b` matches "zr" as a word boundary; if either script legitimately contains "zr" inside a longer identifier (e.g., the rig name in a query), this check will still pass.

- [ ] **Step 4: Rebuild + verify scripts are executable**

Run:

```bash
out=$(nix build .#pgii-pack-pr-support --print-out-paths --no-link)
test -x "$out/scripts/pr-watcher.sh"   && echo "ok: pr-watcher.sh executable"
test -x "$out/scripts/wake-on-work.sh" && echo "ok: wake-on-work.sh executable"
"$out/scripts/pr-watcher.sh" --help    2>&1 | head -3
"$out/scripts/wake-on-work.sh" --help  2>&1 | head -3
```

Expected: two `ok:` lines; --help output is informational (may exit non-zero if scripts don't implement --help — that's fine).

- [ ] **Step 5: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/scripts
git commit -m "feat(pgii-packs): port pr-watcher / wake-on-work scripts (cache path pg-pr-zr → pgii-pr-support)"
```

---

### Task 5: Port doctor checks with agent-prefix rewrites

Carry six doctor checks from legacy `~/gc/assets/imports/zr/doctor/`. Where they reference `zr.pr-*` agent prefixes (in `subject == "..."`, `case` matchers, error messages, etc.), rewrite to `pgii-pr-support.pr-*`. Where they reference legacy pack file paths in error-message guidance, rewrite to point at `pgii-pr-support` paths (the new pack's order files).

**Files:**

- Create: 6 doctor check directories under `packages/pgii-pack-pr-support/pack-src/doctor/`

- [ ] **Step 1: Copy the six PR-related doctor check directories**

Run:

```bash
for d in check-pr-watcher-recent-runs \
         check-pr-agent-woke-no-progress \
         check-pr-feedback-backlog \
         check-pr-feedback-throughput \
         check-pr-orphan-beads \
         check-hack-1-still-needed; do
  cp -R "/Users/phillipg/gc/assets/imports/zr/doctor/$d" \
        "packages/pgii-pack-pr-support/pack-src/doctor/$d"
done
```

- [ ] **Step 2: Rewrite `zr.pr-*` → `pgii-pr-support.pr-*` in all six run.sh files**

Run:

```bash
for f in packages/pgii-pack-pr-support/pack-src/doctor/*/run.sh; do
  sed -i.bak \
    -e 's|zr\.pr-self-fixer|pgii-pr-support.pr-self-fixer|g' \
    -e 's|zr\.pr-reviewer|pgii-pr-support.pr-reviewer|g' \
    -e 's|zr\.pr-triage|pgii-pr-support.pr-triage|g' \
    -e 's|zr\.pr-|pgii-pr-support.pr-|g' \
    "$f"
  rm "$f.bak"
done
```

The four sed rules are layered (specific → generic) so any later occurrences not matching the first three still get rewritten.

- [ ] **Step 3: Rewrite legacy `assets/imports/zr/` path-refs in error messages**

`check-pr-watcher-recent-runs/run.sh` has a "Fix:" message that points at `~/gc/assets/imports/zr/orders/pr-watcher.toml` — that file goes away at cutover. Repoint at the new pack's order:

Run:

```bash
sed -i.bak \
  -e 's|review timeout/interval in assets/imports/zr/orders/pr-watcher.toml|review timeout/interval in the pgii-pr-support pack (orders/pr-watcher.toml.template before nix build)|' \
  packages/pgii-pack-pr-support/pack-src/doctor/check-pr-watcher-recent-runs/run.sh
rm packages/pgii-pack-pr-support/pack-src/doctor/check-pr-watcher-recent-runs/run.sh.bak
```

Also audit cache-path references:

```bash
sed -i.bak \
  -e 's|\.cache/pr-watcher|\.cache/pgii-pr-support/pr-watcher|g' \
  packages/pgii-pack-pr-support/pack-src/doctor/check-pr-watcher-recent-runs/run.sh
rm packages/pgii-pack-pr-support/pack-src/doctor/check-pr-watcher-recent-runs/run.sh.bak
```

The legacy script wrote run logs to `~/gc/.cache/pr-watcher/run-*.log`, but the new `pr-watcher.sh` writes to `~/gc/.cache/pgii-pr-support/run-*.log` (Task 4 changed that). The doctor check's "Fix" message needs to match.

- [ ] **Step 4: Verify no `zr.` agent-prefix references remain**

Run:

```bash
grep -rnE 'zr\.pr-' packages/pgii-pack-pr-support/pack-src/doctor/ \
  && echo "FAIL: stale zr.pr- refs" \
  || echo "ok: no stale zr.pr- refs"
```

Expected: `ok: no stale zr.pr- refs`.

- [ ] **Step 5: Spot-check one rewrite**

Run:

```bash
grep -n 'pgii-pr-support\.pr-' packages/pgii-pack-pr-support/pack-src/doctor/check-pr-agent-woke-no-progress/run.sh | head -5
```

Expected: 5+ lines showing `pgii-pr-support.pr-self-fixer`, `pgii-pr-support.pr-triage`, `pgii-pr-support.pr-reviewer` in matcher contexts.

- [ ] **Step 6: Rebuild + verify doctor checks land in $out**

Run:

```bash
out=$(nix build .#pgii-pack-pr-support --print-out-paths --no-link)
for d in check-pr-watcher-recent-runs \
         check-pr-agent-woke-no-progress \
         check-pr-feedback-backlog \
         check-pr-feedback-throughput \
         check-pr-orphan-beads \
         check-hack-1-still-needed; do
  test -f "$out/doctor/$d/doctor.toml" && test -x "$out/doctor/$d/run.sh" && echo "ok: $d"
done
```

Expected: six `ok: <name>` lines.

- [ ] **Step 7: Commit**

```bash
git add packages/pgii-pack-pr-support/pack-src/doctor
git commit -m "feat(pgii-packs): port six PR-related doctor checks (zr.pr-* → pgii-pr-support.pr-*)"
```

---

### Task 6: Wire `packs.pr-support` into the HM module

Add the per-pack toggle to `home/programs/pgii-packs/default.nix`. Include the `pr-support → pg-pr` dependency assertion from the spec.

**Files:**

- Modify: `home/programs/pgii-packs/default.nix`

- [ ] **Step 1: Add the option declaration**

In `home/programs/pgii-packs/default.nix`, locate the `packs = { ... };` block (inside `options.phillipgreenii.programs.pgii`). It currently has only `test-fixture.enable`:

```nix
    packs = {
      test-fixture.enable = lib.mkEnableOption ''
        pgii-pack-test-fixture (validation pack for the pgii-packs pipeline).
      '';
      # Real packs (pr-support, dolt-hacks, workers, gastown, bead-importer)
      # are added in their respective phase plans.
    };
```

Replace the trailing comment line with the new option (delete the comment about pr-support being added in its phase plan, since this IS that plan):

```nix
    packs = {
      test-fixture.enable = lib.mkEnableOption ''
        pgii-pack-test-fixture (validation pack for the pgii-packs pipeline).
      '';
      pr-support.enable = lib.mkEnableOption ''
        pgii-pack-pr-support (PR review / triage / self-fix agents + pr-watcher
        and wake-on-work orders + PR-related doctor checks). Pack scripts depend
        on `pg-pr` in PATH — enable via `phillipgreenii.programs.pg-pr.enable`.
      '';
      # Real packs (dolt-hacks, workers, gastown, bead-importer) are added in
      # their respective phase plans.
    };
```

- [ ] **Step 2: Add to `enabledPacks`**

In the same file, locate the `enabledPacks` definition (currently it `lib.optional`s only the test-fixture). Replace with a list comprehension that includes both:

OLD:

```nix
  enabledPacks = lib.optional cfg.packs.test-fixture.enable {
    name = "pgii-pack-test-fixture";
    drv = pkgs.pgii-pack-test-fixture;
  };
```

NEW:

```nix
  enabledPacks =
    lib.optional cfg.packs.test-fixture.enable {
      name = "pgii-pack-test-fixture";
      drv = pkgs.pgii-pack-test-fixture;
    }
    ++ lib.optional cfg.packs.pr-support.enable {
      name = "pgii-pr-support";
      drv = pkgs.pgii-pack-pr-support;
    };
```

Note: `name` is the GASCITY pack name (`pgii-pr-support`, as written in the pack.toml), NOT the nix package name (`pgii-pack-pr-support`). The activation script writes `[imports.pgii-pr-support]` blocks.

- [ ] **Step 3: Add the pr-support → pg-pr dependency assertion**

Locate the `assertions = [ ... ]` block (in the unconditional config block from Phase 0's d292536 fix). Add a new assertion entry:

```nix
        {
          assertion =
            !cfg.packs.pr-support.enable
            || (config.phillipgreenii.programs.pg-pr.enable or false);
          message = ''
            phillipgreenii.programs.pgii.packs.pr-support.enable requires
            phillipgreenii.programs.pg-pr.enable = true (pack scripts call
            pg-pr).
          '';
        }
```

Place it BEFORE the existing `!anyPackEnabled || cfg.gascity.cities != [ ]` assertion (more specific first).

- [ ] **Step 4: Verify the module evaluates**

Run:

```bash
nix flake check 2>&1 | tail -20
```

Expected: exit 0, no assertion failures, no unused-binding warnings.

If `nix flake check` is too slow, just evaluate the module on its own with the home-manager test harness:

```bash
nix eval --raw .#homeConfigurations 2>&1 | head -5
```

(May or may not work depending on flake exposure; the `nix flake check` is the canonical run.)

- [ ] **Step 5: Commit**

```bash
git add home/programs/pgii-packs/default.nix
git commit -m "feat(pgii-packs): wire packs.pr-support option + pg-pr dependency assertion"
```

---

### Task 7: Add a build-only check that pgii-pack-pr-support shape matches gascity expectations

The existing `test-pgii-packs-activation` flake check covers `activation.sh` (bats). There's no flake check that validates a NON-fixture pack's $out shape. Add one for pgii-pack-pr-support that mirrors the verification commands from Task 1 step 5 + Task 3 step 4.

This is cheap insurance: if a future change breaks the pack layout (missing dir, unsubstituted template, etc.), the flake check fails.

**Files:**

- Modify: `flake.nix`

- [ ] **Step 1: Add a `check-pgii-pack-pr-support-layout` flake check**

In `flake.nix`, locate the `checks = ...` (or `perSystem.checks`) block where `test-pgii-packs-activation` is defined. Add a sibling check:

```nix
            check-pgii-pack-pr-support-layout = pkgs.runCommand "check-pgii-pack-pr-support-layout"
              { } ''
                pack=${pkgs.pgii-pack-pr-support}
                test -f "$pack/pack.toml"                                   || { echo "missing pack.toml"; exit 1; }
                test -f "$pack/.pack-meta.json"                             || { echo "missing .pack-meta.json"; exit 1; }
                test -f "$pack/agents/pr-reviewer/agent.toml"               || { echo "missing pr-reviewer"; exit 1; }
                test -f "$pack/agents/pr-self-fixer/agent.toml"             || { echo "missing pr-self-fixer"; exit 1; }
                test -f "$pack/agents/pr-triage/agent.toml"                 || { echo "missing pr-triage"; exit 1; }
                test -f "$pack/orders/pr-watcher.toml"                      || { echo "missing pr-watcher order"; exit 1; }
                test -f "$pack/orders/wake-on-work.toml"                    || { echo "missing wake-on-work order"; exit 1; }
                test -x "$pack/scripts/pr-watcher.sh"                       || { echo "pr-watcher.sh not exec"; exit 1; }
                test -x "$pack/scripts/wake-on-work.sh"                     || { echo "wake-on-work.sh not exec"; exit 1; }
                for d in check-pr-watcher-recent-runs \
                         check-pr-agent-woke-no-progress \
                         check-pr-feedback-backlog \
                         check-pr-feedback-throughput \
                         check-pr-orphan-beads \
                         check-hack-1-still-needed; do
                  test -f "$pack/doctor/$d/doctor.toml" || { echo "missing doctor/$d/doctor.toml"; exit 1; }
                  test -x "$pack/doctor/$d/run.sh"      || { echo "doctor/$d/run.sh not exec"; exit 1; }
                done
                ! find "$pack" -name "*.template" | grep -q . || { echo "stale .template files in pack"; exit 1; }
                ! grep -rnE 'zr\.pr-' "$pack/doctor" >/dev/null 2>&1 || { echo "stale zr.pr- refs in doctor/"; exit 1; }
                touch $out
              '';
```

- [ ] **Step 2: Run the new check**

Run:

```bash
nix build .#checks.aarch64-darwin.check-pgii-pack-pr-support-layout
```

(Replace `aarch64-darwin` with your system if different.)

Expected: exit 0, builds the `$out` sentinel.

- [ ] **Step 3: Run the full flake check set**

Run:

```bash
nix flake check 2>&1 | tail -5
```

Expected: exit 0.

- [ ] **Step 4: Commit**

```bash
git add flake.nix
git commit -m "test(pgii-packs): flake check for pgii-pack-pr-support layout + ZR-ref scrub"
```

---

### Task 8: Enable in machine config for parallel-run

Switch on `pgii.packs.pr-support.enable = true` in the test machine. Legacy `zr` pack stays imported via `~/gc/pack.toml` until cutover — both packs run side-by-side for the validation week.

**Files:**

- Modify: `phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix`

- [ ] **Step 1: Update the flake.lock in nix-ziprecruiter**

The agent-support input must point at a revision that contains pgii-pack-pr-support. Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
nix flake lock --update-input phillipgreenii-nix-agent-support 2>&1 | tail -5
```

Expected: lock file updated; new rev recorded.

- [ ] **Step 2: Edit `machines/phillipg-mbp-02/default.nix`**

Locate the `pgii = { ... };` block (around line 227 after Phase 0). It currently reads:

```nix
        pgii = {
          gascity.cities = [ "/Users/phillipg/gc" ];
          packs.test-fixture.enable = false;
        };
```

Replace with:

```nix
        pgii = {
          gascity.cities = [ "/Users/phillipg/gc" ];
          packs.test-fixture.enable = false;
          packs.pr-support.enable = true;
        };
```

Also confirm `phillipgreenii.programs.pg-pr.enable = true` is set elsewhere in the same file (it should be — `pg-pr.enable = true` was added in commit `94c77e9`). If not, add it.

- [ ] **Step 3: Build the new system without applying (dry-run)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
darwin-rebuild build --flake .#phillipg-mbp-02 2>&1 | tail -20
```

Expected: exit 0. If an assertion fires (e.g., pr-support without pg-pr), fix and retry.

- [ ] **Step 4: Commit (do NOT apply yet)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
git add flake.lock machines/phillipg-mbp-02/default.nix
git commit -m "config(phillipg-mbp-02): enable pgii.packs.pr-support for Phase 1 parallel-run

Bumps phillipgreenii-nix-agent-support lock to pick up pgii-pack-pr-support.
Legacy ~/gc/assets/imports/zr/ remains imported via pack.toml — both packs
run side-by-side for the validation week before cutover (gated on Phase 5).
Refs: gc-jz2l."
```

---

### Task 9: Apply and verify on the real city

Run the rebuild and validate the pack is loaded, the agents are registered, the orders fire, and doctor checks pass.

- [ ] **Step 1: Apply**

Run:

```bash
zn-self-apply
```

Wait for completion. Expected: exit 0, "activation: pgii-packs" log line mentions writing an `[imports.pgii-pr-support]` block to `~/gc/pack.toml`.

- [ ] **Step 2: Verify pack.toml gained the managed block**

Run:

```bash
grep -A 4 'BEGIN pgii-pack:pgii-pr-support' /Users/phillipg/gc/pack.toml
```

Expected: shows `[imports.pgii-pr-support]` with `source = "/nix/store/<hash>-pgii-pr-support-0.1.0"` and `export = true`.

- [ ] **Step 3: Verify gc supervisor reload was clean**

Run:

```bash
gc supervisor reload
tail -20 ~/.gc/supervisor.log | grep -E "(reload|pr-support|pr-watcher|wake-on-work)"
```

Expected: `Config reloaded: ... agents, ... rigs (rev <hash>)` and no error lines about pgii-pr-support.

- [ ] **Step 4: Verify orders are registered**

Run:

```bash
gc order list | grep -E "(pr-watcher|wake-on-work)"
```

Expected: shows TWO sets of each order — one from legacy `zr`, one from `pgii-pr-support`. They're distinguishable by pack name in `gc order show <name>`.

Note: if both packs define orders with the same name (e.g., `pr-watcher`), gascity may handle this by namespace or by collision-error. Verify behavior; if collision-error, document and consider renaming new orders to `pr-watcher-pgii` for the parallel-run window only.

- [ ] **Step 5: Verify the doctor checks are registered**

Run:

```bash
gc doctor list | grep -E "(pr-watcher-recent-runs|pr-agent-woke-no-progress|pr-feedback-backlog|pr-feedback-throughput|pr-orphan-beads|hack-1-still-needed)"
```

Expected: six entries from pgii-pr-support (in addition to whatever legacy zr contributes).

- [ ] **Step 6: Run a single doctor check manually**

```bash
gc doctor run check-pr-watcher-recent-runs 2>&1 | head -20
```

Expected: exit 0 or 2 (informational/alert; both are acceptable) — NOT a script error. If the script errors out (parse failure, missing tool, etc.), fix the carryover.

---

### Task 10: Validation-week checklist + cutover prerequisites

Document what to watch during the parallel-run week, and the prerequisites that gate cutover.

- [ ] **Step 1: Open a follow-up bead for cutover**

```bash
gc bd create --title="Phase 1 cutover: delete legacy zr pack + retire pg-pr-zr module" \
  --type=task --priority=2 \
  --description="Gated on Phase 5 (pgii-bead-importer) per spec — legacy zr/scripts/bead-importer.sh is the reference source for Phase 5's rewrite.

After Phase 5 lands AND ~7 days of parallel-run show equivalent bead activity:
1. Set pgii.packs.pr-support.enable = true (already done in parallel-run task).
2. Remove [imports.zr] from ~/gc/pack.toml manually (it's hand-written, not pgii-managed).
3. zn-self-apply; supervisor reload; verify legacy zr orders are gone from \`gc order list\`.
4. \`git rm -rf ~/gc/assets/imports/zr\`; commit.
5. \`git rm -rf phillipg-nix-ziprecruiter/modules/pg-pr-zr/pack-src\`; keep modules/pg-pr-zr/default.nix only for the Jira wrapper, renamed pg-pr-issues-jira-zr → pg-pr-issues-jira.
6. Update zr.pgPrZr → phillipgreenii.programs.pg-pr-issues-jira in the machine config.

Validation criteria for cutover:
- pgii-pr-support's pr-watcher has fired ≥48 times (10m interval × 7 days) with ≥95% success rate.
- pgii-pr-support's three pr-* agents have processed ≥10 beads each.
- pgii-pr-support's doctor checks all return 0 over the last 24h.
- No mail escalations about pgii-pr-support agents.
"
```

Note the returned bead ID; link it to gc-jz2l with `gc bd dep add <new-id> gc-jz2l` (so the cutover depends on Phase 1 build closing).

- [ ] **Step 2: Daily validation script (informational)**

For each day of the parallel-run week, the operator should run:

```bash
# Compare order completion counts: legacy vs new
gc events --since 24h --type order.completed | jq -r '.subject' | sort | uniq -c | grep -E "pr-watcher|wake-on-work"

# Compare bead activity by agent prefix
gc bd list --status=open --json | jq -r '[.[] | .assignee // "unassigned"] | group_by(.) | map({k: .[0], v: length}) | .[]' | grep -E "(zr|pgii-pr-support)"

# Doctor health on the new pack
gc doctor run check-pr-watcher-recent-runs check-pr-agent-woke-no-progress check-pr-feedback-backlog
```

Expected gradient: pgii-pr-support activity rises while legacy zr activity stays steady; no doctor errors on the new pack.

- [ ] **Step 3: Close the build sub-phase bead**

Once Task 9 verifies cleanly (or after a 24h soak with no surprises), close gc-jz2l:

```bash
gc bd close gc-jz2l --reason="Build sub-phase complete. pgii-pack-pr-support derivation builds clean, flake check passes, parallel-run live on phillipg-mbp-02. Cutover tracked in <new-bead-id>; gated on Phase 5."
```

This unblocks Phase 2 (gc-sjz9).

---

## Risks and unknowns

1. **Order-name collision during parallel-run.** Legacy `zr` pack and new `pgii-pr-support` both define `pr-watcher` and `wake-on-work` orders. Gascity may collision-error or may load both. Task 9 Step 4 verifies — if collision, rename new orders to `pr-watcher-pgii` / `wake-on-work-pgii` for the parallel window (separate commit; revert at cutover).

2. **Agent-name collision.** Both packs register `pr-self-fixer`, `pr-reviewer`, `pr-triage` as `named_session` templates. Per the gascity supervisor's earlier collision logs I've seen in this workspace ("session alias already exists"), the new pack's templates may shadow the legacy ones. If that surfaces, document and treat as cutover trigger (or rename templates during parallel window).

3. **Doctor check ownership.** Both packs may register the same `check-pr-watcher-recent-runs` directory. `gc doctor list` may show duplicates or collision-error. Same triage as above.

4. **`gc bd list` query in `pr-reviewer/agent.toml`.** The query filters on `process-feedback:` title prefix and `role != "mine"`. The new pack's query is identical to legacy zr's. If beads end up being processed twice (once by legacy zr, once by pgii-pr-support), it's a sign the on_demand semantics aren't deduplicating across packs.

5. **`pg-pr` PATH expectation.** Scripts call `pg-pr` unqualified. Confirm `pg-pr` is on PATH for the order's `exec=` environment (it should be, since `pg-pr` is a `home.packages` entry from `phillipgreenii.programs.pg-pr.enable = true`).

---

## Self-review checklist (run before declaring this plan ready)

- [ ] Spec coverage: Every row in the Phase 1 table of the spec has a corresponding task. Cross-check: pack body (Tasks 2-4), pack name in markers (built into mkPgiiPack from Phase 0, no task needed), pack.toml.template header scrub (Task 1), Jira wrapper rename (out of scope — separate bead), doctor checks (Task 5), legacy bead-importer drop (handled by NOT carrying it over — Task 5), legacy notify-terminal-notifier drop (same), parallel-run (Task 8), cutover (Task 10 follow-up bead).
- [ ] Placeholder scan: search this plan for `TBD`, `TODO`, `fill in`, `similar to`. None should remain.
- [ ] Type consistency: gascity pack name is `pgii-pr-support`; nix package name is `pgii-pack-pr-support`. Both used consistently.
- [ ] All `git commit` commands include the right files (no stray `git add .`).
- [ ] All `nix build`/`nix flake check` commands target the correct attribute paths.

---

## References

- **Spec (this phase):** `docs/superpowers/specs/2026-05-26-pgii-packs-migration-design.md` (Phase 1 row)
- **Phase 0 plan (pattern source):** `docs/superpowers/plans/2026-05-26-pgii-packs-phase0-machinery.md`
- **Phase 0 module fix:** `phillipgreenii-nix-agent-support@d292536` (activation now runs when `cities != []`)
- **Half-done migration source:** `/Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr/`
- **Legacy pack source:** `/Users/phillipg/gc/assets/imports/zr/`
- **mkPgiiPack:** `lib/mkPgiiPack.nix` (Phase 0)
- **Activation script:** `home/programs/pgii-packs/activation.sh` (Phase 0)
- **gc-jz2l epic:** Phase 1 work tracking bead.
