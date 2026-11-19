#!/usr/bin/env bash
# PromQL behaviour check for pg-router Grafana alert rules (pg2-owccx).
#
#   check-alert-rules.sh <alerts.yaml> <rule-tests-dir>
#
# Modelled on packages/beads-exporter/check-alert-rules.sh. Every rule listed in
# RULE_UIDS has its refId A wrapped as a Prometheus `alert:` rule
# `expr: (<A>) <op> <threshold>` (op and threshold from the refId C evaluator, `for`
# and labels carried over) next to a recording rule `wrapped:<uid>` over the same
# expression, so a case can count instances (plus `raw:<uid with _>` over the bare refId A
# expression, whose 0 samples the `> 0` wrapper drops), and every `*.test.yaml` in the
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
rule_uids='["pg-router-listener-oldest-pending", "pg-router-pool-full-idle", "pg-router-ccpool-dead-needs-input", "pg-router-gate-held", "pg-router-sensor-stale", "pg-router-resource-held-nonbest", "pg-router-role-paused-long", "pg-router-sensor-pull-failing", "pg-router-override-left", "pg-router-level-events-rejected", "pg-router-queue-stalled", "pg-router-queue-depth-growing", "pg-router-queue-slow-growth", "pg-router-event-dispatch-failed", "pg-router-pr-dispatch-failed", "pg-router-failure-rate", "pg-router-role-stalled", "pg-router-role-busy-long", "pg-router-worktree-growth", "pg-router-liveness-down", "pg-router-ccpool-janitor-stale", "pg-router-ccpool-janitor-deadline", "pg-router-ccpool-janitor-idle", "pg-router-ccpool-janitor-stagnant", "pg-router-ccpool-janitor-no-progress", "pg-router-ccpool-janitor-list-timeout","pg-router-ccpool-pool-capacity-absent", "pg-router-ccpool-no-launch-while-queued", "pg-router-source-persistent-failure", "pg-router-producer-load-backoff", "pg-router-git-auth-expiring", "pg-router-reoffered-after-shutdown", "pg-router-supervisors-per-session"]'

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
           == (if $uid == "pg-router-liveness-down"
               then { type: "lt", params: [1] }
               else { type: "gt", params: [0] } end))' >/dev/null ||
    fail "$uid: condition C must be a single gt 0 threshold (lt 1 for pg-router-liveness-down) over refIds A, B, C"
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cp "$tests_dir"/*.test.yaml "$work/"
cd "$work"

# gen_rules <uids-json> <out-file>: wrap refId A of every listed rule uid as an alert rule
# plus the `wrapped:` and `raw:` recording rules.
gen_rules() {
  printf '%s\n' "$alerts_json" | jq --argjson uids "$1" '
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
              { record: ("wrapped:" + (.uid | gsub("-"; "_"))), expr: $wrapped },
              { record: ("raw:" + (.uid | gsub("-"; "_"))), expr: $a } ) ]
      } ] }' >"$2"
}

# The whole rule set must stay well-formed.
gen_rules "$rule_uids" rules.yml
echo "--- wrapped rules"
yq '.' rules.yml
promtool check rules rules.yml

# promtool evaluates every rule of the rules file at each evaluation step, so a case pays for
# all of them whatever it tests. Each test file therefore runs against a rules.yml holding only
# the rules whose uid (dash form, or underscore form as in `wrapped:`/`raw:`) its text names.
# A rule a file does not name cannot influence its assertions: a case reads series only by
# alertname or by a `wrapped:`/`raw:` name.
for t in *.test.yaml; do
  echo "--- $t"
  yq '.tests[].name | "case: " + .' "$t"
  used='[]'
  for uid in $(printf '%s' "$rule_uids" | jq -r '.[]'); do
    if grep -qF -e "$uid" -e "${uid//-/_}" "$t"; then
      used="$(printf '%s' "$used" | jq -c --arg u "$uid" '. + [$u]')"
    fi
  done
  [ "$used" != '[]' ] || fail "$t: names none of the rule uids in rule_uids"
  echo "rules: $(printf '%s' "$used" | jq -r 'join(" ")')"
  d="run-${t%.test.yaml}"
  mkdir "$d"
  gen_rules "$used" "$d/rules.yml"
  cp "$t" "$d/$t"
  promtool check rules "$d/rules.yml" >/dev/null
  (cd "$d" && promtool test rules "$t")
done

echo "OK: PromQL cases pass"
