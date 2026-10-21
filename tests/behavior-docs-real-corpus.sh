#!/usr/bin/env bash
# behavior-docs-real-corpus.sh — run all three behavior-docs conformance
# evaluators over the REAL in-repo sets (bead pg2-wr6lm.4, plan item 6).
#
# WHY THIS EXISTS. Every other behavior-docs check in this repo runs over
# FIXTURES. Fixtures prove an evaluator CAN see a defect and prove nothing
# whatsoever about the docs that actually ship — and two known defects reached
# main for exactly that reason. This is the gate that reads the real sets.
#
# ONE RUNNER, TWO CALLERS. `checks.test-behavior-docs-real-corpus` invokes it with
# the flake source as its root; the `behavior-docs-real-corpus` pre-commit hook
# invokes it with the WORKING TREE as its root. Everything — sets, evaluator
# scripts, baseline — is resolved under that one root, so each caller is
# internally consistent and neither can accidentally check the store copy of one
# thing against the working copy of another.
#
# Usage: behavior-docs-real-corpus.sh [<repo-root>]
# Env:   BASELINE  path to the recorded-findings file (default
#                  <repo-root>/tests/behavior-docs-real-corpus.baseline)
# Exit:  0 when the real corpus matches the recorded baseline exactly, 1 otherwise.
#
# THE BASELINE IS A RATCHET, NOT AN EXEMPTION LIST. Some findings below are open
# work with a scheduled owner (pg-router has not retrofitted INV-22 to its journeys;
# five implementation citations name elements pg-router does not declare). Loosening
# a check until those pass would make it worthless — a gate that only ever agrees
# with the current state. Instead every open finding is recorded VERBATIM in the
# baseline, and this runner fails on ANY difference in EITHER direction:
#
#   a NEW finding      -> the gate fails: a regression, or newly shipped defect.
#   a FIXED finding     -> the gate fails too, telling you to delete that baseline
#                          line. A baseline that may silently keep entries for
#                          defects that no longer exist is how the record rots
#                          into folklore.
#
# THE ZR SET IS NOT HERE. The ZR deployment set lives in your-private-flake.
# It is in another repo, so it is absent from this flake source and unreachable
# from the build sandbox. Its seams are checked by running these same scripts
# against a workspace checkout by hand; this gate covers the in-repo sets and the
# seams among them.
set -euo pipefail

# DETERMINISM: every sort, comm, uniq and shell glob below MUST order bytes, not
# locale-collated characters. Without this the SAME finding serializes differently
# on a UTF-8 workstation (`invariants.md:75 README.md:61`) and in the `C`-locale
# nix build sandbox (`README.md:61 invariants.md:75`) — so a gate that compares
# finding strings reports one identical finding as BOTH a new regression AND a
# no-longer-occurring entry, and is flaky rather than useful. The finding string is
# the record: it MUST be canonical where it is WRITTEN, never normalized where it
# is compared.
export LC_ALL=C

LIST_ONLY=0
if [ "${1:-}" = "--list-sets" ]; then
  LIST_ONLY=1
  shift
fi

ROOT="${1:-.}"
cd "$ROOT" || {
  echo "not a directory: $ROOT" >&2
  exit 2
}
ROOT=$(pwd)

# discover_sets — every in-repo behavior-docs set, one repo-relative path per line,
# byte-ordered. A set is a `docs/behavior/` directory that carries a README.md.
#
# DISCOVERY, NOT A LIST (bead pg2-ksssh). The runner used to hard-code five sets, so
# four sets that were authored after it (pg-connector, pg-router-ccpool-handler,
# pg-router-source-pg-connector, work-report) sat outside every automated gate, and
# the next new set would have joined them silently. A set now enters the gate by
# existing, which is the only way a new one cannot be missed.
#
# Dot-directories are pruned: `.worktrees/` and `.git/` hold COPIES of the tree, and
# the pre-commit hook runs this over a working tree that may contain them. That is a
# statement about where sets can live, not a finding exclude: no set path is ever
# skipped by name.
discover_sets() {
  find . -name '.?*' -prune -o -type f -path '*/docs/behavior/README.md' -print |
    sed -e 's|^\./||' -e 's|/README\.md$||' |
    sort
}

if [ "$LIST_ONLY" -eq 1 ]; then
  discover_sets
  exit 0
fi
BASELINE="${BASELINE:-$ROOT/tests/behavior-docs-real-corpus.baseline}"

