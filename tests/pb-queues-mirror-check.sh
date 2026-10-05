#!/usr/bin/env bash
# pb-queues-mirror-check.sh -- drift check between the pb command markdown (canonical for
# agents) and queues.json (the machine-readable mirror).
#
# Usage: pb-queues-mirror-check.sh <queues.json> <command.md> [<command.md> ...]
#
# Each canonical query in the markdown is tagged with a marker comment of the form
#   <!-- pb-queue:NAME -->
# placed on the line(s) directly above the fenced code block holding the query. The check:
#   * requires exactly one marker per queue name across ALL the given markdown files,
#     and no marker for a name that queues.json lacks (and vice versa);
#   * joins the fenced block's wrapped lines (backslash continuations and newlines);
#   * compares the flags with queues.json order-insensitively: --label,
#     --exclude-label and --exclude-type values are compared as sets;
#   * ignores --json, -n N and --claim, which do not change which beads a queue selects.
#
# Exit 0 when the markdown and the JSON agree, 1 on any drift (each reported on stderr).
set -euo pipefail

if [ "$#" -lt 2 ]; then
  echo "usage: ${0##*/} <queues.json> <command.md>..." >&2
  exit 2
fi
queues_json=$1
shift

# Canonicalise a JSON array of bd-ready arguments (everything after `bd ready`) into a sorted,
# order-insensitive JSON array of strings. Ignored: --json, --claim, -n N. Label/type flag values
# are split on commas so "--exclude-label a,b" equals "--exclude-label b --exclude-label a".
canonicalise() {
  # stdin: JSON array of tokens. stdout: canonical JSON array.
  jq -c '
    . as $in_args
    | def listflag: . == "--label" or . == "--exclude-label" or . == "--exclude-type";
      reduce range(0; length) as $i ({skip: false, out: []};
        if .skip then .skip = false
        else
          $in_args[$i] as $tok
          | if ($tok == "--json" or $tok == "--claim") then .
            elif $tok == "-n" then .skip = true
            elif ($tok | listflag) then
              ($in_args[$i + 1] // "") as $val
              | .skip = true
              | .out += [$val | split(",") | map(select(length > 0))[] | "\($tok)=\(.)"]
            else .out += [$tok]
            end
        end)
      | .out | unique'
}

rc=0
fail() {
  echo "pb-queues-mirror: $*" >&2
  rc=1
}

# Pass 1: extract (name, command) pairs from the markdown. Output lines "NAME<TAB>COMMAND",
# or "NAME<TAB>!no-fence" when a marker is not followed by a fenced block.
extract() {
  awk '
    function flush_marker() { pending = "" }
    {
      line = $0
      if (infence) {
        if (line ~ /^[ \t]*```/) {
          infence = 0
          printf "%s\t%s\n", name, cmd
          name = ""; cmd = ""
        } else {
          sub(/[ \t]*\\[ \t]*$/, "", line)
          gsub(/^[ \t]+|[ \t]+$/, "", line)
          cmd = (cmd == "" ? line : cmd " " line)
        }
        next
      }
      if (match(line, /<!-- pb-queue:[A-Za-z0-9_-]+ -->/)) {
        if (pending != "") printf "%s\t!no-fence\n", pending
        m = substr(line, RSTART, RLENGTH)
        sub(/^<!-- pb-queue:/, "", m); sub(/ -->$/, "", m)
        pending = m
        next
      }
      if (pending != "") {
        if (line ~ /^[ \t]*```/) {
          infence = 1; name = pending; pending = ""; cmd = ""
        } else if (line !~ /^[ \t]*$/) {
          printf "%s\t!no-fence\n", pending
          pending = ""
        }
      }
    }
    END { if (pending != "") printf "%s\t!no-fence\n", pending }
  ' "$1"
}

pairs=""
for md in "$@"; do
  [ -f "$md" ] || {
    fail "cannot read $md"
    continue
  }
  out="$(extract "$md")"
  [ -n "$out" ] && pairs="${pairs}${out}"$'\n'
done
pairs="${pairs%$'\n'}"

json_names="$(jq -r '.[].name' "$queues_json" | sort)"
marker_names="$(printf '%s\n' "$pairs" | cut -f1 | sed '/^$/d' | sort)"

# Exactly one marker per queue name; no unknown markers.
while IFS= read -r name; do
  [ -n "$name" ] || continue
  n="$(printf '%s\n' "$marker_names" | grep -cx -- "$name" || true)"
  [ "$n" -eq 1 ] || fail "queue '$name' has $n markers (expected exactly 1)"
done <<<"$json_names"
while IFS= read -r name; do
  [ -n "$name" ] || continue
  printf '%s\n' "$json_names" | grep -qx -- "$name" || fail "marker for unknown queue '$name'"
done < <(printf '%s\n' "$marker_names" | sort -u)

# Compare each marked command with its JSON entry.
while IFS=$'\t' read -r name cmd; do
  [ -n "$name" ] || continue
  if [ "$cmd" = "!no-fence" ]; then
    fail "marker '$name' is not directly followed by a fenced block"
    continue
  fi
  printf '%s\n' "$json_names" | grep -qx -- "$name" || continue
  case "$cmd" in
  "bd ready" | "bd ready "*) ;;
  *)
    fail "queue '$name': fenced command does not start with 'bd ready': $cmd"
    continue
    ;;
  esac
  read -r -a words <<<"${cmd#bd ready}" || true
  md_canon="$(printf '%s\n' "${words[@]+"${words[@]}"}" | jq -R . | jq -sc . | canonicalise)"
  json_canon="$(jq -c --arg n "$name" '.[] | select(.name == $n) | .args' "$queues_json" | canonicalise)"
  if [ "$md_canon" != "$json_canon" ]; then
    fail "queue '$name' drifted: markdown=$md_canon json=$json_canon"
  fi
done <<<"$pairs"

if [ "$rc" -eq 0 ]; then
  echo "pb-queues-mirror: markdown and queues.json agree"
fi
exit "$rc"
