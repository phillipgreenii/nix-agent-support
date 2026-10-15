#!/usr/bin/env bash
# Structure check for the pg-connector-github alert rules file, plus mutant self-tests
# proving each rule can fail.
#
#   check-alert-structure.sh <alerts.yaml> <metrics-catalog.txt>
#   check-alert-structure.sh --self-test <alerts.yaml> <metrics-catalog.txt>
#
# Needs yq (mikefarah), jq, grep, sed, and logcli (grafana-loki) for rule S7.
#
# Every failure line starts with RULE-Sn so a mutant can assert the exact rule that fired.
#
#   S1  exactly the seven uids, each once, each at most 40 characters
#   S2  one group pg-connector-github in folder pg-connector, no folderUid key anywhere
#   S3  every rule has condition C, refIds A B C, annotations.summary and description, labels
#       with exactly the one key severity; each Prometheus rule has a non-empty for
#   S4  the Prometheus rules use datasourceUid prometheus (instant: true), the Loki rule uses
#       loki with queryType: instant
#   S5  daemon-down has noDataState Alerting, every other rule OK
#   S6  every pg_connector_github_* token of an expression is a catalogued name (a histogram's
#       _bucket/_sum/_count stripped first)
#   S7  the Loki expression selects {service_name="pg-connector-github"}, names store_moved_aside
#       and store_refused, and parses as LogQL offline
#
# Exit codes: 0 consistent (self-test: real inputs pass and every mutant fails with exactly
# its own rule), 1 a rule failed, 2 usage error or unreadable input or a missing tool.
set -euo pipefail

usage() {
  echo "usage: check-alert-structure.sh [--self-test] <alerts.yaml> <metrics-catalog.txt>" >&2
  exit 2
}

selftest=0
if [ "${1:-}" = "--self-test" ]; then
  selftest=1
  shift
fi
[ "$#" -eq 2 ] || usage
alerts="$1"
catalog="$2"
for f in "$alerts" "$catalog"; do
  if [ ! -r "$f" ] || [ ! -f "$f" ]; then
    echo "check-alert-structure.sh: cannot read $f" >&2
    exit 2
  fi
done
for tool in yq jq logcli; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "check-alert-structure.sh: $tool is not on PATH" >&2
    exit 2
  fi
done

loki_uid="pg-conn-github-store-aside"
down_uid="pg-conn-github-daemon-down"
want_uids="pg-conn-github-consumer-stuck
pg-conn-github-daemon-down
pg-conn-github-interactive-slow
pg-conn-github-query-stale
pg-conn-github-settle-failing
pg-conn-github-spend-over-cap
pg-conn-github-store-aside"