SKILLS="claude-marketplace/behavior-docs-conformance/skills"
SELF_CHECKS="$SKILLS/behavior-docs-intra-conformance/scripts/self-checks.sh"
TRACE_EXTRACT="$SKILLS/behavior-docs-intra-conformance/scripts/trace-extract.sh"
RELOCATION_CHECK="$SKILLS/behavior-docs-intra-conformance/scripts/relocation-check.sh"
RESOLVE_IMPORTS="$SKILLS/behavior-docs-inter-conformance/scripts/resolve-imports.sh"
RECONCILE_IMPORTS="$SKILLS/behavior-docs-inter-conformance/scripts/reconcile-imports.sh"
NAME_COLLISIONS="$SKILLS/behavior-docs-inter-conformance/scripts/name-collisions.sh"
IMPL_TRACES="$SKILLS/behavior-docs-impl-conformance/scripts/impl-traces.sh"

METHOD_SET="behavior-docs/docs/behavior"

for f in "$SELF_CHECKS" "$TRACE_EXTRACT" "$RELOCATION_CHECK" "$RESOLVE_IMPORTS" "$RECONCILE_IMPORTS" "$NAME_COLLISIONS" "$IMPL_TRACES"; do
  [ -f "$f" ] || {
    echo "missing evaluator script: $ROOT/$f" >&2
    echo "(a rename that did not update this runner? the paths are listed at the top of this file)" >&2
    exit 2
  }
done

SETS=()
while IFS= read -r s; do
  SETS+=("$s")
done < <(discover_sets)
# A vacuous gate is the failure this runner exists to prevent: finding no set at
# all (a changed root, a find that matched nothing) MUST be loud, and so MUST the
# method set going missing, because every inter seam below hangs off it.
if [ "${#SETS[@]}" -eq 0 ]; then
  echo "no behavior-docs set discovered under $ROOT (looked for */docs/behavior/README.md)" >&2
  exit 2
fi
printf '%s\n' "${SETS[@]}" | grep -Fx -- "$METHOD_SET" >/dev/null || {
  echo "missing real corpus path: $ROOT/$METHOD_SET (the method set is the root of every seam)" >&2
  exit 2
}

# impl_root <set> — the implementation tree the IMPL evaluator reconciles a set
# against, or empty when this runner has no implementation path for the set.
#
# A set gains an implementation path by gaining a line here, and gains it ONLY once
# impl-traces.sh reports no FAIL against its own package tree (bead pg2-30r9q: each
# line was added after the code-side citations, or the set's imports table, were
# fixed so the citations resolve). The lines are per set, not a glob, so the next
# set a new package brings is not silently asserted against code that has never been
# reconciled with it; add its line when it is clean.
#
# `behavior-docs/docs/behavior` (the method) has no implementation tree, and the
# nested `packages/pg-connector/docs/behavior/github` set is not a discovered set
# (discovery matches `*/docs/behavior/README.md`); its code is reconciled through the
# parent's imports table, which declares the `INV-GH-*` elements the code cites.
impl_root() {
  case "$1" in
  packages/ccpool/docs/behavior) printf '%s\n' "packages/ccpool" ;;
  packages/pa-monitor/docs/behavior) printf '%s\n' "packages/pa-monitor" ;;
  packages/pg-connector/docs/behavior) printf '%s\n' "packages/pg-connector" ;;
  packages/pg-router-ccpool-handler/docs/behavior) printf '%s\n' "packages/pg-router-ccpool-handler" ;;
  packages/pg-router-source-pg-connector/docs/behavior) printf '%s\n' "packages/pg-router-source-pg-connector" ;;
  packages/pg-router/docs/behavior) printf '%s\n' "packages/pg-router" ;;
  packages/work-report/docs/behavior) printf '%s\n' "packages/work-report" ;;
  *) ;;
  esac
}
for set in "${SETS[@]}"; do
  d=$(impl_root "$set")
  [ -z "$d" ] || [ -d "$d" ] || {
    echo "missing real corpus path: $ROOT/$d" >&2
    exit 2
  }
done

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
findings="$tmp/findings"
: >"$findings"
hard_fail=0

# record <check-id> <output-file> — append this run's finding lines, normalized.
# Column padding is squeezed so a cosmetic width change in a report does not read
# as a changed finding.
record() {
  { grep -E '^[[:space:]]*(FAIL|WARN|CANDIDATE)' "$2" || true; } |
    sed -E 's/^[[:space:]]+//; s/[[:space:]]+/ /g; s/[[:space:]]+$//' |
    sed -E "s|^|$1\||" >>"$findings"
}

