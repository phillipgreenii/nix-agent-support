#!/usr/bin/env bash
# impl-traces.sh — the mechanical core of the IMPL evaluator (bead pg2-wr6lm.4):
# an IMPLEMENTATION reconciled against ITS OWN behavior-docs set.
#
# The method's own `INTF-1` says the docs provide intended behavior at the floor
# and downstream MUST cite the ID it implements. That makes the doc<->code seam
# mechanically checkable from the code side, which is what this script does: it
# collects every behavior-docs ID the implementation cites and classifies it.
#
#   ok         — resolves to a definition in this set. The citation is live.
#   external   — resolves only through the set's `## External references` imports
#                table: the code cites an element ANOTHER set owns, and this set
#                declares it. Correct, and worth distinguishing from ok.
#   historical — the citing line frames the ID as gone (former / formerly /
#                removed / deleted / resolved / superseded). A set leaves NO
#                tombstone when an element is removed (`INV-4`), so the ID
#                legitimately resolves to nothing while the code explains WHY it
#                once existed. Failing these would push the code to either delete
#                its rationale or resurrect a dead ID.
#   FAIL       — resolves to nothing and is not framed as historical: a stale
#                citation, or an element of another set that this set never
#                declared.
#
# It also reports COVERAGE — set elements the implementation never cites — as a
# NOTICE, never a failure: citation retrofit is lazy (the same reason the UUID
# retrofit is), so an uncited invariant is a gap to work through, not a
# regression to block on.
#
# Usage: impl-traces.sh [--strict] <behavior-docs-set-dir> <impl-root>
# Exit: 0 when no FAIL, 1 on any FAIL, 2 on a usage error.
#
# The set directory is EXCLUDED from the implementation scan even when it sits
# inside <impl-root> (it usually does: `<impl-root>/docs/behavior`). Without that
# every definition would read as its own implementation citation and the coverage
# report would always be 100%.
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

# sort_locs — canonical order for a multi-location list: by path, then by line
# NUMERICALLY. Sorting the whole `path:line` string lexically puts `a.md:10` ahead
# of `a.md:9`, which is a second, quieter way for the same finding to serialize two
# ways depending on which line happened to be seen first.
sort_locs() { sort -t: -k1,1 -k2,2n -u; }

STRICT=0
SET=""
IMPL=""

show_help() {
  cat <<'HELP'
impl-traces.sh: reconcile an implementation against its own behavior-docs set

Usage: impl-traces.sh [OPTIONS] <behavior-docs-set-dir> <impl-root>

Options:
  -h, --help    Show this help message
      --strict  Also fail when a set element is cited nowhere in the
                implementation (coverage), not only on a dangling citation

Classifies every behavior-docs ID the implementation cites as ok / external /
historical / FAIL, and reports which set elements the implementation never cites.
HELP
}

while [ $# -gt 0 ]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --strict) STRICT=1 ;;
  --)
    shift
    break
    ;;
  -*)
    echo "unknown option: $1" >&2
    exit 2
    ;;
  *)
    if [ -z "$SET" ]; then SET="$1"; else IMPL="${IMPL:-$1}"; fi
    ;;
  esac
  shift
done
[ -n "$SET" ] && [ -n "$IMPL" ] || {
  show_help >&2
  exit 2
}
[ -d "$SET" ] && [ -d "$IMPL" ] || {
  echo "both arguments must be directories" >&2
  exit 2
}

IDPAT='(INV|GOAL|STORY|USECASE|JOURNEY|INTF|ACTOR|OQ)-[A-Za-z0-9]+(-[A-Za-z0-9]+)*'

defined=$(
  awk -v idpat="$IDPAT" '
    BEGIN { defpat = "^[ \t]*(([-*+][ \t]+)|(#+[ \t]+))[*_`]*" idpat }
    $0 ~ defpat { if (match($0, idpat) > 0) print substr($0, RSTART, RLENGTH) }
  ' "$SET"/*.md 2>/dev/null | sort -u
)
imported=$(
  awk '
    FNR==1 { insec=0 }
    toupper($0) ~ /^##[[:space:]]+EXTERNAL REFERENCES/ { insec=1; next }
    /^##[[:space:]]/ && insec { insec=0 }
    insec { print }
  ' "$SET"/*.md 2>/dev/null |
    awk -F'|' -v idpat="$IDPAT" '
      /^\|/ { if (match($2, idpat) > 0) print substr($2, RSTART, RLENGTH) }' | sort -u
)

# Resolve the set dir to an absolute path so the -prune below matches regardless
# of how either argument was spelled on the command line.
set_abs=$(cd "$SET" && pwd)
impl_abs=$(cd "$IMPL" && pwd)

