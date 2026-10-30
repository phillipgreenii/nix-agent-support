#!/usr/bin/env bats
# bats file_tags=type:integration
#
# Tests the one-off backfill (bead pg2-eclw3j.5) against the REAL `bd` in a
# hermetic throwaway embedded database (V-3; no mock of bd's metadata merge):
# every test gets a fresh database in its own temp dir, with BEADS_*/BD_* scrubbed
# and `bd where` asserted to resolve inside it before any write. The only
# stand-in is a PATH shim that execs the real bd, used to log argv, to force one
# write to fail, and to answer `list` with nothing.

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
source "${SCRIPTS_DIR}/pg-decider-backfill-pr-metadata.bash"
source "${SCRIPTS_DIR}/pg-decider-backfill-pr-metadata.sh"
WRAPPER
  chmod +x "$TDIR/run"
  RUN="$TDIR/run"
  cd "$TDIR" || return 1
}

teardown() {
  for _ in 1 2 3 4 5; do
    rm -rf "$TDIR" "${SHIM_DIR:-}" "${DECOY:-}" 2>/dev/null && return 0
  done
}

# mk_anchor <repo> <pr_number-as-JSON>: prints the anchor id.
mk_anchor() {
  bd -C "$TDIR" create anchor -t task --silent \
    --metadata "{\"dedup_key\":\"pr:N:anchor\",\"repo\":\"$1\",\"pr_number\":$2,\"extra\":1}"
}

# mk_child <parent> <metadata-json> [title]: prints the child id. The default
# title is the decider's `process-feedback: <repo>#<pr>` for the parent's values.
mk_child() {
  local title="${3:-}" pm
  if [ -z "$title" ]; then
    pm="$(meta "$1")"
    title="process-feedback: $(jq -r .repo <<<"$pm")#$(jq -r '.pr_number|tostring' <<<"$pm")"
  fi
  bd -C "$TDIR" create "$title" -t task --silent --parent "$1" --metadata "$2"
}

meta() { # id -> compact metadata JSON
  bd -C "$TDIR" show "$1" --json |
    jq -c '(if type=="object" and has("data") then .data else . end) | .[0].metadata'
}

pf_meta() { printf '{"dedup_key":"pr:N:process-feedback:%s","keep":"me"}' "$1"; }

backfill() { "$RUN" --bd-dir "$TDIR" --actor test-actor "$@"; }

# install_shim [list-empty]: a bd shim first on PATH that logs argv to
# $SHIM_DIR/calls.log, then execs the real bd. With FAIL_ID set, an `update` of
# that id fails; with "list-empty", `list` prints nothing.
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
  if [ "\$a" = list ] && [ "${1:-}" = list-empty ]; then exit 0; fi
done
exec "$real_bd" "\$@"
SHIM
  chmod +x "$SHIM_DIR/bd"
  PATH="$SHIM_DIR:$PATH"
  export PATH
}

@test "dry-run is the default: prints target, title, id, key, old, new and writes nothing" {
  a="$(mk_anchor org/repo 108278)"
  c="$(mk_child "$a" "$(pf_meta a)")"
  before="$(meta "$c")"
  install_shim
  run --separate-stderr backfill
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "$(printf 'TARGET\t%s' "$TDIR/.beads")" ]
  [[ $output == *$'CHILD\t'"$c"$'\tprocess-feedback: org/repo#108278'* ]]
  [[ $output == *$'PLAN\t'"$c"$'\trepo\t<absent>\torg/repo'* ]]
  [[ $output == *$'PLAN\t'"$c"$'\tpr_number\t<absent>\t108278'* ]]
  [[ $output == *"SUMMARY mode=dry-run discovered=1 skipped=0 planned=1 updated=0 problems=0 failed=0"* ]]
  [ "$(meta "$c")" = "$before" ]
  ! grep -E ' update ' "$SHIM_DIR/calls.log"
}

