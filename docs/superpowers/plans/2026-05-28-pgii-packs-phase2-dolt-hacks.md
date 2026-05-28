# pgii-pack-dolt-hacks Implementation Plan (Phase 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the nix-packaged `pgii-pack-dolt-hacks` pack — the migration target for the legacy `~/gc/assets/imports/pgii-dolt-hacks/` pack. Cutover is straight (no parallel-run, orders are idempotent) and finishes by removing the legacy hand-written `[imports.pgii-dolt-hacks]` block from `~/gc/pack.toml`.

**Architecture:** Use `mkPgiiPack` (Phase 0) to build a derivation from `packages/pgii-pack-dolt-hacks/pack-src/`. Source content is a verbatim copy of `~/gc/assets/imports/pgii-dolt-hacks/` minus the order TOMLs, which are converted to `*.toml.template` with `${SCRIPTS_DIR}` substitution (legacy uses absolute `/Users/phillipg/gc/...` paths in `exec=`). Script bodies need NO rewrites — every script already anchors state paths via `${GC_CITY}`-rooted env vars. Doctor checks need only one path-comment cleanup (the `check-formulas-dir` / `check-hack-11-still-needed` "Fix:" messages still reference `assets/imports/pgii-dolt-hacks/...`). Bats tests under `scripts/tests/` come along verbatim (they use `$BATS_TEST_DIRNAME/..`-relative addressing, which resolves identically in the nix store). Wire `phillipgreenii.programs.pgii.packs.dolt-hacks.enable` into the HM module with no extra dependency assertion (these orders are self-contained — `gc`, `bd`, `jq`, `dolt`, `git` are baseline tools, already on the system PATH).

**Tech Stack:** Nix flakes, home-manager, bash, bats-core, mkPgiiPack lib (Phase 0).

**Spec:** `docs/superpowers/specs/2026-05-26-pgii-packs-migration-design.md` (Phase 2 row)

**Repo root for all paths in this plan:** `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`

**Companion repo paths:**

- Source of pack body: `/Users/phillipg/gc/assets/imports/pgii-dolt-hacks/`
- Machine config to enable in: `/Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix`
- City pack.toml to clean up at cutover: `/Users/phillipg/gc/pack.toml`

**Phase 1 dependency:** Done (gc-jz2l closed at gc@9c290bd; pgii-pr-support live on phillipg-mbp-02).

---

## File structure

**Files to create (under `packages/pgii-pack-dolt-hacks/`):**

| File                                                                                            | Purpose                                                                                  |
| ----------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| `packages/pgii-pack-dolt-hacks/default.nix`                                                     | callPackage entry; calls `mkPgiiPack` with `name = "pgii-dolt-hacks"`.                   |
| `packages/pgii-pack-dolt-hacks/pack-src/pack.toml`                                              | Gascity manifest. Just `[pack] name + schema` — no `[[named_session]]` (no agents).      |
| `packages/pgii-pack-dolt-hacks/pack-src/formulas/.gitkeep`                                      | Empty dir marker. Required: gascity 1.1.0 silently drops orders if `formulas/` missing.  |
| `packages/pgii-pack-dolt-hacks/pack-src/orders/hack-archive-and-compact.toml.template`          | Order TOML, `exec=${SCRIPTS_DIR}/hack-archive-and-compact.sh`.                           |
| `packages/pgii-pack-dolt-hacks/pack-src/orders/hack-autoclose-completed-mols.toml.template`     | Order TOML, `exec=${SCRIPTS_DIR}/hack-autoclose-completed-mols.sh`.                      |
| `packages/pgii-pack-dolt-hacks/pack-src/orders/hack-daily-summary.toml.template`                | Order TOML, `exec=${SCRIPTS_DIR}/hack-daily-summary.sh`.                                 |
| `packages/pgii-pack-dolt-hacks/pack-src/orders/hack-message-forwarder.toml.template`            | Order TOML, `exec=${SCRIPTS_DIR}/hack-message-forwarder.sh`.                             |
| `packages/pgii-pack-dolt-hacks/pack-src/orders/hack-mol-dog-jsonl.toml.template`                | Order TOML, `exec=${SCRIPTS_DIR}/hack-mol-dog-jsonl.sh`.                                 |
| `packages/pgii-pack-dolt-hacks/pack-src/orders/hack-order-override-watchdog.toml.template`      | Order TOML, `exec=${SCRIPTS_DIR}/hack-order-override-watchdog.sh`.                       |
| `packages/pgii-pack-dolt-hacks/pack-src/orders/hack-stale-lock-sweeper.toml.template`           | Order TOML, `exec=${SCRIPTS_DIR}/hack-stale-lock-sweeper.sh`.                            |
| `packages/pgii-pack-dolt-hacks/pack-src/scripts/hack-archive-and-compact.sh`                    | Verbatim copy.                                                                           |
| `packages/pgii-pack-dolt-hacks/pack-src/scripts/hack-archive-and-compact.RUNBOOK.md`            | Verbatim copy.                                                                           |
| `packages/pgii-pack-dolt-hacks/pack-src/scripts/hack-autoclose-completed-mols.sh`               | Verbatim copy.                                                                           |
| `packages/pgii-pack-dolt-hacks/pack-src/scripts/hack-daily-summary.sh`                          | Verbatim copy.                                                                           |
| `packages/pgii-pack-dolt-hacks/pack-src/scripts/hack-message-forwarder.sh`                      | Verbatim copy.                                                                           |
| `packages/pgii-pack-dolt-hacks/pack-src/scripts/hack-mol-dog-jsonl.sh`                          | Verbatim copy.                                                                           |
| `packages/pgii-pack-dolt-hacks/pack-src/scripts/hack-order-override-watchdog.sh`                | Verbatim copy.                                                                           |
| `packages/pgii-pack-dolt-hacks/pack-src/scripts/hack-stale-lock-sweeper.sh`                     | Verbatim copy.                                                                           |
| `packages/pgii-pack-dolt-hacks/pack-src/scripts/tests/hack-daily-summary.bats`                  | Verbatim copy.                                                                           |
| `packages/pgii-pack-dolt-hacks/pack-src/scripts/tests/fixtures/*` (7 files)                     | Verbatim copy.                                                                           |
| `packages/pgii-pack-dolt-hacks/pack-src/doctor/check-formulas-dir/{doctor.toml,run.sh}`         | Copied; "Fix:" message updated to point at the pack-src dir, not the legacy assets path. |
| `packages/pgii-pack-dolt-hacks/pack-src/doctor/check-hack-2-still-needed/{doctor.toml,run.sh}`  | Verbatim copy.                                                                           |
| `packages/pgii-pack-dolt-hacks/pack-src/doctor/check-hack-10-still-needed/{doctor.toml,run.sh}` | Verbatim copy.                                                                           |
| `packages/pgii-pack-dolt-hacks/pack-src/doctor/check-hack-11-still-needed/{doctor.toml,run.sh}` | "Fix:" message updated to point at the pack-src dir, not legacy assets paths.            |

