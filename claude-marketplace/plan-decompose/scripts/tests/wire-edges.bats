# bats file_tags=type:unit

SCRIPT="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)/wire-edges.sh"

setup() {
  TEST_DIR="$(mktemp -d)"
  MOCK_BIN="$TEST_DIR/bin"
  mkdir -p "$MOCK_BIN"
  PATH="$MOCK_BIN:$PATH"
  STORE="$TEST_DIR/edges.store" # lines: "<issue> <depends-on>"
  CALLS="$TEST_DIR/calls.log"
  : >"$STORE"
  : >"$CALLS"
  EDGES="$TEST_DIR/edges.txt"
  export STORE CALLS
}

teardown() {
  rm -rf "$TEST_DIR"
}

# A stateful mock `bd` keeping dependencies in $STORE. MOCK_MODE:
#   good      dep add stores "<issue> <depends-on>" faithfully
#   reversed  dep add stores the edge backwards (the failure read-back exists for)
#   dropped   dep add silently stores nothing
#   flaky     the first dep add stores nothing, later ones work
#   cycle     like good, and `bd dep cycles` reports $MOCK_CYCLE_TEXT
write_mock() {
  cat >"$MOCK_BIN/bd" <<'MOCK'
#!/usr/bin/env bash
echo "$*" >>"$CALLS"
case "$1 $2" in
"dep add")
  issue="$3"; blocker=""
  while [[ $# -gt 0 ]]; do [[ "$1" == "--blocked-by" ]] && blocker="$2"; shift; done
  case "${MOCK_MODE:-good}" in
  good | cycle) echo "$issue $blocker" >>"$STORE" ;;
  reversed) echo "$blocker $issue" >>"$STORE" ;;
  dropped) ;;
  flaky) if [[ ! -e "$STORE.tried" ]]; then touch "$STORE.tried"; else echo "$issue $blocker" >>"$STORE"; fi ;;
  esac
  ;;
"dep list")
  issue="$3"
  printf '{"data":['
  first=1
  while read -r i d; do
    [[ "$i" == "$issue" ]] || continue
    [[ $first -eq 1 ]] || printf ','
    first=0
    printf '{"id":"%s","dependency_type":"blocks"}' "$d"
  done <"$STORE"
  printf ']}\n'
  ;;
"dep cycles")
  if [[ "${MOCK_MODE:-good}" == cycle ]]; then echo "$MOCK_CYCLE_TEXT"; else echo "No dependency cycles detected"; fi
  ;;
*)
  echo "mock bd: unexpected invocation: $*" >&2
  exit 99
  ;;
esac
MOCK
  chmod +x "$MOCK_BIN/bd"
}

@test "every edge is added and read back; one ok line per edge" {
  write_mock
  printf 'd.2 d.1\nd.3 d.2\n' >"$EDGES"
  MOCK_MODE=good run "$SCRIPT" --edges "$EDGES" --actor s-1
  [ "$status" -eq 0 ]
  [[ "$output" == *"ok d.2 <- d.1"* ]]
  [[ "$output" == *"ok d.3 <- d.2"* ]]
  [ "$(grep -c '^dep add' "$CALLS")" -eq 2 ]
  # a read-back (dep list on the BLOCKED id) follows every add
  [ "$(grep -c '^dep list d.2' "$CALLS")" -ge 1 ]
  [ "$(grep -c '^dep list d.3' "$CALLS")" -ge 1 ]
  grep -q -- '--actor s-1' "$CALLS"
}

@test "a reversed edge fails the run with exit 3 and is named as reversed" {
  write_mock
  printf 'd.2 d.1\n' >"$EDGES"
  MOCK_MODE=reversed run "$SCRIPT" --edges "$EDGES"
  [ "$status" -eq 3 ]
  [[ "$output" == *"REVERSED"* ]]
  [[ "$output" == *"d.2 <- d.1"* ]]
}

@test "an edge that never lands fails after exactly one retry" {
  write_mock
  printf 'd.2 d.1\n' >"$EDGES"
  MOCK_MODE=dropped run "$SCRIPT" --edges "$EDGES"
  [ "$status" -eq 3 ]
  [[ "$output" == *"failed read-back after retry"* ]]
  [ "$(grep -c '^dep add' "$CALLS")" -eq 2 ]
}

@test "a transient miss is retried once and then passes" {
  write_mock
  printf 'd.2 d.1\n' >"$EDGES"
  MOCK_MODE=flaky run "$SCRIPT" --edges "$EDGES"
  [ "$status" -eq 0 ]
  [ "$(grep -c '^dep add' "$CALLS")" -eq 2 ]
}

@test "a cycle naming one of this run's packets exits 4" {
  write_mock
  printf 'd.2 d.1\n' >"$EDGES"
  MOCK_MODE=cycle MOCK_CYCLE_TEXT="cycle: d.1 -> d.2 -> d.1" run "$SCRIPT" --edges "$EDGES"
  [ "$status" -eq 4 ]
  [[ "$output" == *"d.1 -> d.2"* ]]
}

@test "a database-global cycle that does not touch this run is noted, not fatal" {
  write_mock
  printf 'd.2 d.1\n' >"$EDGES"
  MOCK_MODE=cycle MOCK_CYCLE_TEXT="cycle: other-1 -> other-2 -> other-1" run "$SCRIPT" --edges "$EDGES"
  [ "$status" -eq 0 ]
  [[ "$output" == *"none through this run's packets"* ]]
}

@test "cycle filtering matches whole ids: d.1 does not match d.10 or d.1.5" {
  write_mock
  printf 'd.2 d.1\n' >"$EDGES"
  MOCK_MODE=cycle MOCK_CYCLE_TEXT="cycle: d.10 -> d.1.5 -> d.10" run "$SCRIPT" --edges "$EDGES"
  [ "$status" -eq 0 ]
}

@test "comments and blank lines are skipped; malformed lines are usage errors" {
  write_mock
  printf '# plan\n\nd.2 d.1\n' >"$EDGES"
  MOCK_MODE=good run "$SCRIPT" --edges "$EDGES"
  [ "$status" -eq 0 ]
  printf 'd.2\n' >"$EDGES"
  run "$SCRIPT" --edges "$EDGES"
  [ "$status" -eq 2 ]
  printf 'd.2 d.2\n' >"$EDGES"
  run "$SCRIPT" --edges "$EDGES"
  [ "$status" -eq 2 ]
}

@test "missing --edges, unknown option and an empty file are usage errors" {
  run "$SCRIPT"
  [ "$status" -eq 2 ]
  run "$SCRIPT" --bogus
  [ "$status" -eq 2 ]
  : >"$EDGES"
  write_mock
  run "$SCRIPT" --edges "$EDGES"
  [ "$status" -eq 2 ]
}