@test "--apply writes repo and pr_number, preserves every other key, reads back" {
  a="$(mk_anchor org/repo 108278)"
  c="$(mk_child "$a" "$(pf_meta a)")"
  run --separate-stderr backfill --apply --expect 1
  [ "$status" -eq 0 ]
  [[ $output == *"OK $c"* ]]
  [[ $output == *"updated=1 problems=0 failed=0"* ]]
  m="$(meta "$c")"
  [ "$(jq -r .repo <<<"$m")" = "org/repo" ]
  [ "$(jq -r '.pr_number|tostring' <<<"$m")" = "108278" ]
  [ "$(jq -r .keep <<<"$m")" = "me" ]
  [ "$(jq -r .dedup_key <<<"$m")" = "pr:N:process-feedback:a" ]
  # the parent anchor is untouched
  [ "$(jq -r .extra <<<"$(meta "$a")")" = "1" ]
}

@test "pins bd's coercion: the stored pr_number is a JSON number, not a string" {
  a="$(mk_anchor org/repo 108278)"
  c="$(mk_child "$a" "$(pf_meta a)")"
  backfill --apply >/dev/null
  [ "$(jq -r '.pr_number|type' <<<"$(meta "$c")")" = "number" ]
  [ "$(jq -r '.repo|type' <<<"$(meta "$c")")" = "string" ]
}

@test "idempotent: a second --apply plans and writes nothing" {
  a="$(mk_anchor org/repo 7)"
  c="$(mk_child "$a" "$(pf_meta a)")"
  backfill --apply >/dev/null
  after_first="$(meta "$c")"
  run --separate-stderr backfill --apply
  [ "$status" -eq 0 ]
  [[ $output != *PLAN* ]]
  [[ $output == *"discovered=1 skipped=1 planned=0 updated=0 problems=0 failed=0"* ]]
  [ "$(meta "$c")" = "$after_first" ]
}

@test "skip-when-present: both keys skip; one key present plans only the other" {
  a="$(mk_anchor org/repo 5)"
  both="$(mk_child "$a" '{"dedup_key":"pr:N:process-feedback:b","repo":"x/y","pr_number":99}')"
  half="$(mk_child "$a" '{"dedup_key":"pr:N:process-feedback:h","repo":"x/y"}')"
  run --separate-stderr backfill
  [ "$status" -eq 0 ]
  [[ $output != *"$both"* ]]
  [[ $output == *$'PLAN\t'"$half"$'\tpr_number\t<absent>\t5'* ]]
  [[ $output != *$'PLAN\t'"$half"$'\trepo'* ]]
  [[ $output == *"discovered=2 skipped=1 planned=1"* ]]
}

@test "ignores children that are not process-feedback and beads without a parent" {
  a="$(mk_anchor org/repo 5)"
  c="$(mk_child "$a" "$(pf_meta ok)")"
  mk_child "$a" '{"dedup_key":"pr:N:work:w"}' >/dev/null
  bd -C "$TDIR" create orphan -t task --silent --metadata '{"dedup_key":"pr:N:process-feedback:o"}' >/dev/null
  run --separate-stderr backfill --expect 1
  [ "$status" -eq 0 ]
  [[ $output == *"discovered=1 "* ]]
  [[ $output == *$'PLAN\t'"$c"$'\trepo'* ]]
}

@test "stop on first write failure: later children are untouched, exit 1, summary printed" {
  a="$(mk_anchor org/repo 5)"
  mk_child "$a" "$(pf_meta 1)" >/dev/null
  mk_child "$a" "$(pf_meta 2)" >/dev/null
  mk_child "$a" "$(pf_meta 3)" >/dev/null
  mapfile -t ids < <(backfill | awk -F'\t' '$1=="PLAN" && $3=="repo" {print $2}')
  [ "${#ids[@]}" -eq 3 ]
  install_shim
  FAIL_ID="${ids[1]}" run --separate-stderr backfill --apply
  [ "$status" -eq 1 ]
  [[ $output == *"updated=1 problems=0 failed=1"* ]]
  [ "$(jq -r .repo <<<"$(meta "${ids[0]}")")" = "org/repo" ]
  [ "$(jq -r '.repo // "none"' <<<"$(meta "${ids[1]}")")" = "none" ]
  [ "$(jq -r '.repo // "none"' <<<"$(meta "${ids[2]}")")" = "none" ]
}

