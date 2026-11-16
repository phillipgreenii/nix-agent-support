#!/usr/bin/env bats
# bats file_tags=type:integration
# Integration tier (PGDR_INTEGRATION=1; checks.<system>.test-pg-disk-reclaimer-integration):
# two spool files whose headers are identical except the file id both reach a
# real collector, and no file_id label survives. Skipped in the unit gate.
bats_require_minimum_version 1.5.0
load 'hermetic'

setup() {
  [[ ${PGDR_INTEGRATION:-} == 1 ]] || skip "needs PGDR_INTEGRATION=1 (checks.<system>.test-pg-disk-reclaimer-integration)"
  [[ -n ${SCRIPT_UNDER_TEST:-} ]] || skip "SCRIPT_UNDER_TEST is set by the nix check"
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  pgdr_test_isolate "$TEST_DIR"
  pgdr_test_assert_hermetic "$TEST_DIR"
  COLLECTOR_PID=""
}

teardown() {
  [[ -z ${COLLECTOR_PID:-} ]] || kill "$COLLECTOR_PID" 2>/dev/null || true
  rm -rf "$TEST_DIR"
}

@test "two same-header files with different file ids both arrive, with no file_id label" {
  local fixture="$BATS_TEST_DIRNAME/fixtures/otelcol-spool.yaml"
  mkdir -p "$HOME/.config/pg-disk-reclaimer" "$TEST_DIR/area"
  jq -n --arg p "$TEST_DIR/area" '[{id: "it", description: "d", path: $p, displayCommand: "true",
    sizeCommand: "echo 1", variants: [{aggressiveness: 1, variantDescription: "v",
    dryRunCommand: "true", removeCommand: "true"}]}]' >"$HOME/.config/pg-disk-reclaimer/registry.json"
  printf '#!/bin/sh\necho 1700000000.000000\n' >"$TEST_DIR/now"
  chmod +x "$TEST_DIR/now"
  export PGDR_NOW_CMD="$TEST_DIR/now" PGDR_OTEL_SPOOL_DIR="$TEST_DIR/spool"
  mkdir -p "$TEST_DIR/spool/metrics"

  echo "collector: $(otelcol-contrib --version 2>&1 | head -n1)" >&3
  SPOOL_DIR="$TEST_DIR/spool" OUT_FILE="$TEST_DIR/out.json" \
    otelcol-contrib --config "$fixture" \
    --feature-gates=filelog.allowFileDeletion,filelog.mtimeSortType \
    >"$TEST_DIR/collector.log" 2>&1 &
  COLLECTOR_PID=$!

  PGDR_OTEL_FILE_ID=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa run "$SCRIPT_UNDER_TEST" reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  PGDR_OTEL_FILE_ID=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb run "$SCRIPT_UNDER_TEST" reclaim --aggressiveness 1
  [ "$status" -eq 0 ]

  local n=0
  for _ in $(seq 1 60); do
    n=$(grep -o 'pg_disk_reclaimer_item_duration_seconds' "$TEST_DIR/out.json" 2>/dev/null | wc -l)
    [ "$n" -ge 2 ] && break
    sleep 0.5
  done
  if [ "$n" -lt 2 ]; then
    echo "--- collector log" >&3
    cat "$TEST_DIR/collector.log" >&3
    echo "--- spool" >&3
    ls -la "$TEST_DIR/spool/metrics" >&3
    echo "--- out" >&3
    head -c 2000 "$TEST_DIR/out.json" >&3 2>/dev/null || true
  fi
  [ "$n" -ge 2 ]
  run grep -q file_id "$TEST_DIR/out.json"
  [ "$status" -ne 0 ]
}
