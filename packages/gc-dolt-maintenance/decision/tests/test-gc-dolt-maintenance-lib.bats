setup() {
  # shellcheck source=/dev/null
  source "${LIB_PATH:-$BATS_TEST_DIRNAME/../gc-dolt-maintenance-lib.bash}"
}
# args: commit busy hours breaker_applied has_remote ; thresholds 5000 4 6 24
flat() { should_flatten "$1" "$2" "$3" "$4" "$5" 5000 4 6 24; }

@test "remote DB never flattens"        { run flat 9000 0 99 1 1; [ "$status" -eq 0 ]; [[ "$output" == no:* ]]; [[ "$output" == *remote* ]]; }
@test "breaker off never flattens"      { run flat 9000 0 99 0 0; [[ "$output" == no:* ]]; [[ "$output" == *breaker* ]]; }
@test "below commit threshold: no"      { run flat 4999 0 99 1 0; [[ "$output" == no:* ]]; [[ "$output" == *threshold* ]]; }
@test "max-age force overrides busy"    { run flat 9000 50 25 1 0; [[ "$output" == yes:* ]]; [[ "$output" == *force* ]]; }
@test "anti-thrash: too soon"           { run flat 9000 0 3 1 0;  [[ "$output" == no:* ]]; [[ "$output" == *recent* ]]; }
@test "busy blocks within window"       { run flat 9000 9 10 1 0; [[ "$output" == no:* ]]; [[ "$output" == *busy* ]]; }
@test "happy path: need+safe+interval"  { run flat 9000 0 10 1 0; [[ "$output" == yes:* ]]; }
