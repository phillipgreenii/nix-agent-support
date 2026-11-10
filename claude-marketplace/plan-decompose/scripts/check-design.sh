#!/usr/bin/env bash
# check-design.sh
#
# The mechanical half of mode `check`'s decomposability pre-check (core skill
# mode `check`, step 3), run BEFORE any docket or packet is created so that a
# design amendment happens once, up front. Read-only; never calls bd.
#
# Findings (each printed as "<kind>: line <n>: <text>", exit 3 when any):
#   marker   a TBD/TODO/TBC/FIXME marker, or wording that says a decision is
#            still open ("to be decided", "undecided", "open question",
#            "decide later", "not yet decided")
#   open     a heading named "Open items" / "Open questions" with content
#   count    a stated count ("nine named tests", "3 named checks") that
#            disagrees with the list or table directly under it
#   title    the design title names a version (vNN) that disagrees with the
#            version in the design file's own name
#
# It cannot judge whether a seam is decided; it finds the mechanical signs, and
# the caller still lists every undecided seam per sketched packet (the skill).
set -euo pipefail

show_help() {
  cat <<'HELP'
check-design.sh: mechanical seam and consistency scan of a design file

Usage: check-design.sh --design <design.md>

Exit codes: 0 no findings, 3 findings (listed on stdout), 2 usage, 1 other.
HELP
}

die() {
  echo "check-design.sh: error: $1" >&2
  exit "${2:-1}"
}

DESIGN=""
while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --design)
    [[ $# -ge 2 ]] || die "--design requires a value" 2
    DESIGN="$2"
    shift 2
    ;;
  *) die "unknown option: $1" 2 ;;
  esac
done
[[ -n $DESIGN ]] || die "--design is required" 2
[[ -f $DESIGN ]] || die "design file not found: $DESIGN" 2

NAME="$(basename "$DESIGN")"

FINDINGS="$(awk -v fname="$NAME" '
function numval(w,   n, k, names) {
  w = tolower(w)
  if (w ~ /^[0-9]+$/) return w + 0
  n = split("one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty", names, " ")
  for (k = 1; k <= n; k++) if (names[k] == w) return k
  return -1
}
function islist(s) { return s ~ /^[[:space:]]*([-*+]|[0-9]+[.)])[[:space:]]+/ }
function istable(s) { return s ~ /^[[:space:]]*\|/ }
{ line[NR] = $0 }
END {
  incode = 0; openhead = 0; openhl = 0; opencontent = 0
  for (i = 1; i <= NR; i++) {
    s = line[i]
    if (s ~ /^[[:space:]]*(```|~~~)/) { incode = !incode; continue }
    if (incode) continue

    # title vs file name
    if (!titled && s ~ /^# /) {
      titled = 1
      if (match(s, /[vV][0-9]+/)) {
        tv = substr(s, RSTART + 1, RLENGTH - 1) + 0
        if (match(fname, /[vV][0-9]+/)) {
          fv = substr(fname, RSTART + 1, RLENGTH - 1) + 0
          if (tv != fv) printf "title: line %d: title says v%d but the file name %s says v%d\n", i, tv, fname, fv
        }
      }
    }

    # open-items sections
    if (s ~ /^#+[[:space:]]/) {
      if (openhead && !opencontent) { }
      if (openhead && opencontent) printf "open: line %d: section \"%s\" has %d content line(s)\n", openhl, line[openhl], opencontent
      openhead = 0; opencontent = 0
      if (tolower(s) ~ /^#+[[:space:]]+(([0-9.]+|[a-z]\.)[[:space:]]+)?open[[:space:]]+(items|questions|decisions)/) { openhead = 1; openhl = i }
    } else if (openhead && s ~ /[^[:space:]]/) {
      t = tolower(s)
      if (t !~ /^[[:space:]]*(none|n\/a|nothing)\.?[[:space:]]*$/) opencontent++
    }

    # decision markers
    t = tolower(s)
    if (s ~ /(^|[^A-Za-z0-9_])(TBD|TODO|TBC|FIXME)([^A-Za-z0-9_]|$)/ || t ~ /to be decided|undecided|open question|decide later|not yet decided|decided later/) {
      printf "marker: line %d: %s\n", i, s
    }

    # stated counts: "<number> named <noun>"
    low = tolower(s)
    if (match(low, /(^|[^a-z0-9])(one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|[0-9]+)[[:space:]]+named[[:space:]]+[a-z-]+/)) {
      frag = substr(low, RSTART, RLENGTH)
      sub(/^[^a-z0-9]/, "", frag)
      split(frag, parts, /[[:space:]]+/)
      stated = numval(parts[1])
      if (stated >= 0) {
        j = i + 1
        while (j <= NR && line[j] !~ /[^[:space:]]/) j++
        cnt = 0; tbl = 0
        if (j <= NR && (islist(line[j]) || istable(line[j]))) {
          tbl = istable(line[j])
          while (j <= NR && (islist(line[j]) || istable(line[j]))) { cnt++; j++ }
          if (tbl) cnt -= 2
          if (cnt != stated) printf "count: line %d: says \"%s\" but the list or table under it has %d item(s)\n", i, frag, cnt
        }
      }
    }
  }
  if (openhead && opencontent) printf "open: line %d: section \"%s\" has %d content line(s)\n", openhl, line[openhl], opencontent
}
' "$DESIGN")"

if [[ -n $FINDINGS ]]; then
  printf '%s\n' "$FINDINGS"
  exit 3
fi
echo "check-design.sh: no findings"
