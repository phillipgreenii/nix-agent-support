#!/usr/bin/env bash
# Structure check for the pg-claude-code-recorder alert rules file and its deleteRules twin, plus
# negative-control mutants proving each rule can fail (docket pg2-v77tsx, phase 7c).
#
#   check-alert-structure.sh <alerts.yaml> <metrics-catalog.txt> <alerts-delete.yaml>
#   check-alert-structure.sh --self-test <alerts.yaml> <metrics-catalog.txt> <alerts-delete.yaml>
#
# A sibling of packages/pg-connector/grafana/check-alert-structure.sh (which hard-codes the GitHub
# daemon's uids, group, metric prefix and selector, so it cannot be reused) and of pg-desk's.
# Needs yq (mikefarah), jq, grep. Every failure line starts with RULE-Sn so a mutant can assert the
# exact rule that fired.
#
#   S1  exactly the nineteen expected uids, each once, each of the form pg-ccrec-<name> and at most
#       40 characters (Grafana's cap)
#   S2  one group pg-claude-code-recorder in folder Claude Agents, no folderUid anywhere
#   S3  every rule has condition C, refIds A B C, annotations.summary and description (the
#       description names the first command to run), labels with exactly the one key severity
#       (warning or critical), and a non-empty for
#   S4  every rule uses datasourceUid prometheus with instant: true
#   S5  noDataState is Alerting for daemon-down, heartbeat-stale and doctor-mismatch,
#       OK for every other rule
#   S6  every pg_claude_code_recorder_* token of an expression is a catalogued name (a histogram's
#       _bucket/_sum/_count stripped first)
#   S7  the guards: daemon-down adds the SYSTEM_PAUSE gate to an `or vector(0)` value (never empty, so a
#       pause cannot read as NoData-as-Alerting; no `unless`) and its description states the
#       30s scrape interval; heartbeat-stale and doctor-mismatch carry `and on() (max(up{...}) == 1)`
#       and `or vector(0)`; control-inbox-gone selects only state="gone"; crash-share carries
#       a volume floor and the stale-at-discovery counter
#   S8  the deleteRules file is apiVersion 1, has no groups, and its deleteRules entries are exactly
#       the uids of S1 plus the retired uids (RETIRED_UIDS: rules removed from the rules file whose
#       already-provisioned copies Grafana keeps live until a deleteRules entry names them), each once,
#       each with orgId 1
#   S9  the retired-only file (alerts-retired-delete.yaml, registered NEXT TO the rules file while the
#       daemon is deployed) is apiVersion 1, has no groups, and its deleteRules entries are exactly
#       RETIRED_UIDS, each once, each with orgId 1, so a deployed daemon still deletes the retired rules
#       and no uid is both a live rule and deleted
#
# Exit codes: 0 clean (self-test: the real inputs pass and every mutant fails with exactly its own
# rule), 2 usage error or unreadable input or a missing tool, 3 a rule failed (1 is reserved for
# unexpected errors).
set -euo pipefail

usage() {
  echo "usage: check-alert-structure.sh [--self-test] <alerts.yaml> <metrics-catalog.txt> <alerts-delete.yaml> <alerts-retired-delete.yaml>" >&2
  exit 2
}

selftest=0
if [ "${1:-}" = "--self-test" ]; then
  selftest=1
  shift
fi
[ "$#" -eq 4 ] || usage
alerts="$1"
catalog="$2"
deletes="$3"
retired="$4"
for f in "$alerts" "$catalog" "$deletes" "$retired"; do
  [ -r "$f" ] || {
    echo "check-alert-structure.sh: cannot read $f" >&2
    exit 2
  }
done
for tool in yq jq grep; do
  command -v "$tool" >/dev/null || {
    echo "check-alert-structure.sh: missing tool $tool" >&2
    exit 2
  }
done

EXPECTED_UIDS=(
  pg-ccrec-daemon-down pg-ccrec-heartbeat-stale pg-ccrec-copy-lag pg-ccrec-parse-stuck
  pg-ccrec-held-old pg-ccrec-unknown-key-burst pg-ccrec-data-loss
  pg-ccrec-removal-due pg-ccrec-doctor-mismatch pg-ccrec-control-inbox-gone
  pg-ccrec-crash-share pg-ccrec-removal-stuck pg-ccrec-settle-tick-slow
  pg-ccrec-field-type-mismatch pg-ccrec-cpu-high pg-ccrec-wal-large pg-ccrec-loop-stalled
  pg-ccrec-request-slow pg-ccrec-schema-rebuild-required
)
# Rules removed from alerts.yaml that alerts-delete.yaml MUST still delete (Grafana keeps a
# file-provisioned rule live after its file entry is dropped): free-space (removed 2026-11-04 on
# the operator ruling "there shouldn't be a pg-claud-code-recorder specific file system alert") and
# removal-backlog (replaced by removal-due 2026-11-05, removal being operator-run).
RETIRED_UIDS=(pg-ccrec-free-space pg-ccrec-removal-backlog)
NODATA_ALERTING=(pg-ccrec-daemon-down pg-ccrec-heartbeat-stale pg-ccrec-doctor-mismatch)

