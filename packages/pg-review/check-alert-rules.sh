#!/usr/bin/env bash
# PromQL behaviour check for the pg-review Grafana alert rules.
#
#   check-alert-rules.sh [<alerts.yaml> <rule-tests-dir>]
#
# With no arguments it checks grafana/alerting/ next to this script. Modelled on
# packages/pg-keep-awake/check-alert-rules.sh: every rule listed in rule_uids has its refId A
# wrapped as a Prometheus `alert:` rule `expr: (<A>) <op> <threshold>` (op and threshold from the
# refId C evaluator, `for` and labels carried over) next to a recording rule `wrapped:<uid>` over
# the same expression, and every `*.test.yaml` in the rule-tests directory runs under
# `promtool test rules`.
#
# It also pins the Grafana-only fields promtool cannot run (noDataState, severity, for) and the
# expressions of the three rules.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
case "$#" in
0)
  alerts="$here/grafana/alerting/alerts.yaml"
  tests_dir="$here/grafana/alerting/rule-tests"
  ;;
2)
  alerts="$1"
  tests_dir="$2"
  ;;
*)
  echo "usage: check-alert-rules.sh [<alerts.yaml> <rule-tests-dir>]" >&2
  exit 2
  ;;
esac

rule_uids='["pg-review-pending-too-large", "pg-review-lens-not-running", "pg-review-runs-failing"]'

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

# Every rule: condition C over refIds A, B, C, severity warning, noDataState OK, uid <= 40 chars.
for uid in $(printf '%s' "$rule_uids" | jq -r '.[]'); do
  printf '%s' "$alerts_json" | jq -e --arg uid "$uid" '
    .groups[].rules[] | select(.uid == $uid)
    | .condition == "C"
      and ((.data | map(.refId)) == ["A", "B", "C"])
      and .labels.severity == "warning"
      and .noDataState == "OK"
      and (.uid | length) <= 40' >/dev/null ||
    fail "$uid: condition C over refIds A, B, C, severity warning, noDataState OK, uid <= 40 chars"
done

# `for`: 1h for the pending-too-large rule, 30m for the other two.
printf '%s' "$alerts_json" | jq -e '
  [.groups[].rules[] | {key: .uid, value: .for}] | from_entries
  | .["pg-review-pending-too-large"] == "1h"
    and .["pg-review-lens-not-running"] == "30m"
    and .["pg-review-runs-failing"] == "30m"' >/dev/null ||
  fail "for: pending-too-large must be 1h, the other two 30m"

# Thresholds: pending above 12; lens-not-running above 0 (the expression is the excess over
# twice the window); runs-failing above 2 (3 or more).
printf '%s' "$alerts_json" | jq -e '
  [.groups[].rules[] | {key: .uid, value: (.data[] | select(.refId == "C") | .model.conditions[0].evaluator)}]
  | from_entries
  | .["pg-review-pending-too-large"] == {type: "gt", params: [12]}
    and .["pg-review-lens-not-running"] == {type: "gt", params: [0]}
    and .["pg-review-runs-failing"] == {type: "gt", params: [2]}' >/dev/null ||
  fail "thresholds: pending gt 12, not-running gt 0, failing gt 2"

expr_of() {
  printf '%s' "$alerts_json" | jq -r --arg uid "$1" '
    .groups[].rules[] | select(.uid == $uid) | .data[] | select(.refId == "A") | .model.expr'
}
by='lens, scope, schedule'
[ "$(expr_of pg-review-pending-too-large)" = 'sum(max_over_time(pg_review_pending_items[10m])) or vector(0)' ] ||
  fail "pending-too-large expression changed"
[ "$(expr_of pg-review-lens-not-running)" = "(time() - max by ($by) (max_over_time(pg_review_last_success_timestamp_seconds[1d]))) - on ($by) 2 * max by ($by) (max_over_time(pg_review_last_window_seconds[1d])) or vector(0)" ] ||
  fail "lens-not-running expression changed"
[ "$(expr_of pg-review-runs-failing)" = "max by ($by) (max_over_time(pg_review_consecutive_failed_runs[10m])) or vector(0)" ] ||
  fail "runs-failing expression changed"

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