@test "every write passes --actor and only --set-metadata (never --metadata)" {
  a="$(mk_anchor org/repo 5)"
  mk_child "$a" "$(pf_meta 1)" >/dev/null
  install_shim
  run --separate-stderr backfill --apply
  [ "$status" -eq 0 ]
  grep -E ' update ' "$SHIM_DIR/calls.log" | grep -q -- '--actor test-actor'
  grep -E ' update ' "$SHIM_DIR/calls.log" | grep -q -- '--set-metadata repo=org/repo'
  ! grep -E ' update ' "$SHIM_DIR/calls.log" | grep -q -- ' --metadata'
}

@test "usage: --actor (or PG_BACKFILL_ACTOR) and --bd-dir are required, and no bd call is made" {
  install_shim
  run --separate-stderr "$RUN" --bd-dir "$TDIR"
  [ "$status" -eq 2 ]
  [[ $stderr == *"--actor"* ]]
  run --separate-stderr "$RUN" --actor x
  [ "$status" -eq 2 ]
  [ ! -e "$SHIM_DIR/calls.log" ]
  PG_BACKFILL_ACTOR=env-actor run --separate-stderr "$RUN" --bd-dir "$TDIR"
  [ "$status" -eq 1 ] # arguments accepted; no candidates in an empty tracker
  [[ $stderr == *"WARN no candidates"* ]]
}

@test "an option value that starts with - is refused with exit 2 and no bd call" {
  install_shim
  run --separate-stderr "$RUN" --bd-dir "$TDIR" --actor --apply
  [ "$status" -eq 2 ]
  run --separate-stderr "$RUN" --bd-dir -x --actor a
  [ "$status" -eq 2 ]
  run --separate-stderr "$RUN" --bd-dir "$TDIR" --actor a --expect -1
  [ "$status" -eq 2 ]
  run --separate-stderr "$RUN" --bd-dir "$TDIR" --actor a --expect many
  [ "$status" -eq 2 ]
  run --separate-stderr "$RUN" --bd-dir "$TDIR" --actor a --bogus
  [ "$status" -eq 2 ]
  [ ! -e "$SHIM_DIR/calls.log" ]
}

@test "a decoy BEADS_DIR / BEADS_DB cannot redirect the run: TARGET is --bd-dir" {
  a="$(mk_anchor org/repo 5)"
  c="$(mk_child "$a" "$(pf_meta 1)")"
  DECOY="$(mktemp -d)"
  DECOY="$(cd "$DECOY" && pwd -P)"
  (cd "$DECOY" && bd init --prefix dec >/dev/null 2>&1)
  d="$(bd -C "$DECOY" create decoy -t task --silent)"
  BEADS_DIR="$DECOY/.beads" BEADS_DB="$DECOY/.beads" run --separate-stderr backfill --apply --expect 1
  [ "$status" -eq 0 ]
  [ "${lines[0]}" = "$(printf 'TARGET\t%s' "$TDIR/.beads")" ]
  [[ $output == *"OK $c"* ]]
  [ "$(jq -r .repo <<<"$(meta "$c")")" = "org/repo" ]
  # the decoy tracker was neither read nor written
  [ "$(bd -C "$DECOY" list --all -n 0 --json | jq 'if type=="object" and has("data") then .data else . end | length')" = "1" ]
  [ -n "$d" ]
}

