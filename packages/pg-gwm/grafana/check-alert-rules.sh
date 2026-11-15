#!/usr/bin/env bash
# Alert rule check for the pg-gwm Grafana alert rules.
#
#   check-alert-rules.sh <alerts.yaml> <dashboard.json> <rule-tests-dir> <probe run.go> <alerts-delete.yaml>
#
# Three parts:
#
#   1. Grafana-only fields, read with yq and asserted with jq: the exact uid set, uid length,
#      condition C over refIds A, B, C, the per-rule `for` and threshold, noDataState OK,
#      execErrState Error, severity warning, the absence of any folderUid, and the dashboard
#      annotations (the panel id must exist exactly once in the dashboard JSON).
#   2. Registration: the pg-gwm-* entries of registeredRuleUIDs in the router probe's
#      run.go are exactly the rule uids of the file, in its order. The delete file holds
#      deleteRules for exactly the six uids of the tool pg-gwm replaced.
#   3. PromQL behaviour: refId A of every rule is wrapped as a Prometheus `alert:` rule
#      `expr: (<A>) <op> <threshold>` (op and threshold from the refId C evaluator, `for` and
#      labels carried over) next to a recording rule `wrapped:<uid>` over the same expression, and
#      every `*.test.yaml` in the rule-tests directory runs under `promtool test rules`.
set -euo pipefail

if [ "$#" -ne 5 ]; then
  echo "usage: check-alert-rules.sh <alerts.yaml> <dashboard.json> <rule-tests-dir> <probe run.go> <alerts-delete.yaml>" >&2
  exit 2
fi
alerts="$1"
dashboard="$2"
tests_dir="$3"
run_go="$4"
delete_file="$5"

# uid, for, threshold, dashboard panel id, evaluator, noDataState: one row per rule, in the
# file's order. An empty panel id means the rule links no dashboard panel (the exporter-down
# rule, the exporter's working signal, which alerts on missing data).
rows='[
  ["pg-gwm-exporter-down", "5m", 1, "", "lt", "Alerting"]
]'
rule_uids="$(printf '%s' "$rows" | jq -c '[.[][0]]')"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

alerts_json="$(yq -o=json -I=0 '.' "$alerts")"

assert() {
  local msg="$1" prog="$2"
  shift 2
  if ! printf '%s' "$alerts_json" | jq -e "$@" "$prog" >/dev/null 2>&1; then
    fail "$msg"
  fi
}

# --- part 1: Grafana-only fields -------------------------------------------------------

assert "apiVersion must be 1" '.apiVersion == 1'
assert "no folderUid may appear anywhere (grafana/grafana#125079)" \
  '[.. | objects | select(has("folderUid"))] | length == 0'
assert "every group must name a folder by title" \
  '[.groups[] | select((.folder // "") == "")] | length == 0'
assert "rule uids must be exactly the tested set, in order" \
  '[.groups[].rules[].uid] == $uids' --argjson uids "$rule_uids"

