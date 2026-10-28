#!/usr/bin/env bash
# LogQL behaviour check for ONE ratio rule of a pg-connector Grafana alert file (bead pg2-4pfrkm),
# plus mutant self-tests proving each check can fail.
#
#   check-loki-ratio-rule.sh <alerts.yaml> <loki-fixtures-dir> <rule-uid>
#   check-loki-ratio-rule.sh --self-test <alerts.yaml> <loki-fixtures-dir> <rule-uid>
#
# check-loki-rules.sh covers the Loki rules that count lines. A ratio rule has no such shape, and
# neither promtool nor logcli can evaluate a LogQL metric query offline, so this script cuts the
# rule's refId A expression, which MUST have exactly the form
#
#   (sum(count_over_time(<NUM> [W])) / sum(count_over_time(<DEN> [W]))) and on() (sum(count_over_time(<DEN> [W])) >= <MIN>)
#
# into its two log pipelines <NUM> and <DEN>, counts the lines each selects with
# `logcli query --stdin` over three fixture logs written in the daemon's real row shape, and applies
# the same arithmetic the rule does: fire when DEN >= MIN and NUM / DEN > the threshold of refId C.
# logcli in stdin mode applies the line filters and `| json` stages and ignores the stream selector,
# so the selector is pinned by the structure check (check-alert-structure.sh), not here.
#
#   <uid>.positive.jsonl         the rule MUST fire (enough requests, ratio above the threshold)
#   <uid>.negative-ratio.jsonl   the rule MUST stay quiet: enough requests, ratio at or below it
#   <uid>.negative-volume.jsonl  the rule MUST stay quiet ONLY because of the volume guard: the
#                                ratio is above the threshold but DEN is under MIN
#
# Every failure line starts with RULE-Rn so a mutant can assert the rule that fired.
#
#   R1  the three fixture files exist and are not empty
#   R2  the expression has the form above, both pipelines run under logcli, and the two DEN
#       pipelines (the ratio's and the guard's) are the same
#   R3  the positive fixture fires
#   R4  the negative-ratio fixture does not fire, and has enough requests to isolate the ratio
#   R5  the negative-volume fixture does not fire because of the guard (ratio above the threshold,
#       DEN under MIN, at least one NUM line)
#
# Needs yq (mikefarah), jq, sed, awk and logcli (grafana-loki).
#
# Exit codes: 0 consistent (self-test: real inputs pass and every mutant fails with its rule), 1 a
# rule failed, 2 usage error or unreadable input or a missing tool.
set -euo pipefail

usage() {
  echo "usage: check-loki-ratio-rule.sh [--self-test] <alerts.yaml> <loki-fixtures-dir> <rule-uid>" >&2
  exit 2
}

selftest=0
if [ "${1:-}" = "--self-test" ]; then
  selftest=1
  shift
fi
[ "$#" -eq 3 ] || usage
alerts="$1"
fixtures="$2"
uid="$3"
if [ ! -r "$alerts" ] || [ ! -f "$alerts" ]; then
  echo "check-loki-ratio-rule.sh: cannot read $alerts" >&2
  exit 2
fi
if [ ! -d "$fixtures" ]; then
  echo "check-loki-ratio-rule.sh: not a directory: $fixtures" >&2
  exit 2
fi
for tool in yq jq sed awk logcli; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "check-loki-ratio-rule.sh: $tool is not on PATH" >&2
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

# fires <num> <den> <min> <threshold>: exit 0 when the rule's arithmetic fires.
fires() {
  awk -v n="$1" -v d="$2" -v m="$3" -v t="$4" 'BEGIN { exit !(d >= m && d > 0 && n / d > t) }'
}

