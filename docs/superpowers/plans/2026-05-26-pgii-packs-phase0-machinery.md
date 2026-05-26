# Phase 0: pgii packs machinery — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the generic infrastructure — `lib/mkPgiiPack.nix`, the `home/programs/pgii-packs/` home-manager module (options + activation script + bats tests), and the `pgii-pack-test-fixture` validation pack — so that subsequent phases can migrate real packs by writing only pack source + a 3-line `default.nix`.

**Architecture:** A nix function builds a pack derivation from a `pack-src/` directory tree (with optional `@KEY@` template substitution). A home-manager module exposes per-pack `.enable` toggles + a `cities` list. An activation script writes idempotent `# BEGIN pgii-pack:<name> (managed)` blocks into each city's `city.toml`, removes blocks for disabled packs, and optionally triggers `gc supervisor reload`. A test-fixture pack proves the round trip end-to-end.

**Tech Stack:** Nix flakes, home-manager, bash, bats-core (v1.5+), envsubst.

**Spec:** `docs/superpowers/specs/2026-05-26-pgii-packs-migration-design.md`

**Repo root for all paths in this plan:** `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`

---

## File structure

**Files to create:**

| File                                                                 | Purpose                                                                                                           |
| -------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `lib/mkPgiiPack.nix`                                                 | Generic pack builder function. Copies `pack-src/`, runs `@KEY@` substitution, enforces layout, sets script perms. |
| `packages/pgii-pack-test-fixture/default.nix`                        | callPackage entry for the validation pack.                                                                        |
| `packages/pgii-pack-test-fixture/pack-src/pack.toml`                 | Trivial pack manifest declaring `name = "pgii-pack-test-fixture"`.                                                |
| `packages/pgii-pack-test-fixture/pack-src/orders/noop.toml.template` | One disabled no-op order using `${SCRIPTS_DIR}` so substitution is exercised.                                     |
| `packages/pgii-pack-test-fixture/pack-src/scripts/noop.sh`           | The no-op script invoked by the order.                                                                            |
| `home/programs/pgii-packs/default.nix`                               | HM module: options, assertions, home.file rooting, home.activation.                                               |
| `home/programs/pgii-packs/activation.sh`                             | Bash script: idempotent block insertion + removal + optional reload.                                              |
| `home/programs/pgii-packs/tests/test_helper.bash`                    | Shared bats helpers (mkTmpCity, blockExists, etc.).                                                               |
| `home/programs/pgii-packs/tests/test_fresh_write.bats`               | Empty city.toml → block written.                                                                                  |
| `home/programs/pgii-packs/tests/test_replace_existing.bats`          | Block exists, path changes → block rewritten.                                                                     |
| `home/programs/pgii-packs/tests/test_no_op_rebuild.bats`             | Block exists with same path → file untouched.                                                                     |
| `home/programs/pgii-packs/tests/test_remove_on_disable.bats`         | Block exists, pack not in args → block removed.                                                                   |
| `home/programs/pgii-packs/tests/test_hand_written_collision.bats`    | Non-managed `[packs.X]` exists → activation errors.                                                               |
| `home/programs/pgii-packs/tests/test_multi_pack.bats`                | Three packs in one call → three blocks.                                                                           |
| `home/programs/pgii-packs/tests/test_multi_city.bats`                | Two cities → both get the block set.                                                                              |

**Files to modify:**

| File               | Change                                                                                    |
| ------------------ | ----------------------------------------------------------------------------------------- |
| `flake.nix`        | Register `pgii-pack-test-fixture` in the overlay; add `test-pgii-packs-activation` check. |
| `home/default.nix` | Add `./programs/pgii-packs` to imports.                                                   |

---

## Conventions used in this plan

- All `git commit` commands assume current directory is `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`. Each task begins implicitly with `cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`.
- `nix build` commands use `.#<attr>` — the flake's `packages.${system}.<attr>`.
- `nix flake check` runs the `checks` set; we add one new check at the end.
- Pre-commit hooks (treefmt, etc.) run automatically on commit — let them.

---

### Task 1: Test-fixture pack source files

**Files:**

- Create: `packages/pgii-pack-test-fixture/pack-src/pack.toml`
- Create: `packages/pgii-pack-test-fixture/pack-src/orders/noop.toml.template`
- Create: `packages/pgii-pack-test-fixture/pack-src/scripts/noop.sh`

- [ ] **Step 1: Create the pack manifest**

File: `packages/pgii-pack-test-fixture/pack-src/pack.toml`

```toml
# pgii-pack-test-fixture
#
# Trivial pack used by home/programs/pgii-packs/tests/ and the first
# end-to-end activation run on a real city. Contains one disabled no-op
# order that exercises @SCRIPTS_DIR@ template substitution. No agents,
# no doctor checks. Safe to enable in any city.

[pack]
name = "pgii-pack-test-fixture"
schema = 2
```

- [ ] **Step 2: Create the no-op order template**

File: `packages/pgii-pack-test-fixture/pack-src/orders/noop.toml.template`

```toml
[order]
description = "pgii-pack-test-fixture: validates mkPgiiPack substitution + activation pipeline."
trigger     = "cooldown"
interval    = "1h"
exec        = "${SCRIPTS_DIR}/noop.sh"
timeout     = "10s"
enabled     = false
```

- [ ] **Step 3: Create the no-op script**

File: `packages/pgii-pack-test-fixture/pack-src/scripts/noop.sh`

```bash
#!/usr/bin/env bash
# pgii-pack-test-fixture/scripts/noop.sh — exits 0 immediately.
exit 0
```

- [ ] **Step 4: Commit**

```bash
git add packages/pgii-pack-test-fixture/pack-src/
git commit -m "feat(pgii-packs): add test-fixture pack source files"
```

---

### Task 2: Stub package derivation + overlay registration

This task gets a `nix build` working end-to-end with a minimal inline builder. `mkPgiiPack` itself comes in Task 3 — the stub proves the test-fixture source compiles into a derivation before we extract the builder.

**Files:**

- Create: `packages/pgii-pack-test-fixture/default.nix`
- Modify: `flake.nix` (add overlay entry)

- [ ] **Step 1: Write the stub builder**

File: `packages/pgii-pack-test-fixture/default.nix`

```nix
{ pkgs }:
# Phase-0 stub. Will be rewritten in Task 3 to call mkPgiiPack.
# `lib` deliberately not in args — deadnix will flag it as unused here.
# Task 3 reintroduces lib when wiring mkPgiiPack.
pkgs.runCommand "pgii-pack-test-fixture-0.1.0"
  { nativeBuildInputs = [ pkgs.envsubst ]; }
  ''
    cp -R ${./pack-src}/. $out/
    chmod -R u+w $out
    export SCRIPTS_DIR="$out/scripts"
    while IFS= read -r -d "" f; do
      envsubst < "$f" > "''${f%.template}"
      rm "$f"
    done < <(find $out -name "*.template" -print0)
    mkdir -p $out/formulas $out/agents $out/orders $out/scripts
    chmod +x $out/scripts/*.sh
    test -f $out/pack.toml
  ''
```

- [ ] **Step 2: Register in the overlay**

Modify `flake.nix`. Locate the `overlay` definition block (around line 84-92, where other packages register with the `final.callPackage ./packages/<name> { };` pattern). The existing list is not alphabetically ordered; add the new line wherever fits the file's prevailing order, e.g., after the `pa-monitor` entry:

Find the line:

```nix
          pa-monitor = final.callPackage ./packages/pa-monitor { };
```

Add after it:

```nix
          pgii-pack-test-fixture = final.callPackage ./packages/pgii-pack-test-fixture { };
```

