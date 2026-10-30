#!/usr/bin/env bash
# Runs the pg-ccrec-crash-share PromQL against the shared label table
# (packages/pg-claude-code-recorder/client/testdata/labels.json), under promtool (docket pg2-v77tsx,
# phase 7c).
#
#   check-crash-share-label-table.sh <alerts.yaml> <labels.json>
#
# Each row of the table that ended becomes the counter series the daemon would export for it:
# a row with registry_stale_at_discovery true counts once in registry_stale_at_discovery_total (a
# stale discovery never reaches conversation_end_total); any other ended row counts once in
# conversation_end_total{registry_present,exit_observed,clear_entry,exit_entry} with its own evidence
# classes. Every count is scaled by 5 (so the volume floor of 20 ends is met) and spread linearly
# over a day. The rule's expression must then read the table's own crashed-plus-suspected share:
# the rows with "suspected": true divided by the ended rows. The script also cross-checks that the
# crashed evidence class plus the stale rows equal the table's suspected rows, so a label-table
# change that the rule does not follow fails here.
#
# Needs yq (mikefarah), jq and promtool. Exit codes: 0 pass, 2 usage or missing input or tool,
# 3 a check failed (1 is reserved for unexpected errors).
set -euo pipefail

[ "$#" -eq 2 ] || {
  echo "usage: check-crash-share-label-table.sh <alerts.yaml> <labels.json>" >&2
  exit 2
}
alerts="$1"
table="$2"
for f in "$alerts" "$table"; do
  [ -r "$f" ] || {
    echo "check-crash-share-label-table.sh: cannot read $f" >&2
    exit 2
  }
done
for tool in yq jq promtool; do
  command -v "$tool" >/dev/null || {
    echo "check-crash-share-label-table.sh: missing tool $tool" >&2
    exit 2
  }
done

scale=5
fail() {
  echo "FAIL: $*" >&2
  exit 3
}

expr="$(yq -o=json '.' "$alerts" | jq -r '.groups[].rules[] | select(.uid == "pg-ccrec-crash-share") | .data[] | select(.refId == "A") | .model.expr')"
[ -n "$expr" ] || fail "no pg-ccrec-crash-share rule in $alerts"

# Counts from the table.
read -r ended suspected stale crashed < <(jq -r '
  .rows as $r
  | [ ($r | map(select(.evidence.ended)) | length),
      ($r | map(select(.suspected == true)) | length),
      ($r | map(select(.evidence.ended and .evidence.registry_stale_at_discovery)) | length),
      ($r | map(select(.evidence.ended and (.evidence.registry_stale_at_discovery | not)
                        and .evidence.registry_present_at_exit and .evidence.exit_observed
                        and (.evidence.clear_entry | not) and (.evidence.exit_entry | not))) | length) ]
  | @tsv' "$table")
[ "$((crashed + stale))" -eq "$suspected" ] ||
  fail "the table's crashed class ($crashed) plus stale rows ($stale) is not its suspected rows ($suspected)"
[ "$((ended * scale))" -ge 20 ] || fail "the scaled ended count is under the volume floor"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# One series per evidence-class combination of the non-stale ended rows, plus the stale counter.
series="$(jq -r --argjson k "$scale" '
  [.rows[] | select(.evidence.ended and (.evidence.registry_stale_at_discovery | not))
   | {rp: (.evidence.registry_present_at_exit | tostring), eo: (.evidence.exit_observed | tostring),
      ce: (.evidence.clear_entry | tostring), xe: (.evidence.exit_entry | tostring)}]
  | group_by([.rp, .eo, .ce, .xe])
  | map("      - series: pg_claude_code_recorder_conversation_end_total{registry_present=\"\(.[0].rp)\",exit_observed=\"\(.[0].eo)\",clear_entry=\"\(.[0].ce)\",exit_entry=\"\(.[0].xe)\"}\n        values: \"0+\(length * $k / 1440)x2900\"")
  | .[]' "$table")"
stale_series="      - series: pg_claude_code_recorder_registry_stale_at_discovery_total
        values: \"0+$(jq -n "$stale * $scale / 1440")x2900\""
want="$(jq -n "($suspected / $ended * 100 | round) / 100")"

jq -n --arg e "$expr" '{groups: [{name: "g", interval: "1m", rules: [{record: "crash_share", expr: $e}]}]}' >"$work/rules.yml"
cat >"$work/test.yml" <<EOF
rule_files:
  - rules.yml
evaluation_interval: 1m
tests:
  - name: the crash share of the label table
    interval: 1m
    input_series:
$series
$stale_series
    promql_expr_test:
      - expr: round(crash_share, 0.01)
        eval_time: 2d
        exp_samples:
          - labels: '{}'
            value: $want
EOF
(cd "$work" && promtool check rules rules.yml && promtool test rules test.yml)
echo "OK: crash-share reads $want over the label table ($suspected suspected of $ended ended rows)"
