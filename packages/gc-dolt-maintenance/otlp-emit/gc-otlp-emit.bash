# shellcheck shell=bash
# gc-otlp-emit: best-effort OTLP/JSON emission via curl. Never blocks/fails the
# caller. No-op when OTEL endpoint is unset or GC_OTEL_DISABLE=1.

_otlp_endpoint() { printf '%s' "${OTEL_EXPORTER_OTLP_ENDPOINT:-}"; }
_otlp_enabled() { [[ -n "$(_otlp_endpoint)" && ${GC_OTEL_DISABLE:-0} != "1" ]]; }
_otlp_now_ns() { date +%s%N; } # GNU date (coreutils) on the pinned PATH
_otlp_service() { printf '%s' "${OTEL_SERVICE_NAME:-gc-dolt-maintenance}"; }

# _otlp_attrs k=v k=v ... -> JSON array of OTLP keyValue (string values)
_otlp_attrs() {
  local out="[]" kv k v
  for kv in "$@"; do
    k=${kv%%=*}
    v=${kv#*=}
    out=$(jq -c --arg k "$k" --arg v "$v" \
      '. + [{key:$k,value:{stringValue:$v}}]' <<<"$out")
  done
  printf '%s' "$out"
}

# otlp_gauge NAME VALUE [k=v ...]
otlp_gauge() {
  _otlp_enabled || return 0
  local name=$1 value=$2
  shift 2 || true
  local attrs
  attrs=$(_otlp_attrs "$@")
  local payload
  payload=$(jq -cn --arg svc "$(_otlp_service)" --arg name "$name" \
    --argjson value "$value" --arg ts "$(_otlp_now_ns)" --argjson attrs "$attrs" '
    {resourceMetrics:[{resource:{attributes:[{key:"service.name",value:{stringValue:$svc}}]},
     scopeMetrics:[{metrics:[{name:$name,gauge:{dataPoints:[
       {asDouble:$value,timeUnixNano:$ts,attributes:$attrs}]}}]}]}]}')
  curl -s --max-time 3 -X POST -H 'Content-Type: application/json' \
    --data "$payload" "$(_otlp_endpoint)/v1/metrics" >/dev/null 2>&1 || true
}

# otlp_log SEVERITY BODY [k=v ...]
otlp_log() {
  _otlp_enabled || return 0
  local sev=$1 body=$2
  shift 2 || true
  local attrs
  attrs=$(_otlp_attrs "$@")
  local payload
  payload=$(jq -cn --arg svc "$(_otlp_service)" --arg sev "$sev" --arg body "$body" \
    --arg ts "$(_otlp_now_ns)" --argjson attrs "$attrs" '
    {resourceLogs:[{resource:{attributes:[{key:"service.name",value:{stringValue:$svc}}]},
     scopeLogs:[{logRecords:[{timeUnixNano:$ts,severityText:$sev,
       body:{stringValue:$body},attributes:$attrs}]}]}]}')
  curl -s --max-time 3 -X POST -H 'Content-Type: application/json' \
    --data "$payload" "$(_otlp_endpoint)/v1/logs" >/dev/null 2>&1 || true
}