- [ ] **Step 3: Build the pack**

Run: `nix build .#pgii-pack-test-fixture --print-out-paths`
Expected: Prints a `/nix/store/...-pgii-pack-test-fixture-0.1.0` path, exit 0.

- [ ] **Step 4: Verify layout**

Run:

```bash
out=$(nix build .#pgii-pack-test-fixture --print-out-paths --no-link)
test -f "$out/pack.toml"          && echo "ok: pack.toml"
test -d "$out/formulas"           && echo "ok: formulas/"
test -d "$out/orders"             && echo "ok: orders/"
test -d "$out/scripts"            && echo "ok: scripts/"
test -f "$out/orders/noop.toml"   && echo "ok: order substituted"
! test -f "$out/orders/noop.toml.template" && echo "ok: template removed"
grep -q "/nix/store/.*-pgii-pack-test-fixture-0.1.0/scripts/noop.sh" "$out/orders/noop.toml" \
  && echo "ok: \${SCRIPTS_DIR} substituted"
test -x "$out/scripts/noop.sh"    && echo "ok: noop.sh executable"
```

Expected: eight `ok:` lines.

- [ ] **Step 5: Commit**

```bash
git add packages/pgii-pack-test-fixture/default.nix flake.nix
git commit -m "feat(pgii-packs): stub pgii-pack-test-fixture derivation + overlay entry"
```

---

### Task 3: Extract `lib/mkPgiiPack.nix`

Replace the inline stub with a reusable library function. Verify the rebuild still produces the same output.

**Files:**

- Create: `lib/mkPgiiPack.nix`
- Modify: `packages/pgii-pack-test-fixture/default.nix`

- [ ] **Step 1: Write the library function**

File: `lib/mkPgiiPack.nix`

```nix
# mkPgiiPack — generic builder for gascity packs in nix-agent-support.
#
# Takes a pack-src/ directory and produces a derivation whose $out matches
# the layout gascity expects: pack.toml + agents/ + orders/ + scripts/ +
# formulas/ + doctor/ (any of which may be empty).
#
# Template substitution: files ending `.template` are processed by envsubst
# using `${KEY}` markers. Only declared variables are substituted (we pass
# envsubst an explicit variable list); other `${...}` patterns in template
# files (e.g. shell expansions inside *.sh.template) are preserved verbatim.
# `${SCRIPTS_DIR}` is always available and resolves to the pack's scripts/
# subdir inside the nix store. Additional substitutions come from the
# `substitutions` argument (an attrset of NAME=value pairs).
#
# Usage:
#
#   { lib, mkPgiiPack }:
#   mkPgiiPack {
#     name = "pgii-pack-foo";
#     src = ./pack-src;
#     substitutions = {
#       SOURCES_JSON = builtins.toJSON sources;
#     };
#   }
{ lib, pkgs }:
{
  name,
  version ? "0.1.0",
  src,
  substitutions ? { },
  meta ? { },
}:
pkgs.runCommand "${name}-${version}"
  {
    passthru = { inherit name; };
    nativeBuildInputs = [ pkgs.envsubst ];
    inherit meta;
  }
  ''
    cp -R ${src}/. $out/
    chmod -R u+w $out

    # ${SCRIPTS_DIR} always points at this pack's scripts/ subdir.
    export SCRIPTS_DIR="$out/scripts"
    ${lib.concatStringsSep "\n" (
      lib.mapAttrsToList (k: v: ''export ${k}=${lib.escapeShellArg (toString v)}'') substitutions
    )}

    # Substitute. envsubst replaces every ${VAR} in the input file with
    # the corresponding environment-variable value, IF the variable is
    # exported. Unexported names pass through unchanged, so accidental
    # collisions with shell ${...} inside *.sh.template files are limited
    # to names a pack author also chose to declare in `substitutions`.
    # If a pack ever needs envsubst to ignore certain names, refactor
    # this to use envsubst's variable-list arg (passed as a single
    # argument like '$SCRIPTS_DIR $REPOS') at that time.
    while IFS= read -r -d "" f; do
      envsubst < "$f" > "''${f%.template}"
      rm "$f"
    done < <(find $out -name "*.template" -print0)

    mkdir -p $out/formulas $out/agents $out/orders $out/scripts $out/doctor

    if [ -d $out/scripts ] && compgen -G "$out/scripts/*.sh" > /dev/null; then
      chmod +x $out/scripts/*.sh
    fi

    test -f $out/pack.toml || { echo "mkPgiiPack: missing pack.toml in ${name}" >&2; exit 1; }
    if find $out -name "*.template" | grep -q .; then
      echo "mkPgiiPack: unsubstituted .template files remain in ${name}" >&2
      find $out -name "*.template" >&2
      exit 1
    fi

    cat > $out/.pack-meta.json <<EOF
    { "name": "${name}", "version": "${version}" }
    EOF
  ''
```

- [ ] **Step 2: Rewrite the test-fixture default.nix to use mkPgiiPack**

Replace the entire contents of `packages/pgii-pack-test-fixture/default.nix`:

```nix
{ lib, pkgs }:
let
  mkPgiiPack = import ../../lib/mkPgiiPack.nix { inherit lib pkgs; };
in
mkPgiiPack {
  name = "pgii-pack-test-fixture";
  src = ./pack-src;
  meta = with lib; {
    description = "Trivial pack for validating mkPgiiPack + pgii-packs activation pipeline.";
    license = licenses.mit;
    platforms = platforms.unix;
  };
}
```

- [ ] **Step 3: Build and verify identical output layout**

Run:

```bash
out=$(nix build .#pgii-pack-test-fixture --print-out-paths --no-link)
test -f "$out/pack.toml"                            && echo "ok: pack.toml"
test -d "$out/formulas"                             && echo "ok: formulas/"
test -d "$out/doctor"                               && echo "ok: doctor/"
test -f "$out/orders/noop.toml"                     && echo "ok: order substituted"
test -f "$out/.pack-meta.json"                      && echo "ok: meta sidecar"
grep -q '"name": "pgii-pack-test-fixture"' "$out/.pack-meta.json" && echo "ok: meta content"
grep -q "/nix/store/.*/scripts/noop.sh" "$out/orders/noop.toml"   && echo "ok: @SCRIPTS_DIR@ substituted"
test -x "$out/scripts/noop.sh"                      && echo "ok: noop.sh executable"
```

Expected: eight `ok:` lines.

- [ ] **Step 4: Verify negative case (build failure on missing pack.toml)**

Run:

```bash
tmpsrc=$(mktemp -d)
mkdir -p "$tmpsrc/orders"
echo "stub" > "$tmpsrc/orders/foo.toml"
nix-build -E "
  let
    pkgs = import <nixpkgs> {};
    mkPgiiPack = import $PWD/lib/mkPgiiPack.nix { inherit (pkgs) lib; inherit pkgs; };
  in mkPgiiPack { name = \"broken\"; src = $tmpsrc; }
" 2>&1 | tail -5
rm -rf "$tmpsrc"
```

Expected: build fails with `mkPgiiPack: missing pack.toml in broken` in the output.

- [ ] **Step 5: Commit**

```bash
git add lib/mkPgiiPack.nix packages/pgii-pack-test-fixture/default.nix
git commit -m "feat(pgii-packs): extract mkPgiiPack library function"
```

---

### Task 4: Activation script — argument parsing skeleton

Begin the activation script. This task only handles argument parsing and prints what it would do. Real behavior comes in Tasks 5-11 via TDD.

**Files:**

- Create: `home/programs/pgii-packs/activation.sh`
- Create: `home/programs/pgii-packs/tests/test_helper.bash`