# section <name> <output-file> — the body of one self-checks.sh section.
section() {
  awk -v want="$1" '
    /^=== / { insec = (index($0, want) > 0); next }
    insec { print }
  ' "$2"
}

# strip_heuristic_sections <output-file> — self-checks.sh output with the two
# sections it documents as HEURISTIC removed (the >=2x named-concept pass and the
# floor-leakage candidates). Both are judgment surfaces by design, so a hit there
# is advisory, not a gate failure.
strip_heuristic_sections() {
  awk '
    /^=== / { skip = (index($0, "Named concept used") > 0 || index($0, "Floor-leakage") > 0) }
    !skip { print }
  ' "$1"
}

# require_clean <set> <section-name> <expected-marker> <output-file> [<alt-marker>]
# The section is clean when it contains <expected-marker> or, if given, <alt-marker>.
require_clean() {
  local set="$1" name="$2" marker="$3" out="$4" alt="${5:-}" body
  body=$(section "$name" "$out")
  # no grep -q: pipefail + early exit = SIGPIPE false negative (pg2-g9vji)
  if printf '%s\n' "$body" | grep -F -- "$marker" >/dev/null; then return 0; fi
  if [ -n "$alt" ] && printf '%s\n' "$body" | grep -F -- "$alt" >/dev/null; then return 0; fi
  echo "REAL-CORPUS FAIL [$set] self-checks section '$name' is not clean (expected to contain '$marker'):" >&2
  printf '%s\n' "$body" >&2
  hard_fail=1
}

echo "=== behavior-docs real corpus: $ROOT ==="

echo "discovered ${#SETS[@]} set(s):"
printf '  %s\n' "${SETS[@]}"

