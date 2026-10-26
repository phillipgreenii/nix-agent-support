#!/usr/bin/env bash
# PromQL behaviour check for the pg-desk PR lane outcome alert rules (PR lane refactor, P8a-1).
#
#   check-alert-rules.sh <alerts.yaml> <rule-tests-dir>
#   check-alert-rules.sh --self-test <alerts.yaml> <rule-tests-dir>
#
# Modelled on packages/pg-router/check-alert-rules.sh. Every rule listed in RULE_UIDS has its
# refId A wrapped as a Prometheus `alert:` rule `expr: (<A>) <op> <threshold>` (op and threshold
# from the refId C evaluator, `for` and labels carried over) next to a recording rule
# `wrapped:<uid>` over the same expression, so a case can count instances, and every
# `*.test.yaml` in the rule-tests directory runs under `promtool test rules`.
#
# Grafana-only fields (noDataState, execErrState, annotations) are pinned by
# check-alert-structure.sh (noDataState and the SYSTEM_PAUSE guard) and by the Go tests in
# internal/alertrules; promtool cannot run them. This script asserts only the structure the
# wrapping depends on.
#
# --self-test runs the cases against the real rule file (they MUST pass) and then against rule
# files with one guard deliberately broken, each of which MUST make the cases fail: the primary
# rule without its SYSTEM_PAUSE guard, without its absence branch, the stale-anchor rule without its
# apply gate, the residual rule reading every mode, the hard-failure rule without its first-event
# branch, the sweep-bound rule without its scope, the lookup-ceiling rule without its ceiling guard and the
# hidden-PR rule without its SYSTEM_PAUSE guard. A case set that passed over any of them
# would be vacuous.
#
# Exit codes: 0 every case passed (self-test: and every broken rule file was rejected), 1 a check
# or a case failed, 2 usage error.
set -euo pipefail

usage() {
  echo "usage: check-alert-rules.sh [--self-test] <alerts.yaml> <rule-tests-dir>" >&2
  exit 2
}

selftest=0
if [ "${1:-}" = "--self-test" ]; then
  selftest=1
  shift
fi
[ "$#" -eq 2 ] || usage
alerts="$1"
tests_dir="$2"

# Every rule uid this check wraps and tests. A rule added here extends the cases under rule-tests/.
rule_uids='["pg-desk-pr-never-hydrated", "pg-desk-pr-residual-after-apply", "pg-desk-pr-stale-anchor", "pg-desk-pr-anchor-hydration-old", "pg-desk-pr-lane-hard-failure", "pg-desk-pr-lane-failure-rate", "pg-desk-pr-hydration-failing", "pg-desk-pr-hydrate-dead-man", "pg-desk-pr-hidden-open-anchor", "pg-desk-pr-lookup-ceiling", "pg-desk-sweep-bound-violated", "pg-desk-watch-listing-degraded"]'

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