@test "a --bd-dir that does not resolve to its own tracker is refused" {
  mkdir "$TDIR/sub"
  run --separate-stderr "$RUN" --bd-dir "$TDIR/sub" --actor test-actor
  [ "$status" -eq 1 ]
  [[ $stderr == *"not inside"* ]]
}

@test "zero candidates fails closed, with and without --expect, including empty bd output" {
  mk_anchor org/repo 5 >/dev/null
  run --separate-stderr backfill
  [ "$status" -eq 1 ]
  [[ $stderr == *"WARN no candidates"* ]]
  run --separate-stderr backfill --expect 0
  [ "$status" -eq 1 ]
  install_shim list-empty
  run --separate-stderr backfill --apply --expect 7
  [ "$status" -eq 1 ]
  [[ $stderr == *"WARN no candidates"* ]]
  ! grep -E ' update ' "$SHIM_DIR/calls.log"
}

@test "--expect mismatch fails before any write, in apply mode too" {
  a="$(mk_anchor org/repo 5)"
  c="$(mk_child "$a" "$(pf_meta 1)")"
  before="$(meta "$c")"
  install_shim
  run --separate-stderr backfill --apply --expect 2
  [ "$status" -eq 1 ]
  [[ $stderr == *"--expect is 2"* ]]
  [ "$(meta "$c")" = "$before" ]
  ! grep -E ' update ' "$SHIM_DIR/calls.log"
}

@test "a child parented to the wrong anchor is refused and nothing is written" {
  good_a="$(mk_anchor org/repo 5)"
  wrong_a="$(mk_anchor org/repo 6)"
  good="$(mk_child "$good_a" "$(pf_meta 1)")"
  bad="$(mk_child "$wrong_a" "$(pf_meta 2)" "process-feedback: org/repo#5")"
  run --separate-stderr backfill --apply
  [ "$status" -eq 1 ]
  [[ $stderr == *"PROBLEM $bad"* ]]
  [[ $output == *"problems=1"* ]]
  [ "$(jq -r '.repo // "none"' <<<"$(meta "$bad")")" = "none" ]
  [ "$(jq -r '.repo // "none"' <<<"$(meta "$good")")" = "none" ]
}

@test "a non-numeric and a leading-zero parent pr_number are refused; dry-run reports all problems" {
  a_bad="$(mk_anchor org/repo '"abc"')"
  a_zero="$(mk_anchor org/repo '"042"')"
  a_ok="$(mk_anchor org/repo 9)"
  c1="$(mk_child "$a_bad" "$(pf_meta 1)" "process-feedback: org/repo#abc")"
  c2="$(mk_child "$a_zero" "$(pf_meta 2)" "process-feedback: org/repo#042")"
  c3="$(mk_child "$a_ok" "$(pf_meta 3)")"
  run --separate-stderr backfill
  [ "$status" -eq 1 ]
  [[ $stderr == *"PROBLEM $c1"* ]]
  [[ $stderr == *"PROBLEM $c2"* ]]
  [[ $output == *$'PLAN\t'"$c3"$'\tpr_number\t<absent>\t9'* ]]
  [[ $output == *"discovered=3 skipped=0 planned=1 updated=0 problems=2 failed=0"* ]]
}

@test "a malformed parent repo is refused" {
  a="$(mk_anchor "no-slash" 5)"
  c="$(mk_child "$a" "$(pf_meta 1)" "process-feedback: no-slash#5")"
  run --separate-stderr backfill --apply
  [ "$status" -eq 1 ]
  [[ $stderr == *"PROBLEM $c"* ]]
}

@test "bd update --set-metadata merges: untouched keys survive (real bd semantics)" {
  c="$(bd -C "$TDIR" create m -t task --silent --metadata '{"dedup_key":"k","a":"1","b":2}')"
  bd -C "$TDIR" update "$c" --set-metadata c=3 --actor test-actor >/dev/null
  [ "$(jq -S -c . <<<"$(meta "$c")")" = '{"a":"1","b":2,"c":3,"dedup_key":"k"}' ]
}
