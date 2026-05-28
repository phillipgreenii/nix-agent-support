# pgii-pack-gastown Implementation Plan (Phase 4)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the nix-packaged `pgii-pack-gastown` pack — the migration target for `~/gc/assets/imports/pgii-gastown/`. Resolves two side-issues per user direction: drops the foreman pool-config (eliminates the long-standing `pgii-gastown.foreman` alias-conflict surfaced in supervisor.log 1,957× / a day) and updates the foreman prompt's `zr-worker` reference to `pg-worker`.

**Architecture:** Use `mkPgiiPack` (Phase 0, extended in Phase 3 with `${PACK_ROOT}`) to build a derivation from `packages/pgii-pack-gastown/pack-src/`. Pack content is a verbatim copy of `~/gc/assets/imports/pgii-gastown/` minus the legacy `zr-worker/` directory (already gone from the source). Foreman's `agent.toml` becomes `agent.toml.template` so `${PACK_ROOT}/agents/foreman/work_query.sh` resolves at build time, and its pool fields (`min_active_sessions = 0`, `max_active_sessions = 1`) are dropped (per spec — foreman stays purely `[[named_session]] mode = "on_demand"`). Foreman's prompt is updated: `zr-worker` → `pg-worker` (one occurrence, line 4). Also carries `check-misplaced-beads` and `check-stale-beads` doctor checks from `~/gc/assets/imports/zr/doctor/` into pgii-gastown's `doctor/` per the spec (deacon-owned bd-hygiene checks). All four agents (mayor, deacon, operator, foreman) are city-scope so `mkPgiiPack` infers `scope = "city"` and `activation.sh` writes `[imports.pgii-gastown]` (standard Phase 1/2 shape). Mayor keeps its no-agent.toml layout (verified via `gc config show`: gascity uses internal defaults when agent.toml is absent — current effective config is just `name + prompt_template`).

**Tech Stack:** Nix flakes, home-manager, bash, bats-core, mkPgiiPack lib (uses Phase 0 + Phase 3 machinery).

**Spec:** `docs/superpowers/specs/2026-05-26-pgii-packs-migration-design.md` (Phase 4 row + "Open items deferred to their phases" → "Phase 4 — audit prompts for ZR refs")

**Repo root for all paths in this plan:** `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`

**Companion repo paths:**

- Source of pack body: `/Users/phillipg/gc/assets/imports/pgii-gastown/`
- Source of bd-hygiene doctor checks: `/Users/phillipg/gc/assets/imports/zr/doctor/{check-misplaced-beads,check-stale-beads}/`
- Machine config to enable in: `/Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix`
- City pack.toml to clean up at cutover: `/Users/phillipg/gc/pack.toml` (drop `[imports.pgii-gastown]`)

**Phase 3 dependency:** Done (gc-xzh5 closed; pgii-pack-workers live; Phase 0 machinery now supports `${PACK_ROOT}`).

---

## File structure

**Files to create (under `packages/pgii-pack-gastown/`):**

| File                                                                                       | Purpose                                                                                                                            |
| ------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------- |
| `packages/pgii-pack-gastown/default.nix`                                                   | callPackage entry; `mkPgiiPack { name = "pgii-gastown"; ... }`.                                                                    |
| `packages/pgii-pack-gastown/pack-src/pack.toml`                                            | Gascity manifest. Four `[[named_session]]` entries (mayor/deacon/operator/foreman). Comment block stays.                           |
| `packages/pgii-pack-gastown/pack-src/agents/mayor/prompt.md`                               | Verbatim copy. **Mayor has NO `agent.toml` in the legacy pack** — preserves runtime behavior (gascity uses internal defaults).     |
| `packages/pgii-pack-gastown/pack-src/agents/deacon/agent.toml`                             | Verbatim copy.                                                                                                                     |
| `packages/pgii-pack-gastown/pack-src/agents/deacon/prompt.template.md`                     | Verbatim copy.                                                                                                                     |
| `packages/pgii-pack-gastown/pack-src/agents/operator/agent.toml`                           | Verbatim copy.                                                                                                                     |
| `packages/pgii-pack-gastown/pack-src/agents/operator/prompt.md`                            | Verbatim copy.                                                                                                                     |
| `packages/pgii-pack-gastown/pack-src/agents/foreman/agent.toml.template`                   | Modified: `work_query = "${PACK_ROOT}/agents/foreman/work_query.sh"` AND drop `min_active_sessions` + `max_active_sessions` lines. |
| `packages/pgii-pack-gastown/pack-src/agents/foreman/prompt.template.md`                    | Modified: `zr-worker` → `pg-worker` on line 4. Rest unchanged.                                                                     |
| `packages/pgii-pack-gastown/pack-src/agents/foreman/work_query.sh`                         | Verbatim copy.                                                                                                                     |
| `packages/pgii-pack-gastown/pack-src/formulas/mol-deacon-patrol.toml`                      | Verbatim copy.                                                                                                                     |
| `packages/pgii-pack-gastown/pack-src/doctor/check-gastown-divergence/{doctor.toml,run.sh}` | Verbatim copy.                                                                                                                     |
| `packages/pgii-pack-gastown/pack-src/doctor/check-misplaced-beads/{doctor.toml,run.sh}`    | Copy from `~/gc/assets/imports/zr/doctor/check-misplaced-beads/` (deacon-owned bd-hygiene check).                                  |
| `packages/pgii-pack-gastown/pack-src/doctor/check-stale-beads/{doctor.toml,run.sh}`        | Copy from `~/gc/assets/imports/zr/doctor/check-stale-beads/` (deacon-owned bd-hygiene check).                                      |

**Files to modify:**

| File                                                             | Change                                                                                                                   |
| ---------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `flake.nix`                                                      | Register `pgii-pack-gastown` in overlay + `packages` inherit; add `check-pgii-pack-gastown-layout` flake check.          |
| `home/programs/pgii-packs/default.nix`                           | Add `packs.gastown.enable` option; add `pgii-gastown` to `enabledPacks` (gascity name, not nix package name).            |
| `phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix` | Add `pgii.packs.gastown.enable = true`. Bump `phillipgreenii-agent-support` flake.lock.                                  |
| `/Users/phillipg/gc/pack.toml`                                   | Remove the hand-written `[imports.pgii-gastown]` block (lines 7-9). Activation refuses to overwrite hand-written blocks. |

**Files explicitly NOT carried over:**

- `agents/zr-worker/` — already absent from the legacy pack (was removed earlier per the spec's "eliminate in favor of [[rigs.patches]]" guidance; Phase 3's pgii-pack-workers + the existing `[[rigs.patches]] agent="worker" max_active_sessions=3` cover the prior behavior).

---

## Conventions used in this plan

