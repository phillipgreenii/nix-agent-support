#!/usr/bin/env bats
# bats file_tags=type:integration
#
# Tests the minted-by backfill against the REAL `bd` in a hermetic throwaway
# embedded database (V-3; no mock of bd's label handling): every test gets a
# fresh database in its own temp dir, with BEADS_*/BD_* scrubbed and `bd where`
# asserted to resolve inside it before any write. The only stand-in is a PATH
# shim that execs the real bd, used to log argv and to force one write to fail.

bats_require_minimum_version 1.5.0

# SCRIPTS_DIR / TEST_SUPPORT: injected by the nix check, or computed relative
# to this test for a local `bats tests/` run.
if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi
if [[ -z ${TEST_SUPPORT:-} ]]; then
  TEST_SUPPORT="$(cd "${SCRIPTS_DIR}/../test-support" && pwd)"
fi
load "$TEST_SUPPORT/bd_fixture"

LABEL="minted-by:pg-decider"

setup() {
  scrub_git_env
  scrub_bd_env
  TDIR="$(mktemp -d)"
  TDIR="$(cd "$TDIR" && pwd -P)"
  export TDIR HOME="$TDIR"
  (cd "$TDIR" && bd init --prefix tst >/dev/null 2>&1)
  [ -d "$TDIR/.beads" ]
  bd_fixture_assert_isolated
  cat >"$TDIR/run" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "${SCRIPTS_DIR}/pg-decider-backfill-minted-by.bash"
source "${SCRIPTS_DIR}/pg-decider-backfill-minted-by.sh"
WRAPPER
  chmod +x "$TDIR/run"
  RUN="$TDIR/run"
  cd "$TDIR" || return 1
}

teardown() {
  for _ in 1 2 3 4 5; do
    rm -rf "$TDIR" "${SHIM_DIR:-}" 2>/dev/null && return 0
  done
}

# mk <title> <metadata-json> [labels-csv] [parent]: prints the new id.
mk() {
  local -a args=("$1" -t task --silent --metadata "$2")
  [ -n "${3:-}" ] && args+=(--labels "$3")
  [ -n "${4:-}" ] && args+=(--parent "$4")
  bd -C "$TDIR" create "${args[@]}"
}

key() { printf '{"dedup_key":"%s","keep":"me"}' "$1"; }

labels_of() { # id -> sorted labels, comma separated
  bd_fixture_show_json "$1" | jq -r '(.labels // []) | sort | join(",")'
}

has_label() { [[ ,$(labels_of "$1"), == *,"$LABEL",* ]]; }

# lacks_label <id>: fails the test when the bead carries the label. A bare
# `! has_label` would NOT fail a bats test (negated commands are exempt from
# errexit), so the negation lives inside a function.
lacks_label() { ! has_label "$1"; }

backfill() { "$RUN" --bd-dir "$TDIR" --actor test-actor "$@"; }

# install_shim: a bd shim first on PATH that logs argv to $SHIM_DIR/calls.log,
# then execs the real bd. With FAIL_ID set, an `update` of that id fails.
install_shim() {
  local real_bd
  real_bd="$(command -v bd)"
  SHIM_DIR="$(mktemp -d)"
  export SHIM_DIR
  cat >"$SHIM_DIR/bd" <<SHIM
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$SHIM_DIR/calls.log"
for a in "\$@"; do
  if [ "\$a" = update ] && [ -n "\${FAIL_ID:-}" ]; then
    for b in "\$@"; do [ "\$b" = "\$FAIL_ID" ] && { echo "forced failure" >&2; exit 1; }; done
  fi
done
exec "$real_bd" "\$@"
SHIM
  chmod +x "$SHIM_DIR/bd"
  PATH="$SHIM_DIR:$PATH"
  export PATH
}

@test "dry-run is the default: prints target and plan lines and writes nothing" {
  a="$(mk "org/repo#1: add thing" "$(key pr:N:anchor)" "mine")"
  c="$(mk "review-pr: org/repo#1" "$(key pr:N:review-pr:abc)" "" "$a")"
  install_shim
  run --separate-stderr backfill
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "$(printf 'TARGET\t%s' "$TDIR/.beads")" ]
  [[ $output == *$'PLAN\t'"$a"$'\tanchor\topen\torg/repo#1: add thing'* ]]
  [[ $output == *$'PLAN\t'"$c"$'\treview-pr\topen\treview-pr: org/repo#1'* ]]
  [[ $output == *$'COUNT\tanchor\t1'* ]]
  [[ $output == *$'COUNT\treview-pr\t1'* ]]
  [[ $output == *"SUMMARY mode=dry-run skipped=0 planned=2 residual=0 updated=0 failed=0"* ]]
  lacks_label "$a"
  lacks_label "$c"
  run ! grep -E ' update ' "$SHIM_DIR/calls.log"
}

