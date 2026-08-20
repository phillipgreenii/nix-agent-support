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
# work with a scheduled owner (pr-pool has not retrofitted INV-22 to its journeys;
# five implementation citations name elements pr-pool does not declare). Loosening
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
# THE ZR SET IS NOT HERE. The ZR deployment set lives in phillipg-nix-ziprecruiter.
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

ROOT="${1:-.}"
cd "$ROOT" || {
  echo "not a directory: $ROOT" >&2
  exit 2
}
ROOT=$(pwd)
BASELINE="${BASELINE:-$ROOT/tests/behavior-docs-real-corpus.baseline}"

SKILLS="claude-marketplace/behavior-docs-conformance/skills"
SELF_CHECKS="$SKILLS/behavior-docs-intra-conformance/scripts/self-checks.sh"
TRACE_EXTRACT="$SKILLS/behavior-docs-intra-conformance/scripts/trace-extract.sh"
RESOLVE_IMPORTS="$SKILLS/behavior-docs-inter-conformance/scripts/resolve-imports.sh"
RECONCILE_IMPORTS="$SKILLS/behavior-docs-inter-conformance/scripts/reconcile-imports.sh"
NAME_COLLISIONS="$SKILLS/behavior-docs-inter-conformance/scripts/name-collisions.sh"
IMPL_TRACES="$SKILLS/behavior-docs-impl-conformance/scripts/impl-traces.sh"

METHOD_SET="behavior-docs/docs/behavior"
PRPOOL_SET="packages/pr-pool/docs/behavior"
PRPOOL_IMPL="packages/pr-pool"
PAMONITOR_SET="packages/pa-monitor/docs/behavior"

for f in "$SELF_CHECKS" "$TRACE_EXTRACT" "$RESOLVE_IMPORTS" "$RECONCILE_IMPORTS" "$NAME_COLLISIONS" "$IMPL_TRACES"; do
  [ -f "$f" ] || {
    echo "missing evaluator script: $ROOT/$f" >&2
    echo "(a rename that did not update this runner? the paths are listed at the top of this file)" >&2
    exit 2
  }
done
for d in "$METHOD_SET" "$PRPOOL_SET" "$PRPOOL_IMPL" "$PAMONITOR_SET"; do
  [ -d "$d" ] || {
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

# require_clean <set> <section-name> <expected-marker> <output-file>
require_clean() {
  local set="$1" name="$2" marker="$3" out="$4" body
  body=$(section "$name" "$out")
  if printf '%s\n' "$body" | grep -qF -- "$marker"; then return 0; fi
  echo "REAL-CORPUS FAIL [$set] self-checks section '$name' is not clean (expected to contain '$marker'):" >&2
  printf '%s\n' "$body" >&2
  hard_fail=1
}

echo "=== behavior-docs real corpus: $ROOT ==="

for set in "$METHOD_SET" "$PRPOOL_SET" "$PAMONITOR_SET"; do
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
  require_clean "$set" "UUID carriers" "clean (" "$out"
  require_clean "$set" "Status headers" "clean" "$out"
  require_clean "$set" "Inline status framing" "clean" "$out"
  require_clean "$set" "Cross-set relative links" "none" "$out"
  require_clean "$set" "Mermaid fences balanced" "(even)" "$out"
  # Catch a FAIL in a section not asserted above — but ONLY in the deterministic
  # ones. A bare `grep FAIL` over the whole report is wrong twice over: the >=2x
  # heuristic prints its own advisory FAILs and a reminder to "confirm FAILs by
  # hand", and real element names contain the word (`INV-FAIL-1`, `JOURNEY-FAIL`),
  # so the naive form fails every run on both real sets and the gate is useless.
  if strip_heuristic_sections "$out" | grep -qE '^[[:space:]]*FAIL[[:space:]]'; then
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
done

echo
echo "--- inter: resolve-imports.sh (method -> pr-pool) ---"
out="$tmp/resolve"
bash "$RESOLVE_IMPORTS" "$METHOD_SET" "$PRPOOL_SET" >"$out" 2>&1 || {
  echo "REAL-CORPUS FAIL resolve-imports.sh did not resolve the real seam:" >&2
  cat "$out" >&2
  hard_fail=1
}
record "inter/resolve-imports" "$out"
grep -E '^[[:space:]]*(ok|WARN|FAIL|external)' "$out" | sed 's/^/  /' || true

echo "--- inter: reconcile-imports.sh (method <-> pr-pool) ---"
out="$tmp/reconcile"
bash "$RECONCILE_IMPORTS" "$METHOD_SET" "$PRPOOL_SET" >"$out" 2>&1 || true
record "inter/reconcile-imports" "$out"
grep -E '^[[:space:]]*(FAIL|WARN|clean)' "$out" | sed 's/^/  /' || true

echo "--- inter: resolve-imports.sh (method -> pa-monitor) ---"
out="$tmp/resolve-pamonitor"
bash "$RESOLVE_IMPORTS" "$METHOD_SET" "$PAMONITOR_SET" >"$out" 2>&1 || {
  echo "REAL-CORPUS FAIL resolve-imports.sh did not resolve the real seam:" >&2
  cat "$out" >&2
  hard_fail=1
}
record "inter/resolve-imports" "$out"
grep -E '^[[:space:]]*(ok|WARN|FAIL|external)' "$out" | sed 's/^/  /' || true

echo "--- inter: reconcile-imports.sh (method <-> pa-monitor) ---"
out="$tmp/reconcile-pamonitor"
bash "$RECONCILE_IMPORTS" "$METHOD_SET" "$PAMONITOR_SET" >"$out" 2>&1 || true
record "inter/reconcile-imports" "$out"
grep -E '^[[:space:]]*(FAIL|WARN|clean)' "$out" | sed 's/^/  /' || true

echo "--- inter: name-collisions.sh (method, pr-pool, pa-monitor) ---"
out="$tmp/collisions"
bash "$NAME_COLLISIONS" "$METHOD_SET" "$PRPOOL_SET" "$PAMONITOR_SET" >"$out" 2>&1 || true
record "inter/name-collisions" "$out"
grep -E '^[[:space:]]*(FAIL|CANDIDATE|clean|none)' "$out" | sed 's/^/  /' || true

echo "--- impl: impl-traces.sh (pr-pool code <-> pr-pool set) ---"
out="$tmp/impl"
bash "$IMPL_TRACES" "$PRPOOL_SET" "$PRPOOL_IMPL" >"$out" 2>&1 || true
record "impl/impl-traces" "$out"
grep -E '^[[:space:]]*(FAIL|NOTICE)' "$out" | sed 's/^/  /' || true

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
