#!/usr/bin/env bash
# Structure check for the pg-desk PR lane outcome alert rules in grafana/alerting/alerts.yaml,
# with plain negative cases proving the check can fail.
#
#   check-alert-structure.sh <alerts.yaml>
#   check-alert-structure.sh --self-test <alerts.yaml>
#
# Needs yq (mikefarah) and jq. Every failure line starts with RULE-Tn so a negative case can
# assert the exact rule that fired. The rules checked are the OUTCOME_UIDS below: the primary
# never-hydrated tripwire (PR lane refactor P0b, finalized by P8a-1) and the outcome rules P8a-1
# adds (residual, stale anchor, decider and hydrate failures, hydrate dead-man).
#
#   T1  the rule uid exists exactly once and is at most 40 characters (Grafana's cap)
#   T2  noDataState is OK: an absence test belongs in the expression, never in noDataState
#   T3  the expression ends with the SYSTEM_PAUSE guard, byte for byte the guard of
#       pg-desk-sync-stale, so the rule is suppressed under SYSTEM_PAUSE
#   T4  (never-hydrated only) the expression carries the absence branch (liveness up while the
#       gauge has no series)
#   T5  the rule shape: condition C, refIds A B C, a non-empty for, summary and description,
#       labels with exactly the one key severity, Prometheus datasource with instant: true
#   T6  (the residual and the two stale-anchor rules) the apply gate: the residual rule reads the
#       mode="apply" series of its gauge, and each stale-anchor rule is gated on
#       pg_desk_pr_lane_mode{mode="apply"} == 1, so none of them fires while the lane plans
#   T7  (the sweep-bound and watch-listing rules) the scope: both read issue and thread only
#   T8  (the lookup-ceiling rule) the ratio is guarded by `ceiling > 0`, so a ceiling gauge of 0
#       (zero-filled until the decider reports) leaves the rule quiet instead of dividing by zero
#   T9  (the ingest-failed rule) the uid exists once, at most 40 characters, with noDataState:
#       Alerting guarded by pg-desk liveness: the expression carries the branch that answers 0
#       while pg-desk is down (so liveness-down speaks alone and NoData alerts only with pg-desk
#       up), and the rule has the T5 shape. It is NOT an outcome rule: noDataState is Alerting and
#       there is no SYSTEM_PAUSE guard (an empty result would alert).
#
# The negative cases of --self-test: for EVERY outcome rule a rule file whose guard has been
# removed MUST fail T3 and one whose noDataState differs MUST fail T2; the never-hydrated rule
# with its absence branch removed MUST fail T4; each apply-gated rule with its gate removed MUST
# fail T6; the ingest-failed rule with another noDataState or without its liveness guard MUST fail
# T9; and each scoped rule with its scope removed MUST fail T7, and the lookup-ceiling rule with its ceiling
# guard removed MUST fail T8 (each firing only its own rule).
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

# The outcome rules every check below applies to. The first is the primary tripwire.
primary_uid="pg-desk-pr-never-hydrated"
outcome_uids=(
  "pg-desk-pr-never-hydrated"
  "pg-desk-pr-residual-after-apply"
  "pg-desk-pr-stale-anchor"
  "pg-desk-pr-anchor-hydration-old"
  "pg-desk-pr-lane-hard-failure"
  "pg-desk-pr-lane-failure-rate"
  "pg-desk-pr-hydration-failing"
  "pg-desk-pr-hydrate-dead-man"
  "pg-desk-pr-hidden-open-anchor"
  "pg-desk-pr-lookup-ceiling"
)
# Rules that MUST be inert while the lane does not apply (T6), and what each must carry.
residual_uid="pg-desk-pr-residual-after-apply"
residual_gate='{mode="apply"}'
stale_uids=("pg-desk-pr-stale-anchor" "pg-desk-pr-anchor-hydration-old")
stale_gate='and on() (pg_desk_pr_lane_mode{mode="apply"} == 1)'
# Rules scoped to issue and thread (T7).
scoped_uids=("pg-desk-sweep-bound-violated" "pg-desk-watch-listing-degraded")
scope='type=~"issue|thread"'

# The lookup-ceiling rule divides by the ceiling gauge, so it MUST drop a ceiling of 0 (T8).
ceiling_uid="pg-desk-pr-lookup-ceiling"
ceiling_guard='/ (max(pg_desk_pr_lane_lookup_ceiling) > 0)'