**Files to modify:**

| File                                                             | Change                                                                                                                                   |
| ---------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `flake.nix`                                                      | Register `pgii-pack-dolt-hacks` in the overlay; add `check-pgii-pack-dolt-hacks-layout` + `test-pgii-pack-dolt-hacks-bats` flake checks. |
| `home/programs/pgii-packs/default.nix`                           | Add `packs.dolt-hacks.enable` option; add `pgii-dolt-hacks` to the `enabledPacks` list. No extra assertion (no pg-pr-style dep).         |
| `phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix` | Add `pgii.packs.dolt-hacks.enable = true`. Bump `phillipgreenii-nix-agent-support` flake.lock to pull in the new pack.                   |
| `/Users/phillipg/gc/pack.toml`                                   | Remove the hand-written `[imports.pgii-dolt-hacks]` block (lines 11-13). Activation refuses to overwrite hand-written blocks.            |

**Files explicitly NOT carried over:**

- `~/gc/assets/imports/pgii-dolt-hacks/formulas/` (other than `.gitkeep` re-creation) — was empty in source.
- No "drop these scripts" — every script in the legacy tree migrates.

---

## Conventions used in this plan

- All `git commit` commands assume current directory is `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support` unless stated otherwise. Each task begins implicitly with `cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`.
- `nix build` commands use `.#<attr>`.
- Pre-commit hooks (treefmt, statix, deadnix) run automatically on commit — let them.
- After each task that touches a file in `phillipg-nix-ziprecruiter` or `~/gc`, switch with explicit `cd`.
- After Task 9 (cutover), the legacy tree at `~/gc/assets/imports/pgii-dolt-hacks/` is deleted in Task 10.

---

### Task 1: Stub the pack source directory and derivation

Create the empty pack-src skeleton and a minimal `default.nix` that calls mkPgiiPack. This proves the wiring works before we copy real content.

**Files:**

- Create: `packages/pgii-pack-dolt-hacks/default.nix`
- Create: `packages/pgii-pack-dolt-hacks/pack-src/pack.toml`
- Create: `packages/pgii-pack-dolt-hacks/pack-src/formulas/.gitkeep`

- [x] **Step 1: Create the pack-src/pack.toml stub**

File: `packages/pgii-pack-dolt-hacks/pack-src/pack.toml`

```toml
# pgii-dolt-hacks
#
# Workarounds for dolt-related storage / lifecycle issues and a handful
# of gascity 1.1.0 supervisor regressions that the system packs do not
# cover. Every order corresponds to a HACK # in /HACKS.md with the
# upstream problem and retirement criterion.
#
# Migrated from ~/gc/assets/imports/pgii-dolt-hacks/ (hand-built pack).
#
# Retire individual orders as their HACK retirement criteria are met
# upstream; retire the whole pack when zero orders remain enabled.

[pack]
name = "pgii-dolt-hacks"
schema = 2
```

- [x] **Step 2: Create the formulas/.gitkeep**

File: `packages/pgii-pack-dolt-hacks/pack-src/formulas/.gitkeep`

