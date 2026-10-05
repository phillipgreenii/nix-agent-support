#!/usr/bin/env bats
# bats file_tags=type:integration
# Drift check between the pb command markdown (canonical for agents) and queues.json (the
# machine-readable mirror), plus automated mutants proving the check bites.
#
# Environment (set by the flake check, or by hand for a local run):
#   PBQ_CHECK  path to tests/pb-queues-mirror-check.sh
#   PBQ_PB_DIR path to claude-marketplace/pb (holds queues.json and commands/)

setup() {
  : "${PBQ_CHECK:?}" "${PBQ_PB_DIR:?}"
  work="$(mktemp -d)"
  cp "$PBQ_PB_DIR/queues.json" "$work/queues.json"
  cp "$PBQ_PB_DIR/commands/drain-beads.md" "$work/drain-beads.md"
  cp "$PBQ_PB_DIR/commands/unblock-human-beads.md" "$work/unblock-human-beads.md"
  chmod u+w "$work"/*
}

teardown() {
  rm -rf "$work"
}

check() {
  run bash "$PBQ_CHECK" "$work/queues.json" "$work/drain-beads.md" "$work/unblock-human-beads.md"
}

# Literal (non-regex) replace of the first occurrence of $2 by $3 in file $1; fails when $2 is
# absent so a stale mutant cannot silently become a no-op.
mutate() {
  awk -v pat="$2" -v rep="$3" '
    !done && (i = index($0, pat)) {
      $0 = substr($0, 1, i - 1) rep substr($0, i + length(pat)); done = 1
    }
    { print }
    END { exit done ? 0 : 3 }
  ' "$1" >"$1.new"
  mv "$1.new" "$1"
}

# Delete the first line containing $2 from file $1.
delete_line() {
  awk -v pat="$2" '!done && index($0, pat) { done = 1; next } { print } END { exit done ? 0 : 3 }' \
    "$1" >"$1.new"
  mv "$1.new" "$1"
}

@test "real files agree" {
  check
  [ "$status" -eq 0 ]
}

@test "queues.json has exactly the four expected names" {
  run jq -e 'length == 4 and (map(.name) | sort == ["drain-claim","drain-termination","unblock-human","unblock-human-focus"])' "$work/queues.json"
  [ "$status" -eq 0 ]
}

@test "exactly one marker per queue name across the two files" {
  for n in drain-claim drain-termination unblock-human unblock-human-focus; do
    c="$(cat "$work/drain-beads.md" "$work/unblock-human-beads.md" | grep -c "<!-- pb-queue:$n -->")"
    [ "$c" -eq 1 ]
  done
}

# --- mutants that MUST fail ----------------------------------------------------------------

@test "mutant: deleted marker fails" {
  delete_line "$work/drain-beads.md" "pb-queue:drain-claim"
  check
  [ "$status" -ne 0 ]
  [[ "$output" == *"drain-claim"* ]]
}

@test "mutant: duplicated marker fails" {
  printf '\n<!-- pb-queue:unblock-human -->\n```bash\nbd ready --label human --exclude-label refactor-campaign,human-focus-required --exclude-type handoff --json\n```\n' \
    >>"$work/drain-beads.md"
  check
  [ "$status" -ne 0 ]
  [[ "$output" == *"unblock-human"* ]]
}

@test "mutant: marker not followed by a fence fails" {
  mutate "$work/drain-beads.md" "<!-- pb-queue:drain-termination -->" "<!-- pb-queue:drain-termination -->\nnot a fence"
  check
  [ "$status" -ne 0 ]
}

@test "mutant: unknown marker name fails" {
  printf '\n<!-- pb-queue:bogus -->\n```bash\nbd ready --json\n```\n' >>"$work/drain-beads.md"
  check
  [ "$status" -ne 0 ]
  [[ "$output" == *"bogus"* ]]
}

@test "mutant: changed label value in markdown fails" {
  mutate "$work/drain-beads.md" "--exclude-label human,human-focus-required,refactor-campaign --exclude-type epic --json" "--exclude-label human,refactor-campaign --exclude-type epic --json"
  check
  [ "$status" -ne 0 ]
  [[ "$output" == *"drain-claim"* ]]
}

@test "mutant: changed type value in markdown fails" {
  mutate "$work/unblock-human-beads.md" "--exclude-type handoff --json" "--exclude-type task --json"
  check
  [ "$status" -ne 0 ]
}

@test "mutant: extra flag in markdown fails" {
  mutate "$work/drain-beads.md" "--exclude-type epic --json" "--exclude-type epic --priority 0 --json"
  check
  [ "$status" -ne 0 ]
}

@test "mutant: changed flag in queues.json fails" {
  jq '(.[] | select(.name == "unblock-human-focus") | .args[0]) = "--exclude-label"' "$work/queues.json" >"$work/q.new"
  mv "$work/q.new" "$work/queues.json"
  check
  [ "$status" -ne 0 ]
  [[ "$output" == *"unblock-human-focus"* ]]
}

# --- mutants that MUST pass ----------------------------------------------------------------

@test "reordered flags pass" {
  mutate "$work/drain-beads.md" "--exclude-label human,human-focus-required,refactor-campaign --exclude-type epic --json" "--exclude-type epic --exclude-label refactor-campaign,human,human-focus-required --json"
  check
  [ "$status" -eq 0 ]
}

@test "reordered flags in queues.json pass" {
  jq '(.[] | select(.name == "unblock-human") | .args) = ["--exclude-type","handoff","--exclude-label","human-focus-required,refactor-campaign","--label","human"]' "$work/queues.json" >"$work/q.new"
  mv "$work/q.new" "$work/queues.json"
  check
  [ "$status" -eq 0 ]
}

@test "-n difference passes" {
  mutate "$work/drain-beads.md" "--json -n 10" "--json -n 3"
  check
  [ "$status" -eq 0 ]
}

@test "dropped --json passes" {
  mutate "$work/drain-beads.md" "--exclude-type epic --json" "--exclude-type epic"
  check
  [ "$status" -eq 0 ]
}

@test "added --claim passes" {
  mutate "$work/unblock-human-beads.md" "--exclude-type handoff --json" "--exclude-type handoff --claim --json"
  check
  [ "$status" -eq 0 ]
}

@test "wrapped lines with backslash continuations pass" {
  awk '
    !done && index($0, "bd ready --exclude-label human,human-focus-required,refactor-campaign --exclude-type epic --json") {
      print "   bd ready --exclude-label human,human-focus-required,refactor-campaign \\"
      print "     --exclude-type epic \\"
      print "     --json"
      done = 1; next
    }
    { print }
  ' "$work/drain-beads.md" >"$work/d.new"
  mv "$work/d.new" "$work/drain-beads.md"
  grep -q -- '--exclude-type epic \\$' "$work/drain-beads.md"
  check
  [ "$status" -eq 0 ]
}