for set in "${SETS[@]}"; do
  echo
  echo "--- intra: self-checks.sh $set ---"
  out="$tmp/self-checks.$(printf '%s' "$set" | tr / _)"
  bash "$SELF_CHECKS" "$set" >"$out" 2>&1 || {
    echo "REAL-CORPUS FAIL [$set] self-checks.sh exited non-zero:" >&2
    cat "$out" >&2
    hard_fail=1
  }
  # The DETERMINISTIC sections must read exactly their clean marker. The
  # floor-leakage and >=2x sections are deliberately NOT asserted: self-checks.sh
  # documents both as heuristics needing judgment, and the method set legitimately
  # QUOTES a below-floor line ("retry 3x with a 30s backoff") as a counter-example
  # of what must not appear in a behavior doc. Asserting a heuristic would force
  # either a false failure or an allowlist.
  # A set must carry UUIDs (`clean (N carrier(s)...`). The one exception is a set
  # that is a README ALONE: it has no invariants, journeys or interfaces register,
  # so it defines no id-bearing element, and self-checks.sh reports `none present`.
  # The exception is keyed on the set's SHAPE, never on its path, so a register-
  # carrying set that loses its UUIDs still fails here.
  set_files=("$set"/*.md)
  uuid_alt=""
  if [ "${#set_files[@]}" -eq 1 ]; then
    uuid_alt="none present"
  fi
  require_clean "$set" "UUID carriers" "clean (" "$out" "$uuid_alt"
  require_clean "$set" "Status headers" "clean" "$out"
  require_clean "$set" "Inline status framing" "clean" "$out"
  require_clean "$set" "Cross-set relative links" "none" "$out"
  require_clean "$set" "Mermaid fences balanced" "(even)" "$out"
  # Catch a FAIL in a section not asserted above — but ONLY in the deterministic
  # ones. A bare `grep FAIL` over the whole report is wrong twice over: the >=2x
  # heuristic prints its own advisory FAILs and a reminder to "confirm FAILs by
  # hand", and real element names can contain the word (`INV-FAIL-1`, or an
  # obsolete/renamed journey or use-case name), so the naive form fails every
  # run on both real sets and the gate is useless.
  if strip_heuristic_sections "$out" | grep -E '^[[:space:]]*FAIL[[:space:]]' >/dev/null; then
    echo "REAL-CORPUS FAIL [$set] self-checks.sh reported a FAIL in a deterministic section:" >&2
    strip_heuristic_sections "$out" | grep -E '^[[:space:]]*FAIL[[:space:]]' >&2
    hard_fail=1
  fi
  echo "  deterministic sections clean"
  # Drop blank lines rather than decorating them: a `  advisory: ` line with
  # nothing after it carries trailing whitespace, which some log formatters trim
  # and others do not — so the same run compares unequal to itself across
  # transports.
  section "Floor-leakage candidates" "$out" | { grep -v '^[[:space:]]*$' || true; } | sed 's/^/  advisory: /'

  echo "--- intra: trace-extract.sh $set ---"
  out="$tmp/trace.$(printf '%s' "$set" | tr / _)"
  bash "$TRACE_EXTRACT" "$set" >"$out" 2>&1 || true
  record "intra/trace-extract $set" "$out"
  sed -n '/^--- /,$p' "$out" | grep -E '^[[:space:]]*(FAIL|WARN|clean|adopted|NOT ADOPTED)' | sed 's/^/  /' || true

  echo "--- intra: relocation-check.sh $set ---"
  out="$tmp/relocation.$(printf '%s' "$set" | tr / _)"
  bash "$RELOCATION_CHECK" "$set" >"$out" 2>&1 || true
  record "intra/relocation-check $set" "$out"
  sed -n '/^--- /,$p' "$out" | grep -E '^[[:space:]]*(FAIL|clean)' | sed 's/^/  /' || true
done

# declared_owners <set> — the owner set-paths <set>'s `## External references`
# tables declare, one per line. Each row's `<repo> · <set-path>` cell names the
# owner; the seam to check is then (owner, <set>). Deriving the seams from the
# tables means a set that starts importing from another is checked against it with
# no edit here — the hard-coded list this replaced could not do that, and the
# same omission is what it did to the four sets this runner once missed.
declared_owners() {
  awk '
    FNR == 1 { insec = 0 }
    toupper($0) ~ /^##[[:space:]]+EXTERNAL REFERENCES/ { insec = 1; next }
    /^##[[:space:]]/ && insec { insec = 0 }
    insec && /^\|/ { print }
  ' "$1"/*.md 2>/dev/null |
    { grep -oE '`[^`|]*·[[:space:]]*[^`|]+`' || true; } |
    sed -E 's/^.*·[[:space:]]*//; s/`$//' |
    sort -u
}

# SEAMS — "<owner-dir>|<implementer-set>" for every declared owner that lives in
# this repo. An owner is a set OR any other directory of ID-carrying docs (a
# `docs/decisions` area is an owner too); an owner outside this repo (the ZR
# deployment set) cannot be read from here and is reported, not checked.
SEAMS=()
echo
echo "--- inter: seams derived from the sets' External references tables ---"
for set in "${SETS[@]}"; do
  while IFS= read -r owner; do
    [ -n "$owner" ] || continue
    [ "$owner" != "$set" ] || continue
    if [ -d "$owner" ]; then
      SEAMS+=("$owner|$set")
    else
      echo "  external owner not in this repo, not checked: $owner (declared by $set)"
    fi
  done < <(declared_owners "$set")
done
if [ "${#SEAMS[@]}" -eq 0 ]; then
  echo "  (no set declares an in-repo owner)"
else
  SEAM_LIST=$(printf '%s\n' "${SEAMS[@]}" | sort -u)
  SEAMS=()
  while IFS= read -r seam; do
    SEAMS+=("$seam")
  done <<<"$SEAM_LIST"
  printf '  %s\n' "${SEAMS[@]}"
fi

for seam in "${SEAMS[@]}"; do
  owner="${seam%%|*}"
  impl="${seam#*|}"
  tag=$(printf '%s__%s' "$owner" "$impl" | tr / _)

  echo
  echo "--- inter: resolve-imports.sh ($owner -> $impl) ---"
  out="$tmp/resolve.$tag"
  bash "$RESOLVE_IMPORTS" "$owner" "$impl" >"$out" 2>&1 || {
    echo "REAL-CORPUS FAIL resolve-imports.sh did not resolve the real seam $owner -> $impl:" >&2
    cat "$out" >&2
    hard_fail=1
  }
  record "inter/resolve-imports $owner -> $impl" "$out"
  grep -E '^[[:space:]]*(ok|WARN|FAIL|external)' "$out" | sed 's/^/  /' || true

  echo "--- inter: reconcile-imports.sh ($owner <-> $impl) ---"
  out="$tmp/reconcile.$tag"
  bash "$RECONCILE_IMPORTS" "$owner" "$impl" >"$out" 2>&1 || true
  record "inter/reconcile-imports $owner <-> $impl" "$out"
  grep -E '^[[:space:]]*(FAIL|WARN|clean)' "$out" | sed 's/^/  /' || true
done

echo
echo "--- inter: name-collisions.sh (once per declared seam between two discovered sets) ---"
# ONE INVOCATION PER REFERENCE EDGE, not one over every set. Class 1 only ever fails
# across a reference edge, so running it per edge loses nothing. Class 2 asks whether
# a set asserts a name beside its citation of ANOTHER set's element, which has no
# meaning for two sets that never cite each other: handed every set at once it reads
# a set's citation of its OWN `INV-STATE-1` as a citation of an unrelated set's
# same-named `INV-STATE-1` and reports ~60 candidates, none about a real seam. INV-20
# says the same of class 1: vocabulary consistency across unrelated sets is not
# required. An owner that is not itself a discovered set (a `docs/decisions` area) is
# not a name-collisions operand.
PAIRS=()
for seam in "${SEAMS[@]+"${SEAMS[@]}"}"; do
  owner="${seam%%|*}"
  impl="${seam#*|}"
  printf '%s\n' "${SETS[@]}" | grep -Fx -- "$owner" >/dev/null || continue
  if [[ $owner < $impl ]]; then
    PAIRS+=("$owner|$impl")
  else
    PAIRS+=("$impl|$owner")
  fi
done
if [ "${#PAIRS[@]}" -gt 0 ]; then
  PAIR_LIST=$(printf '%s\n' "${PAIRS[@]}" | sort -u)
  PAIRS=()
  while IFS= read -r pair; do
    PAIRS+=("$pair")
  done <<<"$PAIR_LIST"
fi
for pair in "${PAIRS[@]+"${PAIRS[@]}"}"; do
  a="${pair%%|*}"
  b="${pair#*|}"
  echo "  ($a, $b)"
  out="$tmp/collisions.$(printf '%s__%s' "$a" "$b" | tr / _)"
  bash "$NAME_COLLISIONS" "$a" "$b" >"$out" 2>&1 || true
  record "inter/name-collisions $a, $b" "$out"
  # `note` is echoed but deliberately NOT recorded (see `record`, which greps only
  # FAIL/WARN/CANDIDATE). Class 1 emits one per ID name reused across two sets with NO
  # reference edge — INV-20 does not require distinct names there, so it is not a
  # finding and must not consume a baseline line. Echoing it keeps the narrowing
  # VISIBLE in the gate log rather than silent (bead pg2-pffnw).
  grep -E '^[[:space:]]*(FAIL|CANDIDATE|note|clean|none)' "$out" | sed 's/^/    /' || true
done

# impl-traces runs wherever a set has an implementation tree in this repo
# (`impl_root`): the code's citations of the set's IDs, reconciled against it.
for set in "${SETS[@]}"; do
  d=$(impl_root "$set")
  [ -n "$d" ] || continue
  echo
  echo "--- impl: impl-traces.sh ($d code <-> $set) ---"
  out="$tmp/impl.$(printf '%s' "$set" | tr / _)"
  bash "$IMPL_TRACES" "$set" "$d" >"$out" 2>&1 || true
  record "impl/impl-traces $set" "$out"
  grep -E '^[[:space:]]*(FAIL|NOTICE)' "$out" | sed 's/^/  /' || true
done

echo
echo "--- baseline comparison ---"
sort -u "$findings" >"$tmp/actual"
if [ -f "$BASELINE" ]; then
  # Comments and blank lines are for the reader; strip them before comparing.
  { grep -vE '^[[:space:]]*(#|$)' "$BASELINE" || true; } | sort -u >"$tmp/expected"
else
  echo "no baseline at $BASELINE — every finding below is unrecorded" >&2
  : >"$tmp/expected"
fi
added=$(comm -23 "$tmp/actual" "$tmp/expected")
removed=$(comm -13 "$tmp/actual" "$tmp/expected")
if [ -n "$added" ]; then
  echo "REAL-CORPUS FAIL: finding(s) NOT in the baseline — a regression, or a newly shipped defect:" >&2
  printf '%s\n' "$added" | sed 's/^/  + /' >&2
  hard_fail=1
fi
if [ -n "$removed" ]; then
  echo "REAL-CORPUS FAIL: baseline line(s) whose finding no longer occurs — delete them:" >&2
  printf '%s\n' "$removed" | sed 's/^/  - /' >&2
  echo "  (a baseline that may keep entries for defects that no longer exist stops being a record)" >&2
  hard_fail=1
fi
if [ -z "$added" ] && [ -z "$removed" ]; then
  printf '  matches the baseline exactly (%s recorded finding(s))\n' "$(grep -c . "$tmp/expected" || true)"
fi

echo
if [ "$hard_fail" -ne 0 ]; then
  echo "behavior-docs real corpus: FAIL"
  exit 1
fi
echo "behavior-docs real corpus: OK"