# lint <alerts.yaml> <fixtures-dir> <uid>: prints RULE-Rn lines to stderr, returns 1 when any fired.
lint() {
  local file="$1" dir="$2" id="$3" status=0 json expr thr num den den2 min
  json="$(yq -o=json -I=0 '.' "$file")"
  expr="$(jq -r --arg u "$id" '.groups[]?.rules[]? | select(.uid == $u) | .data[]? | select(.refId == "A") | .model.expr // empty' <<<"$json")"
  thr="$(jq -r --arg u "$id" '.groups[]?.rules[]? | select(.uid == $u) | .data[]? | select(.refId == "C") | .model.conditions[0].evaluator.params[0] // empty' <<<"$json")"
  local f
  for f in positive negative-ratio negative-volume; do
    if [ ! -s "$dir/$id.$f.jsonl" ]; then
      echo "RULE-R1: $id: missing or empty $id.$f.jsonl" >&2
      status=1
    fi
  done
  [ "$status" -eq 0 ] || return 1

  local re='^\(sum\(count_over_time\((.*) \[[0-9]+[smh]\]\)\) / sum\(count_over_time\((.*) \[[0-9]+[smh]\]\)\)\) and on\(\) \(sum\(count_over_time\((.*) \[[0-9]+[smh]\]\)\) >= ([0-9]+)\)$'
  if [ -z "$expr" ] || [ -z "$thr" ] || ! [[ $expr =~ $re ]]; then
    echo "RULE-R2: $id: the expression is not (sum(count_over_time(NUM [W])) / sum(count_over_time(DEN [W]))) and on() (sum(count_over_time(DEN [W])) >= MIN): $expr" >&2
    return 1
  fi
  num="${BASH_REMATCH[1]}"
  den="${BASH_REMATCH[2]}"
  den2="${BASH_REMATCH[3]}"
  min="${BASH_REMATCH[4]}"
  if [ "$den" != "$den2" ]; then
    echo "RULE-R2: $id: the ratio's denominator pipeline differs from the volume guard's: '$den' vs '$den2'" >&2
    status=1
  fi

  local pn pd nn nd vn vd
  if ! pn="$(select_count "$num" "$dir/$id.positive.jsonl")" ||
    ! pd="$(select_count "$den" "$dir/$id.positive.jsonl")" ||
    ! nn="$(select_count "$num" "$dir/$id.negative-ratio.jsonl")" ||
    ! nd="$(select_count "$den" "$dir/$id.negative-ratio.jsonl")" ||
    ! vn="$(select_count "$num" "$dir/$id.negative-volume.jsonl")" ||
    ! vd="$(select_count "$den" "$dir/$id.negative-volume.jsonl")"; then
    echo "RULE-R2: $id: logcli cannot run a pipeline: NUM '$num' or DEN '$den'" >&2
    return 1
  fi

  # R3
  if ! fires "$pn" "$pd" "$min" "$thr"; then
    echo "RULE-R3: $id: the positive fixture does not fire (unavailable $pn of $pd requests, floor $min, threshold $thr)" >&2
    status=1
  fi
  # R4
  if fires "$nn" "$nd" "$min" "$thr"; then
    echo "RULE-R4: $id: the negative-ratio fixture fires ($nn of $nd requests, threshold $thr)" >&2
    status=1
  fi
  if [ "$nd" -lt "$min" ]; then
    echo "RULE-R4: $id: the negative-ratio fixture has $nd requests, under the floor $min, so it would stay quiet for the wrong reason" >&2
    status=1
  fi
  # R5
  if fires "$vn" "$vd" "$min" "$thr"; then
    echo "RULE-R5: $id: the negative-volume fixture fires ($vn of $vd requests, floor $min)" >&2
    status=1
  fi
  if [ "$vn" -eq 0 ] || [ "$vd" -ge "$min" ] || ! awk -v n="$vn" -v d="$vd" -v t="$thr" 'BEGIN { exit !(d > 0 && n / d > t) }'; then
    echo "RULE-R5: $id: the negative-volume fixture does not isolate the guard (unavailable $vn of $vd requests, floor $min, threshold $thr)" >&2
    status=1
  fi
  return "$status"
}

if [ "$selftest" -eq 0 ]; then
  lint "$alerts" "$fixtures" "$uid"
  exit $?
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The real inputs must pass, silently.
if ! out="$(lint "$alerts" "$fixtures" "$uid" 2>&1)"; then
  echo "self-test: the real inputs fail:" >&2
  echo "$out" >&2
  exit 1
fi

fail=0
# mutant <rule> <description> <sed-script>: the sed script edits a copy of the alerts file; the
# mutant must change it and fail with a line of <rule>.
mutant() {
  local rule="$1" desc="$2" script="$3" dir ids
  dir="$(mktemp -d "$work/m.XXXXXX")"
  sed -E "$script" "$alerts" >"$dir/alerts.yaml"
  if cmp -s "$alerts" "$dir/alerts.yaml"; then
    echo "self-test: mutant $rule ($desc) did not change the alerts file" >&2
    fail=1
    return
  fi
  if ids="$(lint "$dir/alerts.yaml" "$fixtures" "$uid" 2>&1 >/dev/null)"; then
    echo "self-test: mutant $rule ($desc) passed" >&2
    fail=1
    return
  fi
  if ! grep -q "^RULE-$rule" <<<"$ids"; then
    echo "self-test: mutant $rule ($desc) fired '$(printf '%s\n' "$ids" | grep -o '^RULE-R[0-9]*' | sort -u | tr '\n' ' ')' without RULE-$rule" >&2
    fail=1
  fi
}

# Scope every edit to this rule's own block: from its uid line to the next rule or the file end.
scope="/uid: $uid\$/,\$"
mutant R3 "a threshold above the positive fixture's ratio" \
  "$scope s/params: \\[[0-9.]+\\]/params: [0.5]/"
mutant R4 "a threshold under the negative-ratio fixture's ratio" \
  "$scope s/params: \\[[0-9.]+\\]/params: [0.001]/"
mutant R5 "a volume floor of 1 (the guard can no longer silence a quiet hour)" \
  "$scope s/>= [0-9]+\\)'\$/>= 1)'/"
mutant R3 "a volume floor above the positive fixture's request count" \
  "$scope s/>= [0-9]+\\)'\$/>= 100000)'/"
mutant R4 "a numerator that no longer filters on error_code (every request counts)" \
  "$scope s/ \\| error_code=\"unavailable\" \\[/ [/"
mutant R2 "a volume guard over a different pipeline than the ratio's denominator" \
  "$scope s/(and on\\(\\) \\(sum\\(count_over_time\\(.*) \\| kind=\"request\" \\[1h\\]\\)\\) >=/\\1 | kind=\"origin\" [1h])) >=/"
mutant R2 "an expression that is not a ratio" \
  "$scope s/\\) and on\\(\\) /) or on() /"

[ "$fail" -eq 0 ]
