#!/usr/bin/env bash
# LogQL behaviour check for the Loki rules of a pg-connector Grafana alert file (docket
# pg2-2qyyh.2), plus mutant self-tests proving each rule can fail.
#
#   check-loki-rules.sh <alerts.yaml> <loki-fixtures-dir>
#   check-loki-rules.sh --self-test <alerts.yaml> <loki-fixtures-dir>
#
# promtool cannot evaluate LogQL, so this is the Loki counterpart of check-alert-rules.sh. For
# every rule of the file whose refId A reads the loki datasource, the expression
# `sum(count_over_time(<selector> <pipeline> [<window>]))` is cut into its log pipeline and run
# with `logcli query --stdin` over two fixture logs written in the daemon's real row shape:
# <fixtures-dir>/<uid>.positive.jsonl and <uid>.negative.jsonl. logcli in stdin mode applies the
# line filters and `| json` stages of the pipeline to each line and ignores the stream selector,
# so the selector itself is pinned by the structure check (rule S7), not here.
#
# Every failure line starts with RULE-Ln so a mutant can assert the exact rule that fired.
#
#   L1  the rule has a positive and a negative fixture file, each with at least one line
#   L2  the expression has the shape sum(count_over_time(<pipeline> [<window>])) and the
#       pipeline parses and runs under logcli
#   L3  the positive file selects MORE lines than the rule's threshold (so the rule fires)
#   L4  the negative file selects at most the threshold's number of lines (so it stays quiet)
#
# Needs yq (mikefarah), jq, sed and logcli (grafana-loki).
#
# Exit codes: 0 consistent (self-test: real inputs pass and every mutant fails with exactly its
# own rule), 1 a rule failed, 2 usage error or unreadable input or a missing tool.
set -euo pipefail

usage() {
  echo "usage: check-loki-rules.sh [--self-test] <alerts.yaml> <loki-fixtures-dir>" >&2
  exit 2
}

selftest=0
if [ "${1:-}" = "--self-test" ]; then
  selftest=1
  shift
fi
[ "$#" -eq 2 ] || usage
alerts="$1"
fixtures="$2"
if [ ! -r "$alerts" ] || [ ! -f "$alerts" ]; then
  echo "check-loki-rules.sh: cannot read $alerts" >&2
  exit 2
fi
if [ ! -d "$fixtures" ]; then
  echo "check-loki-rules.sh: not a directory: $fixtures" >&2
  exit 2
fi
for tool in yq jq sed logcli; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "check-loki-rules.sh: $tool is not on PATH" >&2
    exit 2
  fi
done

# select_count <pipeline> <file>: how many lines of the file the pipeline selects; returns 1 when
# logcli cannot parse or run the pipeline.
select_count() {
  local out
  out="$(HOME="${TMPDIR:-/tmp}" logcli query --stdin --quiet "$1" <"$2" 2>/dev/null)" || return 1
  printf '%s' "$out" | grep -c . || true
}

# lint <alerts.yaml> <fixtures-dir>: prints RULE-Ln lines to stderr, returns 1 when any fired.
lint() {
  local file="$1" dir="$2" status=0 json uid expr pipeline thr pos neg
  json="$(yq -o=json -I=0 '.' "$file")"
  local uids
  uids="$(jq -r '.groups[]?.rules[]? | select(.data[0].datasourceUid == "loki") | .uid' <<<"$json")"
  if [ -z "$uids" ]; then
    echo "RULE-L1: no Loki rule found in $file" >&2
    return 1
  fi
  while IFS= read -r uid; do
    expr="$(jq -r --arg u "$uid" '.groups[].rules[] | select(.uid == $u) | .data[0].model.expr' <<<"$json")"
    thr="$(jq -r --arg u "$uid" '.groups[].rules[] | select(.uid == $u) | .data[] | select(.refId == "C") | .model.conditions[0].evaluator.params[0]' <<<"$json")"
    if [ ! -s "$dir/$uid.positive.jsonl" ] || [ ! -s "$dir/$uid.negative.jsonl" ]; then
      echo "RULE-L1: $uid: missing or empty $uid.positive.jsonl or $uid.negative.jsonl" >&2
      status=1
      continue
    fi
    pipeline="$(printf '%s' "$expr" | sed -nE 's/^sum\(count_over_time\((.*) \[[0-9]+[smh]\]\)\)$/\1/p')"
    if [ -z "$pipeline" ]; then
      echo "RULE-L2: $uid: expression is not sum(count_over_time(<pipeline> [<window>])): $expr" >&2
      status=1
      continue
    fi
    if ! pos="$(select_count "$pipeline" "$dir/$uid.positive.jsonl")" ||
      ! neg="$(select_count "$pipeline" "$dir/$uid.negative.jsonl")"; then
      echo "RULE-L2: $uid: logcli cannot run the pipeline: $pipeline" >&2
      status=1
      continue
    fi
    if [ "$pos" -le "$thr" ]; then
      echo "RULE-L3: $uid: the positive fixture selects $pos line(s), not more than the threshold $thr" >&2
      status=1
    fi
    if [ "$neg" -gt "$thr" ]; then
      echo "RULE-L4: $uid: the negative fixture selects $neg line(s), more than the threshold $thr" >&2
      status=1
    fi
  done <<<"$uids"
  return "$status"
}

