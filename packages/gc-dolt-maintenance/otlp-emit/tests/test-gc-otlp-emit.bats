setup() {
  load ../../test-support/test_helper
  TEST_DIR="$(mktemp -d)"; export HOME="$TEST_DIR"
  for v in $(env | grep -oE '^(OTEL|GC|BEADS|DOLT)[A-Z_]*' || true); do unset "$v"; done
  # shellcheck source=/dev/null
  source "${LIB_PATH:-$BATS_TEST_DIRNAME/../gc-otlp-emit.bash}"
  mkdir -p "$TEST_DIR/bin"
  cat >"$TEST_DIR/bin/curl" <<'EOF'
#!/usr/bin/env bash
args="$*"; data=""; while [[ $# -gt 0 ]]; do [[ "$1" == "--data" ]] && data="$2"; shift; done
printf '%s' "$data" >"$CURL_PAYLOAD_FILE"; printf '%s' "$args" >"$CURL_ARGS_FILE"
EOF
  chmod +x "$TEST_DIR/bin/curl"; export PATH="$TEST_DIR/bin:$PATH"
  export CURL_PAYLOAD_FILE="$TEST_DIR/payload" CURL_ARGS_FILE="$TEST_DIR/args"
}
teardown() { rm -rf "$TEST_DIR"; }

@test "otlp_gauge no-ops when endpoint unset" {
  run otlp_gauge dolt_maint_commit_count 5 db=hq
  [ "$status" -eq 0 ]; [ ! -f "$CURL_PAYLOAD_FILE" ]
}

@test "otlp_gauge posts well-formed OTLP to /v1/metrics" {
  export OTEL_EXPORTER_OTLP_ENDPOINT="http://127.0.0.1:4318"
  run otlp_gauge dolt_maint_commit_count 21000 db=hq
  [ "$status" -eq 0 ]
  grep -q "/v1/metrics" "$CURL_ARGS_FILE"
  name=$(jq -r '.resourceMetrics[0].scopeMetrics[0].metrics[0].name' "$CURL_PAYLOAD_FILE")
  [ "$name" = "dolt_maint_commit_count" ]
  db=$(jq -r '.resourceMetrics[0].scopeMetrics[0].metrics[0].gauge.dataPoints[0].attributes[0].value.stringValue' "$CURL_PAYLOAD_FILE")
  [ "$db" = "hq" ]
}

@test "GC_OTEL_DISABLE=1 suppresses emission" {
  export OTEL_EXPORTER_OTLP_ENDPOINT="http://127.0.0.1:4318" GC_OTEL_DISABLE=1
  run otlp_log INFO "hi" db=hq; [ "$status" -eq 0 ]; [ ! -f "$CURL_PAYLOAD_FILE" ]
}
