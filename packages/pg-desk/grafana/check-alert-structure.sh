#!/usr/bin/env bash
# Structure check for the pg-desk PR lane tripwire rule (pg-desk-pr-never-hydrated) in
# grafana/alerting/alerts.yaml, with plain negative cases proving the check can fail.
#
#   check-alert-structure.sh <alerts.yaml>
#   check-alert-structure.sh --self-test <alerts.yaml>
#
# Needs yq (mikefarah) and jq. Every failure line starts with RULE-Tn so a negative case can
# assert the exact rule that fired.
#
#   T1  the rule uid exists exactly once and is at most 40 characters (Grafana's cap)
#   T2  noDataState is OK: the absence test is in the expression, never in noDataState
#   T3  the expression ends with the SYSTEM_PAUSE guard, byte for byte the guard of
#       pg-desk-sync-stale, so the rule is suppressed under SYSTEM_PAUSE
#   T4  the expression carries the absence branch (liveness up while the gauge has no series)
#   T5  the rule shape: condition C, refIds A B C, a non-empty for, summary and description,
#       labels with exactly the one key severity, Prometheus datasource with instant: true
#
# The negative cases of --self-test: a rule file whose guard has been removed MUST fail T3, and
# one whose noDataState differs MUST fail T2 (each firing only its own rule), as must one with
# the absence branch removed (T4).
#
# Exit codes: 0 consistent (self-test: the real input passes and every negative case fails with
# exactly its own rule), 1 a rule failed, 2 usage error or unreadable input or a missing tool.
set -euo pipefail

usage() {
  echo "usage: check-alert-structure.sh [--self-test] <alerts.yaml>" >&2
  exit 2
}

selftest=0
if [ "${1:-}" = "--self-test" ]; then
  selftest=1
  shift
fi
[ "$#" -eq 1 ] || usage
alerts="$1"
if [ ! -r "$alerts" ] || [ ! -f "$alerts" ]; then
  echo "check-alert-structure.sh: cannot read $alerts" >&2
  exit 2
fi
for tool in yq jq; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "check-alert-structure.sh: $tool is not on PATH" >&2
    exit 2
  fi
done

rule_uid="pg-desk-pr-never-hydrated"
guard_uid="pg-desk-sync-stale"
absence='pg_desk_liveness == 1 unless on() pg_desk_pr_unhydrated'

# lint <alerts.yaml>: prints RULE-Tn lines to stderr, returns 1 when any fired.
lint() {
  local file="$1" status=0 json count expr guard
  json="$(yq -o=json '.' "$file")"

  # T1
  count="$(jq --arg uid "$rule_uid" '[.groups[]?.rules[]? | select(.uid == $uid)] | length' <<<"$json")"
  if [ "$count" != "1" ]; then
    echo "RULE-T1: want the uid $rule_uid exactly once, found it $count times" >&2
    status=1
  fi
  if [ "${#rule_uid}" -gt 40 ]; then
    echo "RULE-T1: uid longer than Grafana's 40-character cap" >&2
    status=1
  fi
  if [ "$count" != "1" ]; then
    return 1 # nothing below can be read without the rule
  fi

  # T2
  if ! jq -e --arg uid "$rule_uid" '.groups[].rules[] | select(.uid == $uid) | .noDataState == "OK"' <<<"$json" >/dev/null; then
    echo "RULE-T2: noDataState is not OK (the absence test belongs in the expression)" >&2
    status=1
  fi

  expr="$(jq -r --arg uid "$rule_uid" '.groups[].rules[] | select(.uid == $uid) | .data[] | select(.refId == "A") | .model.expr // empty' <<<"$json")"
  guard="$(jq -r --arg uid "$guard_uid" '.groups[].rules[] | select(.uid == $uid) | .data[] | select(.refId == "A") | .model.expr // empty' <<<"$json" | grep -oE 'unless on\(\) \(pg_router_gates_active\{type="SYSTEM_PAUSE"\} == 1\)$' || true)"

  # T3
  if [ -z "$guard" ]; then
    echo "RULE-T3: the reference rule $guard_uid has no SYSTEM_PAUSE guard to copy" >&2
    status=1
  elif [ "${expr%"$guard"}" = "$expr" ]; then
    echo "RULE-T3: the expression does not end with the SYSTEM_PAUSE guard of $guard_uid: $guard" >&2
    status=1
  fi

  # T4
  case "$expr" in
  *"$absence"*) ;;
  *)
    echo "RULE-T4: the expression lacks the absence branch: $absence" >&2
    status=1
    ;;
  esac

  # T5
  if ! jq -e --arg uid "$rule_uid" '
      .groups[].rules[] | select(.uid == $uid) |
      (.condition == "C")
      and (([.data[]?.refId] | sort) == ["A", "B", "C"])
      and ((.for // "") != "")
      and ((.annotations.summary // "") != "")
      and ((.annotations.description // "") != "")
      and ((.labels // {} | keys) == ["severity"])
      and (.data[0].datasourceUid == "prometheus")
      and (.data[0].model.instant == true)' <<<"$json" >/dev/null; then
    echo "RULE-T5: rule shape wrong (condition C, refIds A B C, for, summary, description, labels {severity}, Prometheus instant query)" >&2
    status=1
  fi
  return "$status"
}

if [ "$selftest" -eq 0 ]; then
  lint "$alerts"
  exit $?
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The real input must pass, silently.
if ! out="$(lint "$alerts" 2>&1)"; then
  echo "self-test: the real input fails:" >&2
  echo "$out" >&2
  exit 1
fi

fail=0
# negative <rule> <description> <yq-expression>: the changed file MUST fail with exactly <rule>.
negative() {
  local rule="$1" desc="$2" yexpr="$3" dir ids
  dir="$(mktemp -d "$work/n.XXXXXX")"
  yq "$yexpr" "$alerts" >"$dir/alerts.yaml"
  if cmp -s "$alerts" "$dir/alerts.yaml"; then
    echo "self-test: negative case $rule ($desc) did not change the file" >&2
    fail=1
    return
  fi
  if ids="$(lint "$dir/alerts.yaml" 2>&1 >/dev/null)"; then
    echo "self-test: negative case $rule ($desc) passed" >&2
    fail=1
    return
  fi
  ids="$(printf '%s\n' "$ids" | grep -o '^RULE-T[0-9]*' | sort -u | tr '\n' ' ')"
  if [ "$ids" != "RULE-$rule " ]; then
    echo "self-test: negative case $rule ($desc) fired '${ids}' instead of only RULE-$rule" >&2
    fail=1
  fi
}

rule_of() { printf '.groups[0].rules[] | select(.uid == "%s")' "$rule_uid"; }

negative T3 "the SYSTEM_PAUSE guard removed from the expression" \
  "($(rule_of) | .data[0].model.expr) |= sub(\" unless on\\(\\) \\(pg_router_gates_active.*\$\"; \"\")"
negative T2 "noDataState differs (Alerting)" \
  "($(rule_of) | .noDataState) = \"Alerting\""
negative T4 "the absence branch removed from the expression" \
  "($(rule_of) | .data[0].model.expr) |= sub(\" or \\(pg_desk_liveness == 1 unless on\\(\\) pg_desk_pr_unhydrated\\)\"; \"\")"

[ "$fail" -eq 0 ]
