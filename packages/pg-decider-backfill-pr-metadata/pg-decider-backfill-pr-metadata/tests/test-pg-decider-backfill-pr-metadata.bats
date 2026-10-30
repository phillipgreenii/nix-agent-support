#!/usr/bin/env bats
# bats file_tags=type:integration
#
# Tests the one-off backfill (bead pg2-eclw3j.5) against the REAL `bd` in a
# hermetic throwaway embedded database (V-3; no mock of bd's metadata merge):
# every test gets a fresh database in its own temp dir, with BEADS_*/BD_* scrubbed
# and `bd where` asserted to resolve inside it before any write. The only stand-in
# is a PATH shim that execs the real bd, used to log argv and to force one write
# to fail for the stop-on-first-failure test.

bats_require_minimum_version 1.5.0

load ../../test-support/bd_fixture.bash

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
  fi
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
  cd "" || return 1
}

teardown() {
  for _ in 1 2 3 4 5; do
    rm -rf "$TDIR" "${SHIM_DIR:-}" 2>/dev/null && return 0
  done
}

# mk_anchor <repo> <pr_number-as-JSON>: prints the anchor id.
mk_anchor() {
  bd -C "$TDIR" create anchor -t task --silent \
    --metadata "{\"dedup_key\":\"pr:N$2:anchor\",\"repo\":\"$1\",\"pr_number\":$2,\"extra\":1}"
}

# mk_child <parent> <metadata-json>: prints the child id.
mk_child() {
  bd -C "$TDIR" create child -t task --silent --parent "$1" --metadata "$2"
}

meta() { # id -> compact metadata JSON
  bd -C "$TDIR" show "$1" --json |
    jq -c '(if type=="object" and has("data") then .data else . end) | .[0].metadata'
}

pf_meta() { printf '{"dedup_key":"pr:N:process-feedback:%s","keep":"me"}' "$1"; }

backfill() { "$RUN" --bd-dir "$TDIR" --actor test-actor "$@"; }

@test "dry-run is the default: prints id, key, old, new and writes nothing" {
  a="$(mk_anchor org/repo 108278)"
  c="$(mk_child "$a" "$(pf_meta a)")"
  before="$(meta "$c")"
  run --separate-stderr backfill
  [ "$status" -eq 0 ]
  [[ $output == *$'PLAN\t'"$c"$'\trepo\t<absent>\torg/repo'* ]]
  [[ $output == *$'PLAN\t'"$c"$'\tpr_number\t<absent>\t108278'* ]]
  [[ $output == *"SUMMARY mode=dry-run discovered=1 skipped=0 planned=2 updated=0 failed=0"* ]]
  [ "$(meta "$c")" = "$before" ]
}

@test "--apply writes repo and pr_number, preserves every other key, reads back" {
  a="$(mk_anchor org/repo 108278)"
  c="$(mk_child "$a" "$(pf_meta a)")"
  run --separate-stderr backfill --apply
  [ "$status" -eq 0 ]
  [[ $output == *"OK $c"* ]]
  [[ $output == *"updated=1 failed=0"* ]]
  m="$(meta "$c")"
  [ "$(jq -r .repo <<<"$m")" = "org/repo" ]
  [ "$(jq -r '.pr_number|tostring' <<<"$m")" = "108278" ]
  [ "$(jq -r .keep <<<"$m")" = "me" ]
  [ "$(jq -r .dedup_key <<<"$m")" = "pr:N:process-feedback:a" ]
  # the parent anchor is untouched
  [ "$(jq -r .extra <<<"$(meta "$a")")" = "1" ]
}

@test "idempotent: a second --apply plans and writes nothing" {
  a="$(mk_anchor org/repo 7)"
  c="$(mk_child "$a" "$(pf_meta a)")"
  backfill --apply >/dev/null
  after_first="$(meta "$c")"
  run --separate-stderr backfill --apply
  [ "$status" -eq 0 ]
  [[ $output != *PLAN* ]]
  [[ $output == *"discovered=1 skipped=1 planned=0 updated=0 failed=0"* ]]
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
  mk_child "$a" '{"dedup_key":"pr:N:work:w"}' >/dev/null
  bd -C "$TDIR" create orphan -t task --silent --metadata '{"dedup_key":"pr:N:process-feedback:o"}' >/dev/null
  run --separate-stderr backfill
  [ "$status" -eq 0 ]
  [[ $output == *"discovered=0"* ]]
}