# The failed-ingest rule (T9): noDataState Alerting, guarded by pg-desk liveness.
ingest_uid="pg-desk-ingest-failed-old"
ingest_guard=' or (vector(0) and on() absent(pg_desk_liveness == 1))'

guard_uid="pg-desk-sync-stale"
absence='pg_desk_liveness == 1 unless on() pg_desk_pr_unhydrated'

# expr_of <json> <uid>: refId A's expression of one rule ("" when absent).
expr_of() {
  jq -r --arg uid "$2" '[.groups[]?.rules[]? | select(.uid == $uid)][0] | (.data // [])[] | select(.refId == "A") | .model.expr // empty' <<<"$1"
}

# lint <alerts.yaml>: prints RULE-Tn lines to stderr, returns 1 when any fired.
lint() {
  local file="$1" status=0 json count expr guard uid
  json="$(yq -o=json '.' "$file")"

  guard="$(expr_of "$json" "$guard_uid" | grep -oE 'unless on\(\) \(pg_router_gates_active\{type="SYSTEM_PAUSE"\} == 1\)$' || true)"
  if [ -z "$guard" ]; then
    echo "RULE-T3: the reference rule $guard_uid has no SYSTEM_PAUSE guard to copy" >&2
    status=1
  fi

  for uid in "${outcome_uids[@]}"; do
    # T1
    count="$(jq --arg uid "$uid" '[.groups[]?.rules[]? | select(.uid == $uid)] | length' <<<"$json")"
    if [ "$count" != "1" ]; then
      echo "RULE-T1: want the uid $uid exactly once, found it $count times" >&2
      status=1
      continue # nothing below can be read without the rule
    fi
    if [ "${#uid}" -gt 40 ]; then
      echo "RULE-T1: uid $uid is longer than Grafana's 40-character cap" >&2
      status=1
    fi

    # T2
    if ! jq -e --arg uid "$uid" '.groups[].rules[] | select(.uid == $uid) | .noDataState == "OK"' <<<"$json" >/dev/null; then
      echo "RULE-T2: $uid: noDataState is not OK (an absence test belongs in the expression)" >&2
      status=1
    fi

    expr="$(expr_of "$json" "$uid")"

    # T3
    if [ -n "$guard" ] && [ "${expr%"$guard"}" = "$expr" ]; then
      echo "RULE-T3: $uid: the expression does not end with the SYSTEM_PAUSE guard of $guard_uid: $guard" >&2
      status=1
    fi

    # T4
    if [ "$uid" = "$primary_uid" ]; then
      case "$expr" in
      *"$absence"*) ;;
      *)
        echo "RULE-T4: $uid: the expression lacks the absence branch: $absence" >&2
        status=1
        ;;
      esac
    fi

    # T5
    if ! jq -e --arg uid "$uid" '
        .groups[].rules[] | select(.uid == $uid) |
        (.condition == "C")
        and (([.data[]?.refId] | sort) == ["A", "B", "C"])
        and ((.for // "") != "")
        and ((.annotations.summary // "") != "")
        and ((.annotations.description // "") != "")
        and ((.labels // {} | keys) == ["severity"])
        and (.data[0].datasourceUid == "prometheus")
        and (.data[0].model.instant == true)' <<<"$json" >/dev/null; then
      echo "RULE-T5: $uid: rule shape wrong (condition C, refIds A B C, for, summary, description, labels {severity}, Prometheus instant query)" >&2
      status=1
    fi
  done

  # T6
  expr="$(expr_of "$json" "$residual_uid")"
  case "$expr" in
  *"$residual_gate"*) ;;
  *)
    echo "RULE-T6: $residual_uid: the expression does not read the mode=\"apply\" series: $residual_gate" >&2
    status=1
    ;;
  esac
  for uid in "${stale_uids[@]}"; do
    expr="$(expr_of "$json" "$uid")"
    case "$expr" in
    *"$stale_gate"*) ;;
    *)
      echo "RULE-T6: $uid: the expression lacks the apply-mode gate: $stale_gate" >&2
      status=1
      ;;
    esac
  done

  # T7
  for uid in "${scoped_uids[@]}"; do
    expr="$(expr_of "$json" "$uid")"
    case "$expr" in
    *"$scope"*) ;;
    *)
      echo "RULE-T7: $uid: the expression is not scoped to issue and thread: $scope" >&2
      status=1
      ;;
    esac
  done

  # T8
  expr="$(expr_of "$json" "$ceiling_uid")"
  case "$expr" in
  *"$ceiling_guard"*) ;;
  *)
    echo "RULE-T8: $ceiling_uid: the expression lacks the ceiling guard: $ceiling_guard" >&2
    status=1
    ;;
  esac

  # T9
  count="$(jq --arg uid "$ingest_uid" '[.groups[]?.rules[]? | select(.uid == $uid)] | length' <<<"$json")"
  if [ "$count" != "1" ]; then
    echo "RULE-T9: want the uid $ingest_uid exactly once, found it $count times" >&2
    status=1
  else
    if [ "${#ingest_uid}" -gt 40 ]; then
      echo "RULE-T9: uid $ingest_uid is longer than Grafana's 40-character cap" >&2
      status=1
    fi
    if ! jq -e --arg uid "$ingest_uid" '.groups[].rules[] | select(.uid == $uid) | .noDataState == "Alerting"' <<<"$json" >/dev/null; then
      echo "RULE-T9: $ingest_uid: noDataState is not Alerting (a missing series with pg-desk up is a failure)" >&2
      status=1
    fi
    expr="$(expr_of "$json" "$ingest_uid")"
    case "$expr" in
    *"$ingest_guard") ;;
    *)
      echo "RULE-T9: $ingest_uid: the expression does not end with the pg-desk liveness guard: $ingest_guard" >&2
      status=1
      ;;
    esac
    if ! jq -e --arg uid "$ingest_uid" '
        .groups[].rules[] | select(.uid == $uid) |
        (.condition == "C")
        and (([.data[]?.refId] | sort) == ["A", "B", "C"])
        and ((.for // "") != "")
        and ((.annotations.summary // "") != "")
        and ((.annotations.description // "") != "")
        and ((.labels // {} | keys) == ["severity"])
        and (.data[0].datasourceUid == "prometheus")
        and (.data[0].model.instant == true)' <<<"$json" >/dev/null; then
      echo "RULE-T9: $ingest_uid: rule shape wrong (condition C, refIds A B C, for, summary, description, labels {severity}, Prometheus instant query)" >&2
      status=1
    fi
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

rule_of() { printf '.groups[0].rules[] | select(.uid == "%s")' "$1"; }
# A rule's refId A expression, whatever the order of its data entries.
expr_path() { printf '(%s | .data[] | select(.refId == "A") | .model.expr)' "$(rule_of "$1")"; }

for uid in "${outcome_uids[@]}"; do
  negative T3 "$uid: the SYSTEM_PAUSE guard removed from the expression" \
    "$(expr_path "$uid") |= sub(\" unless on[(][)] [(]pg_router_gates_active.*\$\"; \"\")"
  negative T2 "$uid: noDataState differs (Alerting)" \
    "($(rule_of "$uid") | .noDataState) = \"Alerting\""
done
negative T4 "$primary_uid: the absence branch removed from the expression" \
  "$(expr_path "$primary_uid") |= sub(\" or [(]pg_desk_liveness == 1 unless on[(][)] pg_desk_pr_unhydrated[)]\"; \"\")"
negative T6 "$residual_uid: the mode=\"apply\" selector removed" \
  "$(expr_path "$residual_uid") |= sub(\"[{]mode=.apply.[}]\"; \"\")"
for uid in "${stale_uids[@]}"; do
  negative T6 "$uid: the apply-mode gate removed" \
    "$(expr_path "$uid") |= sub(\" and on[(][)] [(]pg_desk_pr_lane_mode[{]mode=.apply.[}] == 1[)]\"; \"\")"
done
for uid in "${scoped_uids[@]}"; do
  negative T7 "$uid: the issue and thread scope removed" \
    "$(expr_path "$uid") |= sub(\"type=~\\\"issue\\|thread\\\",?\"; \"\")"
done
negative T8 "$ceiling_uid: the ceiling greater than 0 guard removed" \
  "$(expr_path "$ceiling_uid") |= sub(\" / [(]max[(]pg_desk_pr_lane_lookup_ceiling[)] > 0[)]\"; \" / max(pg_desk_pr_lane_lookup_ceiling)\")"
negative T9 "$ingest_uid: noDataState differs (OK)" \
  "($(rule_of "$ingest_uid") | .noDataState) = \"OK\""
negative T9 "$ingest_uid: the pg-desk liveness guard removed from the expression" \
  "$(expr_path "$ingest_uid") |= sub(\" or [(]vector[(]0[)] and on[(][)] absent.*\$\"; \"\")"

[ "$fail" -eq 0 ]
