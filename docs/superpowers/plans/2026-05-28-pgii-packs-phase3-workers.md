# pgii-pack-workers Implementation Plan (Phase 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the nix-packaged `pgii-pack-workers` pack — the migration target for `~/gc/assets/imports/pgii-workers/`. Resolve the open spec question about rig-scope pack binding: extend the Phase 0 machinery so `mkPgiiPack` infers pack scope from `pack.toml`, records it in `.pack-meta.json`, and `activation.sh` emits the correct block shape (`[defaults.rig.imports.<name>]` for rig packs, `[imports.<name>]` for city packs).

**Architecture:** `mkPgiiPack` parses the pack's `pack.toml` via `builtins.fromTOML` at Nix eval time and reads the first `[[named_session]].scope` (defaulting to `"city"` if absent). The scope is embedded in `.pack-meta.json` alongside name/version. `activation.sh` reads the scope per pack and switches between two block templates: city-scope uses today's `[imports.<name>]` block (unchanged), rig-scope uses `[defaults.rig.imports.<name>]`. The managed-block sentinels (`# BEGIN pgii-pack:<name> (managed)` / `# END pgii-pack:<name> (managed)`) stay identical so the strip-and-rewrite path doesn't need to change. The `pgii-workers` pack ships agent `worker` with `scope = "rig"` in both pack.toml and agent.toml — copied verbatim. Empirically confirmed by inspecting `gc config show` post-experiment: `default_rig_includes = [...]` is populated by `[defaults.rig.imports.*]` blocks; `[imports.*]` does not populate it.

**Tech Stack:** Nix flakes, home-manager, bash, bats-core, mkPgiiPack lib (extended in this phase).

**Spec:** `docs/superpowers/specs/2026-05-26-pgii-packs-migration-design.md` (Phase 3 row + "Open items deferred to their phases" → "Phase 3 — rig-scope registration")

**Repo root for all paths in this plan:** `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`

**Companion repo paths:**

- Source of pack body: `/Users/phillipg/gc/assets/imports/pgii-workers/`
- Machine config to enable in: `/Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix`
- City pack.toml to clean up at cutover: `/Users/phillipg/gc/pack.toml` (drop `[defaults.rig.imports.pgii-workers]`)
- City city.toml to clean up at cutover: `/Users/phillipg/gc/city.toml` (drop `[rigs.imports.pgii-workers]` in the `ziprecruiter` rig section; KEEP `[[rigs.patches]] agent="worker" max_active_sessions=3`)

**Phase 2 dependency:** Done (gc-sjz9 closed; pgii-pack-dolt-hacks live).

---

## File structure

**Files to create (under `packages/pgii-pack-workers/`):**

| File                                                                    | Purpose                                                                                                                       |
| ----------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `packages/pgii-pack-workers/default.nix`                                | callPackage entry; `mkPgiiPack { name = "pgii-workers"; ... }`.                                                               |
| `packages/pgii-pack-workers/pack-src/pack.toml`                         | Gascity manifest: `[pack] name="pgii-workers" schema=2` + `[[named_session]] template="worker" scope="rig" mode="on_demand"`. |
| `packages/pgii-pack-workers/pack-src/agents/worker/agent.toml`          | Verbatim copy.                                                                                                                |
| `packages/pgii-pack-workers/pack-src/agents/worker/prompt.template.md`  | Verbatim copy.                                                                                                                |
| `packages/pgii-pack-workers/pack-src/agents/worker/scripts/bash-env.sh` | Verbatim copy.                                                                                                                |

**Files to modify:**

| File                                                             | Change                                                                                                                                                                                                            |
| ---------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `lib/mkPgiiPack.nix`                                             | Parse pack-src/pack.toml via `builtins.fromTOML`; infer `scope` from first `[[named_session]].scope` (default `"city"`); embed in `.pack-meta.json`.                                                              |
| `home/programs/pgii-packs/activation.sh`                         | For each pack, read `<store>/.pack-meta.json` → `scope`; emit `[defaults.rig.imports.<name>]` for rig packs, `[imports.<name>]` for city packs. Strip-managed-block logic stays scope-agnostic (same sentinels).  |
| `home/programs/pgii-packs/tests/*.bats`                          | Update existing tests' fixture packs to include `.pack-meta.json` with `"scope": "city"`. Add new `rig-scope-write.bats` covering `[defaults.rig.imports.<name>]` emission.                                       |
| `home/programs/pgii-packs/default.nix`                           | Add `packs.workers.enable` option; add `pgii-workers` to `enabledPacks`. No `scope` user-option (auto-derived from pack metadata).                                                                                |
| `flake.nix`                                                      | Register `pgii-pack-workers` in overlay + `packages` inherit; add `check-pgii-pack-workers-layout` flake check.                                                                                                   |
| `phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix` | Add `pgii.packs.workers.enable = true`. Bump `phillipgreenii-agent-support` flake.lock.                                                                                                                           |
| `/Users/phillipg/gc/pack.toml`                                   | Remove the hand-written `[defaults.rig.imports.pgii-workers]` block (lines 12-17 incl. comment). Activation refuses to overwrite hand-written blocks.                                                             |
| `/Users/phillipg/gc/city.toml`                                   | Remove `[rigs.imports]` / `[rigs.imports.pgii-workers]` blocks inside the `[[rigs]] name="ziprecruiter"` section. KEEP the adjacent `[[rigs.patches]]` block (still needed for the `max_active_sessions=3` bump). |

**Files explicitly NOT carried over:** None — the legacy pack is small (3 files) and all migrate.

---

## Conventions used in this plan

- Working directory: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support` unless stated. Cross-repo edits use explicit `cd`.
- `nix build` uses `.#<attr>`.
- treefmt runs on commit; accept its formatting unchanged (precedent from Phase 2 — shfmt is semantically safe for bash; bats syntax is bash).
- The `packages/pg-pr/` files modified at session start are NOT mine; never `git add -A` or `git add .`. Use explicit file lists.

---

### Task 1: Extend `mkPgiiPack` to infer pack scope

