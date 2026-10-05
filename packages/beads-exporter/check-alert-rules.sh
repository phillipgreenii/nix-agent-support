#!/usr/bin/env bash
# Alert rule check for the beads-exporter Grafana alert rules.
#
#   check-alert-rules.sh <alerts.yaml> <dashboard.json> <rule-tests-dir>
#
# Two parts:
#
#   1. Grafana-only fields, read with yq and asserted with jq: the fixed uid and title,
#      for, noDataState, execErrState, severity, the summary and description text, the
#      dashboard annotations (the panel id must exist in the dashboard JSON), the
#      threshold stage and the absence of any folderUid.
#   2. PromQL behaviour: refId A of every rule listed in RULE_UIDS is wrapped as a
#      Prometheus `alert:` rule `expr: (<A>) <op> <threshold>` (op and threshold from the
#      refId C evaluator, `for` and labels carried over) next to a recording rule
#      `wrapped:<uid>` over the same expression, so a case can count instances, and every
#      `*.test.yaml` in the rule-tests directory runs under `promtool test rules`.
#
# Grafana-only behaviour that promtool cannot run (the noDataState mapping) is covered by
# part 1 only.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: check-alert-rules.sh <alerts.yaml> <dashboard.json> <rule-tests-dir>" >&2
  exit 2
fi
alerts="$1"
dashboard="$2"
tests_dir="$3"

# Every rule uid this check wraps and tests. A rule added to the alerts file extends this
# list together with its cases under rule-tests/.
rule_uids='["beads-collect-failing"]'

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

alerts_json="$(yq -o=json -I=0 '.' "$alerts")"

# assert <message> <jq program> [jq args...]: the program must output true.
assert() {
  local msg="$1" prog="$2"
  shift 2
  if ! printf '%s' "$alerts_json" | jq -e "$@" "$prog" >/dev/null 2>&1; then
    fail "$msg"
  fi
}

rule() { # rule <uid> <jq path over the rule>
  printf '%s' "$alerts_json" | jq -r --arg uid "$1" '.groups[].rules[] | select(.uid == $uid) | '"$2"
}

# --- part 1: Grafana-only fields -------------------------------------------------------

assert "apiVersion must be 1" '.apiVersion == 1'
assert "no folderUid may appear anywhere (grafana/grafana#125079)" \
  '[.. | objects | select(has("folderUid"))] | length == 0'
assert "every group must name a folder by title" \
  '[.groups[] | select((.folder // "") == "")] | length == 0'
assert "rule uids must be exactly the tested set" \
  '[.groups[].rules[].uid] | sort == ($uids | sort)' --argjson uids "$rule_uids"

uid=beads-collect-failing
assert "$uid: fixed uid, readable title, condition C" \
  '.groups[].rules[] | select(.uid == "beads-collect-failing")
   | .title == "Beads metrics not collected" and .condition == "C"'
assert "$uid: for 5m, noDataState OK, execErrState Error, severity warning" \
  '.groups[].rules[] | select(.uid == "beads-collect-failing")
   | .for == "5m" and .noDataState == "OK" and .execErrState == "Error"
     and .labels == { severity: "warning" }'
assert "$uid: summary and description text" \
  '.groups[].rules[] | select(.uid == "beads-collect-failing") | .annotations
   | .summary == "Beads metrics for {{ $labels.db }} not collected for 15+ min"
     and .description == $desc' \
  --arg desc 'Dashboard numbers for this db are missing. Find the reason in Beads → Collection health (errors by reason). stale_issues_jsonl: run mv $BEADS_DIR/issues.jsonl $BEADS_DIR/issues.jsonl.disabled-$(date +%s) (bd would auto-import it and clobber rows), then wait one poll. Otherwise run BEADS_DIR=<dir> bd --readonly --sandbox list -n 0 --json to see the bd error. Suppressed while the dolt probe reports the server or this db down.'
assert "$uid: refId A is the plain PromQL expression" \
  '.groups[].rules[] | select(.uid == "beads-collect-failing")
   | (.data[] | select(.refId == "A") | .model.expr) == $expr' \
  --arg expr 'max_over_time(beads_exporter_up[10m]) unless on(db) (mysql_probe_success{target="beads-dolt",mode="read"} < 1) unless on() (mysql_probe_success{target="beads-dolt",mode="connect"} < 1)'
assert "$uid: refId C is a single lt 1 threshold stage" \
  '.groups[].rules[] | select(.uid == "beads-collect-failing")
   | (.data | map(.refId) == ["A", "B", "C"])
     and ((.data[] | select(.refId == "C") | .model)
          | .type == "threshold" and .conditions == [{ evaluator: { type: "lt", params: [1] } }])'

dash_uid="$(jq -r '.uid' "$dashboard")"
panel_id="$(rule "$uid" '.annotations.__panelId__')"
[ "$(rule "$uid" '.annotations.__dashboardUid__')" = "$dash_uid" ] ||
  fail "$uid: __dashboardUid__ must be the dashboard uid ($dash_uid)"
jq -e --arg pid "$panel_id" \
  '[.. | objects | select(has("gridPos")) | select((.id | tostring) == $pid)] | length == 1' \
  "$dashboard" >/dev/null ||
  fail "$uid: __panelId__ $panel_id does not exist in the dashboard JSON exactly once"

# --- part 2: PromQL behaviour ----------------------------------------------------------

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

echo "OK: Grafana-only fields and PromQL cases pass"
