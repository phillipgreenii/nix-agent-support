#!/usr/bin/env bash
# Line accounting for the pb:drain-one extraction (bead pg2-nk6th.4).
#
# Proves that every line of the pre-extraction drain-beads.md appears exactly
# once across the slimmed command, the pb:drain-one skill and its references, or
# is a deliberately edited line named in the allowlist below.
#
# Usage: line-accounting.sh [BASE_REV]
#   BASE_REV defaults to the commit the extraction branched from. The allowlist
#   holds line numbers of THAT revision's drain-beads.md.
#
# Lines are compared after collapsing whitespace and dropping blanks, and after
# mapping the old branch name `drain/<id>` to the new `<work-branch>` variable.
# Counts are a multiset, so repeated lines (code fences, `-`) must keep their
# multiplicity: a line that appears more times in the new files than in the old
# file shows up as ADDED.
#
# Exit codes: 0 every missing old line is allowlisted; 1 the script could not run
# (a file is missing, git failed); 2 some old line is unaccounted for. The ADDED
# list is for a person to read; it never changes the exit code.

set -euo pipefail

base="${1:-07890223}"
root="$(git rev-parse --show-toplevel)"
pb="claude-marketplace/pb"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

git -C "$root" show "${base}:${pb}/commands/drain-beads.md" >"$tmp/old.md"

# New files, in a stable order, as repo-relative paths.
new_files=(
  "${pb}/commands/drain-beads.md"
  "${pb}/skills/drain-one/SKILL.md"
  "${pb}/skills/drain-one/references/delegate.md"
  "${pb}/skills/drain-one/references/land.md"
  "${pb}/skills/drain-one/references/post-deploy-gate.md"
  "${pb}/skills/drain-one/references/rules.md"
)
for f in "${new_files[@]}"; do
  if [ ! -f "${root}/${f}" ]; then
    echo "FAIL: expected file missing: ${f}" >&2
    exit 1
  fi
done

# Old line numbers whose text was deliberately EDITED (or split) rather than
# moved verbatim: one "NUMBER reason" per line.
cat >"$tmp/allow.txt" <<'EOF'
179 Startup step 4 now applies pb:drain-one to a resumed bead (command side).
180 Startup step 4 now applies pb:drain-one to a resumed bead (command side).
366 Epic drill-down step 1: the command keeps its own copy of the children-existence probe.
372 Epic drill-down step 1: an undecomposed epic is handed to pb:drain-one with the probe settled.
373 Epic drill-down step 1: line re-wrapped after the edit above (text kept).
402 Epic drill-down step 3: the claimed descendant is handed to pb:drain-one with the probe settled.
403 Epic drill-down step 3: same edit as above.
404 Epic drill-down step 3: same edit as above (line re-wrapped, text kept).
430 UNDERSTAND (handoff bead): returns to the caller with outcome closed instead of returning to CLAIM.
488 DELEGATE curated path: the claim is held "from the caller", not from step 1.
545 STAMP REFUSAL: returns to the caller with outcome released instead of returning to CLAIM.
767 LAND pinned-session case: returns to the caller with outcome released; release policy park PARKs instead.
914 FINISH: the gate section is now references/post-deploy-gate.md, not "below".
917 Step 8 "Go to 1" becomes "Return to the caller with the outcome".
1016 STUCK routing: also passes the work branch to pb:drain-stuck.
1021 STUCK routing: returns to the caller with outcome released (or closed after CLOSE-AS-MOOT).
1026 Absorption trace: returns to the caller with outcome closed instead of returning to CLAIM.
1079 Rule split: the "ScheduleWakeup / --monitor-if-empty" sentence stays in the command, the rest moves to references/rules.md.
1166 Rule on a pinned session: adds the release policy park clause.
1185 Landing rule: adds the tracker-section clause (authority comes only from it).
EOF

: >"$tmp/missing.txt"
: >"$tmp/added.txt"

status=0
(
  cd "$root"
  awk -v allow="$tmp/allow.txt" -v missing="$tmp/missing.txt" -v added="$tmp/added.txt" -v oldf="$tmp/old.md" '
    function normalize(s) {
      gsub(/drain\/<id>/, "<work-branch>", s)
      gsub(/[ \t]+/, " ", s)
      sub(/^ /, "", s)
      sub(/ $/, "", s)
      return s
    }
    BEGIN {
      while ((getline line < allow) > 0) {
        n = line; sub(/ .*/, "", n)
        reason = line; sub(/^[0-9]+ /, "", reason)
        allowed[n] = reason
      }
      close(allow)

      # Pass 1: count the normalized lines of every new file.
      for (i = 1; i < ARGC; i++) {
        f = ARGV[i]
        while ((getline line < f) > 0) {
          t = normalize(line)
          if (t != "") count[t]++
        }
        close(f)
      }

      # Pass 2: walk the old file and consume one count per line.
      total_old = 0; found = 0; missed = 0; unaccounted = 0; ln = 0
      while ((getline line < oldf) > 0) {
        ln++
        t = normalize(line)
        if (t == "") continue
        total_old++
        if (count[t] > 0) { count[t]--; found++; continue }
        missed++
        if (ln in allowed) {
          printf "ALLOWED     old:%d  %s\n            -> %s\n", ln, line, allowed[ln] >> missing
        } else {
          unaccounted++
          printf "UNACCOUNTED old:%d  %s\n", ln, line >> missing
        }
      }
      close(oldf)

      # Pass 3: whatever count is left over is carried by the new files only.
      extra = 0
      for (i = 1; i < ARGC; i++) {
        f = ARGV[i]; ln = 0
        while ((getline line < f) > 0) {
          ln++
          t = normalize(line)
          if (t == "") continue
          if (count[t] > 0) { count[t]--; extra++; printf "%s:%d: %s\n", f, ln, line >> added }
        }
        close(f)
      }

      printf "old non-blank lines:            %d\n", total_old
      printf "found in the new files:         %d\n", found
      printf "missing from the new files:     %d (unaccounted: %d)\n", missed, unaccounted
      printf "new lines beyond the old file:  %d\n", extra
      exit (unaccounted > 0) ? 2 : 0
    }' "${new_files[@]}"
) || status=$?

echo
echo "== OLD LINES NOT FOUND IN THE NEW FILES (edited, split or removed) =="
cat "$tmp/missing.txt"
echo
echo "== NEW LINES NOT IN THE OLD FILE (added, rewritten or duplicated) =="
cat "$tmp/added.txt"

exit "$status"
