#!/usr/bin/env bash
# Structure and LogQL behaviour check for the pg-origin-guard Grafana alert rule, plus mutant
# self-tests proving each rule can fail.
#
#   check-alert-rules.sh <alerts.yaml> <rule-tests-dir>
#   check-alert-rules.sh --self-test <alerts.yaml> <rule-tests-dir>
#
# promtool cannot evaluate LogQL, so the behaviour is checked with `logcli query --stdin`
# (see packages/pg-connector/grafana/check-loki-rules.sh, whose sum(count_over_time(..))
# shape this rule extends with `sum by (realm)` and `unless`). The expression
#   sum by (realm)(count_over_time(<sel> | json | available="0" [15m])) > 0
#     unless sum by (realm)(count_over_time(<sel> | json | available="1" [15m])) > 0
# is cut into its two pipelines. For every <name>.jsonl in the rule-tests directory (event rows
# in the tool's real shape, the whole file standing for the 15 minute window) and its
# <name>.expect (the realms that must fire, one per line, empty for none), the realms of the
# file are evaluated one by one: a realm fires when its available="0" count is above 0 and its
# available="1" count is 0. logcli ignores the stream selector, which rule S3 pins instead.
#
# Every failure line starts with RULE-xn so a mutant can assert the exact rule that fired.
#
#   S1  one rule pg-origin-guard-auth-unavailable in folder Work, no folderUid, condition C with
#       refIds A B C, a threshold gt 0, labels exactly {severity: critical}, noDataState OK,
#       for 10m
#   S2  the annotations summary and description are non-empty STATIC text (no templating)
#   S3  the expression has the contract's shape: both sides select {service_name="pg-origin-guard"},
#       parse json, use [15m] windows and sum by (realm); no awake gate or other metric is named
#   B1  the fixtures cover unavailable-only, recovery, flapping and no data, each with an
#       .expect file
#   B2  for every fixture the realms that fire are exactly the .expect realms
#
# Needs yq (mikefarah), jq, sed and logcli (grafana-loki).
# Exit codes: 0 consistent (self-test: real inputs pass and every mutant fails with exactly its
# own rule), 1 a rule failed, 2 usage error or unreadable input or a missing tool.
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
tests="$2"
if [ ! -r "$alerts" ] || [ ! -f "$alerts" ]; then
  echo "check-alert-rules.sh: cannot read $alerts" >&2
  exit 2
fi
if [ ! -d "$tests" ]; then
  echo "check-alert-rules.sh: not a directory: $tests" >&2
  exit 2
fi
for tool in yq jq sed logcli; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "check-alert-rules.sh: $tool is not on PATH" >&2
    exit 2
  fi
done

uid="pg-origin-guard-auth-unavailable"
sel='{service_name="pg-origin-guard"}'

# select_count <pipeline> <file>: lines of the file the pipeline selects (1 when logcli fails).
select_count() {
  local out
  out="$(HOME="${TMPDIR:-/tmp}" logcli query --stdin --quiet "$1" <"$2" 2>/dev/null)" || return 1
  printf '%s' "$out" | grep -c . || true
}

