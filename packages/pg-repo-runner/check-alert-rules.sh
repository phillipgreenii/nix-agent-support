#!/usr/bin/env bash
# PromQL behaviour check for the pg-repo-runner Grafana alert rules.
#
#   check-alert-rules.sh <alerts.yaml> <rule-tests-dir>
#
# Modelled on packages/pg-keep-awake/check-alert-rules.sh. Every rule has its refId A wrapped
# as a Prometheus `alert:` rule `expr: (<A>) gt <threshold>` (for and labels carried over),
# and every `*.test.yaml` in the rule-tests directory runs under `promtool test rules`. It also
# pins the Grafana-only fields promtool cannot run (for, severity, noDataState, folder, uid).
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: check-alert-rules.sh <alerts.yaml> <rule-tests-dir>" >&2
  exit 2
fi
alerts="$1"
tests_dir="$2"

# uid -> for, severity (the operator's durations and the alert-severity standard)
expected='{
  "pg-repo-runner-tips-failing":       {"for": "15m",  "severity": "error"},
  "pg-repo-runner-tips-stale":         {"for": "5m",   "severity": "error"},
  "pg-repo-runner-tip-unverified":     {"for": "2h",   "severity": "warning"},
  "pg-repo-runner-red-no-newer-tip":   {"for": "24h",  "severity": "warning"},
  "pg-repo-runner-diverged":           {"for": "1h",   "severity": "warning"},
  "pg-repo-runner-surface-stuck":      {"for": "168h", "severity": "warning"}
}'

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

alerts_json="$(yq -o=json -I=0 '.' "$alerts")"

printf '%s' "$alerts_json" | jq -e '.apiVersion == 1' >/dev/null || fail "apiVersion must be 1"
printf '%s' "$alerts_json" | jq -e '[.. | objects | select(has("folderUid"))] | length == 0' >/dev/null ||
  fail "a folderUid is pinned"
printf '%s' "$alerts_json" | jq -e '[.groups[].folder] | unique == ["Host"]' >/dev/null ||
  fail "every group must be in the Host folder"
printf '%s' "$alerts_json" | jq -e --argjson e "$expected" '
  ([.groups[].rules[].uid] | sort) == ($e | keys | sort)' >/dev/null ||
  fail "the file must hold exactly the expected rule uids"
printf '%s' "$alerts_json" | jq -e --argjson e "$expected" '
  all(.groups[].rules[];
    . as $r | $e[$r.uid] as $x
    | $r.condition == "C"
      and (($r.data | map(.refId)) == ["A", "B", "C"])
      and $r.for == $x.for
      and $r.labels.severity == $x.severity
      and ($r.labels.severity | IN("critical", "error", "warning", "info"))
      and $r.noDataState == "OK"
      and ($r.uid | length) <= 40
      and ($r.data[2].model.conditions[0].evaluator.type == "gt"))' >/dev/null ||
  fail "each rule: condition C over A B C, expected for and severity, noDataState OK, gt threshold, uid <= 40 chars"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cp "$tests_dir"/*.test.yaml "$work/"
cd "$work"

# every rule must have a test file named after its uid minus the prefix
for uid in $(printf '%s' "$expected" | jq -r 'keys[]'); do
  [ -f "${uid#pg-repo-runner-}.test.yaml" ] || fail "no ${uid#pg-repo-runner-}.test.yaml for $uid"
done

printf '%s\n' "$alerts_json" | jq '
  { groups: [ {
      name: "wrapped",
      interval: "1m",
      rules: [ .groups[].rules[]
        | (.data[] | select(.refId == "A") | .model.expr) as $a
        | (.data[] | select(.refId == "C") | .model.conditions[0].evaluator.params[0]) as $t
        | { alert: .uid, expr: "(\($a)) > \($t)", for: .for, labels: .labels } ]
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
