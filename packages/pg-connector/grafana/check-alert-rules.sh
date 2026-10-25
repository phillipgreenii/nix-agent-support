#!/usr/bin/env bash
# PromQL behaviour check for the pg-connector Grafana alert rules (github: docket pg2-z5fax.6;
# Jira daemon: docket pg2-2qyyh.2).
#
#   check-alert-rules.sh <alerts.yaml> <rule-tests-dir>
#
# Modelled on packages/pg-router/check-alert-rules.sh. Every Prometheus rule of the file has its
# refId A wrapped as a Prometheus `alert:` rule `expr: (<A>) <op> <threshold>` (op and threshold
# from the refId C evaluator, `for` and labels carried over) next to a recording rule
# `wrapped:<uid with underscores>` over the same expression, so a case can read the wrapped
# expression, and the `<uid>.test.yaml` of each such rule in the rule-tests directory runs under `promtool test rules`.
#
# The Loki rules are not promtool-testable (check-loki-rules.sh covers them). The
# Grafana-only fields (noDataState, execErrState, annotations) cannot run under promtool; they are
# pinned by check-alert-structure.sh. This script asserts only the structure the wrapping depends
# on: the rule exists exactly once, condition C over refIds A, B, C, and a single gt or lt
# threshold evaluator.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: check-alert-rules.sh <alerts.yaml> <rule-tests-dir>" >&2
  exit 2
fi
alerts="$1"
tests_dir="$2"
if [ ! -r "$alerts" ]; then
  echo "check-alert-rules.sh: cannot read $alerts" >&2
  exit 2
fi
if [ ! -d "$tests_dir" ]; then
  echo "check-alert-rules.sh: not a directory: $tests_dir" >&2
  exit 2
fi

# Every rule uid this check wraps and tests: each rule of the file whose refId A reads the
# Prometheus datasource (the Loki rules are not promtool-testable). A rule added to the file is
# picked up here and needs one <uid>.test.yaml under rule-tests/. The rule-tests directory may hold
# the tests of several rule files; only the files named for this file's uids are copied and run.
fail() {
  echo "FAIL: $*" >&2
  exit 1
}

alerts_json="$(yq -o=json -I=0 '.' "$alerts")"
rule_uids="$(printf '%s' "$alerts_json" | jq -c '
  [.groups[].rules[] | select(.data[0].datasourceUid == "prometheus") | .uid]')"
if [ "$(printf '%s' "$rule_uids" | jq 'length')" -eq 0 ]; then
  fail "no Prometheus rule found in $alerts"
fi

for uid in $(printf '%s' "$rule_uids" | jq -r '.[]'); do
  printf '%s' "$alerts_json" | jq -e --arg uid "$uid" '
    [.groups[].rules[] | select(.uid == $uid)] | length == 1' >/dev/null ||
    fail "$uid: rule must exist exactly once"
  printf '%s' "$alerts_json" | jq -e --arg uid "$uid" '
    .groups[].rules[] | select(.uid == $uid)
    | .condition == "C"
      and ((.data | map(.refId)) == ["A", "B", "C"])
      and ((.data[] | select(.refId == "C") | .model.conditions | length) == 1)
      and ((.data[] | select(.refId == "C") | .model.conditions[0].evaluator.type) as $t
           | ($t == "gt" or $t == "lt"))
      and ((.data[] | select(.refId == "C") | .model.conditions[0].evaluator.params | length) == 1)' >/dev/null ||
    fail "$uid: condition C must be a single gt or lt threshold over refIds A, B, C"
  [ -f "$tests_dir/$uid.test.yaml" ] ||
    fail "$uid: missing test file $tests_dir/$uid.test.yaml"
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
for uid in $(printf '%s' "$rule_uids" | jq -r '.[]'); do
  cp "$tests_dir/$uid.test.yaml" "$work/"
done
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
