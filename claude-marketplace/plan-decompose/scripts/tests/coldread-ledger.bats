# bats file_tags=type:unit

SCRIPT="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)/coldread-ledger.sh"

setup() {
  TEST_DIR="$(mktemp -d)"
  L="$TEST_DIR/ledger"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# Cold-reader answers in the agent's output format.
answer_yes() {
  printf '1. `executable: yes`\n2. `missing:`\n   - none\n3. `unpinned-assumptions:`\n   - none\n' >"$1"
}
# Only an implementer-owned choice (a Go identifier) is open: not a blocker.
answer_owned_only() {
  cat >"$1" <<'A'
executable: no
missing:
- [implementer-owned] the Go identifier names for the new types
- [implementer-owned] the test file name
unpinned-assumptions:
- none
A
}
# A genuinely undecided seam: the band-edge inclusivity is in neither text.
answer_undecided_seam() {
  cat >"$1" <<'A'
executable: no
missing:
- [implementer-owned] the helper's file name
- [decision] whether a value exactly on a band edge belongs to the lower or the upper band
unpinned-assumptions:
- none
A
}

@test "verdict: an implementer-owned choice alone is advisory, an undecided seam is blocking" {
  answer_owned_only "$TEST_DIR/a"
  answer_undecided_seam "$TEST_DIR/b"
  answer_yes "$TEST_DIR/c"
  run "$SCRIPT" verdict "$TEST_DIR/a"
  [ "$output" = "advisory" ]
  run "$SCRIPT" verdict "$TEST_DIR/b"
  [ "$output" = "blocking" ]
  run "$SCRIPT" verdict "$TEST_DIR/c"
  [ "$output" = "ok" ]
}

@test "verdict: an untagged missing item is conservatively blocking" {
  printf 'executable: no\nmissing:\n- the retry count\n' >"$TEST_DIR/a"
  run "$SCRIPT" verdict "$TEST_DIR/a"
  [ "$output" = "blocking" ]
}

@test "verdict: an answer with no executable line is blocking" {
  printf 'I could not tell.\n' >"$TEST_DIR/a"
  run "$SCRIPT" verdict "$TEST_DIR/a"
  [ "$output" = "blocking" ]
}

@test "only the undecided seam blocks the gate; the implementer-owned packet passes" {
  "$SCRIPT" init "$L" p1 p2 p3
  answer_yes "$TEST_DIR/y"
  answer_owned_only "$TEST_DIR/o"
  answer_undecided_seam "$TEST_DIR/u"
  "$SCRIPT" read "$L" p1 "$TEST_DIR/y"
  "$SCRIPT" read "$L" p2 "$TEST_DIR/o"
  "$SCRIPT" read "$L" p3 "$TEST_DIR/u"
  run "$SCRIPT" gate "$L"
  [ "$status" -eq 4 ]
  [[ "$output" == *"p3"* ]]
  [[ "$output" != *"p2"* ]]
  run "$SCRIPT" report "$L"
  [[ "$output" == *"blocking=1"* ]]
  [[ "$output" == *"advisory=1"* ]]
}

@test "refused dispatches are not reads: they retry, and the report states the true count" {
  keys=(k1 k2 k3 k4 k5 k6 k7)
  "$SCRIPT" init "$L" "${keys[@]}"
  answer_yes "$TEST_DIR/y"

  # round 1: batch of 4; the dispatcher refuses the last 2
  mapfile -t b1 < <("$SCRIPT" batch "$L" 4)
  [ "${#b1[@]}" -eq 4 ]
  for k in "${b1[@]}"; do "$SCRIPT" dispatched "$L" "$k"; done
  "$SCRIPT" read "$L" "${b1[0]}" "$TEST_DIR/y"
  "$SCRIPT" read "$L" "${b1[1]}" "$TEST_DIR/y"
  "$SCRIPT" refused "$L" "${b1[2]}"
  "$SCRIPT" refused "$L" "${b1[3]}"

  run "$SCRIPT" report "$L"
  [[ "$output" == *"total=7 read=2 unread=5 dispatch-attempts=4 refused-dispatches=2"* ]]

  # the refused two are offered again before any release
  run "$SCRIPT" gate "$L"
  [ "$status" -eq 3 ]
  [[ "$output" == *"${b1[2]}"* && "$output" == *"${b1[3]}"* ]]

  # drain the rest in batches until nothing is left
  while mapfile -t b < <("$SCRIPT" batch "$L" 4) && [ "${#b[@]}" -gt 0 ]; do
    for k in "${b[@]}"; do
      "$SCRIPT" dispatched "$L" "$k"
      "$SCRIPT" read "$L" "$k" "$TEST_DIR/y"
    done
  done
  run "$SCRIPT" gate "$L"
  [ "$status" -eq 0 ]
  run "$SCRIPT" report "$L"
  [[ "$output" == *"total=7 read=7 unread=0 dispatch-attempts=9 refused-dispatches=2"* ]]
}

@test "a re-read after an edit replaces the earlier verdict; a refusal clears a stale read" {
  "$SCRIPT" init "$L" p1
  answer_undecided_seam "$TEST_DIR/u"
  answer_yes "$TEST_DIR/y"
  "$SCRIPT" read "$L" p1 "$TEST_DIR/u"
  "$SCRIPT" read "$L" p1 "$TEST_DIR/y"
  run "$SCRIPT" gate "$L"
  [ "$status" -eq 0 ]
  "$SCRIPT" refused "$L" p1
  run "$SCRIPT" gate "$L"
  [ "$status" -eq 3 ]
}

@test "usage errors: unknown key, unknown subcommand, bad batch size" {
  "$SCRIPT" init "$L" p1
  run "$SCRIPT" refused "$L" nope
  [ "$status" -eq 2 ]
  run "$SCRIPT" bogus
  [ "$status" -eq 2 ]
  run "$SCRIPT" batch "$L" 0
  [ "$status" -eq 2 ]
  run "$SCRIPT" gate "$TEST_DIR/missing"
  [ "$status" -eq 2 ]
}