@test "--apply adds the label to every kind, preserves other labels and metadata, reads back" {
  ids=()
  for k in anchor process-feedback review-pr fix-ci resolve-conflict focus-item; do
    ids+=("$(mk "$k bead" "$(key "pr:N:$k")" "mine,zeta")")
  done
  run --separate-stderr backfill --apply --expect 6
  [ "$status" -eq 0 ]
  [[ $output == *"updated=6"* ]]
  [[ $output == *"failed=0"* ]]
  for id in "${ids[@]}"; do
    [ "$(labels_of "$id")" = "mine,$LABEL,zeta" ]
    [ "$(bd_fixture_show_json "$id" | jq -r .metadata.keep)" = "me" ]
  done
}

@test "a keyed bead is labelled whether open or closed" {
  o="$(mk "open one" "$(key pr:N:review-pr:a)")"
  c="$(mk "closed one" "$(key pr:N:review-pr:b)")"
  bd -C "$TDIR" close "$c" >/dev/null
  run --separate-stderr backfill --apply --expect 2
  [ "$status" -eq 0 ]
  has_label "$o"
  has_label "$c"
  [ "$(bd_fixture_show_json "$c" | jq -r .status)" = "closed" ]
}

@test "a dedup_key with an unknown kind or too few fields is a residual, never labelled" {
  good="$(mk "good" "$(key pr:N:anchor)")"
  badkind="$(mk "bad kind" "$(key pr:N:mystery)")"
  short="$(mk "short key" "$(key pr:N)")"
  run --separate-stderr backfill --apply
  [ "$status" -eq 0 ]
  has_label "$good"
  lacks_label "$badkind"
  lacks_label "$short"
  [[ $output == *$'RESIDUAL\tmalformed-key\topen\t2'* ]]
  [[ $output == *$'RESIDUAL-OPEN\t'"$badkind"$'\tmalformed-key\topen\tbad kind'* ]]
}

@test "an escalation bead is labelled when its title is the decider's and its parent is a keyed anchor" {
  a="$(mk "org/repo#2: thing" "$(key pr:N:anchor)")"
  r="$(mk "pg-decider: rule all.closed keeps failing on pr org/repo#2" '{}' "human" "$a")"
  n="$(mk "pr not found: org/repo#2" '{}' "human" "$a")"
  run --separate-stderr backfill --apply
  [ "$status" -eq 0 ]
  [[ $output == *$'COUNT\tescalation-rule\t1'* ]]
  [[ $output == *$'COUNT\tescalation-notfound\t1'* ]]
  [ "$(labels_of "$r")" = "human,$LABEL" ]
  [ "$(labels_of "$n")" = "human,$LABEL" ]
}

@test "an escalation-titled bead without a keyed anchor parent is a residual, never labelled" {
  a="$(mk "org/repo#3: thing" "$(key pr:N:anchor)")"
  orphan="$(mk "pg-decider: rule x keeps failing on pr org/repo#3" '{}' "human")"
  keyless_parent="$(mk "plain parent" '{}')"
  under_plain="$(mk "pr not found: org/repo#3" '{}' "human" "$keyless_parent")"
  child="$(mk "review-pr: org/repo#3" "$(key pr:N:review-pr:c)" "" "$a")"
  under_child="$(mk "pg-decider: rule y keeps failing on pr org/repo#3" '{}' "human" "$child")"
  run --separate-stderr backfill --apply
  [ "$status" -eq 0 ]
  lacks_label "$orphan"
  lacks_label "$under_plain"
  lacks_label "$under_child"
  [[ $output == *$'RESIDUAL\tescalation-no-anchor-parent\topen\t3'* ]]
}

@test "keyless look-alikes are reported, never labelled; open ones are itemised, closed ones counted" {
  keyed="$(mk "org/repo#4: real" "$(key pr:N:anchor)")"
  kt_open="$(mk "process-feedback: org/repo#9" '{"repo":"org/repo","pr_number":9}')"
  kt_closed="$(mk "fix-ci: org/repo#9" '{"repo":"org/repo","pr_number":9}')"
  at_open="$(mk "org/repo#9: legacy anchor" '{"repo":"org/repo","pr_number":9}')"
  at_closed="$(mk "org/repo#8: old anchor" '{"repo":"org/repo","pr_number":8}')"
  unrelated="$(mk "an ordinary task" '{}')"
  bd -C "$TDIR" close "$kt_closed" >/dev/null
  bd -C "$TDIR" close "$at_closed" >/dev/null
  run --separate-stderr backfill --apply --expect 1
  [ "$status" -eq 0 ]
  has_label "$keyed"
  for id in "$kt_open" "$kt_closed" "$at_open" "$at_closed" "$unrelated"; do lacks_label "$id"; done
  [[ $output == *$'RESIDUAL\tkeyless-kind-title\topen\t1'* ]]
  [[ $output == *$'RESIDUAL\tkeyless-kind-title\tclosed\t1'* ]]
  [[ $output == *$'RESIDUAL\tkeyless-anchor-title\topen\t1'* ]]
  [[ $output == *$'RESIDUAL\tkeyless-anchor-title\tclosed\t1'* ]]
  [[ $output == *$'RESIDUAL-OPEN\t'"$kt_open"$'\tkeyless-kind-title\topen\t'* ]]
  [[ $output == *$'RESIDUAL-OPEN\t'"$at_open"$'\tkeyless-anchor-title\topen\t'* ]]
  [[ $output != *$'RESIDUAL-OPEN\t'"$kt_closed"* ]]
  [[ $output != *$'RESIDUAL-OPEN\t'"$at_closed"* ]]
  [[ $output == *"residual=4"* ]]
}