# lint <alerts.yaml> <tests-dir>: prints RULE-xn lines to stderr, returns 1 when any fired.
lint() {
  local file="$1" dir="$2" status=0 json expr rule p0 p1 f name want got realm n0 n1 tmp
  json="$(yq -o=json -I=0 '.' "$file")"
  rule="$(jq -c --arg u "$uid" '[.groups[]?.rules[]? | select(.uid == $u)] | if length == 1 then .[0] else empty end' <<<"$json")"
  if [ -z "$rule" ] ||
    ! jq -e '
      ([.groups[].rules[]] | length) == 1
      and (.groups | length) == 1 and .groups[0].folder == "Work"
      and ([.. | objects | select(has("folderUid"))] | length) == 0
      and (.groups[0].rules[0] | .condition == "C" and ((.data | map(.refId)) == ["A", "B", "C"])
        and .data[2].model.conditions[0].evaluator == {"type": "gt", "params": [0]}
        and .labels == {"severity": "critical"} and .noDataState == "OK" and .for == "10m")
    ' <<<"$json" >/dev/null; then
    echo "RULE-S1: the rule file does not hold exactly $uid with the contract's group, folder, threshold, labels, noDataState and for" >&2
    return 1
  fi
  if ! jq -e '
      (.annotations | (.summary // "") != "" and (.description // "") != ""
       and ([.summary, .description] | all(test("[{][{]|[$]") | not)))' <<<"$rule" >/dev/null; then
    echo "RULE-S2: the annotations must be non-empty static text with no templating" >&2
    status=1
  fi
  expr="$(jq -r '.data[0].model.expr' <<<"$rule")"
  p0="" p1=""
  if [[ $expr =~ ^sum\ by\ \(realm\)\(count_over_time\((\{service_name=\"pg-origin-guard\"\}\ \|\ json\ \|\ available=\"0\")\ \[15m\]\)\)\ \>\ 0\ unless\ sum\ by\ \(realm\)\(count_over_time\((\{service_name=\"pg-origin-guard\"\}\ \|\ json\ \|\ available=\"1\")\ \[15m\]\)\)\ \>\ 0$ ]]; then
    p0="${BASH_REMATCH[1]}"
    p1="${BASH_REMATCH[2]}"
  else
    echo "RULE-S3: the expression does not have the contract's shape: $expr" >&2
    return 1
  fi
  [ "${p0%% |*}" = "$sel" ] || status=1

  # B1: the required scenarios exist, each with its .expect.
  for name in unavailable-heartbeats recovery flapping no-data; do
    if [ ! -f "$dir/$name.jsonl" ] || [ ! -f "$dir/$name.expect" ]; then
      echo "RULE-B1: missing fixture $name.jsonl or $name.expect" >&2
      status=1
    fi
  done
  tmp="$(mktemp -d)"
  for f in "$dir"/*.jsonl; do
    name="$(basename "$f" .jsonl)"
    [ -f "$dir/$name.expect" ] || continue
    want="$(sort "$dir/$name.expect")"
    got=""
    while IFS= read -r realm; do
      [ -n "$realm" ] || continue
      jq -c --arg r "$realm" 'select(.realm == $r)' "$f" >"$tmp/realm.jsonl"
      if ! n0="$(select_count "$p0" "$tmp/realm.jsonl")" || ! n1="$(select_count "$p1" "$tmp/realm.jsonl")"; then
        echo "RULE-B2: $name: logcli cannot run the pipelines" >&2
        status=1
        continue
      fi
      if [ "$n0" -gt 0 ] && [ "$n1" -eq 0 ]; then
        got="$got$realm"$'\n'
      fi
    done < <(jq -r '.realm' "$f" 2>/dev/null | sort -u)
    got="$(printf '%s' "$got" | sort)"
    if [ "$got" != "$want" ]; then
      echo "RULE-B2: $name: realms firing [$(echo "$got" | tr '\n' ' ')] differ from the expected [$(echo "$want" | tr '\n' ' ')]" >&2
      status=1
    fi
  done
  rm -rf "$tmp"
  return "$status"
}

if [ "$selftest" -eq 0 ]; then
  lint "$alerts" "$tests"
  exit $?
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

if ! out="$(lint "$alerts" "$tests" 2>&1)"; then
  echo "self-test: the real inputs fail:" >&2
  echo "$out" >&2
  exit 1
fi

fail=0
# mutant <rule> <description> <yq-expression> [<fixture-name-to-remove>]
mutant() {
  local rule="$1" desc="$2" yexpr="$3" rm_name="${4:-}" dir out
  dir="$(mktemp -d "$work/m.XXXXXX")"
  mkdir "$dir/fx"
  for f in "$tests"/*; do
    cat "$f" >"$dir/fx/$(basename "$f")"
  done
  if [ -n "$rm_name" ]; then
    rm -f "$dir/fx/$rm_name.jsonl" "$dir/fx/$rm_name.expect"
  fi
  if [ -n "$yexpr" ]; then
    yq "$yexpr" "$alerts" >"$dir/alerts.yaml"
    if cmp -s "$alerts" "$dir/alerts.yaml"; then
      echo "self-test: mutant $rule ($desc) did not change the alerts file" >&2
      fail=1
      return
    fi
  else
    cat "$alerts" >"$dir/alerts.yaml"
  fi
  if out="$(lint "$dir/alerts.yaml" "$dir/fx" 2>&1)"; then
    echo "self-test: mutant $rule ($desc) passed" >&2
    fail=1
  elif ! grep -q "^$rule:" <<<"$out" || grep -v "^$rule:" <<<"$out" | grep -q '^RULE-'; then
    echo "self-test: mutant $rule ($desc) failed with other rules:" >&2
    echo "$out" >&2
    fail=1
  fi
}

r='.groups[0].rules[0]'
mutant RULE-S1 "for 5m" "$r.for = \"5m\""
mutant RULE-S1 "noDataState Alerting" "$r.noDataState = \"Alerting\""
mutant RULE-S1 "severity warning" "$r.labels.severity = \"warning\""
mutant RULE-S1 "folder renamed" '.groups[0].folder = "Other"'
mutant RULE-S2 "templated annotation" "$r.annotations.description = \"realm {{ \$labels.realm }} is down\""
mutant RULE-S3 "awake gate appended" "$r.data[0].model.expr += \" unless on() (pg_keep_awake_awake == 0)\""
mutant RULE-S3 "unless side dropped" "$r.data[0].model.expr = \"sum by (realm)(count_over_time({service_name=\\\"pg-origin-guard\\\"} | json | available=\\\"0\\\" [15m])) > 0\""
mutant RULE-B1 "recovery fixture missing" "" recovery
dir="$(mktemp -d "$work/m.XXXXXX")"
mkdir "$dir/fx"
for f in "$tests"/*; do cat "$f" >"$dir/fx/$(basename "$f")"; done
echo zr >"$dir/fx/recovery.expect"
if out="$(lint "$alerts" "$dir/fx" 2>&1)"; then
  echo "self-test: mutant RULE-B2 (wrong expectation) passed" >&2
  fail=1
elif ! grep -q '^RULE-B2:' <<<"$out"; then
  echo "self-test: mutant RULE-B2 (wrong expectation) failed with other rules:" >&2
  echo "$out" >&2
  fail=1
fi
exit "$fail"