# check <alerts> <deletes> <retired>: print RULE-Sn lines for each failing rule (at most one line per rule).
check() {
  local a="$1" d="$2" r="$3" j dj rj fails=""
  j="$(yq -o=json -I=0 '.' "$a")"
  dj="$(yq -o=json -I=0 '.' "$d")"
  rj="$(yq -o=json -I=0 '.' "$r")"
  local expected
  expected="$(printf '%s\n' "${EXPECTED_UIDS[@]}" | jq -R . | jq -sc 'sort')"
  local nodata
  nodata="$(printf '%s\n' "${NODATA_ALERTING[@]}" | jq -R . | jq -sc 'sort')"

  rule_fail() { fails="${fails}$1"$'\n'; }

  # S1
  if ! printf '%s' "$j" | jq -e --argjson exp "$expected" '
      ([.groups[].rules[].uid] | sort) as $u
      | ($u == $exp)
        and ($u | all(. | test("^pg-ccrec-[a-z0-9-]+$") and (length <= 40)))' >/dev/null; then
    rule_fail "RULE-S1: the rule uids are not exactly the nineteen expected pg-ccrec-<name> uids, each once, each at most 40 characters"
  fi
  # S2
  if ! printf '%s' "$j" | jq -e '
      (.groups | length == 1)
      and (.groups[0].name == "pg-claude-code-recorder")
      and (.groups[0].folder == "Claude Agents")
      and ([.. | objects | select(has("folderUid"))] | length == 0)' >/dev/null; then
    rule_fail "RULE-S2: expected one group and folder Claude Agents and no folderUid"
  fi
  # S3
  if ! printf '%s' "$j" | jq -e '
      .groups[].rules | all(
        .condition == "C"
        and ((.data | map(.refId)) == ["A", "B", "C"])
        and ((.annotations.summary // "") != "")
        and ((.annotations.description // "") | test("pg-claude-code-recorder |pg-claude-control "))
        and ((.labels | keys) == ["severity"])
        and (.labels.severity == "warning" or .labels.severity == "critical")
        and ((.for // "") != ""))' >/dev/null; then
    rule_fail "RULE-S3: a rule lacks condition C, refIds A B C, a summary, a description naming its first command, exactly the one label severity, or a non-empty for"
  fi
  # S4
  if ! printf '%s' "$j" | jq -e '
      .groups[].rules | all(
        . as $r | .data[] | select(.refId == "A") |
          if $r.uid == "pg-ccrec-schema-rebuild-required"
          then (.datasourceUid == "loki" and .model.queryType == "instant")
          else (.datasourceUid == "prometheus" and .model.instant == true) end)' >/dev/null; then
    rule_fail "RULE-S4: refId A must use datasourceUid prometheus with instant: true (only schema-rebuild-required reads loki, with queryType instant)"
  fi
  # S5
  if ! printf '%s' "$j" | jq -e --argjson na "$nodata" '
      .groups[].rules | all(
        if (.uid as $u | $na | index($u)) then .noDataState == "Alerting" else .noDataState == "OK" end)' >/dev/null; then
    rule_fail "RULE-S5: noDataState must be Alerting for daemon-down, heartbeat-stale and doctor-mismatch, and OK for every other rule"
  fi
  # S6
  local bad="" tok base
  while IFS= read -r tok; do
    [ -n "$tok" ] || continue
    base="$tok"
    if ! grep -q "^$base " "$catalog"; then
      base="${tok%_bucket}"
      base="${base%_sum}"
      base="${base%_count}"
      if ! grep -q "^$base histogram " "$catalog"; then
        bad="$bad $tok"
      fi
    fi
  done < <(printf '%s' "$j" | jq -r '.groups[].rules[].data[] | select(.refId == "A") | .model.expr' |
    grep -o 'pg_claude_code_recorder_[a-z0-9_]*' | sort -u)
  if [ -n "$bad" ]; then
    rule_fail "RULE-S6: uncatalogued metric names:$bad"
  fi
  # S7
  local up='and on() (max(up{job="pg-claude-code-recorder"}) == 1)'
  if ! printf '%s' "$j" | jq -e --arg up "$up" '
      def rule($n): .groups[].rules[] | select(.uid == "pg-ccrec-" + $n);
      def expr($n): [rule($n) | .data[] | select(.refId == "A") | .model.expr] | first;
      (expr("daemon-down") | endswith("+ on() (max(pg_router_gates_active{type=\"SYSTEM_PAUSE\"} == 1) or vector(0))") and contains("or vector(0)) + on()") and (contains("unless") | not))
      and ([rule("daemon-down") | .annotations.description | test("scrape interval is 30s")] | first)
      and (expr("heartbeat-stale") | contains($up) and contains("or vector(0)"))
      and (expr("doctor-mismatch") | contains($up) and contains("or vector(0)"))
      and (expr("control-inbox-gone") | contains("state=\"gone\"") and (contains("never_loaded") | not))
      and (expr("crash-share") | contains("> 20") and contains("registry_stale_at_discovery_total"))
      and (expr("loop-stalled") | contains("{loop=\"watcher_sweep\"} > 0)) > 2700 or") and contains("loop=~\"tracker_tick|tracker_poll\"} > 0)) > 120 or") and contains("{loop=\"status_refresh\"} > 0)) > 60 or") and contains("loop_in_flight_seconds > 600)") and (contains("parser_drain") | not))
      and (expr("request-slow") | contains("> 50)") and contains("{le=\"10\"}"))
      and (expr("schema-rebuild-required") | contains("service_name=\"pg-claude-code-recorder\"") and contains("event=\"schema_rebuild_required\""))' >/dev/null; then
    rule_fail "RULE-S7: a guard is missing or wrong (daemon-down SYSTEM_PAUSE and scrape interval; heartbeat-stale and doctor-mismatch up guard; control-inbox-gone state gone; crash-share floor; loop-stalled literal bounds and no parser_drain; request-slow floor and le=10 bucket; schema-rebuild-required service_name and event)"
  fi
  # S8
  local ruids
  ruids="$(printf '%s\n' "${RETIRED_UIDS[@]}" | jq -R . | jq -sc --argjson r "$(printf '%s' "$j" | jq -c '[.groups[].rules[].uid]')" '$r + . | sort')"
  if ! printf '%s' "$dj" | jq -e --argjson exp "$ruids" '
      (.apiVersion == 1)
      and (has("groups") | not)
      and ((.deleteRules | map(.uid) | sort) == $exp)
      and (.deleteRules | all(.orgId == 1))' >/dev/null; then
    rule_fail "RULE-S8: the deleteRules twin must be apiVersion 1, have no groups, and list exactly the rule uids plus the retired uids, each once with orgId 1"
  fi
  # S9
  local retiredJson
  retiredJson="$(printf '%s\n' "${RETIRED_UIDS[@]}" | jq -R . | jq -sc 'sort')"
  if ! printf '%s' "$rj" | jq -e --argjson exp "$retiredJson" '
      (.apiVersion == 1)
      and (has("groups") | not)
      and ((.deleteRules | map(.uid) | sort) == $exp)
      and (.deleteRules | all(.orgId == 1))' >/dev/null; then
    rule_fail "RULE-S9: the retired-only delete file must be apiVersion 1, have no groups, and list exactly the retired uids, each once with orgId 1"
  fi
  printf '%s' "$fails"
}

if [ "$selftest" -eq 0 ]; then
  out="$(check "$alerts" "$deletes" "$retired")"
  if [ -n "$out" ]; then
    printf '%s' "$out" >&2
    exit 3
  fi
  echo "OK: alert structure (S1-S8) holds for $alerts, $deletes and $retired"
  exit 0
fi

# --self-test: the real inputs must pass, and each mutant must fail with exactly its own rule.
out="$(check "$alerts" "$deletes" "$retired")"
if [ -n "$out" ]; then
  printf '%s' "$out" >&2
  echo "FAIL: the real inputs do not pass" >&2
  exit 3
fi
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
n=0
# mutant <expected-rule> <description> <yq expression over alerts> [<yq over deletes> [<yq over retired>]]
mutant() {
  local want="$1" desc="$2" ya="$3" yd="${4:-.}" yr="${5:-.}" a d r got
  n=$((n + 1))
  a="$work/alerts.$n.yaml"
  d="$work/deletes.$n.yaml"
  yq "$ya" "$alerts" >"$a"
  yq "$yd" "$deletes" >"$d"
  r="$work/retired.$n.yaml"
  yq "$yr" "$retired" >"$r"
  got="$(check "$a" "$d" "$r" | grep -o '^RULE-S[0-9]*' | sort -u | tr '\n' ' ')"
  if [ "$got" != "$want " ]; then
    echo "FAIL: mutant '$desc' expected exactly $want, got: ${got:-nothing}" >&2
    exit 3
  fi
  echo "ok: mutant '$desc' fails with $want"
}
long='pg-ccrec-this-uid-is-forty-one-characters-xx'
mutant RULE-S1 "a 41-character uid" \
  "(.groups[0].rules[] | select(.uid == \"pg-ccrec-copy-lag\") | .uid) = \"$long\"" \
  "(.deleteRules[] | select(.uid == \"pg-ccrec-copy-lag\") | .uid) = \"$long\""
mutant RULE-S1 "a rule removed" 'del(.groups[0].rules[] | select(.uid == "pg-ccrec-copy-lag"))' \
  'del(.deleteRules[] | select(.uid == "pg-ccrec-copy-lag"))'
mutant RULE-S2 "a folderUid" '.groups[0].folderUid = "x"'
mutant RULE-S3 "a missing for" 'del(.groups[0].rules[0].for)'
mutant RULE-S3 "an extra label" '.groups[0].rules[0].labels.team = "x"'
mutant RULE-S4 "a non-instant query" '.groups[0].rules[0].data[0].model.instant = false'
mutant RULE-S5 "a missing noDataState" 'del(.groups[0].rules[] | select(.uid == "pg-ccrec-daemon-down") | .noDataState)'
mutant RULE-S5 "a wrong noDataState" '(.groups[0].rules[] | select(.uid == "pg-ccrec-copy-lag") | .noDataState) = "Alerting"'
mutant RULE-S6 "an uncatalogued metric" \
  '(.groups[0].rules[] | select(.uid == "pg-ccrec-copy-lag") | .data[0].model.expr) = "max(pg_claude_code_recorder_not_a_metric)"'
mutant RULE-S7 "daemon-down without the SYSTEM_PAUSE guard" \
  '(.groups[0].rules[] | select(.uid == "pg-ccrec-daemon-down") | .data[0].model.expr) = "min(up{job=\"pg-claude-code-recorder\"})"'
mutant RULE-S7 "heartbeat-stale without the up guard" \
  '(.groups[0].rules[] | select(.uid == "pg-ccrec-heartbeat-stale") | .data[0].model.expr) = "(time() - max(pg_claude_code_recorder_last_sweep_timestamp_seconds))"'
mutant RULE-S7 "daemon-down back on the empty-under-pause unless form" \
  '(.groups[0].rules[] | select(.uid == "pg-ccrec-daemon-down") | .data[0].model.expr) = "min(up{job=\"pg-claude-code-recorder\"}) unless on() (pg_router_gates_active{type=\"SYSTEM_PAUSE\"} == 1)"'
mutant RULE-S7 "loop-stalled with a changed watcher_sweep bound" \
  '(.groups[0].rules[] | select(.uid == "pg-ccrec-loop-stalled") | .data[0].model.expr) |= sub("> 2700"; "> 270")'
mutant RULE-S7 "loop-stalled covering parser_drain" \
  '(.groups[0].rules[] | select(.uid == "pg-ccrec-loop-stalled") | .data[0].model.expr) |= sub("status_refresh"; "parser_drain")'
mutant RULE-S7 "request-slow without the request floor" \
  '(.groups[0].rules[] | select(.uid == "pg-ccrec-request-slow") | .data[0].model.expr) |= sub("> 50[)]"; "> 0)")'
mutant RULE-S4 "schema-rebuild-required back on a prometheus datasource" \
  '(.groups[0].rules[] | select(.uid == "pg-ccrec-schema-rebuild-required") | .data[0].datasourceUid) = "prometheus"'
mutant RULE-S7 "schema-rebuild-required matching the wrong event" \
  '(.groups[0].rules[] | select(.uid == "pg-ccrec-schema-rebuild-required") | .data[0].model.expr) |= sub("schema_rebuild_required"; "open_failed")'
mutant RULE-S8 "a twin uid that differs" \
  '.' '(.deleteRules[0].uid) = "pg-ccrec-other"'
mutant RULE-S8 "a twin with a group" '.' '.groups = []'
mutant RULE-S8 "a twin that no longer deletes the retired free-space uid" \
  '.' 'del(.deleteRules[] | select(.uid == "pg-ccrec-free-space"))'
mutant RULE-S9 "a retired file that drops the free-space uid" '.' '.' \
  'del(.deleteRules[] | select(.uid == "pg-ccrec-free-space"))'
mutant RULE-S9 "a retired file that also deletes a live rule" '.' '.' \
  '.deleteRules += [{"orgId": 1, "uid": "pg-ccrec-copy-lag"}]'
mutant RULE-S9 "a retired file with a group" '.' '.' '.groups = []'
echo "OK: $n mutants each fail with exactly their own rule"
