#!/usr/bin/env bash
# Signals-registration check of the pg-claude-code-recorder (docket pg2-v77tsx, phase 7c): the metric
# catalogue against the daemon's Go constants, the rule uids against the router probe's list, the
# deleteRules twin and the dashboard, plus negative-control mutants proving each rule can fail.
#
#   check-signals-registration.sh <catalog.txt> <obs.go> <parser.go> <alerts.yaml> <alerts-delete.yaml> \
#                                 <run.go> <dashboard.json> <readme.md>
#   check-signals-registration.sh --self-test <the same eight files>
#
# Needs yq (mikefarah), jq, grep, promtool. Every failure line starts with RULE-Rn.
#
#   R1  the catalogue names (prefix pg_claude_code_recorder_ stripped) are exactly the values of the
#       M* string constants of obs.go and parser.go, each once
#   R2  the pg-ccrec-* entries of registeredRuleUIDs in run.go are exactly the rule uids of the
#       rules file, in its order
#   R3  the deleteRules twin lists exactly the rule uids
#   R4  every pg_claude_code_recorder_* token of a dashboard expression is catalogued, every
#       expression parses under promtool, and the eight panels the design names are present
#   R5  the README documents remove.enable's readiness checklist only with the daemon-down and
#       free-space alerts named, doctor staleness, a state directory backup, remove --dry-run and
#       the "no undo other than extract" statement
#
# Exit codes: 0 clean, 2 usage error or unreadable input or missing tool, 3 a rule failed (1 is
# reserved for unexpected errors).
set -euo pipefail

selftest=0
if [ "${1:-}" = "--self-test" ]; then
  selftest=1
  shift
fi
[ "$#" -eq 8 ] || {
  echo "usage: check-signals-registration.sh [--self-test] <catalog> <obs.go> <parser.go> <alerts.yaml> <alerts-delete.yaml> <run.go> <dashboard.json> <readme.md>" >&2
  exit 2
}
for f in "$@"; do
  [ -r "$f" ] || {
    echo "check-signals-registration.sh: cannot read $f" >&2
    exit 2
  }
done
for tool in yq jq grep promtool; do
  command -v "$tool" >/dev/null || {
    echo "check-signals-registration.sh: missing tool $tool" >&2
    exit 2
  }
done

check() {
  local catalog="$1" obs="$2" parser="$3" alerts="$4" deletes="$5" runGo="$6" dash="$7" readme="$8" fails=""
  rule_fail() { fails="${fails}$1"$'\n'; }

  # R1
  local want have
  want="$(cat "$obs" "$parser" | grep -E '^[[:space:]]+M[A-Za-z0-9]+[[:space:]]*= "[a-z0-9_]+"' |
    sed -E 's/^[^"]*"([a-z0-9_]+)".*/\1/' | sort)"
  have="$(grep -vE '^(#|$)' "$catalog" | awk '{print $1}' | sed 's/^pg_claude_code_recorder_//' | sort)"
  if [ "$want" != "$have" ] || [ "$(printf '%s\n' "$have" | uniq -d | wc -l)" -ne 0 ]; then
    rule_fail "RULE-R1: the catalogue and the Go M* constants differ: $(diff <(printf '%s\n' "$want") <(printf '%s\n' "$have") | grep '^[<>]' | tr '\n' ' ')"
  fi
  # R2
  local fromFile fromGo
  fromFile="$(yq -o=json '.' "$alerts" | jq -r '.groups[].rules[].uid')"
  fromGo="$(grep -oE '^[[:space:]]+"pg-ccrec-[a-z0-9-]+",' "$runGo" | sed -E 's/^[^"]*"([^"]+)".*/\1/')"
  if [ -z "$fromFile" ] || [ "$fromFile" != "$fromGo" ]; then
    rule_fail "RULE-R2: the rule file uids and the pg-ccrec-* entries of registeredRuleUIDs differ"
  fi
  # R3
  if [ "$(yq -o=json '.' "$deletes" | jq -r '.deleteRules[].uid' | sort)" != "$(printf '%s\n' "$fromFile" | sort)" ]; then
    rule_fail "RULE-R3: the deleteRules twin does not list exactly the rule uids"
  fi
  # R4
  local bad="" tok base exprs
  exprs="$(jq -r '.panels[].targets[].expr' "$dash")"
  while IFS= read -r tok; do
    [ -n "$tok" ] || continue
    if ! grep -q "^$tok " "$catalog"; then
      base="${tok%_bucket}"
      base="${base%_sum}"
      base="${base%_count}"
      grep -q "^$base histogram " "$catalog" || bad="$bad $tok"
    fi
  done < <(printf '%s\n' "$exprs" | grep -o 'pg_claude_code_recorder_[a-z0-9_]*' | sort -u)
  if [ -n "$bad" ]; then
    rule_fail "RULE-R4: dashboard names not in the catalogue:$bad"
  fi
  local tmp
  tmp="$(mktemp -d)"
  jq -c '{groups: [{name: "d", rules: [.panels[].targets[] | {record: ("d:" + .refId), expr: (.expr | gsub("\\$__[a-z_]+"; "5m"))}]}]}' "$dash" >"$tmp/r.yml"
  if ! promtool check rules "$tmp/r.yml" >"$tmp/out" 2>&1; then
    rule_fail "RULE-R4: a dashboard expression does not parse: $(tr '\n' ' ' <"$tmp/out")"
  fi
  rm -rf "$tmp"
  local topic
  for topic in "copy and parse lag" "conversations by stage" "held age" "dropped bytes by reason" "free space and its slope" "removal backlog" "processes without an inbox" "request rate by client"; do
    jq -e --arg t "$topic" '[.panels[].title | ascii_downcase | select(. == $t)] | length == 1' "$dash" >/dev/null ||
      rule_fail "RULE-R4: no panel titled '$topic'"
  done
  # R5
  local missing=""
  for needle in "remove.enable" "daemon-down" "free-space" "no undo other than" "remove --dry-run" "doctor" "backed up"; do
    grep -qi -- "$needle" "$readme" || missing="$missing [$needle]"
  done
  if [ -n "$missing" ]; then
    rule_fail "RULE-R5: the README is missing:$missing"
  fi
  printf '%s' "$fails"
}

