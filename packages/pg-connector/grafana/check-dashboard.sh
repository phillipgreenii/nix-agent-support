#!/usr/bin/env bash
# Lint of the pg-connector-github Grafana dashboard against the daemon's metric catalog, plus
# mutant self-tests proving each rule can fail.
#
#   check-dashboard.sh <dashboard.json> <metrics-catalog.txt>
#   check-dashboard.sh --self-test <dashboard.json> <metrics-catalog.txt>
#
# The catalog is pg-connector-github-metrics.txt: one line per metric,
# "<prometheus_name> <type> <labels>", type gauge, counter or histogram. A histogram is listed
# once under its base name; its _bucket, _sum and _count series are implied.
#
# Needs jq and promtool (rule D7). Every failure line starts with RULE-Dn, at most one line per
# rule, so a mutant can assert the exact rule that fired.
#
#   D1  uid and title are pg-connector-github; .time.from, .time.to and .refresh are non-empty
#       strings
#   D2  every pg_connector_github_* token of an expr is a catalogued name once a _bucket, _sum
#       or _count suffix is stripped (a token is a maximal run of [a-z0-9_])
#   D3  every occurrence of a counter name is followed by an optional {...} selector and a
#       [<range>], and its expression contains rate(, irate( or increase(; a _bucket, _sum or
#       _count suffix appears only on a histogram's name, and a histogram's name appears only
#       with one of those suffixes
#   D4  each of the eight topic phrases is a case-insensitive substring of at least one panel's
#       title, with - and _ read as spaces
#   D5  every panel whose type is not row has a non-empty description
#   D6  every target's datasource is {"type": "prometheus", "uid": "prometheus"}
#   D7  every expr, with the Grafana variables $__rate_interval, $__interval, $__range and any
#       other $variable replaced by 5m, parses under promtool check rules
#
# Panels are collected recursively, so a panel nested under a row counts.
#
# Exit codes: 0 clean (self-test: the real inputs pass and every mutant fails with exactly its
# own rule), 1 a rule failed, 2 usage error or unreadable input or a missing tool.
# jq and sed programs are single-quoted on purpose: their $names are jq or regex syntax, not shell.
# shellcheck disable=SC2016
set -euo pipefail

usage() {
  echo "usage: check-dashboard.sh [--self-test] <dashboard.json> <metrics-catalog.txt>" >&2
  exit 2
}

selftest=0
if [ "${1:-}" = "--self-test" ]; then
  selftest=1
  shift
fi
[ "$#" -eq 2 ] || usage
dash="$1"
catalog="$2"
for f in "$dash" "$catalog"; do
  if [ ! -f "$f" ] || [ ! -r "$f" ]; then
    echo "check-dashboard.sh: cannot read $f" >&2
    exit 2
  fi
done
for tool in jq promtool; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "check-dashboard.sh: $tool is not on PATH" >&2
    exit 2
  fi
done

topics='["budget against the cap","spend by purpose and group","queue depth and oldest overdue","freshness per query","served_from ratio","detection delay","settle results and pending rows","consumer lag"]'

# jq definitions shared by the rules.
defs='
def panels: (.panels[]? | ., panels);
def exprs: [.. | objects | select(has("expr")) | .expr];
def tokens: [scan("(?<![a-z0-9_])pg_connector_github_[a-z0-9_]+")];
def base: if test("_(bucket|sum|count)$") then sub("_(bucket|sum|count)$"; "") else . end;
'