# lint <alerts.yaml> <catalog>: prints RULE-Sn lines to stderr, returns 1 when any fired.
lint() {
  local file="$1" cat="$2" status=0
  local json got bad
  json="$(yq -o=json '.' "$file")"

  # S1
  got="$(jq -r '[.groups[]?.rules[]?.uid] | sort | .[]' <<<"$json")"
  if [ "$got" != "$want_uids" ]; then
    echo "RULE-S1: the rule uids are not exactly the seven expected ones, each once:" \
      "$(diff <(printf '%s\n' "$want_uids") <(printf '%s\n' "$got") | grep '^[<>]' | tr '\n' ' ')" >&2
    status=1
  fi
  bad="$(jq -r '.groups[]?.rules[]?.uid | select(length > 40)' <<<"$json")"
  if [ -n "$bad" ]; then
    echo "RULE-S1: uid longer than Grafana's 40-character cap: $(printf '%s' "$bad" | tr '\n' ' ')" >&2
    status=1
  fi

  # S2
  if ! jq -e '(.groups | length) == 1 and .groups[0].name == "pg-connector-github"
      and .groups[0].folder == "pg-connector"' <<<"$json" >/dev/null; then
    echo "RULE-S2: want one group named pg-connector-github in folder pg-connector" >&2
    status=1
  fi
  if jq -e '[.. | objects | select(has("folderUid"))] | length > 0' <<<"$json" >/dev/null; then
    echo "RULE-S2: a folderUid key is present (the folder converges by title)" >&2
    status=1
  fi

  # S3
  bad="$(jq -r --arg loki "$loki_uid" '
    .groups[]?.rules[]? |
    select(
      (.condition != "C")
      or (([.data[]?.refId] | sort) != ["A", "B", "C"])
      or ((.annotations.summary // "") == "")
      or ((.annotations.description // "") == "")
      or ((.labels // {} | keys) != ["severity"])
      or (.uid != $loki and (.for // "") == "")
    ) | .uid' <<<"$json")"
  if [ -n "$bad" ]; then
    echo "RULE-S3: rule shape wrong (condition C, refIds A B C, summary, description, labels {severity}, for): $(printf '%s' "$bad" | tr '\n' ' ')" >&2
    status=1
  fi

  # S4
  bad="$(jq -r --arg loki "$loki_uid" '
    .groups[]?.rules[]? |
    select(
      if .uid == $loki
      then (.data[0].datasourceUid != "loki" or .data[0].model.queryType != "instant")
      else (.data[0].datasourceUid != "prometheus" or .data[0].model.instant != true)
      end
    ) | .uid' <<<"$json")"
  if [ -n "$bad" ]; then
    echo "RULE-S4: wrong datasource or query mode: $(printf '%s' "$bad" | tr '\n' ' ')" >&2
    status=1
  fi

  # S5
  bad="$(jq -r --arg down "$down_uid" '
    .groups[]?.rules[]? |
    select(.noDataState != (if .uid == $down then "Alerting" else "OK" end)) | .uid' <<<"$json")"
  if [ -n "$bad" ]; then
    echo "RULE-S5: wrong noDataState (daemon-down Alerting, all others OK): $(printf '%s' "$bad" | tr '\n' ' ')" >&2
    status=1
  fi

  # S6
  local tok base known hist
  known="$(awk 'NF {print $1}' "$cat")"
  hist="$(awk '$2 == "histogram" {print $1}' "$cat")"
  while IFS= read -r tok; do
    [ -n "$tok" ] || continue
    base="$tok"
    case "$tok" in
    *_bucket | *_sum | *_count)
      if grep -qxF "${tok%_*}" <<<"$hist"; then base="${tok%_*}"; fi
      ;;
    esac
    if ! grep -qxF "$base" <<<"$known"; then
      echo "RULE-S6: $tok is not a name in the metrics catalog" >&2
      status=1
    fi
  done < <(jq -r '.groups[]?.rules[]?.data[]?.model.expr // empty' <<<"$json" |
    grep -oE 'pg_connector_github_[A-Za-z0-9_]*' | sort -u || true)

  # S7
  local expr parsed
  expr="$(jq -r --arg loki "$loki_uid" '.groups[]?.rules[]? | select(.uid == $loki) | .data[0].model.expr // empty' <<<"$json")"
  case "$expr" in
  *'{service_name="pg-connector-github"}'*) ;;
  *)
    echo 'RULE-S7: the Loki expression does not select {service_name="pg-connector-github"}' >&2
    status=1
    ;;
  esac
  for name in store_moved_aside store_refused; do
    case "$expr" in
    *"$name"*) ;;
    *)
      echo "RULE-S7: the Loki expression does not name $name" >&2
      status=1
      ;;
    esac
  done
  parsed="$(HOME="${TMPDIR:-/tmp}" logcli query --stdin --quiet "$expr" </dev/null 2>&1 | tail -n 1 || true)"
  case "$parsed" in
  *"metrics Query: not supported"*) ;;
  *)
    echo "RULE-S7: the Loki expression does not parse as LogQL: $parsed" >&2
    status=1
    ;;
  esac
  return "$status"
}

if [ "$selftest" -eq 0 ]; then
  lint "$alerts" "$catalog"
  exit $?
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The real inputs must pass, silently.
if ! out="$(lint "$alerts" "$catalog" 2>&1)"; then
  echo "self-test: the real inputs fail:" >&2
  echo "$out" >&2
  exit 1
fi

fail=0
# mutant <rule> <description> <yq-expression-for-alerts> <sed-script-for-catalog>
# An empty expression leaves that copy unchanged. The mutant must fail with exactly <rule>.
mutant() {
  local rule="$1" desc="$2" yexpr="$3" csed="$4" dir ids
  dir="$(mktemp -d "$work/m.XXXXXX")"
  cat "$alerts" >"$dir/alerts.yaml" # not cp: a store input is read-only and cp keeps the mode
  cat "$catalog" >"$dir/catalog.txt"
  if [ -n "$yexpr" ]; then
    yq "$yexpr" "$alerts" >"$dir/alerts.yaml"
    if cmp -s "$alerts" "$dir/alerts.yaml"; then
      echo "self-test: mutant $rule ($desc) did not change the alerts file" >&2
      fail=1
      return
    fi
  fi
  if [ -n "$csed" ]; then
    sed -E "$csed" "$catalog" >"$dir/catalog.txt"
    if cmp -s "$catalog" "$dir/catalog.txt"; then
      echo "self-test: mutant $rule ($desc) did not change the catalog" >&2
      fail=1
      return
    fi
  fi
  if ids="$(lint "$dir/alerts.yaml" "$dir/catalog.txt" 2>&1 >/dev/null)"; then
    echo "self-test: mutant $rule ($desc) passed" >&2
    fail=1
    return
  fi
  ids="$(printf '%s\n' "$ids" | grep -o '^RULE-S[0-9]*' | sort -u | tr '\n' ' ')"
  if [ "$ids" != "RULE-$rule " ]; then
    echo "self-test: mutant $rule ($desc) fired '${ids}' instead of only RULE-$rule" >&2
    fail=1
  fi
}

rule_of() { printf '.groups[0].rules[] | select(.uid == "%s")' "$1"; }

mutant S1 "a uid longer than 40 characters" \
  "($(rule_of pg-conn-github-settle-failing) | .uid) = \"pg-conn-github-settle-failing-and-much-too-long\"" ""
mutant S2 "a folderUid key" \
  "($(rule_of pg-conn-github-query-stale) | .folderUid) = \"pg-connector\"" ""
mutant S3 "a rule without its description" \
  "del($(rule_of pg-conn-github-query-stale) | .annotations.description)" ""
mutant S4 "a Prometheus rule on the Loki datasource" \
  "($(rule_of pg-conn-github-consumer-stuck) | .data[0].datasourceUid) = \"loki\"" ""
mutant S5 "a rule other than daemon-down at noDataState Alerting" \
  "($(rule_of pg-conn-github-interactive-slow) | .noDataState) = \"Alerting\"" ""
mutant S6 "an expression name missing from the catalog" "" \
  '/^pg_connector_github_queue_oldest_overdue_seconds /d'
mutant S7 "a Loki expression that no longer names store_refused" \
  "($(rule_of "$loki_uid") | .data[0].model.expr) |= sub(\"store_refused\"; \"store_other\")" ""
mutant S7 "a Loki expression that does not parse" \
  "($(rule_of "$loki_uid") | .data[0].model.expr) |= sub(\"\\[10m\\]\"; \"[10m\")" ""

[ "$fail" -eq 0 ]
