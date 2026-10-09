#!/usr/bin/env bash
# PromQL behaviour check for pg-router Grafana alert rules (pg2-owccx).
#
#   check-alert-rules.sh <alerts.yaml> <rule-tests-dir>
#
# Modelled on packages/beads-exporter/check-alert-rules.sh. Every rule listed in
# RULE_UIDS has its refId A wrapped as a Prometheus `alert:` rule
# `expr: (<A>) <op> <threshold>` (op and threshold from the refId C evaluator, `for`
# and labels carried over) next to a recording rule `wrapped:<uid>` over the same
# expression, so a case can count instances, and every `*.test.yaml` in the
# rule-tests directory runs under `promtool test rules`.
#
# Grafana-only fields (noDataState, execErrState, annotations) are pinned by the Go
# tests in internal/alertrules; promtool cannot run them. This script asserts only the
# structure the wrapping depends on.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: check-alert-rules.sh <alerts.yaml> <rule-tests-dir>" >&2
  exit 2
fi
alerts="$1"
tests_dir="$2"

# Every rule uid this check wraps and tests. A rule added here extends the cases under
# rule-tests/.
rule_uids='["pg-router-pool-full-idle", "pg-router-ccpool-dead-needs-input", "pg-router-gate-held"]'

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

alerts_json="$(yq -o=json -I=0 '.' "$alerts")"

for uid in $(printf '%s' "$rule_uids" | jq -r '.[]'); do
  printf '%s' "$alerts_json" | jq -e --arg uid "$uid" '
    [.groups[].rules[] | select(.uid == $uid)] | length == 1' >/dev/null ||
    fail "$uid: rule must exist exactly once"
  printf '%s' "$alerts_json" | jq -e --arg uid "$uid" '
    .groups[].rules[] | select(.uid == $uid)
    | .condition == "C"
      and ((.data | map(.refId)) == ["A", "B", "C"])
      and ((.data[] | select(.refId == "C") | .model.conditions[0].evaluator)
           == { type: "gt", params: [0] })' >/dev/null ||
    fail "$uid: condition C must be a single gt 0 threshold over refIds A, B, C"
done

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
        | ( { alert: .uid, expr: $wrapped, for: .for, labels: .labels },
            { record: ("wrapped:" + (.uid | gsub("-"; "_"))), expr: $wrapped } ) ]
    } ] }' >rules.yml

echo "--- wrapped rules"
yq '.' rules.yml
promtool check rules rules.yml

for t in *.test.yaml; do
  echo "--- $t"
  yq '.tests[].name | "case: " + .' "$t"
  promtool test rules "$t"
done

echo "OK: PromQL cases pass"