- Working directory: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support` unless stated. Cross-repo edits use explicit `cd`.
- `nix build` uses `.#<attr>`.
- treefmt runs on commit; accept its formatting unchanged (Phase 2/3 precedent).
- The `packages/pg-pr/` files modified at session start are NOT mine; never `git add -A`. Use explicit file lists.

---

### Task 1: Stub the pgii-pack-gastown derivation

Same pattern as Phase 2/3 Task 1.

**Files:**

- Create: `packages/pgii-pack-gastown/default.nix`
- Create: `packages/pgii-pack-gastown/pack-src/pack.toml`

- [x] **Step 1: Create `pack.toml`**

File: `packages/pgii-pack-gastown/pack-src/pack.toml`

```toml
# pgii-gastown — local gastown-derived agents + formulas.
#
# Pack contents:
#   agents/mayor/          — city's customized 53-line mayor prompt
#                            (gastown's stock mayor is 237 lines; this
#                            city keeps its own). No agent.toml —
#                            gascity uses internal defaults when absent.
#   agents/deacon/         — gastown's deacon (verbatim) scaffolded
#                            here because the gastown system pack is
#                            not enabled (would also try to manage mayor
#                            and other defaults).
#   agents/operator/       — user-pairing agent. Keeps the mayor
#                            unblocked for coordination by absorbing
#                            long user sessions.
#   agents/foreman/        — tier-2 triage between worker pools and mayor.
#                            Fills missing acceptance_criteria, resolves
#                            worker escalations, mails mayor when human
#                            input is required. On-demand, city scope,
#                            opus model.
#   formulas/mol-deacon-patrol.toml — gastown's patrol formula, copied
#                            here so deacon can pour its next-iteration
#                            wisp without enabling the gastown pack.
#   doctor/check-gastown-divergence/ — alerts when this pack's agent
#                            configs drift from the gastown system pack.
#   doctor/check-misplaced-beads/    — bd-hygiene check (deacon-owned).
#   doctor/check-stale-beads/        — bd-hygiene check (deacon-owned).
#
# Migrated from ~/gc/assets/imports/pgii-gastown/ + the two bd-hygiene
# checks from ~/gc/assets/imports/zr/doctor/ (deacon-owned, per spec).
#
# Retirement: when the gastown system pack is enabled and converges
# with what's in this pack.

[pack]
name = "pgii-gastown"
schema = 2

[[named_session]]
template = "mayor"
mode = "always"

[[named_session]]
template = "deacon"
mode = "always"

[[named_session]]
template = "operator"
mode = "always"

[[named_session]]
template = "foreman"
mode = "on_demand"
```

NOTE the absence of a `zr-worker` `[[named_session]]` block — the legacy pack didn't have one either (was intentionally excluded since pgii-workers handles the rig-scope worker).

- [x] **Step 2: Create `default.nix`**

File: `packages/pgii-pack-gastown/default.nix`

```nix
{ lib, pkgs }:
let
  mkPgiiPack = import ../../lib/mkPgiiPack.nix { inherit lib pkgs; };
in
mkPgiiPack {
  name = "pgii-gastown";
  src = ./pack-src;
  meta = with lib; {
    description = "Local gastown-derived agents (mayor/deacon/operator/foreman) + mol-deacon-patrol formula + 3 doctor checks.";
    license = licenses.mit;
    platforms = platforms.unix;
  };
}
```

- [x] **Step 3: Register in flake.nix**

Two adds, mirroring how `pgii-pack-workers` was exposed in Phase 3 Task 4:

1. Overlay block, after the pgii-pack-workers line:

```nix
          pgii-pack-gastown = final.callPackage ./packages/pgii-pack-gastown { };
```

2. `packages` inherit list — add `pgii-pack-gastown` adjacent to `pgii-pack-workers`.

- [x] **Step 4: Build the stub**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pgii-pack-gastown/default.nix \
        packages/pgii-pack-gastown/pack-src/pack.toml \
        flake.nix