Read the pack's source pack.toml via `builtins.fromTOML`, infer scope from the first `[[named_session]].scope`, embed in `.pack-meta.json`. Default to `"city"` if no `[[named_session]]` block has a `scope` field.

**Files:**

- Modify: `lib/mkPgiiPack.nix`

- [x] **Step 1: Add scope inference to `mkPgiiPack`**

Edit `lib/mkPgiiPack.nix`. Find the start of the function body (after the argument destructuring):

```nix
{ lib, pkgs }:
{
  name,
  version ? "0.1.0",
  src,
  substitutions ? { },
  meta ? { },
}:
pkgs.runCommand "${name}-${version}"
```

Insert a `let` binding between the argument list and the `pkgs.runCommand` call:

```nix
{ lib, pkgs }:
{
  name,
  version ? "0.1.0",
  src,
  substitutions ? { },
  meta ? { },
}:
let
  # Infer pack scope from pack.toml's first [[named_session]] entry. This is
  # the same field gascity reads to decide where to bind the session
  # template. Packs with no [[named_session]] block default to "city" — they
  # contribute orders, scripts, or doctor checks, not session templates.
  packToml = builtins.fromTOML (builtins.readFile (src + "/pack.toml"));
  sessions =
    if packToml ? named_session then
      (if builtins.isList packToml.named_session then packToml.named_session else [ packToml.named_session ])
    else
      [ ];
  firstScoped = lib.findFirst (s: s ? scope) null sessions;
  scope = if firstScoped == null then "city" else firstScoped.scope;
in
pkgs.runCommand "${name}-${version}"
```

- [x] **Step 2: Embed scope in `.pack-meta.json`**

Find the existing `.pack-meta.json` write at the bottom of the runCommand body:

```nix
    cat > $out/.pack-meta.json <<EOF
    { "name": "${name}", "version": "${version}" }
    EOF
```

Replace with:

```nix
    cat > $out/.pack-meta.json <<EOF
    { "name": "${name}", "version": "${version}", "scope": "${scope}" }
    EOF
```

- [x] **Step 3: Verify existing packs still build with `scope: "city"`**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support