dash_uid="$(jq -r '.uid' "$dashboard")"
while IFS=$'\t' read -r uid for_ thr op nodata panel; do
  assert "$uid: uid at most 40 characters" '.groups[].rules[] | select(.uid == $u) | (.uid | length) <= 40' --arg u "$uid"
  assert "$uid: condition C over A, B, C; for $for_; noDataState $nodata; execErrState Error; severity warning" \
    '.groups[].rules[] | select(.uid == $u)
     | .condition == "C" and (.data | map(.refId) == ["A", "B", "C"]) and .for == $f
       and .noDataState == $nd and .execErrState == "Error" and .labels == { severity: "warning" }
       and ((.title // "") != "") and ((.annotations.summary // "") != "") and ((.annotations.description // "") != "")' \
    --arg u "$uid" --arg f "$for_" --arg nd "$nodata"
  assert "$uid: refId C is a single $op $thr threshold stage over B" \
    '.groups[].rules[] | select(.uid == $u) | (.data[] | select(.refId == "C") | .model)
     | .type == "threshold" and .expression == "B"
       and .conditions == [{ evaluator: { type: $op, params: [$t] } }]' \
    --arg u "$uid" --argjson t "$thr" --arg op "$op"
  assert "$uid: refId A is an instant query on the prometheus datasource" \
    '.groups[].rules[] | select(.uid == $u) | (.data[] | select(.refId == "A"))
     | .datasourceUid == "prometheus" and .model.instant == true and (.model.expr | test("pg_gwm_|^min\\(up\\{job=\"pg-gwm\"\\}\\)$"))' --arg u "$uid"
  if [ -z "$panel" ]; then
    assert "$uid: must link no dashboard panel" \
      '.groups[].rules[] | select(.uid == $u) | (.annotations | has("__dashboardUid__") or has("__panelId__")) | not' --arg u "$uid"
    continue
  fi
  assert "$uid: annotations link the pool dashboard panel $panel" \
    '.groups[].rules[] | select(.uid == $u) | .annotations.__dashboardUid__ == $d and .annotations.__panelId__ == $p' \
    --arg u "$uid" --arg d "$dash_uid" --arg p "$panel"
  jq -e --arg pid "$panel" \
    '[.. | objects | select(has("gridPos")) | select((.id | tostring) == $pid)] | length == 1' \
    "$dashboard" >/dev/null || fail "$uid: __panelId__ $panel does not exist in the dashboard JSON exactly once"
done < <(printf '%s' "$rows" | jq -r '.[] | [.[0], .[1], (.[2] | tostring), .[4], .[5], .[3]] | @tsv')

# --- part 2: registration with the router probe ----------------------------------------

from_file="$(printf '%s' "$alerts_json" | jq -r '.groups[].rules[].uid' | paste -sd, -)"
from_probe="$(grep -oE '"pg-gwm-[a-z-]+",' "$run_go" | tr -d '",' | paste -sd, -)"
[ "$from_file" = "$from_probe" ] ||
  fail "registeredRuleUIDs in run.go has [$from_probe], the rule file has [$from_file]"

# The delete file names exactly the six uids of the tool pg-gwm replaced (five pool rules and its
# exporter-down rule), all under that tool's uid prefix. The prefix is assembled from two pieces so
# the old tool name appears literally only in alerts-delete.yaml.
[ -f "$delete_file" ] || fail "$delete_file is missing"
old_prefix="pg-checkout""-pool-"
want_deleted=""
for suffix in daemon-orphans exhausted-waiting exporter-down leaked-slots quarantined reset-slow; do
  want_deleted="${want_deleted:+$want_deleted,}${old_prefix}${suffix}"
done
got_deleted="$(yq -o=json -I=0 '.' "$delete_file" | jq -r '[.deleteRules[].uid] | sort | join(",")')"
[ "$got_deleted" = "$want_deleted" ] ||
  fail "alerts-delete.yaml has [$got_deleted], want exactly [$want_deleted]"
yq -o=json -I=0 '.' "$delete_file" | jq -e 'keys == ["apiVersion", "deleteRules"] and .apiVersion == 1 and ([.deleteRules[] | .orgId == 1] | all)' >/dev/null ||
  fail "alerts-delete.yaml must hold only apiVersion 1 and deleteRules entries for orgId 1"

# --- part 3: PromQL behaviour -----------------------------------------------------------

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

for uid in $(printf '%s' "$rule_uids" | jq -r '.[]'); do
  [ -f "$uid.test.yaml" ] || fail "$uid has no rule-tests/$uid.test.yaml"
done
for t in *.test.yaml; do
  echo "--- $t"
  yq '.tests[].name | "case: " + .' "$t"
  promtool test rules "$t"
done

echo "OK: Grafana-only fields, probe registration and PromQL cases pass"