nix build .#pgii-pack-gastown --print-out-paths
```

Expected: prints `/nix/store/...-pgii-gastown-0.1.0`, exit 0.

- [x] **Step 5: Verify layout + scope=city (default — no scope=rig in pack.toml)**

```bash
out=$(nix build .#pgii-pack-gastown --print-out-paths --no-link)
test -f "$out/pack.toml"                                && echo "ok: pack.toml"
test -d "$out/agents"                                    && echo "ok: agents/"
test -d "$out/orders"                                    && echo "ok: orders/"
test -d "$out/scripts"                                   && echo "ok: scripts/"
test -d "$out/formulas"                                  && echo "ok: formulas/"
test -d "$out/doctor"                                    && echo "ok: doctor/"
grep -q 'name = "pgii-gastown"' "$out/pack.toml"         && echo "ok: pack name"
jq -r '.scope' "$out/.pack-meta.json" | grep -qx 'city'  && echo "ok: scope=city in meta (default — no scope field on any named_session)"
```

Expected: 8 `ok:` lines.

- [x] **Step 6: Commit**

```bash
git diff --cached --name-only  # 3 files
git commit -m "feat(pgii-packs): stub pgii-pack-gastown derivation + overlay entry"
```

- [x] **Step 7: Tick off `- [ ]` boxes for Task 1.**

---

### Task 2: Port the mayor agent (prompt.md only, no agent.toml)

Mayor in the legacy pack has only `prompt.md` — no `agent.toml`. Gascity falls back to internal defaults (verified via `gc config show`: effective config is just `name = "mayor"` + `prompt_template = "..."`). Preserve this layout.

**Files:**

- Create: `packages/pgii-pack-gastown/pack-src/agents/mayor/prompt.md`

- [x] **Step 1: Copy prompt.md verbatim**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
mkdir -p packages/pgii-pack-gastown/pack-src/agents/mayor
cp /Users/phillipg/gc/assets/imports/pgii-gastown/agents/mayor/prompt.md \
   packages/pgii-pack-gastown/pack-src/agents/mayor/
```

- [x] **Step 2: Verify no stale legacy-path references in mayor's prompt**

```bash
grep -nE '/Users/phillipg/gc/assets/imports/pgii-gastown' \
  packages/pgii-pack-gastown/pack-src/agents/mayor/prompt.md \
  && echo "FAIL: stale legacy ref in mayor/prompt.md" \
  || echo "ok: mayor/prompt.md clean"
```

Expected: `ok: mayor/prompt.md clean`.

- [x] **Step 3: Rebuild + verify mayor lands in $out**

```bash
git add packages/pgii-pack-gastown/pack-src/agents/mayor
out=$(nix build .#pgii-pack-gastown --print-out-paths --no-link)
test -f "$out/agents/mayor/prompt.md"          && echo "ok: mayor/prompt.md"
! test -f "$out/agents/mayor/agent.toml"       && echo "ok: no mayor/agent.toml (intentional)"
```

Expected: 2 `ok:` lines.

- [x] **Step 4: Commit**

```bash
git diff --cached --name-only
git commit -m "feat(pgii-packs): port pgii-gastown mayor (prompt.md only, no agent.toml)"
```

- [x] **Step 5: Tick off `- [ ]` boxes for Task 2.**

---

### Task 3: Port deacon (verbatim)

**Files:**

- Create: `packages/pgii-pack-gastown/pack-src/agents/deacon/agent.toml`
- Create: `packages/pgii-pack-gastown/pack-src/agents/deacon/prompt.template.md`

- [x] **Step 1: Copy verbatim**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
mkdir -p packages/pgii-pack-gastown/pack-src/agents/deacon
cp /Users/phillipg/gc/assets/imports/pgii-gastown/agents/deacon/agent.toml \
   /Users/phillipg/gc/assets/imports/pgii-gastown/agents/deacon/prompt.template.md \
   packages/pgii-pack-gastown/pack-src/agents/deacon/
```

- [x] **Step 2: Verify no stale legacy-path references**

```bash
grep -nE '/Users/phillipg/gc/assets/imports/pgii-gastown' \
  packages/pgii-pack-gastown/pack-src/agents/deacon/agent.toml \
  packages/pgii-pack-gastown/pack-src/agents/deacon/prompt.template.md \
  && echo "FAIL: stale legacy ref" \
  || echo "ok: deacon clean"
```

Expected: `ok: deacon clean`. If FAIL, STOP and report — needs `${PACK_ROOT}` substitution like the foreman.

- [x] **Step 3: Rebuild + verify**

```bash
git add packages/pgii-pack-gastown/pack-src/agents/deacon
out=$(nix build .#pgii-pack-gastown --print-out-paths --no-link)
test -f "$out/agents/deacon/agent.toml"                  && echo "ok: deacon/agent.toml"
test -f "$out/agents/deacon/prompt.template.md"          && echo "ok: deacon/prompt.template.md"
```

- [x] **Step 4: Commit**

```bash
git commit -m "feat(pgii-packs): port pgii-gastown deacon (verbatim)"
```

- [x] **Step 5: Tick off `- [ ]` boxes for Task 3.**

---

### Task 4: Port operator (verbatim)

**Files:**

- Create: `packages/pgii-pack-gastown/pack-src/agents/operator/agent.toml`
- Create: `packages/pgii-pack-gastown/pack-src/agents/operator/prompt.md`

- [x] **Step 1: Copy verbatim**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
mkdir -p packages/pgii-pack-gastown/pack-src/agents/operator
cp /Users/phillipg/gc/assets/imports/pgii-gastown/agents/operator/agent.toml \
   /Users/phillipg/gc/assets/imports/pgii-gastown/agents/operator/prompt.md \
   packages/pgii-pack-gastown/pack-src/agents/operator/
```

- [x] **Step 2: Verify no stale legacy-path references**

```bash
grep -nE '/Users/phillipg/gc/assets/imports/pgii-gastown' \
  packages/pgii-pack-gastown/pack-src/agents/operator/*.toml \
  packages/pgii-pack-gastown/pack-src/agents/operator/*.md \
  && echo "FAIL: stale legacy ref" \
  || echo "ok: operator clean"
```

Expected: `ok: operator clean`.

- [x] **Step 3: Rebuild + verify**

```bash
git add packages/pgii-pack-gastown/pack-src/agents/operator
out=$(nix build .#pgii-pack-gastown --print-out-paths --no-link)
test -f "$out/agents/operator/agent.toml"     && echo "ok: operator/agent.toml"
test -f "$out/agents/operator/prompt.md"       && echo "ok: operator/prompt.md"
```

- [x] **Step 4: Commit**

```bash
git commit -m "feat(pgii-packs): port pgii-gastown operator (verbatim)"
```

- [x] **Step 5: Tick off `- [ ]` boxes for Task 4.**

---

### Task 5: Port foreman (TEMPLATE for ${PACK_ROOT} + pool config dropped + zr-worker → pg-worker)

Three modifications from the legacy:

1. `agent.toml` → `agent.toml.template` with `work_query = "${PACK_ROOT}/agents/foreman/work_query.sh"` (legacy uses absolute legacy-tree path).
2. Drop the pool fields (`min_active_sessions = 0`, `max_active_sessions = 1`) from agent.toml — eliminates the alias-conflict with `[[named_session]] foreman mode = "on_demand"` (user direction: foreman stays purely on-demand).
3. `prompt.template.md` line 4: replace `zr-worker` with `pg-worker` (user direction).

`work_query.sh` is copied verbatim.

**Files:**

- Create: `packages/pgii-pack-gastown/pack-src/agents/foreman/agent.toml.template`
- Create: `packages/pgii-pack-gastown/pack-src/agents/foreman/prompt.template.md`
- Create: `packages/pgii-pack-gastown/pack-src/agents/foreman/work_query.sh`

- [x] **Step 1: Copy work_query.sh + prompt.template.md verbatim**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
mkdir -p packages/pgii-pack-gastown/pack-src/agents/foreman
cp /Users/phillipg/gc/assets/imports/pgii-gastown/agents/foreman/work_query.sh \
   /Users/phillipg/gc/assets/imports/pgii-gastown/agents/foreman/prompt.template.md \
   packages/pgii-pack-gastown/pack-src/agents/foreman/
```

- [x] **Step 2: Convert agent.toml → agent.toml.template with PACK_ROOT + drop pool config**

```bash
sed \
  -e 's|^\(work_query = \)"/Users/phillipg/gc/assets/imports/pgii-gastown/agents/foreman/work_query\.sh"$|\1"${PACK_ROOT}/agents/foreman/work_query.sh"|' \
  -e '/^min_active_sessions = /d' \
  -e '/^max_active_sessions = /d' \
  /Users/phillipg/gc/assets/imports/pgii-gastown/agents/foreman/agent.toml \
  > packages/pgii-pack-gastown/pack-src/agents/foreman/agent.toml.template
```

The `sed` does three things at once:

- Replaces the absolute legacy path with `${PACK_ROOT}/...` (envsubst substitutes at build time).
- Deletes the `min_active_sessions = ...` line (pool config drop).
- Deletes the `max_active_sessions = ...` line (pool config drop).

- [x] **Step 3: Update foreman prompt: zr-worker → pg-worker**

```bash
sed -i.bak 's|zr-worker|pg-worker|' \
  packages/pgii-pack-gastown/pack-src/agents/foreman/prompt.template.md
rm packages/pgii-pack-gastown/pack-src/agents/foreman/prompt.template.md.bak
```

The `sed` replaces ONLY the first occurrence by default (no `g` flag) — there's only one occurrence (line 4) per the audit. Verify:

```bash
grep -nE '\bzr-worker\b' packages/pgii-pack-gastown/pack-src/agents/foreman/prompt.template.md \
  && echo "FAIL: zr-worker still present" \
  || echo "ok: zr-worker replaced"

grep -nE '\bpg-worker\b' packages/pgii-pack-gastown/pack-src/agents/foreman/prompt.template.md \
  || echo "FAIL: pg-worker not present"
```

Expected: `ok: zr-worker replaced` + a grep match showing pg-worker on line 4.

- [x] **Step 4: Verify the agent.toml.template edits landed correctly**

```bash
# work_query line uses ${PACK_ROOT}
grep -nE '\$\{PACK_ROOT\}/agents/foreman/work_query\.sh' \
  packages/pgii-pack-gastown/pack-src/agents/foreman/agent.toml.template \
  || echo "FAIL: PACK_ROOT not substituted in agent.toml.template"

# pool fields are gone
grep -nE '(min|max)_active_sessions' packages/pgii-pack-gastown/pack-src/agents/foreman/agent.toml.template \
  && echo "FAIL: pool fields still present" \
  || echo "ok: pool fields dropped"

# no other legacy refs
grep -nE '/Users/phillipg/gc/assets/imports' \
  packages/pgii-pack-gastown/pack-src/agents/foreman/agent.toml.template \
  packages/pgii-pack-gastown/pack-src/agents/foreman/work_query.sh \
  packages/pgii-pack-gastown/pack-src/agents/foreman/prompt.template.md \
  && echo "FAIL: stale legacy ref" \
  || echo "ok: foreman files clean of legacy paths"
```

Expected: PACK_ROOT match + 2 ok: lines.

- [x] **Step 5: Build + verify PACK_ROOT substitution + go-template markers intact**

```bash
git add packages/pgii-pack-gastown/pack-src/agents/foreman
out=$(nix build .#pgii-pack-gastown --print-out-paths --no-link)
test -f "$out/agents/foreman/agent.toml"                && echo "ok: agent.toml present in $out"
! test -f "$out/agents/foreman/agent.toml.template"     && echo "ok: .template suffix stripped"
test -f "$out/agents/foreman/prompt.template.md"        && echo "ok: prompt.template.md present"
test -x "$out/agents/foreman/work_query.sh"             && echo "ok: work_query.sh executable" \
  || echo "WARN: work_query.sh NOT executable"

# PACK_ROOT must be substituted with the nix-store path
grep -q "work_query = \"$out/agents/foreman/work_query.sh\"" "$out/agents/foreman/agent.toml" \
  && echo "ok: PACK_ROOT substituted in agent.toml" \
  || { echo "FAIL: PACK_ROOT NOT substituted"; grep work_query "$out/agents/foreman/agent.toml"; }

# Pool fields stay gone
grep -nE '(min|max)_active_sessions' "$out/agents/foreman/agent.toml" \
  && echo "FAIL: pool fields leaked into \$out" \
  || echo "ok: pool fields absent in \$out"

# Foreman prompt has pg-worker, no zr-worker
grep -q 'pg-worker' "$out/agents/foreman/prompt.template.md" && echo "ok: prompt has pg-worker"
! grep -qE '\bzr-worker\b' "$out/agents/foreman/prompt.template.md" && echo "ok: prompt has no zr-worker"

# go-template markers in prompt — intact
grep -cE '\{\{[[:space:]]*\.(Rig|RigName|RigRoot|AgentBase|WorkDir)' \
  "$out/agents/foreman/prompt.template.md" \
  | awk '{ if ($1 > 0) print "ok: " $1 " go-template marker(s) intact"; else print "FAIL: no go-template markers" }'
```

Expected: ~8 `ok:` lines (with the WARN possibly for work_query.sh if mode preservation didn't happen — see Step 6).

- [x] **Step 6: If work_query.sh isn't executable, fix the source mode**

mkPgiiPack's `chmod +x` only targets `scripts/*.sh` at the pack root. Agent-nested scripts (`agents/foreman/work_query.sh`) rely on the source file's mode (cp preserves mode).

```bash
stat -f '%Sp' /Users/phillipg/gc/assets/imports/pgii-gastown/agents/foreman/work_query.sh 2>/dev/null \
  || stat -c '%A' /Users/phillipg/gc/assets/imports/pgii-gastown/agents/foreman/work_query.sh
```

If the legacy is `-rwxr-xr-x` but the new copy isn't, run:

```bash
chmod +x packages/pgii-pack-gastown/pack-src/agents/foreman/work_query.sh
```

Then rebuild and re-verify with the Step 5 commands.

- [x] **Step 7: Commit**

```bash
git diff --cached --name-only
git commit -m "$(cat <<'EOF'
feat(pgii-packs): port pgii-gastown foreman (PACK_ROOT for work_query, drop pool config, prompt zr-worker→pg-worker)

agent.toml.template uses ${PACK_ROOT} so envsubst rewrites the work_query
path to the nix-store $out at build time. Pool fields (min/max_active_sessions)
are dropped — foreman is purely on-demand via [[named_session]] mode="on_demand"
in pack.toml. This removes the long-standing pgii-gastown.foreman alias
collision (~1,957 occurrences/day in supervisor.log per human-reported audit).

Prompt update: 'zr-worker' → 'pg-worker' on line 4 (the legacy zr-worker
template no longer exists; consolidated into the generic `worker` via
Phase 3 pgii-pack-workers).

Refs: gc-0gj0.
EOF
)"
```

NOTE: treefmt may reformat. Accept it.

- [x] **Step 8: Tick off `- [ ]` boxes for Task 5.**

---

### Task 6: Port mol-deacon-patrol formula (verbatim)

**Files:**

- Create: `packages/pgii-pack-gastown/pack-src/formulas/mol-deacon-patrol.toml`

- [ ] **Step 1: Copy verbatim**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
mkdir -p packages/pgii-pack-gastown/pack-src/formulas
cp /Users/phillipg/gc/assets/imports/pgii-gastown/formulas/mol-deacon-patrol.toml \
   packages/pgii-pack-gastown/pack-src/formulas/
```

- [ ] **Step 2: Verify no stale absolute paths**

```bash
grep -nE '/Users/phillipg/gc/assets/imports/pgii-gastown' \
  packages/pgii-pack-gastown/pack-src/formulas/mol-deacon-patrol.toml \
  && echo "FAIL: stale legacy ref" \
  || echo "ok: formula clean"
```

- [ ] **Step 3: Rebuild + verify**

```bash
git add packages/pgii-pack-gastown/pack-src/formulas
out=$(nix build .#pgii-pack-gastown --print-out-paths --no-link)
test -f "$out/formulas/mol-deacon-patrol.toml" && echo "ok: formula in \$out"
```

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(pgii-packs): port pgii-gastown mol-deacon-patrol formula (verbatim)"
```

- [ ] **Step 5: Tick off `- [ ]` boxes for Task 6.**

---

### Task 7: Port check-gastown-divergence doctor check (verbatim)

**Files:**

- Create: `packages/pgii-pack-gastown/pack-src/doctor/check-gastown-divergence/{doctor.toml,run.sh}`

- [ ] **Step 1: Copy verbatim**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
mkdir -p packages/pgii-pack-gastown/pack-src/doctor
cp -R /Users/phillipg/gc/assets/imports/pgii-gastown/doctor/check-gastown-divergence \
      packages/pgii-pack-gastown/pack-src/doctor/
```

- [ ] **Step 2: Verify no stale legacy-path references in run.sh**

```bash
grep -nE '/Users/phillipg/gc/assets/imports/pgii-gastown' \
  packages/pgii-pack-gastown/pack-src/doctor/check-gastown-divergence/run.sh \
  && echo "FAIL: stale legacy ref — consider PACK_ROOT substitution" \
  || echo "ok: doctor check clean"
```

If FAIL, STOP and report — needs `${PACK_ROOT}` substitution similar to Phase 5's foreman fix.

- [ ] **Step 3: Rebuild + verify**

```bash
git add packages/pgii-pack-gastown/pack-src/doctor/check-gastown-divergence
out=$(nix build .#pgii-pack-gastown --print-out-paths --no-link)
test -f "$out/doctor/check-gastown-divergence/doctor.toml"  && echo "ok: doctor.toml in \$out"
test -x "$out/doctor/check-gastown-divergence/run.sh"        && echo "ok: run.sh executable"
```

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(pgii-packs): port pgii-gastown check-gastown-divergence doctor check (verbatim)"
```

- [ ] **Step 5: Tick off `- [ ]` boxes for Task 7.**

---

### Task 8: Port bd-hygiene doctor checks from legacy zr/

Per the spec: `check-misplaced-beads` and `check-stale-beads` from `~/gc/assets/imports/zr/doctor/` are deacon-owned bd-hygiene checks. They migrate into pgii-pack-gastown's doctor/ directory.

**Files:**

- Create: `packages/pgii-pack-gastown/pack-src/doctor/check-misplaced-beads/{doctor.toml,run.sh}`
- Create: `packages/pgii-pack-gastown/pack-src/doctor/check-stale-beads/{doctor.toml,run.sh}`

- [x] **Step 1: Inspect the legacy checks for path-rewrites needed**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
head -50 /Users/phillipg/gc/assets/imports/zr/doctor/check-misplaced-beads/run.sh
head -50 /Users/phillipg/gc/assets/imports/zr/doctor/check-stale-beads/run.sh
```

Look for:

- Absolute paths to `~/gc/assets/imports/zr/...` (will be stale post-cutover when legacy zr/ deletes).
- Absolute paths to `~/gc/assets/imports/pgii-gastown/...` (already retired post Phase 4 cutover).
- "Fix:" messages that name legacy paths.

If matches surface, they need either updating (point at the new pack-src path in agent-support) or `${PACK_ROOT}` substitution.

- [x] **Step 2: Copy verbatim, then audit + edit if needed**

```bash
for d in check-misplaced-beads check-stale-beads; do
  cp -R "/Users/phillipg/gc/assets/imports/zr/doctor/$d" \
        "packages/pgii-pack-gastown/pack-src/doctor/$d"
done
```

If Step 1's audit found stale path references, edit them out. Likely patterns to expect:

- "Fix: edit assets/imports/zr/..." → rewrite to point at the new agent-support pack-src.

```bash
grep -rnE '/Users/phillipg/gc/assets/imports' \
  packages/pgii-pack-gastown/pack-src/doctor/check-misplaced-beads/ \
  packages/pgii-pack-gastown/pack-src/doctor/check-stale-beads/ \
  && echo "FAIL: stale legacy refs — edit before commit" \
  || echo "ok: bd-hygiene doctor checks clean"
```

If FAIL: use sed to replace each match. Likely substitution patterns:

- `assets/imports/zr/doctor/X` → `packages/pgii-pack-pr-support/pack-src/doctor/X (in phillipgreenii-nix-agent-support)` if the check references its own legacy location.
- `assets/imports/pgii-gastown/X` → `packages/pgii-pack-gastown/pack-src/X` (in agent-support) — for cross-pack references.

Make minimal, targeted edits — don't reformat the whole file.

- [x] **Step 3: Rebuild + verify both checks land**

```bash
git add packages/pgii-pack-gastown/pack-src/doctor/check-misplaced-beads \
        packages/pgii-pack-gastown/pack-src/doctor/check-stale-beads
out=$(nix build .#pgii-pack-gastown --print-out-paths --no-link)
for d in check-misplaced-beads check-stale-beads; do
  test -f "$out/doctor/$d/doctor.toml"  && echo "ok: $d/doctor.toml"
  test -x "$out/doctor/$d/run.sh"       && echo "ok: $d/run.sh executable"
done
```

Expected: 4 `ok:` lines (2 checks × 2 file checks).

- [x] **Step 4: Commit**

```bash
git diff --cached --name-only
git commit -m "feat(pgii-packs): carry check-misplaced-beads + check-stale-beads from legacy zr/ into pgii-pack-gastown (deacon-owned)"
```

- [x] **Step 5: Tick off `- [ ]` boxes for Task 8.**

---

### Task 9: Wire packs.gastown into the HM module

Same pattern as Phase 2/3 — add a per-pack toggle. No assertion needed (no external dep like pg-pr).

**Files:**

- Modify: `home/programs/pgii-packs/default.nix`

- [ ] **Step 1: Add the option**

After Phase 3, the `packs = { ... }` block has `test-fixture`, `pr-support`, `dolt-hacks`, `workers`. Append `gastown`:

```nix
      gastown.enable = lib.mkEnableOption ''
        pgii-pack-gastown (mayor/deacon/operator/foreman city-scope agents
        + mol-deacon-patrol formula + 3 doctor checks). Locally-customized
        copies of gastown's defaults; replaces enabling the gastown system
        pack outright (which would also try to manage other defaults).
      '';
```

Remove `gastown` from the trailing "Real packs not yet added" comment — Phase 4 IS the gastown plan.

- [ ] **Step 2: Add to `enabledPacks`**

```nix
    ++ lib.optional cfg.packs.gastown.enable {
      name = "pgii-gastown";
      drv = pkgs.pgii-pack-gastown;
    };
```

Note `name = "pgii-gastown"` (gascity pack name in pack.toml), NOT `"pgii-pack-gastown"` (nix package name).

- [ ] **Step 3: Verify module evaluates**

```bash
nix flake check 2>&1 | tail -15
```

Expected: pre-existing upstream failures only (`test-bash-scripts`, `test-update-locks-lib`, `treefmt-check`). All `check-pgii-pack-*-layout` and `test-pgii-packs-activation` should pass.

- [ ] **Step 4: Commit**

```bash
git add home/programs/pgii-packs/default.nix
git commit -m "feat(pgii-packs): wire packs.gastown option into HM module"
```

- [ ] **Step 5: Tick off `- [ ]` boxes for Task 9.**

---

### Task 10: Add flake check for pgii-pack-gastown layout

Sibling to Phase 3's `check-pgii-pack-workers-layout`.

**Files:**

- Modify: `flake.nix`

- [ ] **Step 1: Add the check**

```nix
            check-pgii-pack-gastown-layout = pkgs.runCommand "check-pgii-pack-gastown-layout"
              { nativeBuildInputs = [ pkgs.jq ]; } ''
                pack=${pkgs.pgii-pack-gastown}
                test -f "$pack/pack.toml"                                    || { echo "missing pack.toml"; exit 1; }
                test -f "$pack/.pack-meta.json"                              || { echo "missing .pack-meta.json"; exit 1; }
                test "$(jq -r .scope "$pack/.pack-meta.json")" = "city"      || { echo ".pack-meta.json scope != city"; exit 1; }

                # Mayor: prompt.md only, NO agent.toml (intentional).
                test -f "$pack/agents/mayor/prompt.md"                       || { echo "missing mayor/prompt.md"; exit 1; }
                ! test -f "$pack/agents/mayor/agent.toml"                    || { echo "unexpected mayor/agent.toml (legacy has none)"; exit 1; }

                # Deacon, operator: standard agent.toml + prompt
                for a in deacon operator; do
                  test -f "$pack/agents/$a/agent.toml"                       || { echo "missing $a/agent.toml"; exit 1; }
                done
                test -f "$pack/agents/deacon/prompt.template.md"             || { echo "missing deacon/prompt.template.md"; exit 1; }
                test -f "$pack/agents/operator/prompt.md"                    || { echo "missing operator/prompt.md"; exit 1; }

                # Foreman: agent.toml (post-template), no .template suffix, work_query under PACK_ROOT
                test -f "$pack/agents/foreman/agent.toml"                    || { echo "missing foreman/agent.toml"; exit 1; }
                ! test -f "$pack/agents/foreman/agent.toml.template"         || { echo "stale foreman/agent.toml.template"; exit 1; }
                test -f "$pack/agents/foreman/prompt.template.md"            || { echo "missing foreman/prompt.template.md"; exit 1; }
                test -x "$pack/agents/foreman/work_query.sh"                 || { echo "foreman/work_query.sh not exec"; exit 1; }
                grep -qE "work_query = \"$pack/agents/foreman/work_query\\.sh\"" "$pack/agents/foreman/agent.toml" \
                  || { echo "PACK_ROOT not substituted in foreman agent.toml"; exit 1; }

                # Pool fields dropped per Phase 4 design
                ! grep -qE '(min|max)_active_sessions' "$pack/agents/foreman/agent.toml" \
                  || { echo "foreman pool config still present (should be dropped)"; exit 1; }

                # Foreman prompt fix-up: pg-worker present, zr-worker absent
                grep -q 'pg-worker' "$pack/agents/foreman/prompt.template.md" \
                  || { echo "pg-worker missing in foreman prompt"; exit 1; }
                ! grep -qE '\bzr-worker\b' "$pack/agents/foreman/prompt.template.md" \
                  || { echo "zr-worker still in foreman prompt"; exit 1; }

                # Formula + 3 doctor checks
                test -f "$pack/formulas/mol-deacon-patrol.toml"              || { echo "missing mol-deacon-patrol formula"; exit 1; }
                for d in check-gastown-divergence check-misplaced-beads check-stale-beads; do
                  test -f "$pack/doctor/$d/doctor.toml"                      || { echo "missing doctor/$d/doctor.toml"; exit 1; }
                  test -x "$pack/doctor/$d/run.sh"                           || { echo "doctor/$d/run.sh not exec"; exit 1; }
                done

                # No leftover envsubst .template files (excluding go-template *.template.md)
                ! find "$pack" -name "*.template" -not -name "*.template.md" | grep -q . \
                  || { echo "stale envsubst .template files"; exit 1; }

                # No stale legacy assets/imports paths
                ! grep -rnE '/Users/phillipg/gc/assets/imports' "$pack" >/dev/null 2>&1 \
                  || { echo "stale legacy assets paths"; exit 1; }
                touch $out
              '';
```

- [ ] **Step 2: Run the check**

```bash
system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
nix build ".#checks.$system.check-pgii-pack-gastown-layout" 2>&1 | tail -10
```

Expected: exit 0.

- [ ] **Step 3: Run full flake check**

```bash
nix flake check 2>&1 | tail -10
```

Expected: pre-existing failures only.

- [ ] **Step 4: Commit**

```bash
git add flake.nix
git commit -m "test(pgii-packs): flake check for pgii-pack-gastown layout (incl. mayor-no-agent.toml, foreman pool-drop, prompt zr→pg)"
```

- [ ] **Step 5: Tick off `- [ ]` boxes for Task 10.**

---

### Task 11: Bump nix-ziprecruiter lock + enable (build-only)

Mirrors Phase 2/3 Task 8.

**Files:**

- Modify: `phillipg-nix-ziprecruiter/flake.lock`
- Modify: `phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix`

- [ ] **Step 1: Push pending agent-support commits**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git log --oneline origin/main..HEAD
git push origin main 2>&1 | tail -5
```

- [ ] **Step 2: Update flake.lock in nix-ziprecruiter**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
git status --short  # verify clean tree
nix flake update phillipgreenii-agent-support 2>&1 | tail -5
grep -A 2 'phillipgreenii-agent-support' flake.lock | head -10
```

- [ ] **Step 3: Edit machine config**

After Phase 3 the pgii block reads:

```nix
        pgii = {
          gascity.cities = [ "/Users/phillipg/gc" ];
          packs = {
            test-fixture.enable = false;
            pr-support.enable = true;
            dolt-hacks.enable = true;
            workers.enable = true;
          };
        };
```

Add `gastown.enable = true;`:

```nix
        pgii = {
          gascity.cities = [ "/Users/phillipg/gc" ];
          packs = {
            test-fixture.enable = false;
            pr-support.enable = true;
            dolt-hacks.enable = true;
            workers.enable = true;
            gastown.enable = true;
          };
        };
```

- [ ] **Step 4: Build (do NOT apply)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
darwin-rebuild build --flake .#phillipg-mbp-02 2>&1 | tail -20
```

Expected: exit 0. `pgii-gastown-0.1.0` in the system closure.

- [ ] **Step 5: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
git add flake.lock machines/phillipg-mbp-02/default.nix
git commit -m "$(cat <<'EOF'
config(phillipg-mbp-02): enable pgii.packs.gastown (Phase 4 build, not yet applied)

Bumps phillipgreenii-agent-support lock to pick up pgii-pack-gastown
(city-scope, includes the foreman pool-config drop + prompt zr-worker→pg-worker
+ bd-hygiene doctor checks carried over from legacy zr/).
Apply happens in Phase 4 cutover (gc-0gj0 Task 12): atomic with removal of
the hand-written [imports.pgii-gastown] block from ~/gc/pack.toml.
Refs: gc-0gj0.
EOF
)"
```

- [ ] **Step 6: Tick off `- [ ]` boxes for Task 11.**

---

### Task 12: Cutover — remove legacy block + apply

Same pattern as Phase 2/3 Task 9. Single file edit. `zn-self-apply` is agent-restricted; the human runs it.

**Files:**

- Modify: `/Users/phillipg/gc/pack.toml`

- [ ] **Step 1: Inspect current pack.toml block**

```bash
grep -B 1 -A 3 'imports\.pgii-gastown' /Users/phillipg/gc/pack.toml
```

Expected (currently lines 7-9):

```
[imports.pgii-gastown]
source = "./assets/imports/pgii-gastown"
export = true
```

If anything else, STOP and report.

- [ ] **Step 2: Backup and remove the legacy block**

```bash
cd /Users/phillipg/gc
cp pack.toml pack.toml.pre-phase4-cutover
sed -i.bak '/^\[imports\.pgii-gastown\]$/,/^export = true$/d' pack.toml
rm pack.toml.bak
```

Verify:

```bash
grep -nE 'imports\.pgii-gastown' /Users/phillipg/gc/pack.toml \
  && { echo "FAIL: stale pgii-gastown ref"; exit 1; } \
  || echo "ok: pack.toml cleaned"

gc config show 2>&1 | head -5
```

Expected: `ok: pack.toml cleaned` + no TOML parse errors.

- [ ] **Step 3: STOP and report BLOCKED — awaiting human zn-self-apply**

`zn-self-apply` is agent-restricted. Report status `BLOCKED — awaiting human zn-self-apply` with:

- pack.toml diff (legacy block removed)
- Backup location
- Apply command: `zn-self-apply`
- Rollback: `cp /Users/phillipg/gc/pack.toml.pre-phase4-cutover /Users/phillipg/gc/pack.toml; gc supervisor reload`

DO NOT run `gc supervisor reload` here — would drop all four pgii-gastown agents (mayor/deacon/operator/foreman) until apply lands.

(Resume verification after the human confirms `applied`.)

- [ ] **Step 4: Verify managed block written**

```bash
grep -A 4 'BEGIN pgii-pack:pgii-gastown' /Users/phillipg/gc/pack.toml
```

Expected:

```
# BEGIN pgii-pack:pgii-gastown (managed)
[imports.pgii-gastown]
source = "/nix/store/<hash>-pgii-gastown-0.1.0"
export = true
# END pgii-pack:pgii-gastown (managed)
```

Critical: header is `[imports.pgii-gastown]` (city-scope), NOT `[defaults.rig.imports.pgii-gastown]`. If activation wrote the rig-scope shape, mkPgiiPack's inference is buggy.

- [ ] **Step 5: Verify supervisor reload clean**

```bash
gc supervisor reload
sleep 3
tail -30 ~/.gc/supervisor.log | grep -E '(reload|pgii-gastown|mayor|deacon|operator|foreman)' | grep -v 'session beads:' | tail -10
```

Expected: `Config reloaded: <N> agents, <M> rigs` line. **Critically: no more `session alias already exists: "pgii-gastown.foreman" reserved for configured named session pgii-gastown.foreman` errors** (the foreman pool-config drop should eliminate them).

- [ ] **Step 6: Verify all 4 named_session templates registered**

```bash
gc session list 2>&1 | grep -E 'pgii-gastown\.(mayor|deacon|operator|foreman)' | head -10
```

Expected: 4 (or more, counting any extra) lines naming the four templates. mayor, deacon, operator stay active; foreman stays asleep until work_query returns matches.

- [ ] **Step 7: Verify the 3 doctor checks register**

```bash
gc doctor --verbose 2>&1 | grep -E 'pgii-gastown:check-(gastown-divergence|misplaced-beads|stale-beads)' | head -5
```

Expected: 3 entries.

- [ ] **Step 8: Verify foreman alias-conflict is GONE**

The human's earlier mail flagged `pool_alias_conflict_count: 290+ rising every ~4min`. After the pool-config drop, the counter should stop rising.

```bash
gc bd show gc-dmak 2>&1 | grep -E 'pool_alias_conflict' | head -3
# Note the count, then wait ~5 minutes (or until the next 4-min cycle), then re-check.
```

If the counter keeps rising AT THE SAME RATE post-cutover, the pool-config drop didn't fix it — investigate. If it stays flat or rises only sporadically, the fix worked.

(Don't wait synchronously here — note current count and move to Task 13. Task 13 records this verification as a post-cutover observation, not a blocker.)

- [ ] **Step 9: Tick off `- [ ]` boxes for Task 12.**

---

### Task 13: Retire legacy tree + close bead

Same pattern as Phase 2/3 Task 10.

- [ ] **Step 1: Final health check**

```bash
cd /Users/phillipg/gc
grep -A 4 'BEGIN pgii-pack:pgii-gastown' pack.toml | head -6
gc config show 2>&1 | grep -E '^name = "(mayor|deacon|operator|foreman)"' | head -10
```

Confirm managed block is in pack.toml, all 4 agents resolve from the new pack.

- [ ] **Step 2: Delete the legacy tree**

```bash
cd /Users/phillipg/gc
git rm -rf assets/imports/pgii-gastown 2>&1 | tail -10
```

- [ ] **Step 3: Audit remaining references**

```bash
grep -rn 'assets/imports/pgii-gastown' /Users/phillipg/gc/ \
  --include='*.md' --include='*.toml' --include='*.sh' \
  --exclude-dir=.git --exclude-dir=.gc --exclude-dir=archive \
  || echo "ok: no remaining references"
```

Likely candidates:

- `HACKS.md` — if any HACK entries reference pgii-gastown paths, rewrite the same way Phase 2 did (`assets/imports/pgii-gastown/...` → `packages/pgii-pack-gastown/pack-src/...`).
- `CLAUDE.md` — unlikely to have references, but check.
- `docs/superpowers/...` — historical plans/specs, leave alone.

If matches surface and they're live references (HACKS.md, CLAUDE.md), update them; commit alongside the deletion.

- [ ] **Step 4: Stage pack.toml + deletions + any HACKS.md updates**

```bash
git status --short
git add pack.toml [HACKS.md if updated]
git diff --cached --stat | tail -20
```

Verify only the expected files are staged (pack.toml, the deletion `D` lines, optional HACKS.md update). Per CLAUDE.md, ignore always-modified files like `ziprecruiter/settings/teammates.json` and dated archive jsonl entries.

- [ ] **Step 5: Commit retirement**

```bash
git commit -m "$(cat <<'EOF'
feat(pgii-packs): Phase 4 cutover — retire legacy pgii-gastown tree

pgii-pack-gastown now ships from phillipgreenii-nix-agent-support
(city-scope, [imports.pgii-gastown] managed block in pack.toml). The
pack body is a verbatim copy of the legacy tree with three surgical
edits:
  - foreman/agent.toml.template uses ${PACK_ROOT} for the work_query
    path (envsubst at build time).
  - foreman pool fields (min/max_active_sessions) dropped — eliminates
    the long-standing pgii-gastown.foreman alias collision.
  - foreman/prompt.template.md: 'zr-worker' → 'pg-worker' (per
    human-direction; zr-worker no longer exists post Phase 3).

Also carries check-misplaced-beads + check-stale-beads from
~/gc/assets/imports/zr/doctor/ into pgii-pack-gastown's doctor/
(deacon-owned bd-hygiene per Phase 4 spec). The two legacy zr/ copies
remain on disk until Phase 5 cutover.

Verified post-apply: 4 named_session templates (mayor/deacon/operator/
foreman) bind cleanly, no new alias-conflict errors in supervisor.log,
3 doctor checks register.

Refs: gc-0gj0.
EOF
)"
```

- [ ] **Step 6: Clean up backup**

```bash
rm /Users/phillipg/gc/pack.toml.pre-phase4-cutover
```

- [ ] **Step 7: Close the bead**

```bash
gc bd close gc-0gj0 --reason="Phase 4 build + cutover complete. pgii-pack-gastown ships from phillipgreenii-nix-agent-support (city-scope). Three surgical edits from legacy: foreman/agent.toml.template uses \${PACK_ROOT} for work_query (envsubst at build time); foreman pool fields (min/max_active_sessions) dropped — eliminates pgii-gastown.foreman alias collision that was firing 290+/day; foreman/prompt.template.md zr-worker→pg-worker per human-direction. Also carried check-misplaced-beads + check-stale-beads from ~/gc/assets/imports/zr/doctor/ (deacon-owned bd-hygiene). Verified post-apply: 4 named_session templates (mayor/deacon/operator/foreman) bind cleanly, supervisor reload clean, no alias-conflict errors, 3 doctor checks register. Legacy ~/gc/assets/imports/pgii-gastown/ retired. Plan: docs/superpowers/plans/2026-05-28-pgii-packs-phase4-gastown.md. Unblocks gc-r6el (Phase 5 pgii-bead-importer)."
```

- [ ] **Step 8: Tick off `- [ ]` boxes for Task 13.**

---

## Risks and unknowns

1. **Mayor has no agent.toml — runtime depends on gascity's internal defaults.** Legacy has worked this way for weeks (verified active session `gc-b9nm`). Same nix-shipped path should work identically. If it doesn't, the mitigation is to add a minimal `agent.toml` in a follow-up that mirrors the system gastown pack's defaults (scope/wake_mode/idle_timeout/max_active_sessions=1).

2. **Foreman pool-config drop is a semantic change.** The legacy had both `[[named_session]] mode = "on_demand"` AND pool config in agent.toml (the source of the alias-conflict). Dropping pool fields means foreman stays purely on-demand — spawns when `work_query` returns rows, exits after `idle_timeout`. If the foreman should instead be always-running at min=0/max=1 (pool semantics), the alternative resolution would be to drop `[[named_session]]` from pack.toml instead. The user-direction picked the on-demand path.

3. **Deacon's agent.toml has a multi-line "TODO: Re-evaluate this copy" comment block.** It references plans to enable the gastown system pack and retire this pack. The migration to nix doesn't change those retirement criteria, but the TODO references "assets/imports/" paths that get stale. Update during the audit (Task 3 Step 2) if Step 2's grep flags any path references in the deacon TODO. (Spot-check: the TODO is documentation only, not load-bearing.)

4. **Sessions persist across the cutover.** Mayor/deacon/operator are `mode = "always"` and currently active. On supervisor reload after cutover, they should re-bind to the new pack's templates (same template names). The sessions' metadata (`template = "pgii-gastown.mayor"` etc.) is path-agnostic — the template-to-binary mapping comes from the active pack at reload time.

5. **`check-misplaced-beads` / `check-stale-beads` may have stale path references.** Task 8 Step 1 audits this. If the legacy checks reference `~/gc/assets/imports/zr/...` or `pgii-gastown/...` paths, edit them to point at the new pack-src locations.

---

## Self-review checklist

- [ ] **Spec coverage:** Phase 4 spec row → tasks. Pack body verbatim (Tasks 2-7). zr-worker deletion (already absent; noted). bd-hygiene doctor checks moved (Task 8). Audit prompts for ZR refs (Task 5 fixes zr-worker; foreman prompt's rig-prefix table left intact per human-direction). Cutover deletes pack.toml block (Task 12) + legacy tree (Task 13).
- [ ] **Placeholder scan:** Search for `TBD`, `TODO`, `fill in`. None should remain in the plan body. (The legacy deacon agent.toml has its own TODO comment which carries over — that's content, not a plan placeholder.)
- [ ] **Type consistency:** Gascity pack name is `pgii-gastown`. Nix package name is `pgii-pack-gastown`. HM short option is `packs.gastown`. Used consistently.
- [ ] **All `git commit` commands include the right files (no stray `git add .`).**
- [ ] **All `nix build` / `nix flake check` commands target the correct attribute paths.**

---

## References

- **Spec (this phase):** `docs/superpowers/specs/2026-05-26-pgii-packs-migration-design.md` (Phase 4 row + "Open items" → "Phase 4 — audit prompts for ZR refs")
- **Phase 3 plan (pattern source):** `docs/superpowers/plans/2026-05-28-pgii-packs-phase3-workers.md`
- **Legacy pack source:** `/Users/phillipg/gc/assets/imports/pgii-gastown/`
- **Legacy doctor checks (to carry over):** `/Users/phillipg/gc/assets/imports/zr/doctor/{check-misplaced-beads,check-stale-beads}/`
- **mkPgiiPack:** `lib/mkPgiiPack.nix` (Phase 0 + Phase 1 + Phase 3 contributions; `${PACK_ROOT}` available)
- **Activation script:** `home/programs/pgii-packs/activation.sh` (Phase 0 + Phase 3 contributions; scope-aware)
- **gc-0gj0 bead:** Phase 4 work tracking bead.
- **User-direction context (this session):** foreman alias-conflict resolution → drop pool config; foreman prompt → zr-worker → pg-worker.
