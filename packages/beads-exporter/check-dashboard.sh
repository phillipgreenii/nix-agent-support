#!/usr/bin/env bash
# Lint of the Beads Grafana dashboard against the exporter's metric catalog, plus mutant
# self-tests proving each rule can fail.
#
#   check-dashboard.sh <dashboard.json> <metrics.txt>
#
# The catalog is metrics.txt: one metric family per line. A family whose name ends in
# _total is a counter.
#
# Every failure line starts with RULE-Bn so a mutant can assert the exact rule that fired.
# Each rule is evaluated with jq -e and ANY non-zero jq exit (1 = false, 4 = no output,
# 5 = runtime error) counts as that rule failing, so a jq error can never read as a pass.
#
#   B1  uid, title, default time range and refresh
#   B2  every beads_* token in a query is a catalogued family
#   B3  every catalogued family is queried
#   B4  counters only appear inside rate, irate or increase
#   B5  the db variable: label_values(beads_exporter_up, db), multi, All, ".*", default All
#   B6  every domain target filters db=~"$db"
#   B7  aggregation under All: counts sum, *_up min, ages time() - min(), durations max
#   B8  every non-row panel has a description
#   B9  queue tiles aggregate with sum by (db ...): never summed across databases
#   B10 the data-age tile is titled "(worst db)" and says totals exclude a stale db
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: check-dashboard.sh <dashboard.json> <metrics.txt>" >&2
  exit 2
fi
dash="$1"
cat="$2"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cd "$work"

# lint <dashboard.json> <catalog>; prints RULE-Bn lines on failure.
lint() {
  local d="$1" c="$2" names counters status=0
  names="$(jq -R . "$c" | jq -s -c .)"
  counters="$(grep '_total$' "$c" | jq -R . | jq -s -c .)"

  rule() { # rule <id> <message> <jq args...>
    local id="$1" msg="$2"
    shift 2
    if ! jq -e "$@" >/dev/null 2>&1; then
      echo "RULE-$id: $msg" >&2
      status=1
    fi
  }
  local exprs='[.. | objects | select(has("expr")) | .expr]'

  rule B1 'uid must be beads, titled "Beads / Queues & backlog", default now-48h, refresh 5m' \
    '.uid == "beads" and .title == "Beads / Queues & backlog"
     and .time.from == "now-48h" and .time.to == "now" and .refresh == "5m"' "$d"
  rule B2 "a query uses a beads_* token that is not in the metric catalog" \
    --argjson names "$names" \
    "$exprs | [.[] | scan(\"beads_[a-z0-9_]+\")] | length > 0 and all(. as \$t | \$names | index(\$t) != null)" "$d"
  rule B3 "a catalogued metric family is not used by any query" \
    --argjson names "$names" \
    "$exprs | join(\" \") as \$all | \$names | length > 0 and all(. as \$n | [\$all | scan(\$n + \"([^a-z0-9_]|\$)\")] | length > 0)" "$d"
  rule B4 "a counter family is used outside rate, irate or increase" \
    --argjson counters "$counters" \
    "$exprs | join(\" \") as \$all | \$counters | length > 0 and all(. as \$n | ([\$all | scan(\$n + \"([^a-z0-9_]|\$)\")] | length) == ([\$all | scan(\"(rate|irate|increase)\\\\(\" + \$n + \"([^a-z0-9_]|\$)\")] | length))" "$d"
  rule B5 'the db variable must be label_values(beads_exporter_up, db), multi, includeAll, allValue ".*", default All, datasource prometheus' \
    '.templating.list | length == 1 and (.[0]
       | .name == "db" and .type == "query"
         and .definition == "label_values(beads_exporter_up, db)"
         and .multi == true and .includeAll == true and .allValue == ".*"
         and .datasource.uid == "prometheus"
         and .current.value == ["$__all"])' "$d"
  rule B6 'a domain target does not filter db=~"$db"' \
    '[.. | objects | select(has("targets")) | .targets[] | .expr]
     | length > 0 and all(contains("db=~\"$db\""))' "$d"
  rule B7 "aggregation under All: counts use sum, *_up uses min, ages use time() - min(), durations use max" \
    '[.. | objects | select(has("targets")) | .targets[] | .expr]
     | all(
         if contains("beads_exporter_up") then test("^min( by \\([^)]*\\))? ?\\(")
         elif contains("_timestamp_seconds") then test("^time\\(\\) - min( by \\([^)]*\\))? ?\\(")
         elif contains("collect_duration") then test("^max by \\(")
         else test("^(label_replace\\()*(topk\\([0-9]+, )?sum( by \\([^)]*\\))? ?\\(") end)' "$d"
  rule B8 "every non-row panel needs a description" \
    '[.. | objects | select(has("gridPos") and .type != "row")]
     | length > 0 and all((.description // "") | length > 0)' "$d"
  rule B9 "queue tiles must aggregate with sum by (db ...) so they are never summed across databases" \
    '[.. | objects | select(has("expr")) | .expr | select(contains("beads_queue_candidates"))] as $q
     | ($q | length > 0) and all($q[];
         ([scan("beads_queue_candidates")] | length)
         == ([scan("sum by \\(db(, [a-z]+)*\\) \\(beads_queue_candidates")] | length))' "$d"
  rule B10 "the data-age tile must be titled (worst db) and its description must say totals exclude a stale db" \
    '[.. | objects | select(has("gridPos") and (.title // "" | contains("(worst db)")))]
     | length == 1 and (.[0].description | test("excludes a stale database"))' "$d"
  return "$status"
}

if ! lintout="$(lint "$dash" "$cat" 2>&1)"; then
  echo "$lintout" >&2
  echo "ERROR: the Beads dashboard failed the lint" >&2
  exit 1
fi

# expect_rejected <label> <rule> <dashboard> <catalog>
expect_rejected() {
  local name="$1" want="$2" d="$3" c="$4" out
  if out="$(lint "$d" "$c" 2>&1)"; then
    echo "ERROR: mutant $name was NOT rejected by the lint" >&2
    exit 1
  fi
  case "$out" in
  *"RULE-$want"*) ;;
  *)
    echo "ERROR: mutant $name was rejected, but not by RULE-$want:" >&2
    echo "$out" >&2
    exit 1
    ;;
  esac
}