run_cases() {
  local alerts="$1" tests_dir="$2" alerts_json uid t
  alerts_json="$(yq -o=json -I=0 '.' "$alerts")"

  for uid in $(printf '%s' "$rule_uids" | jq -r '.[]'); do
    printf '%s' "$alerts_json" | jq -e --arg uid "$uid" '
    [.groups[].rules[] | select(.uid == $uid)] | length == 1' >/dev/null ||
      fail "$uid: rule must exist exactly once"
    printf '%s' "$alerts_json" | jq -e --arg uid "$uid" '
    .groups[].rules[] | select(.uid == $uid)
    | .condition == "C"
      and ((.data | map(.refId)) == ["A", "B", "C"])
      and ((.data[] | select(.refId == "C") | .model.conditions[0].evaluator.type) as $t
           | ($t == "gt" or $t == "gte"))
      and ((.data[] | select(.refId == "C") | .model.conditions[0].evaluator.params | length) == 1)' >/dev/null ||
      fail "$uid: condition C must be a single gt or gte threshold over refIds A, B, C"
  done

  # A rule test directory with no case files would pass vacuously.
  ls "$tests_dir"/*.test.yaml >/dev/null 2>&1 || fail "no *.test.yaml case files in $tests_dir"

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
        | ({ gt: ">", gte: ">=" }[$e.type] // error("unsupported evaluator \($e.type)")) as $op
        | "(\($a)) \($op) \($e.params[0])" as $wrapped
        | ( { alert: .uid, expr: $wrapped, for: .for, labels: .labels },
            { record: ("wrapped:" + (.uid | gsub("-"; "_"))), expr: $wrapped } ) ]
    } ] }' >rules.yml

  echo "--- wrapped rules"
  yq '.' rules.yml
  promtool check rules rules.yml

  # Every wrapped rule must be exercised by at least one case file named after its uid.
  for uid in $(printf '%s' "$rule_uids" | jq -r '.[]'); do
    [ -f "$uid.test.yaml" ] || fail "$uid: no case file $uid.test.yaml in $tests_dir"
  done

  for t in *.test.yaml; do
    echo "--- $t"
    yq '.tests[].name | "case: " + .' "$t"
    promtool test rules "$t"
  done

  echo "OK: PromQL cases pass"
}

if [ "$selftest" -eq 0 ]; then
  run_cases "$alerts" "$tests_dir"
  exit 0
fi

work_root="$(mktemp -d)"
trap 'rm -rf "$work_root"' EXIT

# The real input must pass.
if ! bash "$0" "$alerts" "$tests_dir" >"$work_root/real.log" 2>&1; then
  echo "self-test: the real input fails:" >&2
  cat "$work_root/real.log" >&2
  exit 1
fi

selftest_fail=0
# broken <description> <yq-expression>: the cases MUST fail against the changed rule file.
broken() {
  local desc="$1" yexpr="$2" dir
  dir="$(mktemp -d "$work_root/b.XXXXXX")"
  yq "$yexpr" "$alerts" >"$dir/alerts.yaml"
  if cmp -s "$alerts" "$dir/alerts.yaml"; then
    echo "self-test: broken case ($desc) did not change the file" >&2
    selftest_fail=1
    return
  fi
  # A fresh process, not a subshell: `set -e` is ignored inside an `if (...)` subshell.
  if bash "$0" "$dir/alerts.yaml" "$tests_dir" >"$dir/out.log" 2>&1; then
    echo "self-test: the cases PASSED against a rule file with $desc" >&2
    selftest_fail=1
  elif ! grep -q 'FAILED:' "$dir/out.log"; then
    # Rejected, but not by a failing case (a syntax error, say): the mutant proves nothing.
    echo "self-test: the rule file with $desc was rejected, but not by a failing case:" >&2
    tail -5 "$dir/out.log" >&2
    selftest_fail=1
  else
    echo "self-test: rejected by a failing case: $desc"
  fi
}
expr_path() { printf '(.groups[0].rules[] | select(.uid == "%s") | .data[] | select(.refId == "A") | .model.expr)' "$1"; }

broken "the never-hydrated SYSTEM_PAUSE guard removed" \
  "$(expr_path pg-desk-pr-never-hydrated) |= sub(\" unless on[(][)] [(]pg_router_gates_active.*\$\"; \"\")"
broken "the never-hydrated absence branch removed" \
  "$(expr_path pg-desk-pr-never-hydrated) |= sub(\" or [(]pg_desk_liveness == 1 unless on[(][)] pg_desk_pr_unhydrated[)]\"; \"\")"
broken "the stale-anchor apply gate removed" \
  "$(expr_path pg-desk-pr-stale-anchor) |= sub(\" and on[(][)] [(]pg_desk_pr_lane_mode[{]mode=.apply.[}] == 1[)]\"; \"\")"
broken "the residual rule reading every mode" \
  "$(expr_path pg-desk-pr-residual-after-apply) |= sub(\"[{]mode=.apply.[}]\"; \"\")"
broken "the hard-failure first-event branch removed" \
  "$(expr_path pg-desk-pr-lane-hard-failure) |= sub(\" or [(]sum by .*offset 10m[)][)]\"; \"\")"
broken "the sweep-bound scope removed" \
  "$(expr_path pg-desk-sweep-bound-violated) |= sub(\"[{]type=.*[}]\"; \"\")"
broken "the dead-man's role selector widened to every role" \
  "$(expr_path pg-desk-pr-hydrate-dead-man) |= sub(\"[{]role=.pr-change.[}]\"; \"\")"
broken "the lookup-ceiling rule without its ceiling greater than 0 guard" \
  "$(expr_path pg-desk-pr-lookup-ceiling) |= sub(\"[(]max[(]pg_desk_pr_lane_lookup_ceiling[)] > 0[)]\"; \"max(pg_desk_pr_lane_lookup_ceiling)\")"
broken "the hidden-PR rule's SYSTEM_PAUSE guard removed" \
  "$(expr_path pg-desk-pr-hidden-open-anchor) |= sub(\" unless on[(][)] [(]pg_router_gates_active.*\$\"; \"\")"

[ "$selftest_fail" -eq 0 ]
echo "OK: self-test passes"