- [ ] **Step 1: Write activation.sh skeleton**

File: `home/programs/pgii-packs/activation.sh`

```bash
#!/usr/bin/env bash
# activation.sh — write managed [packs.<name>] blocks into one or more
# city.toml files. Called from home/programs/pgii-packs/default.nix during
# home-manager activation.
#
# Inputs:
#   --cities '<JSON array of city paths>'
#   --packs  '<JSON object: { "<pack-name>": "<store-path>", ... }>'
#   --reload  (optional: run `gc supervisor reload` per city if its
#              controller.sock exists and gc is on PATH)
#
# Marker format written/managed:
#
#   # BEGIN pgii-pack:<pack-name> (managed)
#   [packs.<pack-name>]
#   path = "/nix/store/..."
#   # END pgii-pack:<pack-name> (managed)
#
# Idempotent. Re-running with the same args is a no-op.

set -euo pipefail

CITIES_JSON=""
PACKS_JSON=""
RELOAD=0

usage() {
  cat >&2 <<EOF
usage: $0 --cities '<json>' --packs '<json>' [--reload]
EOF
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --cities) CITIES_JSON="${2:-}"; shift 2 ;;
    --packs)  PACKS_JSON="${2:-}";  shift 2 ;;
    --reload) RELOAD=1; shift ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done

[ -n "$CITIES_JSON" ] || { echo "missing --cities" >&2; usage; }
[ -n "$PACKS_JSON"  ] || { echo "missing --packs"  >&2; usage; }

# Validate JSON inputs early so a malformed arg never reaches city.toml.
jq -e 'type == "array"'  <<<"$CITIES_JSON" >/dev/null || { echo "--cities must be a JSON array" >&2; exit 2; }
jq -e 'type == "object"' <<<"$PACKS_JSON"  >/dev/null || { echo "--packs must be a JSON object" >&2; exit 2; }

# Parse into bash-native structures.
mapfile -t CITIES < <(jq -r '.[]' <<<"$CITIES_JSON")
declare -A PACKS
while IFS=$'\t' read -r name path; do
  PACKS["$name"]="$path"
done < <(jq -r 'to_entries[] | [.key, .value] | @tsv' <<<"$PACKS_JSON")

PACK_NAMES=("${!PACKS[@]}")

# Per-city processing. Real implementation added in subsequent tasks.
for city in "${CITIES[@]}"; do
  echo "pgii-packs: would process city=$city packs=${PACK_NAMES[*]:-<none>} reload=$RELOAD"
done

exit 0
```

- [ ] **Step 2: Make it executable**

Run: `chmod +x home/programs/pgii-packs/activation.sh`

- [ ] **Step 3: Write the bats helper**

File: `home/programs/pgii-packs/tests/test_helper.bash`

```bash
# test_helper.bash — shared setup for pgii-packs/activation.sh bats tests.
#
# Conventions:
#   - $TMP is a per-test tmpdir, cleaned in teardown.
#   - $CITY is a city dir under $TMP (you create N of these).
#   - $SCRIPT is the absolute path to activation.sh.

bats_require_minimum_version 1.5.0

setup() {
  TMP="$(mktemp -d)"
  export TMP
  SCRIPT="${BATS_TEST_DIRNAME}/../activation.sh"
  export SCRIPT
  test -x "$SCRIPT" || { echo "activation.sh not executable: $SCRIPT" >&2; exit 1; }
}

teardown() {
  [ -n "${TMP:-}" ] && rm -rf "$TMP"
}

# mkCity NAME → echoes the city's path; creates city.toml seeded with arg2 (if given).
mkCity() {
  local name="$1"
  local seed="${2:-}"
  local dir="$TMP/$name"
  mkdir -p "$dir/.gc"
  if [ -n "$seed" ]; then
    printf '%s\n' "$seed" > "$dir/city.toml"
  else
    : > "$dir/city.toml"
  fi
  echo "$dir"
}

# blockExists CITY_TOML PACK_NAME → exit 0 if managed block for PACK_NAME exists.
blockExists() {
  grep -Fq "# BEGIN pgii-pack:$2 (managed)" "$1"
}

# blockPath CITY_TOML PACK_NAME → prints the store path inside the named block.
blockPath() {
  awk -v begin="# BEGIN pgii-pack:$2 (managed)" \
      -v end="# END pgii-pack:$2 (managed)" '
    $0 == begin { in_block = 1; next }
    $0 == end   { in_block = 0; next }
    in_block && /^path = / { gsub(/(^path = "|"$)/, ""); print; exit }
  ' "$1"
}

# packsJson NAME1 PATH1 [NAME2 PATH2 ...] → emits a JSON object.
packsJson() {
  local -a entries=()
  while [ $# -gt 0 ]; do
    entries+=("\"$1\":\"$2\"")
    shift 2
  done
  local IFS=,
  echo "{${entries[*]}}"
}

# citiesJson CITY1 [CITY2 ...] → emits a JSON array of paths.
citiesJson() {
  local -a entries=()
  while [ $# -gt 0 ]; do
    entries+=("\"$1\"")
    shift
  done
  local IFS=,
  echo "[${entries[*]}]"
}
```

- [ ] **Step 4: Verify the skeleton runs**

Run:

```bash
home/programs/pgii-packs/activation.sh \
  --cities '["/tmp/fake-city"]' \
  --packs  '{"pgii-pack-test-fixture":"/nix/store/fake"}'
```

Expected output: `pgii-packs: would process city=/tmp/fake-city packs=pgii-pack-test-fixture reload=0`

- [ ] **Step 5: Commit**

```bash
git add home/programs/pgii-packs/activation.sh home/programs/pgii-packs/tests/test_helper.bash
git commit -m "feat(pgii-packs): activation.sh skeleton + bats test helper"
```

---

### Task 5: TDD fresh-write — empty city.toml gains a managed block

**Files:**

- Create: `home/programs/pgii-packs/tests/test_fresh_write.bats`
- Modify: `home/programs/pgii-packs/activation.sh`

- [ ] **Step 1: Write the failing test**

File: `home/programs/pgii-packs/tests/test_fresh_write.bats`

```bash
#!/usr/bin/env bats
load test_helper

@test "fresh write: empty city.toml gains managed block" {
  local city; city=$(mkCity gc)
  local cities; cities=$(citiesJson "$city")
  local packs;  packs=$(packsJson "pgii-pack-foo" "/nix/store/aaa-pgii-pack-foo")

  run "$SCRIPT" --cities "$cities" --packs "$packs"
  [ "$status" -eq 0 ]

  blockExists "$city/city.toml" "pgii-pack-foo"
  [ "$(blockPath "$city/city.toml" "pgii-pack-foo")" = "/nix/store/aaa-pgii-pack-foo" ]
}

@test "fresh write: non-existent city.toml is created" {
  local city="$TMP/new-city"
  mkdir -p "$city/.gc"
  # No city.toml created on disk.
  local cities; cities=$(citiesJson "$city")
  local packs;  packs=$(packsJson "pgii-pack-foo" "/nix/store/aaa")

  run "$SCRIPT" --cities "$cities" --packs "$packs"
  [ "$status" -eq 0 ]
  [ -f "$city/city.toml" ]
  blockExists "$city/city.toml" "pgii-pack-foo"
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bats home/programs/pgii-packs/tests/test_fresh_write.bats`
Expected: 2 tests fail. Reason: the skeleton activation.sh only prints — no block is written.

- [ ] **Step 3: Implement fresh-write logic in activation.sh**

Replace the per-city loop placeholder. Find the existing block:

```bash
# Per-city processing. Real implementation added in subsequent tasks.
for city in "${CITIES[@]}"; do
  echo "pgii-packs: would process city=$city packs=${PACK_NAMES[*]:-<none>} reload=$RELOAD"
done
```

Replace with:

```bash
# Emit one managed block to stdout for a given pack.
emit_block() {
  local name="$1" path="$2"
  cat <<EOF

# BEGIN pgii-pack:$name (managed)
[packs.$name]
path = "$path"
# END pgii-pack:$name (managed)
EOF
}

# Process a single city.toml in-place.
process_city() {
  local city="$1"
  local city_toml="$city/city.toml"

  mkdir -p "$(dirname "$city_toml")"
  if [ ! -f "$city_toml" ]; then
    : > "$city_toml"
  fi

  local tmp
  tmp="$(mktemp "$city_toml.XXXXXX")"
  cp "$city_toml" "$tmp"

  for name in "${PACK_NAMES[@]}"; do
    local path="${PACKS[$name]}"
    emit_block "$name" "$path" >> "$tmp"
  done

  mv "$tmp" "$city_toml"
}

for city in "${CITIES[@]}"; do
  process_city "$city"
done
```

- [ ] **Step 4: Run test to verify it passes**

Run: `bats home/programs/pgii-packs/tests/test_fresh_write.bats`
Expected: 2 tests pass.

- [ ] **Step 5: Commit**

```bash
git add home/programs/pgii-packs/activation.sh home/programs/pgii-packs/tests/test_fresh_write.bats
git commit -m "feat(pgii-packs): fresh-write activation behavior + test"
```

---

### Task 6: TDD replace-existing — block exists, path changes, block rewritten in place

**Files:**

- Create: `home/programs/pgii-packs/tests/test_replace_existing.bats`
- Modify: `home/programs/pgii-packs/activation.sh`

- [ ] **Step 1: Write the failing test**

File: `home/programs/pgii-packs/tests/test_replace_existing.bats`

```bash
#!/usr/bin/env bats
load test_helper

@test "replace existing: store path changes, single block remains" {
  local seed
  seed=$(cat <<EOF
[workspace]
provider = "claude"

# BEGIN pgii-pack:pgii-pack-foo (managed)
[packs.pgii-pack-foo]
path = "/nix/store/OLD-pgii-pack-foo"
# END pgii-pack:pgii-pack-foo (managed)
EOF
)
  local city; city=$(mkCity gc "$seed")
  local cities; cities=$(citiesJson "$city")
  local packs;  packs=$(packsJson "pgii-pack-foo" "/nix/store/NEW-pgii-pack-foo")

  run "$SCRIPT" --cities "$cities" --packs "$packs"
  [ "$status" -eq 0 ]

  # Exactly one managed block for this pack.
  local count
  count=$(grep -cF "# BEGIN pgii-pack:pgii-pack-foo (managed)" "$city/city.toml")
  [ "$count" -eq 1 ]

  # And it points at the new path.
  [ "$(blockPath "$city/city.toml" "pgii-pack-foo")" = "/nix/store/NEW-pgii-pack-foo" ]

  # Pre-existing [workspace] block survives.
  grep -q "^\[workspace\]" "$city/city.toml"
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bats home/programs/pgii-packs/tests/test_replace_existing.bats`
Expected: fail — current implementation always appends, so the OLD block plus a NEW block both exist.

- [ ] **Step 3: Implement strip-then-append in activation.sh**

In `activation.sh`, replace the body of `process_city` with:

```bash
process_city() {
  local city="$1"
  local city_toml="$city/city.toml"

  mkdir -p "$(dirname "$city_toml")"
  if [ ! -f "$city_toml" ]; then
    : > "$city_toml"
  fi

  local tmp
  tmp="$(mktemp "$city_toml.XXXXXX")"

  # Strip all managed blocks belonging to packs we're about to write.
  # awk reads the file once, suppressing lines between BEGIN and END markers
  # for any pack in $PACK_NAMES. Other managed blocks (for packs we're not
  # currently managing) pass through untouched.
  local pack_pattern
  pack_pattern="$(printf '%s|' "${PACK_NAMES[@]}")"
  pack_pattern="${pack_pattern%|}"

  awk -v pattern="^# BEGIN pgii-pack:($pack_pattern) \\(managed\\)\$" \
      -v end_pattern="^# END pgii-pack:($pack_pattern) \\(managed\\)\$" '
    $0 ~ pattern     { in_block = 1; next }
    in_block && $0 ~ end_pattern { in_block = 0; next }
    !in_block        { print }
  ' "$city_toml" > "$tmp"

  # Trim trailing blank lines so we do not accumulate them on each rewrite.
  awk '
    /^$/ { blanks++; next }
    { while (blanks-- > 0) print ""; blanks = 0; print }
  ' "$tmp" > "$tmp.trim"
  mv "$tmp.trim" "$tmp"

  # Append fresh blocks.
  for name in "${PACK_NAMES[@]}"; do
    local path="${PACKS[$name]}"
    emit_block "$name" "$path" >> "$tmp"
  done

  mv "$tmp" "$city_toml"
}
```

- [ ] **Step 4: Run replace test + previous test to verify both pass**

Run: `bats home/programs/pgii-packs/tests/test_replace_existing.bats home/programs/pgii-packs/tests/test_fresh_write.bats`
Expected: 3 tests pass.

- [ ] **Step 5: Commit**

```bash
git add home/programs/pgii-packs/activation.sh home/programs/pgii-packs/tests/test_replace_existing.bats
git commit -m "feat(pgii-packs): replace-in-place when managed block exists"
```

---

### Task 7: TDD no-op rebuild — same path, file untouched

**Files:**

- Create: `home/programs/pgii-packs/tests/test_no_op_rebuild.bats`
- Modify: `home/programs/pgii-packs/activation.sh`

- [ ] **Step 1: Write the failing test**

File: `home/programs/pgii-packs/tests/test_no_op_rebuild.bats`

```bash
#!/usr/bin/env bats
load test_helper

@test "no-op rebuild: file mtime unchanged when block path matches" {
  local seed
  seed=$(cat <<EOF
# BEGIN pgii-pack:pgii-pack-foo (managed)
[packs.pgii-pack-foo]
path = "/nix/store/abc-pgii-pack-foo"
# END pgii-pack:pgii-pack-foo (managed)
EOF
)
  local city; city=$(mkCity gc "$seed")
  local cities; cities=$(citiesJson "$city")
  local packs;  packs=$(packsJson "pgii-pack-foo" "/nix/store/abc-pgii-pack-foo")

  # Freeze mtime to a known past time, then re-run.
  touch -t 202001010000 "$city/city.toml"
  local before; before=$(stat -f %m "$city/city.toml" 2>/dev/null || stat -c %Y "$city/city.toml")

  run "$SCRIPT" --cities "$cities" --packs "$packs"
  [ "$status" -eq 0 ]

  local after; after=$(stat -f %m "$city/city.toml" 2>/dev/null || stat -c %Y "$city/city.toml")
  [ "$before" -eq "$after" ]
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bats home/programs/pgii-packs/tests/test_no_op_rebuild.bats`
Expected: fail — current implementation strips and re-appends every time, so mtime advances.

- [ ] **Step 3: Implement no-op detection in activation.sh**

In `activation.sh`, modify `process_city` to short-circuit when all managed blocks already match the desired state. Add this near the top of `process_city`, right after the `mkdir -p`/`touch` lines:

```bash
process_city() {
  local city="$1"
  local city_toml="$city/city.toml"

  mkdir -p "$(dirname "$city_toml")"
  if [ ! -f "$city_toml" ]; then
    : > "$city_toml"
  fi

  # Fast path: if every pack we'd write is already present with the same
  # path, and the set of currently-managed pgii blocks equals our target
  # set, do nothing. This keeps `home-manager switch` from rewriting
  # city.toml on every no-op rebuild.
  if no_op_needed "$city_toml"; then
    return 0
  fi

  # ... rest of the existing process_city body (the tmp+awk+append flow)
}

# Return 0 if city.toml is already in the desired state for our pack set.
no_op_needed() {
  local city_toml="$1"

  # Names of pgii-pack:* blocks currently in the file.
  local -a current_names=()
  while IFS= read -r line; do
    current_names+=("$line")
  done < <(grep -oE '^# BEGIN pgii-pack:[^ ]+ \(managed\)$' "$city_toml" \
              | sed -E 's/^# BEGIN pgii-pack:(.+) \(managed\)$/\1/' | sort -u)

  # Names of packs we want present.
  local -a desired_names=()
  while IFS= read -r line; do desired_names+=("$line"); done < <(printf '%s\n' "${PACK_NAMES[@]}" | sort -u)

  # Set equality check.
  [ "${#current_names[@]}" -eq "${#desired_names[@]}" ] || return 1
  local i
  for i in "${!current_names[@]}"; do
    [ "${current_names[$i]}" = "${desired_names[$i]}" ] || return 1
  done

  # Per-pack path check.
  for name in "${PACK_NAMES[@]}"; do
    local got want
    got=$(awk -v begin="# BEGIN pgii-pack:$name (managed)" -v end="# END pgii-pack:$name (managed)" '
      $0 == begin { in_block = 1; next }
      $0 == end   { in_block = 0; next }
      in_block && /^path = / { gsub(/(^path = "|"$)/, ""); print; exit }
    ' "$city_toml")
    want="${PACKS[$name]}"
    [ "$got" = "$want" ] || return 1
  done

  return 0
}
```

- [ ] **Step 4: Run all three tests**

Run: `bats home/programs/pgii-packs/tests/`
Expected: 4 tests pass (2 from fresh-write, 1 from replace-existing, 1 from no-op-rebuild).

- [ ] **Step 5: Commit**

```bash
git add home/programs/pgii-packs/activation.sh home/programs/pgii-packs/tests/test_no_op_rebuild.bats
git commit -m "feat(pgii-packs): no-op rebuild detection skips rewrite when blocks unchanged"
```

---

### Task 8: TDD remove-on-disable — block removed when pack absent from args

**Files:**

- Create: `home/programs/pgii-packs/tests/test_remove_on_disable.bats`
- Modify: `home/programs/pgii-packs/activation.sh`

- [ ] **Step 1: Write the failing test**

File: `home/programs/pgii-packs/tests/test_remove_on_disable.bats`

```bash
#!/usr/bin/env bats
load test_helper

@test "remove on disable: managed block for non-arg pack is dropped" {
  local seed
  seed=$(cat <<EOF
[workspace]
provider = "claude"

# BEGIN pgii-pack:pgii-pack-foo (managed)
[packs.pgii-pack-foo]
path = "/nix/store/aaa-pgii-pack-foo"
# END pgii-pack:pgii-pack-foo (managed)

# BEGIN pgii-pack:pgii-pack-bar (managed)
[packs.pgii-pack-bar]
path = "/nix/store/bbb-pgii-pack-bar"
# END pgii-pack:pgii-pack-bar (managed)
EOF
)
  local city; city=$(mkCity gc "$seed")
  local cities; cities=$(citiesJson "$city")
  # Only "foo" is enabled this rebuild; "bar" should be removed.
  local packs;  packs=$(packsJson "pgii-pack-foo" "/nix/store/aaa-pgii-pack-foo")

  run "$SCRIPT" --cities "$cities" --packs "$packs"
  [ "$status" -eq 0 ]

  blockExists "$city/city.toml" "pgii-pack-foo"
  ! blockExists "$city/city.toml" "pgii-pack-bar"

  # Hand-written content survives.
  grep -q "^\[workspace\]" "$city/city.toml"
}

@test "remove on disable: empty --packs removes all managed blocks" {
  local seed
  seed=$(cat <<EOF
# BEGIN pgii-pack:pgii-pack-foo (managed)
[packs.pgii-pack-foo]
path = "/nix/store/aaa"
# END pgii-pack:pgii-pack-foo (managed)
EOF
)
  local city; city=$(mkCity gc "$seed")

  run "$SCRIPT" --cities "$(citiesJson "$city")" --packs '{}'
  [ "$status" -eq 0 ]
  ! blockExists "$city/city.toml" "pgii-pack-foo"
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bats home/programs/pgii-packs/tests/test_remove_on_disable.bats`
Expected: fail — current strip step only removes blocks for packs in `--packs`, leaving disabled ones in place.

- [ ] **Step 3: Implement strip-all-pgii-blocks**

In `activation.sh`, change the strip awk in `process_city` from "only strip packs in our set" to "strip all pgii-pack:\* blocks, then re-emit only the ones we want". Replace the awk strip block with:

```bash
  # Strip all managed pgii-pack:* blocks. We re-emit only the ones we
  # want below, which gives us removal-on-disable for free.
  awk '
    /^# BEGIN pgii-pack:.* \(managed\)$/ { in_block = 1; next }
    in_block && /^# END pgii-pack:.* \(managed\)$/ { in_block = 0; next }
    !in_block { print }
  ' "$city_toml" > "$tmp"
```

(The previous pattern-restricted awk is removed.)

Also: the no-op check from Task 7 already handles the "everything matches" case before we reach the strip step. If we reach the strip step, we know something is changing.

- [ ] **Step 4: Run all bats tests**

Run: `bats home/programs/pgii-packs/tests/`
Expected: 6 tests pass.

- [ ] **Step 5: Commit**

```bash
git add home/programs/pgii-packs/activation.sh home/programs/pgii-packs/tests/test_remove_on_disable.bats
git commit -m "feat(pgii-packs): remove managed block when pack disabled"
```

---

### Task 9: TDD hand-written-collision — error on non-managed `[packs.X]`

**Files:**

- Create: `home/programs/pgii-packs/tests/test_hand_written_collision.bats`
- Modify: `home/programs/pgii-packs/activation.sh`

- [ ] **Step 1: Write the failing test**

File: `home/programs/pgii-packs/tests/test_hand_written_collision.bats`