if [ "$selftest" -eq 0 ]; then
  out="$(check "$@")"
  if [ -n "$out" ]; then
    printf '%s' "$out" >&2
    exit 3
  fi
  echo "OK: signals registration (R1-R5) holds"
  exit 0
fi

out="$(check "$@")"
if [ -n "$out" ]; then
  printf '%s' "$out" >&2
  echo "FAIL: the real inputs do not pass" >&2
  exit 3
fi
catalog="$1" obs="$2" parser="$3" alerts="$4" deletes="$5" runGo="$6" dash="$7" readme="$8"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
n=0
expect() { # <rule> <description> <fail-output>
  local got
  got="$(printf '%s' "$3" | grep -o '^RULE-R[0-9]*' | sort -u | tr '\n' ' ')"
  if [ "$got" != "$1 " ]; then
    echo "FAIL: mutant '$2' expected exactly $1, got: ${got:-nothing}" >&2
    exit 3
  fi
  n=$((n + 1))
  echo "ok: mutant '$2' fails with $1"
}
# R1: a catalogue line dropped
grep -v 'bytes_pending_settle' "$catalog" >"$work/cat"
expect RULE-R1 "a metric missing from the catalogue" "$(check "$work/cat" "$obs" "$parser" "$alerts" "$deletes" "$runGo" "$dash" "$readme")"
# R2: a uid missing from run.go
grep -v '"pg-ccrec-crash-share",' "$runGo" >"$work/run.go"
expect RULE-R2 "a rule uid missing from registeredRuleUIDs" "$(check "$catalog" "$obs" "$parser" "$alerts" "$deletes" "$work/run.go" "$dash" "$readme")"
# R3: a twin entry dropped
yq 'del(.deleteRules[0])' "$deletes" >"$work/del.yaml"
expect RULE-R3 "a twin uid missing" "$(check "$catalog" "$obs" "$parser" "$alerts" "$work/del.yaml" "$runGo" "$dash" "$readme")"
# R4: a dashboard with an unknown metric
jq '.panels[0].targets[0].expr = "max(pg_claude_code_recorder_not_a_metric)"' "$dash" >"$work/dash.json"
expect RULE-R4 "a dashboard metric not in the catalogue" "$(check "$catalog" "$obs" "$parser" "$alerts" "$deletes" "$runGo" "$work/dash.json" "$readme")"
# R5: a README without the no-undo statement
grep -vi 'no undo other than' "$readme" >"$work/readme.md"
expect RULE-R5 "a README without the no-undo statement" "$(check "$catalog" "$obs" "$parser" "$alerts" "$deletes" "$runGo" "$dash" "$work/readme.md")"
echo "OK: $n mutants each fail with exactly their own rule"