@test "stop on first failure: later children are untouched, exit 1, summary printed" {
  a="$(mk_anchor org/repo 5)"
  mk_child "$a" "$(pf_meta 1)" >/dev/null
  mk_child "$a" "$(pf_meta 2)" >/dev/null
  mk_child "$a" "$(pf_meta 3)" >/dev/null
  mapfile -t ids < <(backfill | awk -F'\t' '$1=="PLAN" && $3=="repo" {print $2}')
  [ "${#ids[@]}" -eq 3 ]
  # shim: real bd, except `update` of the SECOND child fails.
  real_bd="$(command -v bd)"
  SHIM_DIR="$(mktemp -d)"
  cat >"$SHIM_DIR/bd" <<SHIM
#!/usr/bin/env bash
for a in "\$@"; do
  if [ "\$a" = update ]; then
    for b in "\$@"; do [ "\$b" = "${ids[1]}" ] && { echo "forced failure" >&2; exit 1; }; done
  fi
done
exec "$real_bd" "\$@"
SHIM
  chmod +x "$SHIM_DIR/bd"
  PATH="$SHIM_DIR:$PATH" run --separate-stderr backfill --apply
  [ "$status" -eq 1 ]
  [[ $output == *"updated=1 failed=1"* ]]
  [ "$(jq -r .repo <<<"$(meta "${ids[0]}")")" = "org/repo" ]
  [ "$(jq -r '.repo // "none"' <<<"$(meta "${ids[1]}")")" = "none" ]
  [ "$(jq -r '.repo // "none"' <<<"$(meta "${ids[2]}")")" = "none" ]
}

@test "every write passes --actor and only --set-metadata (never --metadata)" {
  a="$(mk_anchor org/repo 5)"
  mk_child "$a" "$(pf_meta 1)" >/dev/null
  real_bd="$(command -v bd)"
  SHIM_DIR="$(mktemp -d)"
  cat >"$SHIM_DIR/bd" <<SHIM
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$SHIM_DIR/calls.log"
exec "$real_bd" "\$@"
SHIM
  chmod +x "$SHIM_DIR/bd"
  PATH="$SHIM_DIR:$PATH" run --separate-stderr backfill --apply
  [ "$status" -eq 0 ]
  grep -E ' update ' "$SHIM_DIR/calls.log" | grep -q -- '--actor test-actor'
  grep -E ' update ' "$SHIM_DIR/calls.log" | grep -q -- '--set-metadata repo=org/repo'
  ! grep -E ' update ' "$SHIM_DIR/calls.log" | grep -q -- ' --metadata'
}

@test "usage: --actor (or PG_BACKFILL_ACTOR) and --bd-dir are required; nothing is written" {
  run --separate-stderr "$RUN" --bd-dir "$TDIR"
  [ "$status" -eq 2 ]
  [[ $stderr == *"--actor"* ]]
  run --separate-stderr "$RUN" --actor x
  [ "$status" -eq 2 ]
  PG_BACKFILL_ACTOR=env-actor run --separate-stderr "$RUN" --bd-dir "$TDIR"
  [ "$status" -eq 0 ]
}

@test "bd update --set-metadata merges: untouched keys survive (real bd semantics)" {
  c="$(bd -C "$TDIR" create m -t task --silent --metadata '{"dedup_key":"k","a":"1","b":2}')"
  bd -C "$TDIR" update "$c" --set-metadata c=3 --actor test-actor >/dev/null
  [ "$(meta "$c")" = "$(jq -cS . <<<'{"dedup_key":"k","a":"1","b":2,"c":3}' | jq -c .)" ] ||
    [ "$(jq -S -c . <<<"$(meta "$c")")" = '{"a":"1","b":2,"c":3,"dedup_key":"k"}' ]
}