@test "idempotent: a bead that already carries the label is skipped and a second --apply writes nothing" {
  a="$(mk "already" "$(key pr:N:anchor)" "$LABEL")"
  b="$(mk "not yet" "$(key pr:N:review-pr:z)")"
  run --separate-stderr backfill --apply
  [ "$status" -eq 0 ]
  [[ $output == *"skipped=1 planned=1"* ]]
  [[ $output == *"updated=1"* ]]
  [ "$(labels_of "$a")" = "$LABEL" ]
  has_label "$b"
  install_shim
  run --separate-stderr backfill --apply
  [ "$status" -eq 0 ]
  [[ $output == *"skipped=2 planned=0"* ]]
  [[ $output == *"updated=0"* ]]
  run ! grep -E ' update ' "$SHIM_DIR/calls.log"
}

@test "--expect that differs from the planned count exits 1 and writes nothing" {
  a="$(mk "one" "$(key pr:N:anchor)")"
  install_shim
  run --separate-stderr backfill --apply --expect 5
  [ "$status" -eq 1 ]
  [[ $stderr == *"planned 1 beads but --expect is 5"* ]]
  lacks_label "$a"
  run ! grep -E ' update ' "$SHIM_DIR/calls.log"
}

@test "a tracker with no decider beads at all exits 1 and writes nothing" {
  t="$(mk "an ordinary task" '{}')"
  run --separate-stderr backfill --apply
  [ "$status" -eq 1 ]
  [[ $stderr == *"no decider beads found"* ]]
  lacks_label "$t"
}

@test "--apply stops at the first failing write and reports it" {
  ids=()
  for i in 1 2 3; do ids+=("$(mk "bead $i" "$(key "pr:N:review-pr:$i")")"); done
  install_shim
  FAIL_ID="${ids[1]}" run --separate-stderr backfill --apply
  [ "$status" -eq 1 ]
  [[ $stderr == *"FAIL ${ids[1]}"* ]]
  [[ $output == *"failed=1"* ]]
  # bd's listing order is not specified, so how many beads came before the
  # failing one is not fixed: the report's `updated` count must equal the beads
  # actually labelled, the failing bead stays unlabelled, and the run stopped
  # there (at most the other two were labelled)
  lacks_label "${ids[1]}"
  n=0
  for id in "${ids[@]}"; do has_label "$id" && n=$((n + 1)); done
  [ "$n" -le 2 ]
  [[ $output == *"updated=$n "* ]]
  # a re-run without the fault completes the remainder and nothing else
  run --separate-stderr backfill --apply
  [ "$status" -eq 0 ]
  [[ $output == *"skipped=$n planned=$((3 - n))"* ]]
  for id in "${ids[@]}"; do has_label "$id"; done
}

@test "refuses a --bd-dir that is not a directory" {
  run --separate-stderr "$RUN" --bd-dir "$TDIR/nope" --actor a
  [ "$status" -eq 1 ]
  [[ $stderr == *"is not a directory"* ]]
}

@test "usage errors exit 2: missing --bd-dir, missing --actor, unknown flag, bad --expect" {
  run --separate-stderr "$RUN" --actor a
  [ "$status" -eq 2 ]
  run --separate-stderr "$RUN" --bd-dir "$TDIR"
  [ "$status" -eq 2 ]
  run --separate-stderr "$RUN" --bd-dir "$TDIR" --actor a --nope
  [ "$status" -eq 2 ]
  run --separate-stderr "$RUN" --bd-dir "$TDIR" --actor a --expect x
  [ "$status" -eq 2 ]
}

@test "--help prints the usage and exits 0" {
  run --separate-stderr "$RUN" --help
  [ "$status" -eq 0 ]
  [[ $output == *"Usage: pg-decider-backfill-minted-by"* ]]
}