# pgii-pack-test-fixture has no [[named_session]] → scope must default to "city"
out=$(nix build .#pgii-pack-test-fixture --print-out-paths --no-link --rebuild)
jq -r '.scope' "$out/.pack-meta.json"
# Expected: city

# pgii-pack-pr-support has named_session entries WITHOUT explicit scope → also "city"
out=$(nix build .#pgii-pack-pr-support --print-out-paths --no-link --rebuild)
jq -r '.scope' "$out/.pack-meta.json"
# Expected: city

# pgii-pack-dolt-hacks has no [[named_session]] → "city"
out=$(nix build .#pgii-pack-dolt-hacks --print-out-paths --no-link --rebuild)
jq -r '.scope' "$out/.pack-meta.json"
# Expected: city
```

All three packs must report `city`. If any reports anything else, the inference logic is wrong — fix before committing.

- [x] **Step 4: Commit**

```bash
git diff --cached --name-only  # confirm scope — only lib/mkPgiiPack.nix
git add lib/mkPgiiPack.nix
git commit -m "feat(mkPgiiPack): infer pack scope from pack.toml + embed in .pack-meta.json"
```

- [x] **Step 5: Tick off `- [ ]` boxes for Task 1** in `docs/superpowers/plans/2026-05-28-pgii-packs-phase3-workers.md`.

---

### Task 2: Extend `activation.sh` to honor pack scope

Per-pack: read `<store-path>/.pack-meta.json` for `scope`. Emit `[defaults.rig.imports.<name>]` block for rig-scope packs, keep the existing `[imports.<name>]` block for city packs. Hand-written-block detection extends to ALSO check `[defaults.rig.imports.<name>]` for rig packs.

**Files:**

- Modify: `home/programs/pgii-packs/activation.sh`

- [x] **Step 1: Read the existing `emit_block` function**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
grep -n 'emit_block\|imports\.' home/programs/pgii-packs/activation.sh | head -20
```

Identify the function that emits the block (likely around line 105-130 of activation.sh).

- [x] **Step 2: Add a `pack_scope` helper**

Above `emit_block` (or wherever pack helpers live), add:

```bash
# pack_scope <store-path>
# Echoes "city" (default) or "rig" based on the pack's .pack-meta.json.
pack_scope() {
  local store_path="$1"
  local meta="$store_path/.pack-meta.json"
  if [ -f "$meta" ]; then
    jq -r '.scope // "city"' "$meta"
  else
    echo "city"
  fi
}
```

- [x] **Step 3: Update `emit_block` to switch on scope**

Find the current `emit_block`. It likely looks like:

```bash
emit_block() {
  local name="$1"
  local path="$2"
  cat <<EOF
# BEGIN pgii-pack:$name (managed)
[imports.$name]
source = "$path"
export = true
# END pgii-pack:$name (managed)
EOF
}
```

Modify it to:

```bash
emit_block() {
  local name="$1"
  local path="$2"
  local scope
  scope="$(pack_scope "$path")"
  local header
  case "$scope" in
    rig)  header="[defaults.rig.imports.$name]" ;;
    city) header="[imports.$name]" ;;
    *)
      echo "pgii-packs: ERROR: pack '$name' has unsupported scope '$scope' (expected city|rig)" >&2
      exit 4
      ;;
  esac
  cat <<EOF
# BEGIN pgii-pack:$name (managed)
$header
source = "$path"
export = true
# END pgii-pack:$name (managed)
EOF
}
```

- [x] **Step 4: Extend the hand-written-collision pre-flight check**

Currently the pre-flight checks for `[imports.<name>]` hand-written blocks. For rig packs, ALSO check `[defaults.rig.imports.<name>]`.

Find the existing pre-flight (search for "Hand-written" or "ERROR.\*\\[imports\\."). The block looks like:

```bash
if grep -Eq "^\[imports\.$name\]\$" "$pack_toml"; then
  ...
  if [ "$inside_managed" = "-1" ]; then
    echo "pgii-packs: ERROR: Hand-written [imports.$name] exists in $pack_toml" >&2
    ...
  fi
fi
```

Adapt: determine the pack's expected header from its scope first, then check that specific header:

```bash
for name in "${PACK_NAMES[@]}"; do
  local store_path="${PACKS[$name]}"
  local scope
  scope="$(pack_scope "$store_path")"

  local toml_key
  case "$scope" in
    rig)  toml_key="defaults.rig.imports.$name" ;;
    city) toml_key="imports.$name" ;;
    *)
      echo "pgii-packs: ERROR: pack '$name' has unsupported scope '$scope'" >&2
      exit 4
      ;;
  esac

  if grep -Eq "^\[${toml_key//./\\.}\]\$" "$pack_toml"; then
    local inside_managed
    inside_managed=$(awk -v name="$name" -v key="$toml_key" '
      BEGIN { in_block = 0; found = 0 }
      $0 == "# BEGIN pgii-pack:" name " (managed)" { in_block = 1; next }
      $0 == "# END pgii-pack:" name " (managed)"   { in_block = 0; next }
      $0 == "[" key "]" {
        if (in_block) { found = 1 }
        else { found = -1; exit }
      }
      END { print found }
    ' "$pack_toml")

    if [ "$inside_managed" = "-1" ]; then
      echo "pgii-packs: ERROR: Hand-written [$toml_key] exists in $pack_toml" >&2
      echo "  Either rename or delete the hand-written block, or remove" >&2
      echo "  phillipgreenii.programs.pgii.packs.$name from your config." >&2
      exit 3
    fi
  fi
done
```

The variable name `name` inside the awk script and `name="$name"` in the `-v` arg shadow each other; that's fine — the `-v` sets awk's `name`, and the bash `$name` resolves before awk runs. If your shell hates `local` outside functions (some bash configs do), wrap the whole loop in a function instead. Apply the same `local` placement the existing code uses.

- [x] **Step 5: Smoke-test by building the test fixture and running an activation**

```bash
nix build .#pgii-pack-test-fixture --print-out-paths --no-link
# Build a fake city dir to exercise activation
tmpcity=$(mktemp -d)
cat > "$tmpcity/pack.toml" <<TOML
[pack]
name = "test-city"
schema = 2

[imports]
TOML
bash home/programs/pgii-packs/activation.sh \
  --cities "[\"$tmpcity\"]" \
  --packs  "{\"pgii-pack-test-fixture\":\"$(nix build .#pgii-pack-test-fixture --print-out-paths --no-link)\"}"
grep -A 3 'pgii-pack:pgii-pack-test-fixture' "$tmpcity/pack.toml"
rm -rf "$tmpcity"
```

Expected: shows `[imports.pgii-pack-test-fixture]` block — city-scope (the test fixture has no rig-scope agents).

- [x] **Step 6: Commit (defer the full bats run to Task 3)**

```bash
git diff --cached --name-only
git add home/programs/pgii-packs/activation.sh
git commit -m "feat(pgii-packs): activation.sh writes scope-aware import blocks (rig vs city)"
```

- [x] **Step 7: Tick off `- [ ]` boxes for Task 2.**

---

### Task 3: Update + extend bats tests to cover scope-aware behavior

Existing tests at `home/programs/pgii-packs/tests/` use fake pack fixtures. Each fixture is a tiny directory with at least a `pack.toml`. Now they also need `.pack-meta.json` with a `scope` field — without this the new `pack_scope` helper falls back to "city" via the `[ -f "$meta" ]` guard, which is actually fine for the existing city-scope tests. Verify, then add one new test covering rig-scope emission.

**Files:**

- Modify (verify only): `home/programs/pgii-packs/tests/*.bats` — should still pass because pack_scope defaults to "city" when meta is absent.
- Modify: existing test fixtures to include `.pack-meta.json` (only if any existing test depends on a specific scope — likely none).
- Create: `home/programs/pgii-packs/tests/rig-scope-write.bats`

- [x] **Step 1: Run the existing bats suite to baseline**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
nix build .#checks.$(nix eval --raw --impure --expr 'builtins.currentSystem').test-pgii-packs-activation 2>&1 | tail -20
```

Expected: all existing tests pass. The new pack_scope helper falls back to "city" when `.pack-meta.json` is absent, so existing fixtures don't need to change.

If a test fails, examine — typically because the pack_scope fallback or the hand-written-detection branch broke something subtle. Fix activation.sh before continuing.

- [x] **Step 2: Read the existing test layout**

```bash
ls home/programs/pgii-packs/tests/
head -50 home/programs/pgii-packs/tests/fresh-write.bats
```

Note the conventions: how fixtures are created, how the activation script is invoked, how assertions are made.

- [x] **Step 3: Write `rig-scope-write.bats`**

File: `home/programs/pgii-packs/tests/rig-scope-write.bats`

```bash
#!/usr/bin/env bats
# rig-scope-write: when a pack's .pack-meta.json declares scope="rig",
# activation.sh writes [defaults.rig.imports.<name>] instead of
# [imports.<name>] inside the managed-block sentinels.

setup() {
  TEST_DIR="$(mktemp -d)"
  CITY="$TEST_DIR/city"
  PACK_RIG="$TEST_DIR/pack-rig"
  mkdir -p "$CITY" "$PACK_RIG"

  cat > "$CITY/pack.toml" <<TOML
[pack]
name = "test-city"
schema = 2

[imports]
TOML

  cat > "$PACK_RIG/pack.toml" <<TOML
[pack]
name = "test-rig-pack"
schema = 2
TOML
  cat > "$PACK_RIG/.pack-meta.json" <<JSON
{ "name": "test-rig-pack", "version": "0.1.0", "scope": "rig" }
JSON
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "rig-scope pack writes [defaults.rig.imports.<name>] block" {
  bash "$BATS_TEST_DIRNAME/../activation.sh" \
    --cities "[\"$CITY\"]" \
    --packs  "{\"test-rig-pack\":\"$PACK_RIG\"}"

  run cat "$CITY/pack.toml"
  [ "$status" -eq 0 ]
  [[ "$output" == *"# BEGIN pgii-pack:test-rig-pack (managed)"* ]]
  [[ "$output" == *"[defaults.rig.imports.test-rig-pack]"* ]]
  [[ "$output" == *"source = \"$PACK_RIG\""* ]]
  [[ "$output" == *"export = true"* ]]
  [[ "$output" == *"# END pgii-pack:test-rig-pack (managed)"* ]]
  ! [[ "$output" =~ "[imports.test-rig-pack]" ]]
}

@test "rig-scope pack hand-written collision is detected" {
  cat >> "$CITY/pack.toml" <<TOML
[defaults.rig.imports.test-rig-pack]
source = "/already-managed-elsewhere"
TOML
  run bash "$BATS_TEST_DIRNAME/../activation.sh" \
    --cities "[\"$CITY\"]" \
    --packs  "{\"test-rig-pack\":\"$PACK_RIG\"}"
  [ "$status" -ne 0 ]
  [[ "$output" == *"Hand-written"* ]]
}
```

- [x] **Step 4: Update the bats flake check to include `rig-scope-write.bats`**

If the existing check uses a glob (`*.bats`), no change needed. If it lists files individually, add the new file.

```bash
grep -n 'rig-scope-write\|\.bats' flake.nix | head -10
```

Adjust as needed.

- [x] **Step 5: Run the bats suite**

```bash
nix build .#checks.$(nix eval --raw --impure --expr 'builtins.currentSystem').test-pgii-packs-activation 2>&1 | tail -20
```

Expected: all tests pass, including the two new ones.

- [x] **Step 6: Commit**

```bash
git diff --cached --name-only
git add home/programs/pgii-packs/tests/rig-scope-write.bats
# Add any modified existing tests too (likely none).
git commit -m "test(pgii-packs): rig-scope-write bats covers [defaults.rig.imports.<name>] emission"
```

- [x] **Step 7: Tick off `- [ ]` boxes for Task 3.**

---

### Task 4: Stub the `pgii-pack-workers` derivation

Same pattern as Phase 2 Task 1 — minimal derivation that builds the pack-src skeleton.

**Files:**

- Create: `packages/pgii-pack-workers/default.nix`
- Create: `packages/pgii-pack-workers/pack-src/pack.toml`

- [x] **Step 1: Create `pack.toml` declaring rig scope**

File: `packages/pgii-pack-workers/pack-src/pack.toml`

```toml
# pgii-workers — rig-scoped worker pool.
#
# One agent template, `worker`, with scope = "rig". Every rig that
# imports this pack gets its own worker materialization. Workers claim
# open beads with acceptance_criteria set; ambiguous work gets labeled
# `needs-foreman`. Per-rig session concurrency is overridden via
# [[rigs.patches]] in city.toml (default 0/1; ziprecruiter wants 0/3).
#
# Migrated from ~/gc/assets/imports/pgii-workers/ (hand-built pack).

[pack]
name = "pgii-workers"
schema = 2

[[named_session]]
template = "worker"
scope = "rig"
mode = "on_demand"
```

The `scope = "rig"` field is what mkPgiiPack reads to set `.pack-meta.json` scope.

- [x] **Step 2: Create `default.nix`**

File: `packages/pgii-pack-workers/default.nix`

```nix
{ lib, pkgs }:
let
  mkPgiiPack = import ../../lib/mkPgiiPack.nix { inherit lib pkgs; };
in
mkPgiiPack {
  name = "pgii-workers";
  src = ./pack-src;
  meta = with lib; {
    description = "Rig-scoped generic worker agent (claims beads with acceptance_criteria).";
    license = licenses.mit;
    platforms = platforms.unix;
  };
}
```

- [x] **Step 3: Register in flake.nix**

Find the overlay block (where `pgii-pack-dolt-hacks` was added in Phase 2):

```nix
          pgii-pack-dolt-hacks = final.callPackage ./packages/pgii-pack-dolt-hacks { };
```

Add immediately after:

```nix
          pgii-pack-workers = final.callPackage ./packages/pgii-pack-workers { };
```

Also add to the `packages` inherit block (mirrors how `pgii-pack-dolt-hacks` was exposed there in Phase 2):

```nix
        pgii-pack-workers
```

- [x] **Step 4: Build the stub**

```bash
nix build .#pgii-pack-workers --print-out-paths
```

Expected: `/nix/store/...-pgii-workers-0.1.0` path, exit 0.

- [x] **Step 5: Verify layout + scope**

```bash
out=$(nix build .#pgii-pack-workers --print-out-paths --no-link)
test -f "$out/pack.toml"                              && echo "ok: pack.toml"
test -d "$out/agents"                                  && echo "ok: agents/"
test -d "$out/orders"                                  && echo "ok: orders/"
test -d "$out/scripts"                                 && echo "ok: scripts/"
test -d "$out/formulas"                                && echo "ok: formulas/"
grep -q 'name = "pgii-workers"' "$out/pack.toml"       && echo "ok: pack name"
jq -r '.scope' "$out/.pack-meta.json" | grep -q '^rig$' && echo "ok: scope=rig in meta"
```

Expected: 7 `ok:` lines. **Critically: `scope=rig` confirms mkPgiiPack's inference works on this real pack.** If meta says `city`, mkPgiiPack's logic is buggy — fix before continuing.

- [x] **Step 6: Commit**

```bash
git add packages/pgii-pack-workers/default.nix \
        packages/pgii-pack-workers/pack-src/pack.toml \
        flake.nix
git commit -m "feat(pgii-packs): stub pgii-pack-workers derivation + overlay entry (rig-scope)"
```

- [x] **Step 7: Tick off `- [ ]` boxes for Task 4.**

---

### Task 5: Port the worker agent verbatim

The agent is small: one `agent.toml`, one `prompt.template.md`, one `bash-env.sh`. The `prompt.template.md` uses gascity's `{{.Foo}}` go-template syntax — those are NOT envsubst-replaceable and pass through mkPgiiPack untouched (envsubst only sees `${...}` markers, not `{{...}}`). Verify after build that no `{{...}}` got mangled.

**Files:**

- Create: `packages/pgii-pack-workers/pack-src/agents/worker/agent.toml`
- Create: `packages/pgii-pack-workers/pack-src/agents/worker/prompt.template.md`
- Create: `packages/pgii-pack-workers/pack-src/agents/worker/scripts/bash-env.sh`

- [x] **Step 1: Copy the agent tree verbatim**

```bash
mkdir -p packages/pgii-pack-workers/pack-src/agents/worker/scripts
cp /Users/phillipg/gc/assets/imports/pgii-workers/agents/worker/agent.toml \
   packages/pgii-pack-workers/pack-src/agents/worker/
cp /Users/phillipg/gc/assets/imports/pgii-workers/agents/worker/prompt.template.md \
   packages/pgii-pack-workers/pack-src/agents/worker/
cp /Users/phillipg/gc/assets/imports/pgii-workers/agents/worker/scripts/bash-env.sh \
   packages/pgii-pack-workers/pack-src/agents/worker/scripts/
```

- [x] **Step 2: Verify no stale absolute-path references in agent.toml**

The legacy `agent.toml` may reference scripts via absolute paths (e.g., `work_query = "/Users/phillipg/gc/assets/imports/pgii-workers/agents/worker/work_query.sh"` if such a script existed). Audit:

```bash
grep -nE '/Users/phillipg/gc/assets/imports/pgii-workers' \
  packages/pgii-pack-workers/pack-src/agents/worker/*.toml \
  packages/pgii-pack-workers/pack-src/agents/worker/*.md \
  packages/pgii-pack-workers/pack-src/agents/worker/scripts/*.sh \
  && echo "FAIL: found absolute-path refs to legacy tree" \
  || echo "ok: no legacy absolute paths"
```

If FAIL, the file needs path rewrites. Likely options:

- Convert `agent.toml` to `agent.toml.template` with `${PACK_ROOT}` substitution (extend mkPgiiPack's exported vars to include `PACK_ROOT = $out`).
- Or use gascity's `{{.PackRoot}}` template variable if it exists (check by `strings -a` the binary for `PackRoot`).

If FAIL surfaces here, STOP and report — the resolution informs the plan.

Expected (best case): `ok: no legacy absolute paths`.

- [x] **Step 3: Verify `bash-env.sh` doesn't reference legacy pack paths**

```bash
grep -nE 'assets/imports/pgii-workers' packages/pgii-pack-workers/pack-src/agents/worker/scripts/bash-env.sh \
  && echo "FAIL" || echo "ok: bash-env.sh clean"
```

- [x] **Step 4: Rebuild and verify the agent tree lands in $out**

```bash
git add packages/pgii-pack-workers/pack-src/agents
out=$(nix build .#pgii-pack-workers --print-out-paths --no-link)
test -f "$out/agents/worker/agent.toml"             && echo "ok: agent.toml"
test -f "$out/agents/worker/prompt.template.md"     && echo "ok: prompt.template.md"
test -f "$out/agents/worker/scripts/bash-env.sh"    && echo "ok: bash-env.sh"
test -x "$out/agents/worker/scripts/bash-env.sh"    && echo "ok: bash-env.sh executable"
# mkPgiiPack's chmod +x only targets scripts/*.sh at the top of scripts/.
# Worker's bash-env lives under agents/worker/scripts/ — verify it has +x
# from the source tree (cp preserves the original mode).

# Confirm go-template syntax wasn't mangled by envsubst.
grep -c '{{.Rig}}\|{{.RigName}}\|{{.RigRoot}}\|{{.AgentBase}}\|{{.WorkDir}}' "$out/agents/worker/prompt.template.md" "$out/agents/worker/agent.toml" \
  | awk -F: '$2>0 { ok=1 } END { if (ok) print "ok: go-template markers intact"; else print "FAIL: no go-template markers found" }'
```

Expected: 4 `ok:` lines + `ok: go-template markers intact`.

If `bash-env.sh` isn't executable in $out, the legacy copy may have lacked +x. Fix the source mode on disk (`chmod +x`) before re-running. Or extend mkPgiiPack to also `chmod +x` agents/_/scripts/_.sh — but check first whether other packs would be affected.

- [x] **Step 5: Commit**

```bash
git diff --cached --name-only
git commit -m "feat(pgii-packs): port pgii-workers worker agent (verbatim, rig-scope)"
```

- [x] **Step 6: Tick off `- [ ]` boxes for Task 5.**

---

### Task 6: Wire `packs.workers` into the HM module

Phase 2 wired `packs.dolt-hacks`. This task adds `packs.workers` in the same pattern. No `scope` user-option is exposed — activation.sh auto-derives scope from `.pack-meta.json`.

**Files:**

- Modify: `home/programs/pgii-packs/default.nix`

- [x] **Step 1: Add the option declaration**

After Phase 2 the `packs = { ... }` block has `test-fixture`, `pr-support`, `dolt-hacks`. Append `workers`:

```nix
      workers.enable = lib.mkEnableOption ''
        pgii-pack-workers (rig-scoped generic worker pool — claims open beads
        with acceptance_criteria set; ambiguous work labeled `needs-foreman`).
        Auto-binds to every rig via [defaults.rig.imports.pgii-workers]; per-rig
        session concurrency is overridden via city.toml [[rigs.patches]].
      '';
```

Remove `workers` from the trailing "Real packs not yet added" comment.

- [x] **Step 2: Add to `enabledPacks`**

```nix
    ++ lib.optional cfg.packs.workers.enable {
      name = "pgii-workers";
      drv = pkgs.pgii-pack-workers;
    };
```

Note the gascity pack name is `pgii-workers` (not `pgii-pack-workers`).

- [x] **Step 3: Verify the module evaluates**

```bash
nix flake check 2>&1 | tail -15
```

Expected: pre-existing failures (`test-bash-scripts`, `test-update-locks-lib`, `treefmt-check`) only. Both `check-pgii-pack-workers-layout` (next task) and `test-pgii-packs-activation` should be green.

- [x] **Step 4: Commit**

```bash
git add home/programs/pgii-packs/default.nix
git commit -m "feat(pgii-packs): wire packs.workers option into HM module (rig-scope auto-derived)"
```

- [x] **Step 5: Tick off `- [ ]` boxes for Task 6.**

---

### Task 7: Add flake check for pgii-pack-workers layout

Sibling to Phase 2's `check-pgii-pack-dolt-hacks-layout`. Verifies the pack's $out shape AND that scope was correctly set in `.pack-meta.json`.

**Files:**

- Modify: `flake.nix`

- [x] **Step 1: Add the check**

Add as a sibling to the existing `check-pgii-pack-dolt-hacks-layout`:

```nix
            check-pgii-pack-workers-layout = pkgs.runCommand "check-pgii-pack-workers-layout"
              { nativeBuildInputs = [ pkgs.jq ]; } ''
                pack=${pkgs.pgii-pack-workers}
                test -f "$pack/pack.toml"                                    || { echo "missing pack.toml"; exit 1; }
                test -f "$pack/.pack-meta.json"                              || { echo "missing .pack-meta.json"; exit 1; }
                test "$(jq -r .scope "$pack/.pack-meta.json")" = "rig"       || { echo ".pack-meta.json scope != rig"; exit 1; }
                test -f "$pack/agents/worker/agent.toml"                     || { echo "missing agent.toml"; exit 1; }
                test -f "$pack/agents/worker/prompt.template.md"             || { echo "missing prompt.template.md"; exit 1; }
                test -x "$pack/agents/worker/scripts/bash-env.sh"            || { echo "bash-env.sh not exec"; exit 1; }
                grep -q '{{.Rig}}\|{{.RigRoot}}' "$pack/agents/worker/prompt.template.md" || { echo "go-template markers stripped"; exit 1; }
                ! find "$pack" -name "*.template" -not -name "*.template.md" | grep -q . || { echo "stale envsubst .template files"; exit 1; }
                ! grep -rnE '/Users/phillipg/gc/assets/imports' "$pack" >/dev/null 2>&1 || { echo "stale legacy assets paths"; exit 1; }
                touch $out
              '';
```

NOTE: The `find` filter `-not -name "*.template.md"` is important — `prompt.template.md` uses go-template syntax (`{{...}}`), not envsubst (`${...}`), and so doesn't get processed by mkPgiiPack. Its filename intentionally contains `.template`.

- [x] **Step 2: Run the check**

```bash
nix build ".#checks.$(nix eval --raw --impure --expr 'builtins.currentSystem').check-pgii-pack-workers-layout"
```

Expected: exit 0.

- [x] **Step 3: Run the full flake check set**

```bash
nix flake check 2>&1 | tail -10
```

Expected: only the pre-existing upstream failures remain.

- [x] **Step 4: Commit**

```bash
git add flake.nix
git commit -m "test(pgii-packs): flake check for pgii-pack-workers layout (incl. rig-scope meta)"
```

- [x] **Step 5: Tick off `- [ ]` boxes for Task 7.**

---

### Task 8: Bump nix-ziprecruiter flake.lock + enable pack (build-only)

Mirrors Phase 2 Task 8.

**Files:**

- Modify: `phillipg-nix-ziprecruiter/flake.lock`
- Modify: `phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix`

- [x] **Step 1: Push pending agent-support commits**

(Phase 2 Task 8 set the precedent: agent-support commits must be on `origin/main` before nix-ziprecruiter can resolve the new lock.)

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git log origin/main..HEAD --oneline
# If the local branch is ahead of origin, push it.
git push origin main
```

- [x] **Step 2: Update flake.lock in nix-ziprecruiter**

The flake input name (verified in Phase 2) is `phillipgreenii-agent-support` — not `phillipgreenii-nix-agent-support`.

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
git status --short  # verify clean tree
nix flake update phillipgreenii-agent-support 2>&1 | tail -5
```

Confirm the new rev:

```bash
grep -A 2 'phillipgreenii-agent-support' flake.lock | head -10
```

- [x] **Step 3: Edit `machines/phillipg-mbp-02/default.nix`**

After Phase 2 the `pgii = { ... }` block reads (the merged-attrset form statix preferred):

```nix
        pgii = {
          gascity.cities = [ "/Users/phillipg/gc" ];
          packs = {
            test-fixture.enable = false;
            pr-support.enable = true;
            dolt-hacks.enable = true;
          };
        };
```

Append the new key inside `packs`:

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

- [x] **Step 4: Build (do NOT apply)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
darwin-rebuild build --flake .#phillipg-mbp-02 2>&1 | tail -20
```

Expected: exit 0. Output should mention building `pgii-workers-0.1.0`.

- [x] **Step 5: Commit**

```bash
git diff --cached --name-only
git add flake.lock machines/phillipg-mbp-02/default.nix
git commit -m "$(cat <<'EOF'
config(phillipg-mbp-02): enable pgii.packs.workers (Phase 3 build, not yet applied)

Bumps phillipgreenii-agent-support lock to pick up pgii-pack-workers
(rig-scope, auto-binds to every rig via [defaults.rig.imports.pgii-workers]).
Apply happens in Phase 3 cutover (gc-xzh5 Task 9): atomic with removal of
the hand-written [defaults.rig.imports.pgii-workers] block from pack.toml
AND [rigs.imports.pgii-workers] from city.toml's ziprecruiter section.
Refs: gc-xzh5.
EOF
)"
```

- [x] **Step 6: Tick off `- [ ]` boxes for Task 8.**

---

### Task 9: Cutover — remove legacy blocks + apply

Two file edits in `/Users/phillipg/gc/`:

1. `pack.toml`: remove `[defaults.rig.imports.pgii-workers]` block (lines 12-17 incl. the explanatory comment).
2. `city.toml`: remove `[rigs.imports]` + `[rigs.imports.pgii-workers]` (lines 28-30 — inside the `[[rigs]] ziprecruiter` block). **KEEP** the `[[rigs.patches]] agent="worker" max_active_sessions=3` block — still required for the per-rig override.

`zn-self-apply` is agent-restricted; the human runs it.

**Files:**

- Modify: `/Users/phillipg/gc/pack.toml`
- Modify: `/Users/phillipg/gc/city.toml`

- [x] **Step 1: Backup both files**

```bash
cp /Users/phillipg/gc/pack.toml /Users/phillipg/gc/pack.toml.pre-phase3-cutover
cp /Users/phillipg/gc/city.toml /Users/phillipg/gc/city.toml.pre-phase3-cutover
```

- [x] **Step 2: Edit pack.toml — remove `[defaults.rig.imports.pgii-workers]` block**

The block is (currently lines ~12-17):

```
# pgii-workers ships rig-scope agents (currently just `worker`). It's
# declared under [defaults.rig.imports] so every rig gets its own
# materialization. The pack contains only rig-scope content, so there's
# no city-level expansion to worry about.
[defaults.rig.imports.pgii-workers]
source = "./assets/imports/pgii-workers"
```

Use Edit tool with the exact content. Replace with an empty string (delete the whole comment+block).

Verify:

```bash
grep -nE '(pgii-workers|defaults\.rig\.imports)' /Users/phillipg/gc/pack.toml \
  && { echo "FAIL: stale pgii-workers ref"; exit 1; } \
  || echo "ok: pack.toml cleaned"
```

Expected: `ok: pack.toml cleaned`.

- [x] **Step 3: Edit city.toml — remove `[rigs.imports]` + `[rigs.imports.pgii-workers]`**

The ziprecruiter rig block currently looks like:

```
[[rigs]]
name = "ziprecruiter"
prefix = "zr"
[rigs.imports]
[rigs.imports.pgii-workers]
source = "./assets/imports/pgii-workers"

[[rigs.patches]]
agent = "worker"
max_active_sessions = 3
```

After edit it should be:

```
[[rigs]]
name = "ziprecruiter"
prefix = "zr"

[[rigs.patches]]
agent = "worker"
max_active_sessions = 3
```

Use Edit tool to remove ONLY the `[rigs.imports]` (the empty parent declaration) and `[rigs.imports.pgii-workers]` block (3 lines).

Verify:

```bash
grep -nE '(pgii-workers|^\[rigs\.imports)' /Users/phillipg/gc/city.toml \
  && { echo "FAIL: stale pgii-workers ref or stray [rigs.imports] in city.toml"; exit 1; } \
  || echo "ok: city.toml cleaned"

# Also confirm [[rigs.patches]] is still present
grep -nE '\[\[rigs\.patches\]\]' /Users/phillipg/gc/city.toml | head -3
```

Expected: `ok: city.toml cleaned` + at least one `[[rigs.patches]]` match.

- [x] **Step 4: Confirm both files are still valid TOML**

```bash
gc config show 2>&1 | head -10
```

Should print config without TOML parse errors.

- [x] **Step 5: Halt and request apply**

`zn-self-apply` is agent-restricted. Report status `BLOCKED — awaiting human zn-self-apply` with:

- pack.toml + city.toml diff summary
- backup locations
- the command to run: `zn-self-apply`
- rollback command if needed: `cp /Users/phillipg/gc/pack.toml.pre-phase3-cutover /Users/phillipg/gc/pack.toml; cp /Users/phillipg/gc/city.toml.pre-phase3-cutover /Users/phillipg/gc/city.toml; gc supervisor reload`

(Resume the remaining steps after the human confirms `applied`.)

- [x] **Step 6: Verify managed block written to pack.toml**

```bash
grep -A 4 'BEGIN pgii-pack:pgii-workers' /Users/phillipg/gc/pack.toml
```

Expected:

```
# BEGIN pgii-pack:pgii-workers (managed)
[defaults.rig.imports.pgii-workers]
source = "/nix/store/<hash>-pgii-workers-0.1.0"
export = true
# END pgii-pack:pgii-workers (managed)
```

**Critically: the block header must be `[defaults.rig.imports.pgii-workers]`, NOT `[imports.pgii-workers]`.** If activation wrote the wrong shape, Task 2 is buggy — investigate before declaring success.

- [x] **Step 7: Verify supervisor reload was clean**

```bash
gc supervisor reload
sleep 3
tail -30 ~/.gc/supervisor.log | grep -E '(reload|pgii-workers|worker)' | grep -v 'session beads:' | tail -10
```

Expected: `Config reloaded: <N> agents, <M> rigs` line. No new errors mentioning pgii-workers.

- [x] **Step 8: Verify worker template is bound to every rig**

```bash
gc config show 2>&1 | grep 'default_rig_includes'
```

Expected: includes `/nix/store/<hash>-pgii-workers-0.1.0` in the array (replacing the legacy `./assets/imports/pgii-workers` path).

```bash
gc session list 2>&1 | grep worker | head -5
```

Existing worker sessions persist; new sessions materialize on-demand when work arrives.

- [x] **Step 9: Tick off `- [ ]` boxes for Task 9.**

---

### Task 10: Retire legacy tree + close bead

Same shape as Phase 2 Task 10.

- [x] **Step 1: Confirm post-cutover health**

Workers are pool agents that materialize on_demand. There's no 5-min cooldown order to confirm "is it firing" — instead confirm:

```bash
# (a) effective config sees the nix-store pack
gc config show 2>&1 | grep -E 'pgii-workers|/nix/store/[a-z0-9]*-pgii-workers' | head -3

# (b) ziprecruiter rig still patched at max_active_sessions=3
gc config show 2>&1 | grep -B 1 -A 2 'max_active_sessions' | grep -A 2 -B 1 'worker' | head -8

# (c) no supervisor errors naming pgii-workers in the last 5 min
since=$(date -v-5M -u '+%Y-%m-%d %H:%M' 2>/dev/null || date -u '+%Y-%m-%d %H:%M' --date='-5 minutes')
awk -v cutoff="$since" '$1" "$2 >= cutoff' ~/.gc/supervisor.log | grep -E '(pgii-workers|worker)' | grep -iE '(error|fail|warn)' | head -5
```

- [x] **Step 2: Delete the legacy tree**

```bash
cd /Users/phillipg/gc
git rm -rf assets/imports/pgii-workers
```

- [x] **Step 3: Audit remaining references**

```bash
grep -rn 'assets/imports/pgii-workers' /Users/phillipg/gc/ \
  --include='*.md' --include='*.toml' --include='*.sh' \
  --exclude-dir=.git --exclude-dir=.gc --exclude-dir=archive \
  || echo "ok: no remaining references"
```

Likely candidates that may remain: CLAUDE.md, HACKS.md (pgii-workers isn't a HACK, but might be mentioned), or docs/superpowers/specs/plans.

For each match, decide: update path (if it's a live reference like HACKS.md path lists) or leave (if it's a historical doc).

- [x] **Step 4: Commit retirement**

```bash
cd /Users/phillipg/gc
git diff --cached --stat | tail -10
git commit -m "$(cat <<'EOF'
feat(pgii-packs): Phase 3 cutover — retire legacy pgii-workers tree

pgii-pack-workers now ships from phillipgreenii-nix-agent-support
(rig-scope, auto-binds to every rig via [defaults.rig.imports.pgii-workers]).
The managed block in pack.toml points at the nix-store derivation.
city.toml's redundant [rigs.imports.pgii-workers] inside the ziprecruiter
rig section is removed; [[rigs.patches]] max_active_sessions=3 stays.

Verified post-apply: managed block shape is [defaults.rig.imports.<name>]
(scope correctly auto-derived from .pack-meta.json), supervisor reload
clean, default_rig_includes points at /nix/store path.

Refs: gc-xzh5.
EOF
)"
```

- [x] **Step 5: Clean up backups**

```bash
rm /Users/phillipg/gc/pack.toml.pre-phase3-cutover
rm /Users/phillipg/gc/city.toml.pre-phase3-cutover
```

- [x] **Step 6: Close the bead**

```bash
gc bd close gc-xzh5 --reason="Phase 3 build + cutover complete. pgii-pack-workers ships from phillipgreenii-nix-agent-support (rig-scope, auto-binds every rig). Phase 0 machinery extended: mkPgiiPack infers scope from pack.toml's [[named_session]] .scope field and embeds in .pack-meta.json; activation.sh reads it and switches between [imports.<name>] and [defaults.rig.imports.<name>] block shapes. New rig-scope-write bats test covers the new behavior. Legacy ~/gc/assets/imports/pgii-workers/ retired; redundant [rigs.imports.pgii-workers] removed from city.toml's ziprecruiter section. [[rigs.patches]] max_active_sessions=3 retained. Plan: docs/superpowers/plans/2026-05-28-pgii-packs-phase3-workers.md. Unblocks gc-0gj0 (Phase 4 pgii-gastown)."
```

- [x] **Step 7: Tick off `- [ ]` boxes for Task 10.**

---

## Risks and unknowns

1. **`agent.toml` may reference absolute legacy paths.** If `work_query` or similar fields use `/Users/phillipg/gc/assets/imports/pgii-workers/...`, the pack needs an additional substitution mechanism. Task 5 Step 2 catches this; if it fires, the resolution is to extend mkPgiiPack to expose `PACK_ROOT = $out` as an envsubst variable and convert `agent.toml` → `agent.toml.template`. (Phase 1 pgii-pr-support's agent TOMLs did NOT need this, so the precedent is "verbatim works".)

2. **`bash-env.sh` executable bit.** mkPgiiPack only `chmod +x`s `scripts/*.sh` at the pack root, not nested. If `bash-env.sh` ends up non-executable in $out, either extend mkPgiiPack's chmod loop OR ensure the legacy source has +x already (cp preserves mode).

3. **`gc supervisor reload` after city.toml edits.** Per CLAUDE.md: "back-to-back reloads cause system-pack orders to go briefly silent." The cutover involves two reloads (one explicit, one from zn-self-apply activation). Acceptable — orders idle for ~2-5 min then resettle.

4. **The pgii-workers session metadata persists.** Even after cutover, `gc session list` may show a session bound to the OLD legacy path (template name based on the pack derivation hash). The OLD session is asleep + drained; new sessions get materialized from the nix-store template on next work arrival. No action needed; verify by inspecting session template names after the next worker fires.

5. **`gc agent` removed `list` subcommand.** Per investigation, `gc agent list` doesn't exist; runtime listing moved to `gc session` / `gc runtime`. Plan uses `gc session list` everywhere this might be needed.

---

## Self-review checklist

- [ ] **Spec coverage:** Phase 3 spec row → tasks. Pack body verbatim copy (Task 5). Rig-scope determination (Tasks 1-3, the activation-script extension). Cutover deletes legacy pack.toml block + city.toml rig-imports block (Task 9). Retirement (Task 10).
- [ ] **Placeholder scan:** Search for `TBD`, `TODO`, `fill in`. None should remain.
- [ ] **Type consistency:** Gascity pack name is `pgii-workers` (matches `[pack] name` in pack.toml + `[defaults.rig.imports.<name>]` block); nix package name is `pgii-pack-workers` (flake overlay + machine config). HM short option is `packs.workers`.
- [ ] **All `git commit` commands include the right files (no stray `git add .`).**
- [ ] **All `nix build` / `nix flake check` commands target the correct attribute paths.**

---

## References

- **Spec (this phase):** `docs/superpowers/specs/2026-05-26-pgii-packs-migration-design.md` (Phase 3 row + "Open items" section)
- **Phase 2 plan (pattern source):** `docs/superpowers/plans/2026-05-28-pgii-packs-phase2-dolt-hacks.md`
- **Legacy pack source:** `/Users/phillipg/gc/assets/imports/pgii-workers/`
- **Investigation evidence:** `gc config show` produces `default_rig_includes = [...]` — the canonical "auto-bind to every rig" array, populated by `[defaults.rig.imports.*]` only. `[imports.<name>]` does NOT populate it. Confirmed empirically 2026-05-28.
- **mkPgiiPack:** `lib/mkPgiiPack.nix` (extended in Task 1)
- **Activation script:** `home/programs/pgii-packs/activation.sh` (extended in Task 2)
- **gc-xzh5 bead:** Phase 3 work tracking bead.
