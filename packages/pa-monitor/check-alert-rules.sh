#!/usr/bin/env bash
# Alert rule check for the pa-monitor scan-stale Grafana rule.
#
#   check-alert-rules.sh <scan-stale.yaml> <rule-tests-dir>
#
# Pins the Grafana-only fields promtool cannot run (uid, for, severity, noDataState, the
# threshold and the expression), then wraps refId A as a Prometheus alert
# `expr: (<A>) <op> <threshold>` (op and threshold from the refId C evaluator, `for` and labels
# carried over) and runs every `*.test.yaml` in the rule-tests directory under
# `promtool test rules`. Modelled on packages/pg-keep-awake/check-alert-rules.sh.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: check-alert-rules.sh <scan-stale.yaml> <rule-tests-dir>" >&2
  exit 2
fi
alerts="$1"
tests_dir="$2"

rule_uids='["pa-monitor-scan-stale"]'

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

alerts_json="$(yq -o=json -I=0 '.' "$alerts")"

printf '%s' "$alerts_json" | jq -e '.apiVersion == 1' >/dev/null || fail "apiVersion must be 1"
printf '%s' "$alerts_json" | jq -e '[.. | objects | select(has("folderUid"))] | length == 0' >/dev/null ||
  fail "a folderUid is pinned"
printf '%s' "$alerts_json" | jq -e --argjson uids "$rule_uids" '
  ([.groups[].rules[].uid] | sort) == ($uids | sort)' >/dev/null ||
  fail "the file must hold exactly the rules in rule_uids"
printf '%s' "$alerts_json" | jq -e '
  .groups[].rules[]
  | .condition == "C"
    and ((.data | map(.refId)) == ["A", "B", "C"])
    and .for == "5m"
    and .noDataState == "OK"
    and .labels.severity == "warning"
    and (.data[0].model.expr == "time() - max(pa_monitor_scan_last_success_timestamp_seconds)")
    and (.data[2].model.conditions[0].evaluator == {"type": "gt", "params": [900]})' >/dev/null ||
  fail "pa-monitor-scan-stale: condition C, for 5m, noDataState OK, severity warning, age expression, gt 900"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cp "$tests_dir"/*.test.yaml "$work/"
cd "$work"

printf '%s\n' "$alerts_json" | jq --argjson uids "$rule_uids" '
  [ .groups[].rules[] | select(.uid as $u | $uids | index($u)) ] as $rules
  | { groups: [ {
      name: "wrapped",
      interval: "1m",
      rules: [ $rules[]
        | (.data[] | select(.refId == "A") | .model.expr) as $a
        | (.data[] | select(.refId == "C") | .model.conditions[0].evaluator) as $e
        | ({ gt: ">", lt: "<" }[$e.type] // error("unsupported evaluator \($e.type)")) as $op
        | "(\($a)) \($op) \($e.params[0])" as $wrapped
        | { alert: .uid, expr: $wrapped, for: .for, labels: .labels } ]
    } ] }' >rules.yml

echo "--- wrapped rules"
yq '.' rules.yml
promtool check rules rules.yml

for t in *.test.yaml; do
  echo "--- $t"
  promtool test rules "$t"
done

echo "OK: PromQL cases pass"