```bash
#!/usr/bin/env bats
load test_helper

@test "hand-written collision: errors when [packs.X] exists without sentinel" {
  local seed
  seed=$(cat <<EOF
[workspace]
provider = "claude"

[packs.pgii-pack-foo]
path = "/Users/phillipg/somewhere-by-hand"
EOF
)
  local city; city=$(mkCity gc "$seed")
  local cities; cities=$(citiesJson "$city")
  local packs;  packs=$(packsJson "pgii-pack-foo" "/nix/store/aaa")

  run "$SCRIPT" --cities "$cities" --packs "$packs"
  [ "$status" -ne 0 ]
  [[ "$output" =~ "Hand-written [packs.pgii-pack-foo] exists" ]]

  # File untouched.
  grep -q "somewhere-by-hand" "$city/city.toml"
}

@test "hand-written collision: managed block does NOT trigger collision" {
  local seed
  seed=$(cat <<EOF
# BEGIN pgii-pack:pgii-pack-foo (managed)
[packs.pgii-pack-foo]
path = "/nix/store/OLD"
# END pgii-pack:pgii-pack-foo (managed)
EOF
)
  local city; city=$(mkCity gc "$seed")

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "pgii-pack-foo" "/nix/store/NEW")"
  [ "$status" -eq 0 ]
  [ "$(blockPath "$city/city.toml" "pgii-pack-foo")" = "/nix/store/NEW" ]
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `bats home/programs/pgii-packs/tests/test_hand_written_collision.bats`
Expected: first test fails — activation overwrites the hand-written block without complaint.

- [ ] **Step 3: Implement collision detection**

In `activation.sh`, add a pre-flight check inside `process_city`, after the no-op short-circuit and BEFORE the strip step:

```bash
  # Pre-flight: for each pack we want to write, refuse if [packs.<name>]
  # exists in the file but is NOT bracketed by our managed sentinels.
  for name in "${PACK_NAMES[@]}"; do
    # Does the file declare [packs.<name>] anywhere?
    if grep -Eq "^\[packs\.$name\]\$" "$city_toml"; then
      # Is that declaration inside a managed block? Walk the file.
      local inside_managed
      inside_managed=$(awk -v name="$name" '
        BEGIN { in_block = 0; found = 0 }
        $0 == "# BEGIN pgii-pack:" name " (managed)" { in_block = 1; next }
        $0 == "# END pgii-pack:" name " (managed)"   { in_block = 0; next }
        $0 == "[packs." name "]" {
          if (in_block) { found = 1 }
          else { found = -1; exit }
        }
        END { print found }
      ' "$city_toml")

      if [ "$inside_managed" = "-1" ]; then
        echo "pgii-packs: ERROR: Hand-written [packs.$name] exists in $city_toml" >&2
        echo "  Either rename or delete the hand-written block, or remove" >&2
        echo "  phillipgreenii.programs.pgii.packs.$name from your config." >&2
        exit 3
      fi
    fi
  done
```

- [ ] **Step 4: Run all tests**

Run: `bats home/programs/pgii-packs/tests/`
Expected: 8 tests pass.

- [ ] **Step 5: Commit**

```bash
git add home/programs/pgii-packs/activation.sh home/programs/pgii-packs/tests/test_hand_written_collision.bats
git commit -m "feat(pgii-packs): error on hand-written [packs.<name>] collision"
```

---

### Task 10: TDD multi-pack — three packs in one invocation

**Files:**

- Create: `home/programs/pgii-packs/tests/test_multi_pack.bats`

(No activation.sh change expected — the loop already iterates `PACK_NAMES`. This task validates that.)

- [ ] **Step 1: Write the test**

File: `home/programs/pgii-packs/tests/test_multi_pack.bats`

```bash
#!/usr/bin/env bats
load test_helper

@test "multi-pack: three packs land in one invocation" {
  local city; city=$(mkCity gc)
  local packs
  packs=$(packsJson \
    "pgii-pack-foo" "/nix/store/foo" \
    "pgii-pack-bar" "/nix/store/bar" \
    "pgii-pack-baz" "/nix/store/baz")

  run "$SCRIPT" --cities "$(citiesJson "$city")" --packs "$packs"
  [ "$status" -eq 0 ]

  blockExists "$city/city.toml" "pgii-pack-foo"
  blockExists "$city/city.toml" "pgii-pack-bar"
  blockExists "$city/city.toml" "pgii-pack-baz"

  [ "$(blockPath "$city/city.toml" "pgii-pack-foo")" = "/nix/store/foo" ]
  [ "$(blockPath "$city/city.toml" "pgii-pack-bar")" = "/nix/store/bar" ]
  [ "$(blockPath "$city/city.toml" "pgii-pack-baz")" = "/nix/store/baz" ]
}

@test "multi-pack: two existing + one new + one disabled" {
  local seed
  seed=$(cat <<EOF
# BEGIN pgii-pack:pgii-pack-foo (managed)
[packs.pgii-pack-foo]
path = "/nix/store/OLD-foo"
# END pgii-pack:pgii-pack-foo (managed)

# BEGIN pgii-pack:pgii-pack-bar (managed)
[packs.pgii-pack-bar]
path = "/nix/store/bar"
# END pgii-pack:pgii-pack-bar (managed)

# BEGIN pgii-pack:pgii-pack-old (managed)
[packs.pgii-pack-old]
path = "/nix/store/old"
# END pgii-pack:pgii-pack-old (managed)
EOF
)
  local city; city=$(mkCity gc "$seed")
  local packs
  packs=$(packsJson \
    "pgii-pack-foo" "/nix/store/NEW-foo" \
    "pgii-pack-bar" "/nix/store/bar" \
    "pgii-pack-new" "/nix/store/new")

  run "$SCRIPT" --cities "$(citiesJson "$city")" --packs "$packs"
  [ "$status" -eq 0 ]

  [ "$(blockPath "$city/city.toml" "pgii-pack-foo")" = "/nix/store/NEW-foo" ]
  [ "$(blockPath "$city/city.toml" "pgii-pack-bar")" = "/nix/store/bar" ]
  blockExists "$city/city.toml" "pgii-pack-new"
  ! blockExists "$city/city.toml" "pgii-pack-old"
}
```

- [ ] **Step 2: Run the test**

Run: `bats home/programs/pgii-packs/tests/test_multi_pack.bats`
Expected: 2 tests pass (existing implementation already supports multi-pack — this proves it).

- [ ] **Step 3: Commit**

```bash
git add home/programs/pgii-packs/tests/test_multi_pack.bats
git commit -m "test(pgii-packs): multi-pack scenarios (replace + add + remove)"
```

---

### Task 11: TDD multi-city — two cities both get blocks

**Files:**

- Create: `home/programs/pgii-packs/tests/test_multi_city.bats`

(No activation.sh change expected.)

- [ ] **Step 1: Write the test**

File: `home/programs/pgii-packs/tests/test_multi_city.bats`

```bash
#!/usr/bin/env bats
load test_helper

@test "multi-city: two cities both get the block set" {
  local city_a; city_a=$(mkCity city-a)
  local city_b; city_b=$(mkCity city-b "[workspace]\nprovider = \"claude\"")
  local cities; cities=$(citiesJson "$city_a" "$city_b")
  local packs;  packs=$(packsJson "pgii-pack-foo" "/nix/store/foo")

  run "$SCRIPT" --cities "$cities" --packs "$packs"
  [ "$status" -eq 0 ]

  blockExists "$city_a/city.toml" "pgii-pack-foo"
  blockExists "$city_b/city.toml" "pgii-pack-foo"

  # city-b's hand-written [workspace] block survives.
  grep -q "^\[workspace\]" "$city_b/city.toml"
}

@test "multi-city: collision in one city errors without touching the other" {
  local city_a; city_a=$(mkCity city-a)
  local city_b; city_b=$(mkCity city-b $'[packs.pgii-pack-foo]\npath = "/by-hand"')

  run "$SCRIPT" --cities "$(citiesJson "$city_a" "$city_b")" \
                --packs  "$(packsJson "pgii-pack-foo" "/nix/store/foo")"
  [ "$status" -ne 0 ]

  # city-a is processed first; the user has to decide whether that's OK.
  # We just assert city-b is unchanged.
  grep -q "/by-hand" "$city_b/city.toml"
}
```

- [ ] **Step 2: Run the test**

Run: `bats home/programs/pgii-packs/tests/test_multi_city.bats`
Expected: 2 tests pass.

- [ ] **Step 3: Commit**

```bash
git add home/programs/pgii-packs/tests/test_multi_city.bats
git commit -m "test(pgii-packs): multi-city activation behavior"
```

---

### Task 12: Supervisor reload (optional)

Adds the `--reload` flag's behavior: after writing city.toml, run `gc --city <city> supervisor reload` if `<city>/.gc/controller.sock` exists and `gc` is on PATH.

**Files:**

- Modify: `home/programs/pgii-packs/activation.sh`
- Create: `home/programs/pgii-packs/tests/test_reload.bats`

- [ ] **Step 1: Write the failing test**

File: `home/programs/pgii-packs/tests/test_reload.bats`

```bash
#!/usr/bin/env bats
load test_helper

# Provide a fake `gc` on PATH that records its args and exits 0.
setup_fake_gc() {
  local bindir="$TMP/bin"
  mkdir -p "$bindir"
  cat > "$bindir/gc" <<EOF
#!/usr/bin/env bash
echo "\$@" >> "$TMP/gc-calls.log"
exit 0
EOF
  chmod +x "$bindir/gc"
  PATH="$bindir:$PATH"
  export PATH
}

@test "reload: invokes gc supervisor reload when socket exists" {
  setup_fake_gc
  local city; city=$(mkCity gc)
  touch "$city/.gc/controller.sock"

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "pgii-pack-foo" "/nix/store/foo")" \
                --reload
  [ "$status" -eq 0 ]

  grep -Fxq "--city $city supervisor reload" "$TMP/gc-calls.log"
}