# mutate <name> <expected rule> <jq program over the dashboard>
mutants=0
mutate() {
  local name="$1" want="$2" prog="$3"
  if ! jq "$prog" "$dash" >"mutant-$name.json"; then
    echo "ERROR: mutant $name: jq could not build the mutant" >&2
    exit 1
  fi
  if cmp -s "$dash" "mutant-$name.json"; then
    echo "ERROR: mutant $name changed nothing" >&2
    exit 1
  fi
  expect_rejected "$name" "$want" "mutant-$name.json" "$cat"
  mutants=$((mutants + 1))
}

# mutate_catalog <name> <expected rule> <sed expression>
mutate_catalog() {
  local name="$1" want="$2"
  sed "$3" "$cat" >"catalog-$name.txt"
  if cmp -s "$cat" "catalog-$name.txt"; then
    echo "ERROR: catalog mutant $name changed nothing" >&2
    exit 1
  fi
  expect_rejected "$name" "$want" "$dash" "catalog-$name.txt"
  mutants=$((mutants + 1))
}

# Replace the expr of the first target of panel <id>.
set_expr() { # set_expr <panel id> <expr>
  printf '(.. | objects | select(has("gridPos") and .id == %s) | .targets[0].expr) = %s' "$1" "$(jq -n --arg e "$2" '$e')"
}

mutate wrong-uid B1 '.uid = "beads-x"'
mutate wrong-refresh B1 '.refresh = "1m"'
mutate unknown-metric B2 '(.. | objects | select(has("expr")) | .expr) |= gsub("beads_issues_stored"; "beads_issues_storedz")'
mutate unused-family B3 "$(set_expr 25 'sum by (status) (beads_issues{db=~"$db"})')"
mutate_catalog extra-family B3 '$a beads_phantom_family'
mutate counter-outside-rate B4 "$(set_expr 29 'sum by (db, pass, reason) (beads_exporter_collect_errors_total{db=~"$db"})')"
mutate variable-not-all B5 '.templating.list[0].includeAll = false'
mutate variable-all-value B5 '.templating.list[0].allValue = "*"'
mutate variable-single B5 '.templating.list[0].multi = false'
mutate variable-query B5 '.templating.list[0].definition = "label_values(beads_issues, db)"'
mutate target-without-db-filter B6 "$(set_expr 2 'sum(beads_issues)')"
mutate up-not-min B7 "$(set_expr 27 'sum by (db) (beads_exporter_up{db=~"$db"})')"
mutate age-not-min B7 "$(set_expr 1 'time() - max(beads_exporter_pass_last_success_timestamp_seconds{db=~"$db",pass="main"})')"
mutate count-not-sum B7 "$(set_expr 2 'max(beads_issues{db=~"$db"})')"
mutate duration-not-max B7 "$(set_expr 30 'sum by (db, pass) (beads_exporter_collect_duration_seconds{db=~"$db"})')"
mutate missing-description B8 '(.. | objects | select(has("gridPos") and .id == 2)) |= del(.description)'
mutate queue-tile-summed-across-dbs B9 "$(set_expr 8 'sum(beads_queue_candidates{db=~"$db",queue="drain-claim"})')"
mutate data-age-title B10 '(.. | objects | select(has("gridPos") and .id == 1) | .title) = "Data age"'
mutate data-age-description B10 '(.. | objects | select(has("gridPos") and .id == 1) | .description) = "How old the data is."'

echo "OK: the Beads dashboard passes B1-B10 and $mutants mutants were each rejected by the intended rule."