if [ "$selftest" -eq 0 ]; then
  lint "$alerts" "$fixtures"
  exit $?
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The real inputs must pass, silently.
if ! out="$(lint "$alerts" "$fixtures" 2>&1)"; then
  echo "self-test: the real inputs fail:" >&2
  echo "$out" >&2
  exit 1
fi

fail=0
# mutant <rule> <description> <yq-expression-for-alerts> [<fixture-to-remove>]
# The mutant must fail with exactly <rule>. A fixture to remove is a file name inside the fixtures
# directory; the mutant then runs against a copy of the directory without it.
mutant() {
  local rule="$1" desc="$2" yexpr="$3" rm_file="${4:-}" dir ids
  dir="$(mktemp -d "$work/m.XXXXXX")"
  mkdir "$dir/fx"
  for f in "$fixtures"/*; do
    cat "$f" >"$dir/fx/$(basename "$f")" # not cp: a store input is read-only and cp keeps the mode
  done
  cat "$alerts" >"$dir/alerts.yaml"
  if [ -n "$yexpr" ]; then
    yq "$yexpr" "$alerts" >"$dir/alerts.yaml"
    if cmp -s "$alerts" "$dir/alerts.yaml"; then
      echo "self-test: mutant $rule ($desc) did not change the alerts file" >&2
      fail=1
      return
    fi
  fi
  [ -z "$rm_file" ] || rm -f "$dir/fx/$rm_file"
  if ids="$(lint "$dir/alerts.yaml" "$dir/fx" 2>&1 >/dev/null)"; then
    echo "self-test: mutant $rule ($desc) passed" >&2
    fail=1
    return
  fi
  ids="$(printf '%s\n' "$ids" | grep -o '^RULE-L[0-9]*' | sort -u | tr '\n' ' ')"
  if [ "$ids" != "RULE-$rule " ]; then
    echo "self-test: mutant $rule ($desc) fired '${ids}' instead of only RULE-$rule" >&2
    fail=1
  fi
}

rule_of() { printf '.groups[0].rules[] | select(.uid == "%s")' "$1"; }

first_uid="$(yq '.groups[0].rules[] | select(.data[0].datasourceUid == "loki") | .uid' "$alerts" | head -n 1)"
mutant L1 "a rule without its negative fixture" "" "$first_uid.negative.jsonl"
mutant L2 "an expression that is not a count_over_time sum" \
  "($(rule_of "$first_uid") | .data[0].model.expr) |= sub(\"sum\\(count_over_time\"; \"count_over_time\")"
mutant L2 "a pipeline logcli cannot parse" \
  "($(rule_of "$first_uid") | .data[0].model.expr) |= sub(\"\\| json\"; \"| jsonx\")"
mutant L3 "a filter value no positive line carries" \
  "($(rule_of pg-conn-jira-unavailable) | .data[0].model.expr) |= sub(\"error_code=\\\"unavailable\\\"\"; \"error_code=\\\"nothing\\\"\")"
mutant L3 "a threshold the positive fixture does not exceed" \
  "($(rule_of pg-conn-jira-rate-limited) | .data[] | select(.refId == \"C\") | .model.conditions[0].evaluator.params[0]) = 3"
mutant L4 "a filter widened so the negative fixture is selected" \
  "($(rule_of pg-conn-jira-unavailable) | .data[0].model.expr) |= sub(\" \\| failure_class!=\\\"rate_limited\\\"\"; \"\")"
mutant L4 "a threshold lowered below what the negative fixture selects" \
  "($(rule_of pg-conn-jira-rate-limited) | .data[] | select(.refId == \"C\") | .model.conditions[0].evaluator.params[0]) = 1"

[ "$fail" -eq 0 ]