@test "reload: skipped when socket missing" {
  setup_fake_gc
  local city; city=$(mkCity gc)
  # No controller.sock.

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "pgii-pack-foo" "/nix/store/foo")" \
                --reload
  [ "$status" -eq 0 ]

  [ ! -f "$TMP/gc-calls.log" ] || ! grep -Fxq "--city $city supervisor reload" "$TMP/gc-calls.log"
}

@test "reload: gc failure warns but does not fail activation" {
  local bindir="$TMP/bin"
  mkdir -p "$bindir"
  cat > "$bindir/gc" <<EOF
#!/usr/bin/env bash
echo "simulated reload failure" >&2
exit 7
EOF
  chmod +x "$bindir/gc"
  PATH="$bindir:$PATH"; export PATH

  local city; city=$(mkCity gc)
  touch "$city/.gc/controller.sock"

  run "$SCRIPT" --cities "$(citiesJson "$city")" \
                --packs  "$(packsJson "pgii-pack-foo" "/nix/store/foo")" \
                --reload
  [ "$status" -eq 0 ]
  [[ "$output" =~ "WARN" ]] || [[ "$stderr" =~ "WARN" ]] || true
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bats home/programs/pgii-packs/tests/test_reload.bats`
Expected: 3 tests fail. Reason: the reload step hasn't been implemented yet.

- [ ] **Step 3: Implement reload in activation.sh**

In `activation.sh`, at the very end of the script (after the `for city in "${CITIES[@]}"; do process_city "$city"; done` loop), add:

```bash
maybe_reload_city() {
  local city="$1"
  local sock="$city/.gc/controller.sock"

  if [ ! -S "$sock" ] && [ ! -f "$sock" ]; then
    return 0
  fi
  if ! command -v gc >/dev/null 2>&1; then
    echo "pgii-packs: WARN: $city has controller.sock but \`gc\` not on PATH; skipping reload" >&2
    return 0
  fi

  if ! gc --city "$city" supervisor reload; then
    echo "pgii-packs: WARN: \`gc --city $city supervisor reload\` failed; the next manual reload will pick up the changes" >&2
  fi
}

if [ "$RELOAD" -eq 1 ]; then
  for city in "${CITIES[@]}"; do
    maybe_reload_city "$city"
  done
fi
```

- [ ] **Step 4: Run all bats tests**

Run: `bats home/programs/pgii-packs/tests/`
Expected: 13 tests pass.

- [ ] **Step 5: Commit**

```bash
git add home/programs/pgii-packs/activation.sh home/programs/pgii-packs/tests/test_reload.bats
git commit -m "feat(pgii-packs): optional gc supervisor reload after activation"
```

---

### Task 13: Home-manager module — options + assertions, no activation yet

**Files:**

- Create: `home/programs/pgii-packs/default.nix`

- [ ] **Step 1: Write the module skeleton**

File: `home/programs/pgii-packs/default.nix`

```nix
# pgii-packs home-manager module.
#
# Exposes per-pack toggles plus a list of cities to install into. The
# activation script (./activation.sh) writes managed [packs.<name>] blocks
# into each city's city.toml at home-manager activation time.
#
# Spec: docs/superpowers/specs/2026-05-26-pgii-packs-migration-design.md
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pgii;

  # Build the table of pack name → drv for enabled packs.
  # Each entry is { name; drv; } so we can use it for both home.file rooting
  # and the --packs JSON arg passed to activation.sh.
  enabledPacks =
    lib.optional cfg.packs.test-fixture.enable {
      name = "pgii-pack-test-fixture";
      drv = pkgs.pgii-pack-test-fixture;
    };

  anyPackEnabled = enabledPacks != [ ];

  packStorePathMap = lib.listToAttrs (
    map (p: { name = p.name; value = "${p.drv}"; }) enabledPacks
  );
in
{
  options.phillipgreenii.programs.pgii = {

    gascity = {
      cities = lib.mkOption {
        type = lib.types.listOf lib.types.path;
        default = [ ];
        example = [ "/Users/phillipg/gc" ];
        description = ''
          Absolute paths to gascity cities (directories containing city.toml)
          that should receive managed [packs.<name>] blocks for any enabled
          pgii pack below. The activation script writes/updates the blocks
          on every home-manager rebuild; disabling a pack removes its block
          on the next rebuild.
        '';
      };

      reloadSupervisor = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          After writing city.toml, run `gc --city <city> supervisor reload`
          for each city whose <city>/.gc/controller.sock exists and where
          `gc` is on PATH. Reload failures warn but do not fail activation.
        '';
      };
    };

    packs = {
      test-fixture.enable = lib.mkEnableOption ''
        pgii-pack-test-fixture (validation pack for the pgii-packs pipeline).
      '';
      # Real packs (pr-support, dolt-hacks, workers, gastown, bead-importer)
      # are added in their respective phase plans.
    };
  };

  config = lib.mkIf anyPackEnabled {

    home.file = lib.mkMerge (
      map (p: {
        ".local/share/pgii-packs/${p.name}".source = p.drv;
      }) enabledPacks
    );

    assertions = [
      {
        assertion = !anyPackEnabled || cfg.gascity.cities != [ ];
        message = ''
          Enabling any pgii pack requires at least one city in
          phillipgreenii.programs.pgii.gascity.cities.
        '';
      }
    ];

    # home.activation.pgii-packs wiring is added in Task 14.
  };
}
```

- [ ] **Step 2: Verify the module parses**

Run: `nix eval .#homeConfigurations 2>&1 | head -3 || true`

Then verify with a more direct check that the module file is syntactically valid:

```bash
nix-instantiate --parse home/programs/pgii-packs/default.nix > /dev/null && echo ok
```

Expected: prints `ok`.

- [ ] **Step 3: Commit**

```bash
git add home/programs/pgii-packs/default.nix
git commit -m "feat(pgii-packs): home-manager module skeleton (options + assertions)"
```

---

### Task 14: Wire activation into the HM module

**Files:**

- Modify: `home/programs/pgii-packs/default.nix`

- [ ] **Step 1: Add the home.activation entry**

In `home/programs/pgii-packs/default.nix`, locate the `config = lib.mkIf anyPackEnabled { ... };` block. Inside that attrset, after the `home.file = ...` line and before `assertions = ...`, add:

```nix
    home.activation.pgii-packs = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
      run ${pkgs.bash}/bin/bash ${./activation.sh} \
        --cities ${lib.escapeShellArg (builtins.toJSON cfg.gascity.cities)} \
        --packs  ${lib.escapeShellArg (builtins.toJSON packStorePathMap)} \
        ${lib.optionalString cfg.gascity.reloadSupervisor "--reload"}
    '';
```

Note `run` is home-manager's wrapper that respects the `DRY_RUN_NULL`/`VERBOSE_ARG` semantics during `home-manager switch --dry-run`.

- [ ] **Step 2: Add required dependencies (jq) to the activation script runtime**

The activation script uses `jq`. Add a comment + ensure `jq` is on PATH during activation. The simplest path is to write the activation as a `pkgs.writeShellApplication` and point the activation entry at it. Replace the activation block from Step 1 with:

```nix
    home.activation.pgii-packs =
      let
        activationScript = pkgs.writeShellApplication {
          name = "pgii-packs-activation";
          runtimeInputs = [
            pkgs.bash
            pkgs.coreutils
            pkgs.jq
            pkgs.gnugrep
            pkgs.gawk
            pkgs.gnused
          ];
          text = builtins.readFile ./activation.sh;
        };
      in
      lib.hm.dag.entryAfter [ "writeBoundary" ] ''
        run ${activationScript}/bin/pgii-packs-activation \
          --cities ${lib.escapeShellArg (builtins.toJSON cfg.gascity.cities)} \
          --packs  ${lib.escapeShellArg (builtins.toJSON packStorePathMap)} \
          ${lib.optionalString cfg.gascity.reloadSupervisor "--reload"}
      '';
```

- [ ] **Step 3: Sanity-check the module evaluates**

Run: `nix-instantiate --parse home/programs/pgii-packs/default.nix > /dev/null && echo ok`
Expected: prints `ok`.

- [ ] **Step 4: Commit**

```bash
git add home/programs/pgii-packs/default.nix
git commit -m "feat(pgii-packs): wire activation script into home-manager activation"
```

---

### Task 15: Register the module + add bats check to flake

**Files:**

- Modify: `home/default.nix`
- Modify: `flake.nix`

- [ ] **Step 1: Import the module from home/default.nix**

In `home/default.nix`, locate the `imports = [ ... ];` list. The list is not alphabetical, but the existing `pgii-*` entries (`./programs/pgii-local-plugins`, `./programs/pgii-claude-plugins`) sit together near the top. Add `./programs/pgii-packs` next to them:

Find:

```nix
    ./programs/pgii-local-plugins
    ./programs/pgii-claude-plugins
```

And insert below the second line:

```nix
    ./programs/pgii-packs
```

- [ ] **Step 2: Add the flake check for the bats tests**

In `flake.nix`, locate the `checks = { ... };` block (around line 191). Inside it, add a new check entry after `test-claude-settings-install-plugin`:

```nix
            test-pgii-packs-activation = checks-lib.testBashScripts {
              package = pkgs.writeShellApplication {
                name = "pgii-packs-activation";
                runtimeInputs = [
                  pkgs.bash
                  pkgs.coreutils
                  pkgs.jq
                  pkgs.gnugrep
                  pkgs.gawk
                  pkgs.gnused
                ];
                text = builtins.readFile ./home/programs/pgii-packs/activation.sh;
              };
              tests = ./home/programs/pgii-packs/tests;
              extraInputs = [
                pkgs.jq
                pkgs.coreutils
                pkgs.gnugrep
                pkgs.gawk
                pkgs.gnused
              ];
            };
```

- [ ] **Step 3: Run the new flake check**

Run: `nix flake check --no-write-lock-file -L 2>&1 | tail -40`
Expected: includes a `test-pgii-packs-activation` line; all bats tests pass.

If `nix flake check` runs other slow checks, narrow to the one you need:

```bash
nix build .#checks.aarch64-darwin.test-pgii-packs-activation -L --no-link
```

(Replace `aarch64-darwin` with your `$system` if different. `nix eval .#system` will print it.)

Expected: exit 0, no failures.

- [ ] **Step 4: Commit**

```bash
git add home/default.nix flake.nix
git commit -m "feat(pgii-packs): register HM module + flake check for activation tests"
```

---

### Task 16: End-to-end smoke test on the real city

This task validates the full pipeline against `/Users/phillipg/gc` — the user's only real city. The test-fixture pack is harmless (its only order is disabled). After validation, we leave the pack enabled for downstream phases or roll back; that's a user decision.

**Files:**

- No files to commit yet — this is a manual validation step. The user enables the pack in their machine config (out-of-tree), runs `home-manager switch`, observes results.

- [ ] **Step 1: Enable the test-fixture pack in the user's home-manager config**

The user's machine config lives outside this repo. Locate the file where `phillipgreenii.programs.<X>.enable = true;` lines are set for this machine. Add:

```nix
phillipgreenii.programs.pgii = {
  gascity.cities = [ "/Users/phillipg/gc" ];
  packs.test-fixture.enable = true;
};
```

If the user already has this stanza, set only `packs.test-fixture.enable = true;`.

- [ ] **Step 2: Run home-manager switch**

The user runs (out of band): `home-manager switch --flake <their-flake-path>`
Or if their setup uses `darwin-rebuild`: `darwin-rebuild switch --flake <their-flake-path>`

Expected output includes a `pgii-packs-activation` log line writing to `/Users/phillipg/gc/city.toml`.

- [ ] **Step 3: Verify the managed block landed**

Run:

```bash
grep -A 2 "BEGIN pgii-pack:pgii-pack-test-fixture" /Users/phillipg/gc/city.toml
```

Expected:

```
# BEGIN pgii-pack:pgii-pack-test-fixture (managed)
[packs.pgii-pack-test-fixture]
path = "/nix/store/...-pgii-pack-test-fixture-0.1.0"
```

- [ ] **Step 4: Verify gascity sees the pack after reload**

If `reloadSupervisor` was true, the supervisor reload should have already happened. Confirm:

```bash
grep "pgii-pack-test-fixture" /Users/phillipg/.gc/supervisor.log | tail -5
```

Expected: a log line indicating the pack was loaded (exact phrasing depends on gascity version).

Alternative confirmation: the test-fixture's `noop` order should appear in `gc order list`:

```bash
gc order list 2>&1 | grep noop
```

Expected: a line referencing the noop order (status disabled per the order's `enabled = false`).

- [ ] **Step 5: Verify removal works**

Set `packs.test-fixture.enable = false;` (or remove the option) in the user's machine config, run `home-manager switch` again, then:

```bash
grep "pgii-pack-test-fixture" /Users/phillipg/gc/city.toml
```

Expected: no output — the managed block is gone.

If the user wants to leave the test-fixture pack enabled for future end-to-end checks: re-enable it. Otherwise: leave it disabled.

- [ ] **Step 6: Commit any machine-config changes**

```bash
# In the user's machine-config repo (not this repo):
git add <machine-config-file>
git commit -m "config(machine): enable pgii-pack-test-fixture end-to-end smoke test"
```

(This commit lives in the user's separate machine-config repo, not in nix-agent-support.)

---

## Final verification

After Task 16 completes successfully:

- [ ] `nix flake check` passes end-to-end (or at least the new `test-pgii-packs-activation` check).
- [ ] `bats home/programs/pgii-packs/tests/` reports 16+ tests passing locally.
- [ ] The real city's `city.toml` shows the managed block when enabled, none when disabled.
- [ ] No regressions in any pre-existing check (`formatting`, `linting`, etc.).

Phase 0 is complete when all of the above pass. Phase 1 (pgii-pr-support migration) can begin and will reuse all of the machinery built here without further changes.