(Empty file. Gascity 1.1.0 silently drops a pack's orders if `formulas/` is missing — see `doctor/check-formulas-dir/`. mkPgiiPack always creates `$out/formulas`, so this `.gitkeep` is only needed so the source tree is git-trackable; the runtime requirement is satisfied by mkPgiiPack regardless.)

- [x] **Step 3: Create the default.nix**

File: `packages/pgii-pack-dolt-hacks/default.nix`

```nix
{ lib, pkgs }:
let
  mkPgiiPack = import ../../lib/mkPgiiPack.nix { inherit lib pkgs; };
in
mkPgiiPack {
  name = "pgii-dolt-hacks";
  src = ./pack-src;
  meta = with lib; {
    description = "HACK orders for dolt storage/lifecycle issues and gascity 1.1.0 supervisor regressions (HACK 2, 10, 11, 12, 14, 15 + hack-daily-summary).";
    license = licenses.mit;
    platforms = platforms.unix;
  };
}
```

- [x] **Step 4: Register in the flake overlay**

Modify `flake.nix`. Locate the overlay block where `pgii-pack-pr-support` is registered:

Find the line:

```nix
          pgii-pack-pr-support = final.callPackage ./packages/pgii-pack-pr-support { };
```

Add immediately after it:

```nix
          pgii-pack-dolt-hacks = final.callPackage ./packages/pgii-pack-dolt-hacks { };
```

- [x] **Step 5: Build the stub**

Run:

```bash
nix build .#pgii-pack-dolt-hacks --print-out-paths
```

Expected: prints a `/nix/store/...-pgii-dolt-hacks-0.1.0` path, exit 0.

- [x] **Step 6: Verify minimal layout**

Run:

```bash
out=$(nix build .#pgii-pack-dolt-hacks --print-out-paths --no-link)
test -f "$out/pack.toml"                                              && echo "ok: pack.toml"
test -d "$out/agents"                                                  && echo "ok: agents/"
test -d "$out/orders"                                                  && echo "ok: orders/"
test -d "$out/scripts"                                                 && echo "ok: scripts/"
test -d "$out/formulas"                                                && echo "ok: formulas/"
grep -q 'name = "pgii-dolt-hacks"' "$out/pack.toml"                    && echo "ok: pack name"
grep -q '"name": "pgii-dolt-hacks"' "$out/.pack-meta.json"             && echo "ok: meta sidecar"
```

Expected: seven `ok:` lines.

- [x] **Step 7: Commit**

```bash
git add packages/pgii-pack-dolt-hacks/default.nix \
        packages/pgii-pack-dolt-hacks/pack-src/pack.toml \
        packages/pgii-pack-dolt-hacks/pack-src/formulas/.gitkeep \
        flake.nix
git commit -m "feat(pgii-packs): stub pgii-pack-dolt-hacks derivation + overlay entry"
```

---

### Task 2: Port the seven scripts verbatim (with RUNBOOK)

All seven scripts use `${GC_CITY}` / `${GC_CITY_RUNTIME_DIR}` / `${GC_PACK_STATE_DIR}` env anchors and `$CITY/.cache/<hack-name>/` for logs — NO references to the pack source tree. Copy verbatim.

**Files:**

- Create: 7 `.sh` files under `packages/pgii-pack-dolt-hacks/pack-src/scripts/`
- Create: `packages/pgii-pack-dolt-hacks/pack-src/scripts/hack-archive-and-compact.RUNBOOK.md`

- [x] **Step 1: Copy the scripts tree verbatim**

Run:

```bash
mkdir -p packages/pgii-pack-dolt-hacks/pack-src/scripts
cp \
  /Users/phillipg/gc/assets/imports/pgii-dolt-hacks/scripts/hack-archive-and-compact.sh \
  /Users/phillipg/gc/assets/imports/pgii-dolt-hacks/scripts/hack-archive-and-compact.RUNBOOK.md \
  /Users/phillipg/gc/assets/imports/pgii-dolt-hacks/scripts/hack-autoclose-completed-mols.sh \
  /Users/phillipg/gc/assets/imports/pgii-dolt-hacks/scripts/hack-daily-summary.sh \
  /Users/phillipg/gc/assets/imports/pgii-dolt-hacks/scripts/hack-message-forwarder.sh \
  /Users/phillipg/gc/assets/imports/pgii-dolt-hacks/scripts/hack-mol-dog-jsonl.sh \
  /Users/phillipg/gc/assets/imports/pgii-dolt-hacks/scripts/hack-order-override-watchdog.sh \
  /Users/phillipg/gc/assets/imports/pgii-dolt-hacks/scripts/hack-stale-lock-sweeper.sh \
  packages/pgii-pack-dolt-hacks/pack-src/scripts/
```

- [x] **Step 2: Verify scripts contain NO references to the pack source tree**

Scripts must NOT reference `/Users/phillipg/gc/assets/imports/pgii-dolt-hacks/` directly (a stale absolute path is the migration's main risk).

Run:

```bash
grep -nE 'assets/imports/pgii-dolt-hacks' packages/pgii-pack-dolt-hacks/pack-src/scripts/*.sh \
  && echo "FAIL: found pack-source path references — fix before continuing" \
  || echo "ok: no pack-source path references"
```

Expected: `ok: no pack-source path references`.

NOTE: `hack-stale-lock-sweeper.sh` line 25 has a header-comment-only example like `# - /Users/phillipg/gc/.gc/runtime/packs/pgii-dolt-hacks/jsonl-archive/.git/index.lock —` — this is a RUNTIME path under `.gc/runtime/packs/pgii-dolt-hacks/`, NOT under `assets/imports/`. The grep above won't match it (`assets/imports/` is the pattern), and the runtime path is correct for both legacy and migrated pack. No edit needed.

- [x] **Step 3: Rebuild + verify scripts land in $out as executable**

Run:

```bash
out=$(nix build .#pgii-pack-dolt-hacks --print-out-paths --no-link)
for s in hack-archive-and-compact \
         hack-autoclose-completed-mols \
         hack-daily-summary \
         hack-message-forwarder \
         hack-mol-dog-jsonl \
         hack-order-override-watchdog \
         hack-stale-lock-sweeper; do
  test -x "$out/scripts/$s.sh" && echo "ok: $s.sh executable"
done
test -f "$out/scripts/hack-archive-and-compact.RUNBOOK.md" && echo "ok: RUNBOOK present"
```

Expected: seven `ok: <name>.sh executable` lines + one `ok: RUNBOOK present` line.

- [x] **Step 4: Commit**

```bash
git add packages/pgii-pack-dolt-hacks/pack-src/scripts
git commit -m "feat(pgii-packs): port pgii-dolt-hacks scripts verbatim (7 scripts + RUNBOOK)"
```

---

### Task 3: Port + convert the seven order templates

Legacy order TOMLs use absolute paths like `exec = "/Users/phillipg/gc/assets/imports/pgii-dolt-hacks/scripts/<name>.sh"`. Rewrite as `*.toml.template` with `exec = "${SCRIPTS_DIR}/<name>.sh"`, which mkPgiiPack's envsubst replaces with the nix store path at build time.

**Files:**

- Create: 7 `.toml.template` files under `packages/pgii-pack-dolt-hacks/pack-src/orders/`

- [x] **Step 1: Convert all seven order TOMLs in one loop**

Run:

```bash
mkdir -p packages/pgii-pack-dolt-hacks/pack-src/orders
for o in hack-archive-and-compact \
         hack-autoclose-completed-mols \
         hack-daily-summary \
         hack-message-forwarder \
         hack-mol-dog-jsonl \
         hack-order-override-watchdog \
         hack-stale-lock-sweeper; do
  sed "s|/Users/phillipg/gc/assets/imports/pgii-dolt-hacks/scripts/|\${SCRIPTS_DIR}/|g" \
    "/Users/phillipg/gc/assets/imports/pgii-dolt-hacks/orders/$o.toml" \
    > "packages/pgii-pack-dolt-hacks/pack-src/orders/$o.toml.template"
done
```

- [x] **Step 2: Verify each template has the substitution marker and no stale absolute paths**

Run:

```bash
grep -lE '/Users/phillipg/gc/assets/imports' packages/pgii-pack-dolt-hacks/pack-src/orders/*.toml.template \
  && echo "FAIL: stale absolute pack-source paths remain" \
  || echo "ok: no stale pack-source paths"

grep -cE 'exec\s*=\s*"\$\{SCRIPTS_DIR\}/' packages/pgii-pack-dolt-hacks/pack-src/orders/*.toml.template \
  | awk -F: 'BEGIN{ok=1} $2!=1{ok=0; print "FAIL: " $0} END{if(ok) print "ok: every template has exactly one ${SCRIPTS_DIR} exec line"}'
```

Expected: `ok: no stale pack-source paths` + `ok: every template has exactly one ${SCRIPTS_DIR} exec line`.

- [x] **Step 3: Rebuild and verify substitution happened**

Run:

```bash
out=$(nix build .#pgii-pack-dolt-hacks --print-out-paths --no-link)
for o in hack-archive-and-compact \
         hack-autoclose-completed-mols \
         hack-daily-summary \
         hack-message-forwarder \
         hack-mol-dog-jsonl \
         hack-order-override-watchdog \
         hack-stale-lock-sweeper; do
  test -f "$out/orders/$o.toml"          && echo "ok: $o.toml present"
  ! test -f "$out/orders/$o.toml.template" && echo "ok: $o.toml.template removed"
  grep -q "$out/scripts/$o.sh" "$out/orders/$o.toml" && echo "ok: $o exec line substituted"
done
```

Expected: 21 `ok:` lines (3 per order × 7 orders).

- [x] **Step 4: Commit**

```bash
git add packages/pgii-pack-dolt-hacks/pack-src/orders
git commit -m "feat(pgii-packs): port pgii-dolt-hacks order templates (absolute paths → \${SCRIPTS_DIR})"
```

---

### Task 4: Port the four doctor checks (with Fix-message path updates)

Carry all four doctor checks. Two of them (`check-formulas-dir`, `check-hack-11-still-needed`) have "Fix:" messages that reference `assets/imports/pgii-dolt-hacks/...` legacy paths — those become unreachable after Task 10 retires the legacy tree. Update them to point at the pack-src dir in the agent-support repo (the source of truth a human would edit to fix the issue).

**Files:**

- Create: 4 doctor check directories under `packages/pgii-pack-dolt-hacks/pack-src/doctor/`

- [x] **Step 1: Copy the four doctor check directories verbatim**

Run:

```bash
mkdir -p packages/pgii-pack-dolt-hacks/pack-src/doctor
for d in check-formulas-dir \
         check-hack-2-still-needed \
         check-hack-10-still-needed \
         check-hack-11-still-needed; do
  cp -R "/Users/phillipg/gc/assets/imports/pgii-dolt-hacks/doctor/$d" \
        "packages/pgii-pack-dolt-hacks/pack-src/doctor/$d"
done
```

- [x] **Step 2: Update `check-formulas-dir/run.sh` Fix message**

The legacy "Fix:" message reads:

```
echo "Fix: mkdir -p \"$PACK_ROOT/formulas\" && touch \"$PACK_ROOT/formulas/.gitkeep\""
```

Because `PACK_ROOT` is derived from the script's own location, this resolves to the nix-store path — which is read-only. A human can't fix it there. Repoint the guidance at the pack-src under nix-agent-support:

Edit `packages/pgii-pack-dolt-hacks/pack-src/doctor/check-formulas-dir/run.sh`. Find the line:

```sh
    echo "Fix: mkdir -p \"$PACK_ROOT/formulas\" && touch \"$PACK_ROOT/formulas/.gitkeep\""
```

Replace with:

```sh
    echo "Fix: in phillipgreenii-nix-agent-support, mkdir -p packages/pgii-pack-dolt-hacks/pack-src/formulas && touch packages/pgii-pack-dolt-hacks/pack-src/formulas/.gitkeep; then rebuild + zn-self-apply."
```

Also update the docstring comment block above SCRIPT_DIR. Find:

```sh
# This check fails if the workaround dir was removed. Re-create it:
#   mkdir -p assets/imports/pgii-dolt-hacks/formulas
#   touch    assets/imports/pgii-dolt-hacks/formulas/.gitkeep
```

Replace with:

```sh
# This check fails if the workaround dir was removed. Re-create it in the
# pack source (under phillipgreenii-nix-agent-support):
#   mkdir -p packages/pgii-pack-dolt-hacks/pack-src/formulas
#   touch    packages/pgii-pack-dolt-hacks/pack-src/formulas/.gitkeep
# then rebuild and zn-self-apply.
```

- [x] **Step 3: Update `check-hack-11-still-needed/run.sh` Fix message**

The legacy script has a three-line "Fix:" instruction block. Find:

```sh
    echo "  1. Remove [[orders.overrides]] name='mol-dog-jsonl' from city.toml"
    echo "  2. Delete assets/imports/pgii-dolt-hacks/orders/hack-mol-dog-jsonl.toml"
    echo "  3. Delete assets/imports/pgii-dolt-hacks/scripts/hack-mol-dog-jsonl.sh"
```

Replace with:

```sh
    echo "  1. Remove [[orders.overrides]] name='mol-dog-jsonl' from city.toml"
    echo "  2. In phillipgreenii-nix-agent-support, delete:"
    echo "       packages/pgii-pack-dolt-hacks/pack-src/orders/hack-mol-dog-jsonl.toml.template"
    echo "       packages/pgii-pack-dolt-hacks/pack-src/scripts/hack-mol-dog-jsonl.sh"
    echo "  3. Rebuild + zn-self-apply."
```

- [x] **Step 4: Verify no stale legacy-path references remain in doctor/**

Run:

```bash
grep -rnE 'assets/imports/pgii-dolt-hacks' packages/pgii-pack-dolt-hacks/pack-src/doctor/ \
  && echo "FAIL: stale legacy paths remain in doctor/" \
  || echo "ok: doctor/ paths clean"
```

Expected: `ok: doctor/ paths clean`.

- [x] **Step 5: Rebuild + verify doctor checks land in $out**

Run:

```bash
out=$(nix build .#pgii-pack-dolt-hacks --print-out-paths --no-link)
for d in check-formulas-dir \
         check-hack-2-still-needed \
         check-hack-10-still-needed \
         check-hack-11-still-needed; do
  test -f "$out/doctor/$d/doctor.toml" && test -x "$out/doctor/$d/run.sh" && echo "ok: $d"
done
test -d "$out/formulas" && echo "ok: formulas/ exists in $out (gascity 1.1.0 silent-drop guard)"
```

Expected: four `ok: <name>` lines + one `ok: formulas/ exists` line.

Then run the formulas-dir check against the actual nix-store pack to confirm it would pass post-deploy:

```bash
out=$(nix build .#pgii-pack-dolt-hacks --print-out-paths --no-link)
"$out/doctor/check-formulas-dir/run.sh"
```

Expected: prints "pgii-dolt-hacks/formulas/ present" and exits 0.

- [x] **Step 6: Commit**

```bash
git add packages/pgii-pack-dolt-hacks/pack-src/doctor
git commit -m "feat(pgii-packs): port pgii-dolt-hacks doctor checks (4 checks, Fix-message paths updated)"
```

---

### Task 5: Port the bats tests + fixtures

`hack-daily-summary.bats` is real coverage for the daily-summary script. It uses `$BATS_TEST_DIRNAME/..` to locate the script and `$BATS_TEST_DIRNAME/fixtures/` for test data — both resolve identically in the nix store.

**Files:**

- Create: `packages/pgii-pack-dolt-hacks/pack-src/scripts/tests/hack-daily-summary.bats`
- Create: 7 fixture files under `packages/pgii-pack-dolt-hacks/pack-src/scripts/tests/fixtures/`

- [x] **Step 1: Copy tests + fixtures verbatim**

Run:

```bash
mkdir -p packages/pgii-pack-dolt-hacks/pack-src/scripts/tests
cp -R \
  /Users/phillipg/gc/assets/imports/pgii-dolt-hacks/scripts/tests/. \
  packages/pgii-pack-dolt-hacks/pack-src/scripts/tests/
```

- [x] **Step 2: Verify the tree landed**

Run:

```bash
test -f packages/pgii-pack-dolt-hacks/pack-src/scripts/tests/hack-daily-summary.bats && echo "ok: bats file"
test -d packages/pgii-pack-dolt-hacks/pack-src/scripts/tests/fixtures            && echo "ok: fixtures/"
ls packages/pgii-pack-dolt-hacks/pack-src/scripts/tests/fixtures/ | wc -l | awk '{ if ($1 == 7) print "ok: 7 fixtures" ; else print "FAIL: " $1 " fixtures (expected 7)" }'
```

Expected: three `ok:` lines.

- [x] **Step 3: Run bats against the source tree to confirm tests still pass before nix-ifying**

Run:

```bash
bats packages/pgii-pack-dolt-hacks/pack-src/scripts/tests/hack-daily-summary.bats 2>&1 | tail -5
```

Expected: bats reports all-pass (counts depend on the file; should match the legacy run count).

If bats is not on PATH, run via nix:

```bash
nix-shell -p bats --run "bats packages/pgii-pack-dolt-hacks/pack-src/scripts/tests/hack-daily-summary.bats" 2>&1 | tail -5
```

- [x] **Step 4: Rebuild + verify the test tree lands in $out**

Run:

```bash
out=$(nix build .#pgii-pack-dolt-hacks --print-out-paths --no-link)
test -f "$out/scripts/tests/hack-daily-summary.bats" && echo "ok: bats in $out"
test -d "$out/scripts/tests/fixtures"                && echo "ok: fixtures/ in $out"
```

Expected: two `ok:` lines.

- [x] **Step 5: Commit**

```bash
git add packages/pgii-pack-dolt-hacks/pack-src/scripts/tests
git commit -m "feat(pgii-packs): port hack-daily-summary bats tests + fixtures into pgii-pack-dolt-hacks"
```

---

### Task 6: Wire `packs.dolt-hacks` into the HM module

Add the per-pack toggle to `home/programs/pgii-packs/default.nix`. No extra dependency assertion is needed (these orders depend on `gc`, `bd`, `jq`, `dolt`, `git` — all already system-baseline). The cities-required assertion from Phase 0 already covers the "no cities = no install" case generically.

**Files:**

- Modify: `home/programs/pgii-packs/default.nix`

- [x] **Step 1: Add the option declaration**

In `home/programs/pgii-packs/default.nix`, locate the `packs = { ... };` block. After Phase 1 it currently has `test-fixture.enable` and `pr-support.enable`:

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

Replace the trailing comment line with the new option (Phase 2 IS the dolt-hacks plan):

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
      dolt-hacks.enable = lib.mkEnableOption ''
        pgii-pack-dolt-hacks (HACK orders + scripts for dolt storage/lifecycle
        issues and gascity 1.1.0 supervisor regressions: HACK 2, 10, 11, 12, 14,
        15 and hack-daily-summary).
      '';
      # Real packs (workers, gastown, bead-importer) are added in their
      # respective phase plans.
    };
```

- [x] **Step 2: Add to `enabledPacks`**

In the same file, locate the `enabledPacks` definition. After Phase 1 it reads:

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

Append the dolt-hacks branch:

```nix
  enabledPacks =
    lib.optional cfg.packs.test-fixture.enable {
      name = "pgii-pack-test-fixture";
      drv = pkgs.pgii-pack-test-fixture;
    }
    ++ lib.optional cfg.packs.pr-support.enable {
      name = "pgii-pr-support";
      drv = pkgs.pgii-pack-pr-support;
    }
    ++ lib.optional cfg.packs.dolt-hacks.enable {
      name = "pgii-dolt-hacks";
      drv = pkgs.pgii-pack-dolt-hacks;
    };
```

Note: `name` is the GASCITY pack name (`pgii-dolt-hacks` — matches `pack.toml`'s `[pack] name`), NOT the nix package name (`pgii-pack-dolt-hacks`). The activation script writes `[imports.pgii-dolt-hacks]` blocks.

- [x] **Step 3: Verify the module evaluates**

Run:

```bash
nix flake check 2>&1 | tail -20
```

Expected: exit 0, no assertion failures, no unused-binding warnings.

- [x] **Step 4: Commit**

```bash
git add home/programs/pgii-packs/default.nix
git commit -m "feat(pgii-packs): wire packs.dolt-hacks option into HM module"
```

---

### Task 7: Add flake checks for pgii-pack-dolt-hacks (layout + bats)

Two checks:

1. `check-pgii-pack-dolt-hacks-layout` — verifies the pack's $out shape (mirrors Phase 1's pattern).
2. `test-pgii-pack-dolt-hacks-bats` — runs the hack-daily-summary bats tests against the built pack. This catches breakage in either the test or the script under test on every `nix flake check`.

**Files:**

- Modify: `flake.nix`

- [x] **Step 1: Add the layout check**

In `flake.nix`, locate the `checks = ...` block where `check-pgii-pack-pr-support-layout` is defined. Add a sibling check:

```nix
            check-pgii-pack-dolt-hacks-layout = pkgs.runCommand "check-pgii-pack-dolt-hacks-layout"
              { } ''
                pack=${pkgs.pgii-pack-dolt-hacks}
                test -f "$pack/pack.toml"                                   || { echo "missing pack.toml"; exit 1; }
                test -f "$pack/.pack-meta.json"                             || { echo "missing .pack-meta.json"; exit 1; }
                test -d "$pack/formulas"                                    || { echo "missing formulas/"; exit 1; }
                for o in hack-archive-and-compact \
                         hack-autoclose-completed-mols \
                         hack-daily-summary \
                         hack-message-forwarder \
                         hack-mol-dog-jsonl \
                         hack-order-override-watchdog \
                         hack-stale-lock-sweeper; do
                  test -f "$pack/orders/$o.toml" || { echo "missing orders/$o.toml"; exit 1; }
                  test -x "$pack/scripts/$o.sh"  || { echo "scripts/$o.sh not exec"; exit 1; }
                  grep -q "$pack/scripts/$o.sh" "$pack/orders/$o.toml" || { echo "orders/$o.toml exec line not substituted"; exit 1; }
                done
                test -f "$pack/scripts/hack-archive-and-compact.RUNBOOK.md" || { echo "missing RUNBOOK"; exit 1; }
                for d in check-formulas-dir \
                         check-hack-2-still-needed \
                         check-hack-10-still-needed \
                         check-hack-11-still-needed; do
                  test -f "$pack/doctor/$d/doctor.toml" || { echo "missing doctor/$d/doctor.toml"; exit 1; }
                  test -x "$pack/doctor/$d/run.sh"      || { echo "doctor/$d/run.sh not exec"; exit 1; }
                done
                test -f "$pack/scripts/tests/hack-daily-summary.bats" || { echo "missing bats"; exit 1; }
                test -d "$pack/scripts/tests/fixtures"                || { echo "missing fixtures/"; exit 1; }
                ! find "$pack" -name "*.template" | grep -q . || { echo "stale .template files in pack"; exit 1; }
                ! grep -rnE '/Users/phillipg/gc/assets/imports' "$pack" >/dev/null 2>&1 || { echo "stale legacy assets paths in pack"; exit 1; }
                touch $out
              '';
```

- [x] **Step 2: Add the bats check**

Sibling check, alongside the layout check:

```nix
            test-pgii-pack-dolt-hacks-bats = pkgs.runCommand "test-pgii-pack-dolt-hacks-bats"
              {
                nativeBuildInputs = [ pkgs.bats pkgs.bash pkgs.jq ];
              } ''
                pack=${pkgs.pgii-pack-dolt-hacks}
                # The bats file uses $BATS_TEST_DIRNAME/.. to locate the script,
                # so running it from inside the nix store works without copying.
                bats "$pack/scripts/tests/hack-daily-summary.bats"
                touch $out
              '';
```

If `nix flake check` doesn't find `pkgs.bats`, fall back to `bats-core` (provenance: `nixos-unstable` has both as aliases). The first build will surface the right attribute name.

- [x] **Step 3: Run both new checks**

Determine system:

```bash
system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
echo "system=$system"
```

Run:

```bash
nix build ".#checks.$system.check-pgii-pack-dolt-hacks-layout"
nix build ".#checks.$system.test-pgii-pack-dolt-hacks-bats"
```

Expected: both succeed.

- [x] **Step 4: Run the full flake-check set**

Run:

```bash
nix flake check 2>&1 | tail -10
```

Expected: exit 0.

- [x] **Step 5: Commit**

```bash
git add flake.nix
git commit -m "test(pgii-packs): flake checks for pgii-pack-dolt-hacks layout + bats"
```

---

### Task 8: Bump nix-ziprecruiter flake.lock + enable pack (build-only)

Switch on `pgii.packs.dolt-hacks.enable = true` in the test machine. Build the new system without applying — applying lives in Task 9 (the cutover task) where the legacy block edit happens atomically.

**Files:**

- Modify: `phillipg-nix-ziprecruiter/flake.lock`
- Modify: `phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix`

- [x] **Step 1: Update flake.lock to pick up the new pack**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
nix flake lock --update-input phillipgreenii-nix-agent-support 2>&1 | tail -5
```

Expected: lock file updated; new rev recorded that contains `pgii-pack-dolt-hacks` (the commit landed in Phase 2 Tasks 1-7).

- [x] **Step 2: Edit `machines/phillipg-mbp-02/default.nix`**

Locate the `pgii = { ... };` block. After Phase 1 it reads:

```nix
        pgii = {
          gascity.cities = [ "/Users/phillipg/gc" ];
          packs.test-fixture.enable = false;
          packs.pr-support.enable = true;
        };
```

Replace with:

```nix
        pgii = {
          gascity.cities = [ "/Users/phillipg/gc" ];
          packs.test-fixture.enable = false;
          packs.pr-support.enable = true;
          packs.dolt-hacks.enable = true;
        };
```

- [x] **Step 3: Build the new system (do NOT apply)**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
darwin-rebuild build --flake .#phillipg-mbp-02 2>&1 | tail -20
```

Expected: exit 0. If activation fails the cities/packs assertion (shouldn't), fix and retry.

- [x] **Step 4: Commit (do NOT apply yet — cutover is in Task 9)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
git add flake.lock machines/phillipg-mbp-02/default.nix
git commit -m "config(phillipg-mbp-02): enable pgii.packs.dolt-hacks (Phase 2 build, not yet applied)

Bumps phillipgreenii-nix-agent-support lock to pick up pgii-pack-dolt-hacks.
Apply happens in Phase 2 cutover (gc-sjz9 Task 9): atomic with removal of
the hand-written [imports.pgii-dolt-hacks] block from ~/gc/pack.toml.
Refs: gc-sjz9."
```

---

### Task 9: Cutover — remove legacy pack.toml block + apply

The activation script refuses to overwrite a hand-written `[imports.<name>]` block. Drop the legacy block first, then apply — activation writes the managed block fresh. Brief window (~1-2 min) where orders don't fire; acceptable because every order is idempotent and the longest interval (24h hack-archive-and-compact) tolerates a missed run trivially.

**Files:**

- Modify: `/Users/phillipg/gc/pack.toml` (remove lines 11-13: the hand-written `[imports.pgii-dolt-hacks]` block)

- [ ] **Step 1: Inspect the legacy block one more time**

Run:

```bash
grep -B 1 -A 3 'imports.pgii-dolt-hacks' /Users/phillipg/gc/pack.toml
```

Expected: shows the legacy block:

```
[imports.pgii-dolt-hacks]
source = "./assets/imports/pgii-dolt-hacks"
export = true
```

- [ ] **Step 2: Remove the legacy block (atomic edit)**

Use `sed` to delete the three lines plus the trailing blank line if any. Use a backup so revert is trivial.

Run:

```bash
cd /Users/phillipg/gc
cp pack.toml pack.toml.pre-phase2-cutover
sed -i.bak '/^\[imports\.pgii-dolt-hacks\]$/,/^export = true$/d' pack.toml
rm pack.toml.bak
```

Verify:

```bash
grep -n 'pgii-dolt-hacks' /Users/phillipg/gc/pack.toml || echo "ok: no pgii-dolt-hacks ref in pack.toml"
```

Expected: `ok: no pgii-dolt-hacks ref in pack.toml`.

- [ ] **Step 3: Apply**

Run:

```bash
zn-self-apply
```

Expected: exit 0. Activation log includes a line about writing the `[imports.pgii-dolt-hacks]` managed block to `~/gc/pack.toml`.

- [ ] **Step 4: Verify pack.toml now has the managed block**

Run:

```bash
grep -A 4 'BEGIN pgii-pack:pgii-dolt-hacks' /Users/phillipg/gc/pack.toml
```

Expected:

```
# BEGIN pgii-pack:pgii-dolt-hacks (managed)
[imports.pgii-dolt-hacks]
source = "/nix/store/<hash>-pgii-dolt-hacks-0.1.0"
export = true
# END pgii-pack:pgii-dolt-hacks (managed)
```

- [ ] **Step 5: Confirm supervisor reload was clean**

Run:

```bash
gc supervisor reload
tail -30 ~/.gc/supervisor.log | grep -E "(reload|pgii-dolt-hacks|hack-)"
```

Expected: `Config reloaded: <N> agents, <M> rigs (rev <hash>)`. No error lines naming pgii-dolt-hacks or any hack-\* order.

- [ ] **Step 6: Verify all seven orders are registered**

Run:

```bash
gc order list | grep -E '^hack-(archive-and-compact|autoclose-completed-mols|daily-summary|message-forwarder|mol-dog-jsonl|order-override-watchdog|stale-lock-sweeper)\b'
```

Expected: seven matching lines (one per order). Note: `gc order list` doesn't show effective-state (per CLAUDE.md it scans files), but it confirms the pack's orders/ tree is being walked from the nix store path.

Cross-check with the trace log (authoritative for "did it actually fire"):

```bash
grep "order.fired subject=hack-" /Users/phillipg/gc/.gc/runtime/control-dispatcher-trace.log | tail -10
```

Expected: lines for the 5-minute orders (hack-message-forwarder, hack-order-override-watchdog, hack-stale-lock-sweeper) appear within ~5 minutes of supervisor reload.

- [ ] **Step 7: Verify the four doctor checks are registered**

Run:

```bash
gc doctor list | grep -E 'check-(formulas-dir|hack-(2|10|11)-still-needed)'
```

Expected: four entries.

- [ ] **Step 8: Run one doctor check manually**

```bash
gc doctor run check-formulas-dir 2>&1 | head -10
```

Expected: exit 0; "pgii-dolt-hacks/formulas/ present" or equivalent.

---

### Task 10: Retire the legacy tree + close the bead

Once Task 9 verifies cleanly, delete the legacy `~/gc/assets/imports/pgii-dolt-hacks/` tree and close the bead.

- [ ] **Step 1: Confirm 24h of healthy parallel-free operation OR a 1h soak with the 5-min orders**

The hacks are idempotent, so the canonical soak is "all five short-interval hacks fired without errors over the last hour". Run:

```bash
since=$(date -v-1H -u '+%Y-%m-%dT%H:%M:%S')
grep "order.fired subject=hack-" /Users/phillipg/gc/.gc/runtime/control-dispatcher-trace.log \
  | awk -v cutoff="$since" '$0 ~ cutoff || $0 > cutoff' \
  | awk '{ for (i = 1; i <= NF; i++) if ($i ~ /^subject=/) print $i }' \
  | sort -u
```

Expected: at least these four (24h-only orders may not have fired yet):

```
subject=hack-autoclose-completed-mols
subject=hack-message-forwarder
subject=hack-order-override-watchdog
subject=hack-stale-lock-sweeper
```

(`hack-mol-dog-jsonl` is 15m so within an hour it should appear too. `hack-archive-and-compact` and `hack-daily-summary` are 24h; not expected in a 1h soak.)

If any expected order is absent for >2× its interval, investigate before proceeding.

- [ ] **Step 2: Delete the legacy tree**

Run:

```bash
cd /Users/phillipg/gc
git rm -rf assets/imports/pgii-dolt-hacks
```

- [ ] **Step 3: Sanity-check nothing else references the legacy path**

Run:

```bash
grep -rn 'assets/imports/pgii-dolt-hacks' /Users/phillipg/gc/ \
  --exclude-dir=.git \
  --exclude-dir=.gc \
  --exclude='*.original.md' \
  || echo "ok: no remaining references in ~/gc"
```

Expected: `ok: no remaining references in ~/gc`.

If anything matches, investigate before committing. Likely candidates: CLAUDE.md notes, HACKS.md, archived bead JSONL files (those are historical, OK).

- [ ] **Step 4: Commit the retirement**

```bash
cd /Users/phillipg/gc
git commit -m "feat(pgii-packs): Phase 2 cutover — retire legacy pgii-dolt-hacks tree

pgii-pack-dolt-hacks now ships from phillipgreenii-nix-agent-support and
is wired through the pgii.packs.dolt-hacks HM option. The hand-written
[imports.pgii-dolt-hacks] block was replaced by a managed block in
pack.toml at Phase 2 cutover.

Refs: gc-sjz9."
```

- [ ] **Step 5: Clean up the pre-cutover backup**

Run:

```bash
rm /Users/phillipg/gc/pack.toml.pre-phase2-cutover
```

(Backup is no longer needed; full pre-cutover state is in git history.)

- [ ] **Step 6: Close the bead**

```bash
gc bd close gc-sjz9 --reason="Phase 2 build + cutover complete. pgii-pack-dolt-hacks built (7 orders, 7 scripts, RUNBOOK, 4 doctor checks, bats tests + fixtures), wired through HM module (packs.dolt-hacks.enable), bumped flake.lock, applied on phillipg-mbp-02, legacy ~/gc/assets/imports/pgii-dolt-hacks/ retired. All seven orders fired post-cutover; doctor checks register clean. Plan: docs/superpowers/plans/2026-05-28-pgii-packs-phase2-dolt-hacks.md."
```

This unblocks gc-xzh5 (Phase 3 pgii-workers).

---

## Risks and unknowns

1. **Order timing during cutover.** Between Task 9 Step 2 (removing the legacy block) and Task 9 Step 3 (apply), the dispatcher's effective-orders set still contains the old paths until the supervisor reloads. After `zn-self-apply` triggers the post-activation reload, the supervisor switches to the nix-store paths. Worst-case gap: one missed cycle of the 5-min hacks. Idempotent, acceptable.

2. **`hack-mol-dog-jsonl` SYS_PACK path.** The script reads from `${GC_CITY}/.gc/system/packs/maintenance/assets/scripts/jsonl-export.sh` — that's a system pack inside the city, NOT the pgii-dolt-hacks pack. Unaffected by migration.

3. **`hack-order-override-watchdog` reload target.** The watchdog runs `gc supervisor reload` when it detects a regression. After migration the watchdog's exec= path is the nix-store script, but the reload it triggers is unchanged. No risk.

4. **gascity loads the pack by pack.toml `[pack] name`, not by import-key.** Both legacy and nix-built pack.toml have `name = "pgii-dolt-hacks"`. As long as only ONE `[imports.pgii-dolt-hacks]` (or `[imports.X]` where X resolves to a pack-named-`pgii-dolt-hacks`) is active at a time, no collision. Task 9 enforces this by removing the legacy import before the activation writes the new one. The window where both could exist simultaneously is the gap between Task 9 Step 2 (which removes the legacy block) and Step 3 (which writes the managed block) — and nothing reads pack.toml between those two steps.

5. **Bats may not be on host PATH at Task 5 Step 3 time.** Fallback nix-shell command provided.

---

## Self-review checklist

- [ ] **Spec coverage:** Phase 2 spec row → tasks. Pack body verbatim copy (Tasks 2, 4, 5). Order TOML conversion (Task 3). Script state-path audit (Task 2 Step 2 — no rewrites needed, verified). Cutover deletes legacy tree (Task 10). No parallel-run (spec says not needed; we follow that).
- [ ] **Placeholder scan:** Search for `TBD`, `TODO`, `fill in`. None should remain.
- [ ] **Type consistency:** Gascity pack name is `pgii-dolt-hacks` everywhere it appears (pack.toml, marker syntax, `enabledPacks.name`, `[imports.<name>]`). Nix package name is `pgii-pack-dolt-hacks` (in flake.nix overlay + machine config `packs.dolt-hacks` short form via HM module).
- [ ] **All `git commit` commands include the right files (no stray `git add .`).**
- [ ] **All `nix build` / `nix flake check` commands target the correct attribute paths.**

---

## References

- **Spec (this phase):** `docs/superpowers/specs/2026-05-26-pgii-packs-migration-design.md` (Phase 2 row)
- **Phase 1 plan (pattern source):** `docs/superpowers/plans/2026-05-27-pgii-packs-phase1-pr-support.md`
- **Legacy pack source:** `/Users/phillipg/gc/assets/imports/pgii-dolt-hacks/`
- **mkPgiiPack:** `lib/mkPgiiPack.nix` (Phase 0)
- **Activation script:** `home/programs/pgii-packs/activation.sh` (Phase 0)
- **gc-sjz9 bead:** Phase 2 work tracking bead.