# Citations, one per occurrence: `<ID><TAB><path>:<line><TAB><historical?>`.
# A citation is HISTORICAL when its own line frames the ID as gone. The framing
# words are matched on the LINE, not the file, because one file routinely carries
# both live and historical citations.
citations=$(
  find "$impl_abs" \
    \( -path "$set_abs" -o -name .git -o -name node_modules -o -name vendor -o -name result \) -prune -o \
    -type f \( -name '*.go' -o -name '*.rs' -o -name '*.ts' -o -name '*.py' -o -name '*.sh' \
    -o -name '*.bash' -o -name '*.bats' -o -name '*.nix' -o -name '*.md' -o -name '*.proto' \
    -o -name '*.sql' -o -name '*.yaml' -o -name '*.yml' -o -name '*.toml' \) -print0 2>/dev/null |
    sort -z |
    # SC2016 (info) fires on the awk program below: its `$0` and `$3` are awk
    # FIELDS and MUST NOT be expanded by the shell, so the single quotes are
    # deliberate. Values that do come from the shell are passed with `-v`.
    xargs -0 -r awk -v idpat="$IDPAT" -v root="$impl_abs/" '
      {
        low = tolower($0)
        hist = (low ~ /former|formerly|removed|deleted|resolved|superseded|no longer/) ? "historical" : "live"
        rel = FILENAME
        sub("^" root, "", rel)
        s = $0
        while (match(s, idpat) > 0) {
          printf "%s\t%s:%d\t%s\n", substr(s, RSTART, RLENGTH), rel, FNR, hist
          s = substr(s, RSTART + RLENGTH)
        }
      }'
)

known=$(printf '%s\n%s\n' "$defined" "$imported" | { grep -v '^$' || true; } | sort -u)

# resolves <id> — exact, or a FAMILY reference: the token is a proper prefix of a
# known ID. `INV-EVT-*` tokenizes to the bare family name INV-EVT, which is
# nobody's definition but is a legitimate way for code to cite a whole family.
resolves() {
  printf '%s\n' "$known" | grep -qE -- "^$1(-|\$)"
}

fail=0
printf '=== IMPL trace reconciliation: %s  <->  %s ===\n' "$IMPL" "$SET"
printf '  set defines %s element(s), declares %s import(s)\n' \
  "$(printf '%s\n' "$defined" | grep -c . || true)" \
  "$(printf '%s\n' "$imported" | grep -c . || true)"

cited_ids=$(printf '%s\n' "$citations" | cut -f1 | { grep -v '^$' || true; } | sort -u)

printf '\n--- Implementation citations ---\n'
if [ -z "$cited_ids" ]; then
  echo "  NOTICE: the implementation cites no behavior-docs ID at all — the impl seam is unchecked (INTF-1)"
else
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    n=$(printf '%s\n' "$citations" | awk -F'\t' -v i="$id" '$1 == i' | grep -c . || true)
    if printf '%s\n' "$defined" | grep -qE -- "^$id(-|$)"; then
      printf '  ok         %-18s (%s citation(s))\n' "$id" "$n"
    elif resolves "$id"; then
      printf '  external   %-18s (%s citation(s); declared in the imports table)\n' "$id" "$n"
    else
      # Every occurrence must be historical for the ID to be excused: one live
      # citation of a dead ID is a stale citation regardless of how many comments
      # explain its history.
      live=$(printf '%s\n' "$citations" |
        awk -F'\t' -v i="$id" '$1 == i && $3 == "live" { print $2 }' | sort_locs | tr '\n' ' ')
      if [ -z "${live// /}" ]; then
        printf '  historical %-18s (%s citation(s), all framed as removed/resolved — INV-4 leaves no tombstone)\n' "$id" "$n"
      else
        printf '  FAIL       %-18s (%s) resolves to no definition in the set and no declared import\n' "$id" "${live% }"
        fail=1
      fi
    fi
  done <<<"$cited_ids"
fi

printf '\n--- Coverage: set elements the implementation never cites ---\n'
# Only contract-bearing families: a story or a journey is a user-facing arc, not
# something code cites, so demanding a citation for one would be noise.
uncited=$(
  comm -23 \
    <(printf '%s\n' "$defined" | { grep -E '^(INV|INTF|GOAL)-' || true; } | sort -u) \
    <(printf '%s\n' "$cited_ids" | { grep -v '^$' || true; } | sort -u)
)
if [ -z "$uncited" ]; then
  echo "  clean (every invariant, interface and goal is cited at least once)"
else
  lead="NOTICE"
  if [ "$STRICT" -eq 1 ]; then
    lead="FAIL"
    fail=1
  fi
  printf '  %s %s of %s contract elements are cited nowhere in the implementation: %s\n' \
    "$lead" \
    "$(printf '%s\n' "$uncited" | grep -c .)" \
    "$(printf '%s\n' "$defined" | { grep -cE '^(INV|INTF|GOAL)-' || true; })" \
    "$(
      u=$(printf '%s\n' "$uncited" | tr '\n' ' ')
      printf '%s' "${u% }"
    )"
fi

printf '\n'
if [ "$fail" -ne 0 ]; then
  echo "IMPL: FAIL — see the FAIL lines above"
  exit 1
fi
echo "IMPL: OK (every implementation citation resolves)"
