#!/usr/bin/env bash
# PromQL behaviour check for the pg-keep-awake Grafana alert rules (docket pg2-8bgxg.2,
# packet 18-alerts).
#
#   check-alert-rules.sh <alerts.yaml> <rule-tests-dir>
#
# Modelled on packages/pg-router/check-alert-rules.sh. Every rule listed in RULE_UIDS has its
# refId A wrapped as a Prometheus `alert:` rule `expr: (<A>) <op> <threshold>` (op and
# threshold from the refId C evaluator, `for` and labels carried over) next to a recording
# rule `wrapped:<uid>` over the same expression, and every `*.test.yaml` in the rule-tests
# directory runs under `promtool test rules`.
#
# It also pins the design's expressions (section 12) for rules 1 and 3, and the Grafana-only
# fields (noDataState, severity, for) of all three, which promtool cannot run.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: check-alert-rules.sh <alerts.yaml> <rule-tests-dir>" >&2
  exit 2
fi
alerts="$1"
tests_dir="$2"

rule_uids='["pg-keep-awake-keeper-down-leases-live", "pg-keep-awake-keeper-down-pa-monitor", "pg-keep-awake-held-on-low-battery"]'

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
  fail "the file must hold exactly the rules in RULE_UIDS"

for uid in $(printf '%s' "$rule_uids" | jq -r '.[]'); do
  printf '%s' "$alerts_json" | jq -e --arg uid "$uid" '
    .groups[].rules[] | select(.uid == $uid)
    | .condition == "C"
      and ((.data | map(.refId)) == ["A", "B", "C"])
      and .for == "5m"
      and .labels.severity == "critical"
      and (.uid | length) <= 40' >/dev/null ||
    fail "$uid: condition C over refIds A, B, C, for 5m, severity critical, uid <= 40 chars"
done

# noDataState: rule 1 Alerting (a dead keeper emits nothing), rules 2 and 3 OK.
printf '%s' "$alerts_json" | jq -e '
  [.groups[].rules[] | {key: .uid, value: .noDataState}] | from_entries
  | .["pg-keep-awake-keeper-down-leases-live"] == "Alerting"
    and .["pg-keep-awake-keeper-down-pa-monitor"] == "OK"
    and .["pg-keep-awake-held-on-low-battery"] == "OK"' >/dev/null ||
  fail "noDataState: rule 1 must be Alerting, rules 2 and 3 OK"

# The design's expressions (section 12). Rule 1 keeps the design's expression verbatim as the
# first operand of `or vector(0)` (see the file header).
expr_of() {
  printf '%s' "$alerts_json" | jq -r --arg uid "$1" '
    .groups[].rules[] | select(.uid == $uid) | .data[] | select(.refId == "A") | .model.expr'
}
design1='sum(max_over_time(pg_keep_awake_leases_live[30m] offset 3m)) > 0 unless on() (pg_keep_awake_keeper_up == 1)'
design3='pg_keep_awake_assertion_held == 1 and pg_keep_awake_battery_on_battery == 1 and pg_keep_awake_battery_percent <= pg_keep_awake_battery_release_pct'
[ "$(expr_of pg-keep-awake-keeper-down-leases-live)" = "($design1) or vector(0)" ] ||
  fail "rule 1 must be the design's expression, wrapped as (<design>) or vector(0)"
[ "$(expr_of pg-keep-awake-held-on-low-battery)" = "$design3" ] ||
  fail "rule 3 must be exactly the design's expression"
[ "$(expr_of pg-keep-awake-keeper-down-pa-monitor)" = "min(pa_monitor_keep_awake_keeper_up)" ] ||
  fail "rule 2 must read pa_monitor_keep_awake_keeper_up"

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