# lint <dashboard.json> <catalog>: prints RULE-Dn lines to stderr, returns 1 when any fired.
lint() {
  local d="$1" c="$2" status=0 cat out rules
  cat="$(jq -R -s -c '[split("\n")[] | select(length > 0) | split(" ") | {name: .[0], type: .[1]}]' "$c")"

  rule() { # rule <id> <message> <jq args...>; ANY non-zero jq exit counts as the rule failing
    local id="$1" msg="$2"
    shift 2
    if ! jq -e "$@" >/dev/null 2>&1; then
      echo "RULE-$id: $msg" >&2
      status=1
    fi
  }

  rule D1 'uid and title must be pg-connector-github, and time.from, time.to and refresh non-empty strings' \
    '.uid == "pg-connector-github" and .title == "pg-connector-github"
     and (.time.from | type == "string" and length > 0)
     and (.time.to | type == "string" and length > 0)
     and (.refresh | type == "string" and length > 0)' "$d"

  rule D2 'a query uses a pg_connector_github_* token that is not a catalogued name (after stripping a histogram suffix)' \
    --argjson cat "$cat" \
    "$defs"'($cat | map(.name)) as $names
     | exprs | [.[] | tokens[]] | length > 0
       and all(.[]; . as $t | ($names | index([$t]) != null) or ($names | index([$t | base]) != null))' "$d"

  rule D3 'a counter is used outside rate, irate or increase with a [range], or a histogram suffix is on a non-histogram, or a histogram is used without its suffix' \
    --argjson cat "$cat" \
    "$defs"'($cat | map(select(.type == "counter") | .name)) as $counters
     | ($cat | map(select(.type == "histogram") | .name)) as $hists
     | ($cat | map(.name)) as $names
     | exprs | length > 0 and all(.[]; . as $e
       | all($counters[]; . as $n
           | ([$e | scan("(?<![a-z0-9_])" + $n + "(?![a-z0-9_])")] | length)
             == ([$e | scan("(?<![a-z0-9_])" + $n + "(?![a-z0-9_])(\\{[^}]*\\})?\\[[^\\]]+\\]")] | length))
       and (if any($counters[]; . as $n | $e | test("(?<![a-z0-9_])" + $n + "(?![a-z0-9_])"))
            then ($e | test("(^|[^a-z0-9_])(rate|irate|increase)\\("))
            else true end)
       and all($e | tokens[]; . as $t
           | if ($names | index([$t])) != null then ($hists | index([$t])) == null
             else ($t | base) as $b
               | if $b == $t or ($names | index([$b])) == null then true
                 else ($hists | index([$b])) != null end
             end))' "$d"

  rule D4 'a topic of the dashboard list has no panel whose title names it' \
    --argjson topics "$topics" \
    "$defs"'[panels | .title // "" | ascii_downcase | gsub("[-_]"; " ")] as $titles
     | all($topics[]; . as $t | ($t | ascii_downcase | gsub("[-_]"; " ")) as $p
         | any($titles[]; contains($p)))' "$d"

  rule D5 'every non-row panel needs a non-empty description' \
    "$defs"'[panels | select(.type != "row")]
     | length > 0 and all(.[]; (.description // "" | type == "string" and length > 0))' "$d"

  rule D6 'every target needs the datasource {"type": "prometheus", "uid": "prometheus"}' \
    "$defs"'[panels | .targets[]?] | length > 0
     and all(.[]; .datasource == {"type": "prometheus", "uid": "prometheus"})' "$d"

  # D7: wrap each expr as a recording rule in a JSON (YAML) rules file and let promtool parse it.
  rules="$(mktemp -d)/rules.yaml"
  jq -r "$defs"'exprs[]' "$d" |
    sed -E 's/\$(\{[A-Za-z_][A-Za-z0-9_]*\}|[A-Za-z_][A-Za-z0-9_]*)/5m/g' |
    jq -R -s -c 'split("\n") | map(select(length > 0)) | to_entries
      | {groups: [{name: "dashboard", rules: map({record: ("dashboard:expr\(.key)"), expr: .value})}]}' >"$rules"
  if ! jq -e '.groups[0].rules | length > 0' "$rules" >/dev/null 2>&1; then
    echo "RULE-D7: the dashboard has no query to parse" >&2
    status=1
  elif ! out="$(promtool check rules "$rules" 2>&1)"; then
    echo "RULE-D7: an expression does not parse as PromQL: $(printf '%s' "$out" | tr '\n' ' ')" >&2
    status=1
  fi
  rm -rf "$(dirname "$rules")"
  return "$status"
}

if [ "$selftest" -eq 0 ]; then
  lint "$dash" "$catalog"
  exit $?
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cp "$dash" "$work/dashboard.json"
cp "$catalog" "$work/catalog.txt"
sdash="$work/dashboard.json"
scat="$work/catalog.txt"

# The real inputs must pass, silently.
if ! out="$(lint "$sdash" "$scat" 2>&1)"; then
  echo "self-test: the real inputs fail:" >&2
  echo "$out" >&2
  exit 1
fi

mutants=0
fail=0
# expect <name> <rule> <dashboard> <catalog>: the lint must fail with exactly RULE-<rule>.
expect() {
  local name="$1" want="$2" out ids
  if out="$(lint "$3" "$4" 2>&1)"; then
    echo "self-test: mutant $name was NOT rejected" >&2
    fail=1
    return
  fi
  ids="$(grep -oE '^RULE-D[0-9]+' <<<"$out" | sort -u | tr '\n' ' ' || true)"
  if [ "$ids" != "RULE-$want " ]; then
    echo "self-test: mutant $name fired '${ids}' instead of only RULE-$want:" >&2
    echo "$out" >&2
    fail=1
    return
  fi
  mutants=$((mutants + 1))
}

# mutate <name> <rule> <jq program over the dashboard>
mutate() {
  local name="$1" want="$2"
  if ! jq "$3" "$sdash" >"$work/m-$name.json"; then
    echo "self-test: mutant $name: jq could not build it" >&2
    fail=1
    return
  fi
  if cmp -s "$sdash" "$work/m-$name.json"; then
    echo "self-test: mutant $name changed nothing" >&2
    fail=1
    return
  fi
  expect "$name" "$want" "$work/m-$name.json" "$scat"
}

# mutate_catalog <name> <rule> <sed script>
mutate_catalog() {
  local name="$1" want="$2"
  sed -E "$3" "$scat" >"$work/c-$name.txt"
  if cmp -s "$scat" "$work/c-$name.txt"; then
    echo "self-test: catalog mutant $name changed nothing" >&2
    fail=1
    return
  fi
  expect "$name" "$want" "$sdash" "$work/c-$name.txt"
}

# set_expr <panel id> <expr>: replace the expr of the first target of that panel.
set_expr() {
  printf '(.panels[] | select(.id == %s) | .targets[0].expr) = %s' "$1" "$(jq -n --arg e "$2" '$e')"
}

mutate wrong-uid D1 '.uid = "pg-connector-github-x"'
mutate wrong-title D1 '.title = "pg connector"'
mutate empty-time-from D1 '.time.from = ""'
mutate missing-refresh D1 'del(.refresh)'

mutate unknown-metric D2 '(.. | objects | select(has("expr")) | .expr) |= gsub("queue_depth"; "queue_depthz")'
mutate_catalog dropped-catalog-line D2 '/^pg_connector_github_query_last_success_age_seconds /d'

mutate counter-outside-rate D3 "$(set_expr 1 'sum(pg_connector_github_budget_spent{bucket="graphql"})')"
mutate counter-without-range D3 "$(set_expr 1 'sum(pg_connector_github_budget_spent{bucket="graphql"}) + sum(increase(pg_connector_github_budget_spent{bucket="graphql"}[1h]))')"
mutate suffix-on-gauge D3 '(.. | objects | select(has("expr")) | .expr) |= gsub("queue_depth"; "queue_depth_sum")'
mutate histogram-without-suffix D3 '(.. | objects | select(has("expr")) | .expr) |= gsub("request_duration_count"; "request_duration")'
mutate_catalog counter-retyped D3 's/^(pg_connector_github_queue_depth) gauge/\1 counter/'

mutate missing-topic D4 '(.panels[] | select(.title == "Freshness per query") | .title) = "Staleness"'
mutate topic-underscore-lost D4 '(.panels[] | select(.title == "Served-from ratio") | .title) = "Served ratio"'

mutate missing-description D5 'del(.panels[] | select(.id == 2) | .description)'
mutate empty-description D5 '(.panels[] | select(.id == 3) | .description) = ""'

mutate target-loki D6 '(.panels[] | select(.id == 4) | .targets[0].datasource) = {"type": "loki", "uid": "loki"}'
mutate target-no-datasource D6 'del(.panels[] | select(.id == 5) | .targets[0].datasource)'

mutate bad-promql D7 "$(set_expr 4 'sum by (class (pg_connector_github_queue_depth)')"

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "OK: the dashboard passes D1-D7 and $mutants mutants were each rejected by exactly the intended rule."
