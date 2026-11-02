#!/usr/bin/env bash
# Lint of a pg-connector daemon's Grafana dashboard against the daemon's metric catalog, plus
# mutant self-tests proving each rule can fail. One script serves every connector dashboard
# (pg-connector-github, pg-connector-issue-jira, ...): the connector name, the catalog and the
# topic list are arguments.
#
#   check-dashboard.sh <connector> <dashboard.json> <metrics-catalog.txt> <topics.txt>
#   check-dashboard.sh --self-test <connector> <dashboard.json> <metrics-catalog.txt> <topics.txt>
#
# <connector> is the daemon's name, for example pg-connector-github. The dashboard's uid and title
# are that name, and its metric prefix is the name with - read as _ plus a trailing _
# (pg_connector_github_).
#
# The catalog is <connector>-metrics.txt: one line per metric,
# "<prometheus_name> <type> <labels>", type gauge, counter or histogram. A histogram is listed
# once under its base name; its _bucket, _sum and _count series are implied.
#
# The topic list is <connector>-dashboard-topics.txt: one phrase per line, the subjects the
# dashboard MUST show (rule D4). Blank lines are ignored.
#
# Needs jq and promtool (rule D7). Every failure line starts with RULE-Dn, at most one line per
# rule, so a mutant can assert the exact rule that fired.
#
#   D1  uid and title are the connector name; .time.from, .time.to and .refresh are non-empty
#       strings
#   D2  every <prefix>* token of an expr is a catalogued name once a _bucket, _sum
#       or _count suffix is stripped (a token is a maximal run of [a-z0-9_])
#   D3  every occurrence of a counter name is followed by an optional {...} selector and a
#       [<range>], and its expression contains rate(, irate( or increase(; a _bucket, _sum or
#       _count suffix appears only on a histogram's name, and a histogram's name appears only
#       with one of those suffixes
#   D4  each topic phrase of the topic list is a case-insensitive substring of at least one
#       panel's title, with - and _ read as spaces
#   D5  every panel whose type is not row has a non-empty description
#   D6  every target's datasource is {"type": "prometheus", "uid": "prometheus"}
#   D7  every expr, with the Grafana variables $__rate_interval, $__interval, $__range and any
#       other $variable replaced by 5m, parses under promtool check rules
#
#   D8  every occurrence of a gauge name (catalog type gauge) sits directly inside max(...),
#       max by (...) (...), min or avg, so a restart overlap (several series per label set, one
#       per service_version) cannot be double counted by a bare selector or a sum
#
# Panels are collected recursively, so a panel nested under a row counts.
#
# Exit codes: 0 clean (self-test: the real inputs pass and every mutant fails with exactly its
# own rule), 1 a rule failed, 2 usage error or unreadable input or a missing tool.
# jq and sed programs are single-quoted on purpose: their $names are jq or regex syntax, not shell.
# shellcheck disable=SC2016
set -euo pipefail

usage() {
  echo "usage: check-dashboard.sh [--self-test] <connector> <dashboard.json> <metrics-catalog.txt> <topics.txt>" >&2
  exit 2
}

selftest=0
if [ "${1:-}" = "--self-test" ]; then
  selftest=1
  shift
fi
[ "$#" -eq 4 ] || usage
connector="$1"
dash="$2"
catalog="$3"
topicfile="$4"
if ! [[ $connector =~ ^[a-z][a-z0-9]*(-[a-z0-9]+)*$ ]]; then
  echo "check-dashboard.sh: connector '$connector' must be lower-case words joined by -" >&2
  exit 2
fi
prefix="${connector//-/_}_"
for f in "$dash" "$catalog" "$topicfile"; do
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

topics="$(jq -R -s -c '[split("\n")[] | select(length > 0)]' "$topicfile")"
if ! jq -e 'length > 0' <<<"$topics" >/dev/null 2>&1; then
  echo "check-dashboard.sh: $topicfile lists no topic" >&2
  exit 2
fi

# jq definitions shared by the rules. The prefix is [a-z0-9_] only (checked above), so splicing it
# into the program is safe.
defs='
def panels: (.panels[]? | ., panels);
def exprs: [.. | objects | select(has("expr")) | .expr];
def tokens: [scan("(?<![a-z0-9_])'"$prefix"'[a-z0-9_]+")];
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

  rule D1 "uid and title must be $connector, and time.from, time.to and refresh non-empty strings" \
    --arg connector "$connector" \
    '.uid == $connector and .title == $connector
     and (.time.from | type == "string" and length > 0)
     and (.time.to | type == "string" and length > 0)
     and (.refresh | type == "string" and length > 0)' "$d"

  rule D2 "a query uses a ${prefix}* token that is not a catalogued name (after stripping a histogram suffix)" \
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

  rule D8 'a gauge is selected bare or under sum or count: wrap it in max, max by, min or avg' \
    --argjson cat "$cat" \
    "$defs"'($cat | map(select(.type == "gauge") | .name)) as $gauges
     | exprs | length > 0 and all(.[]; . as $e
       | all($gauges[]; . as $n
           | ([$e | scan("(?<![a-z0-9_])" + $n + "(?![a-z0-9_])")] | length)
             == ([$e | scan("(max|min|avg)(\\s+by\\s*\\([^)]*\\))?\\s*\\(\\s*" + $n + "(?![a-z0-9_])(\\{[^}]*\\})?\\s*\\)")] | length)))' "$d"

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

# The position-based mutants below need five leading panels that each carry a target.
if ! jq -e '(.panels | length) >= 5 and all(.panels[:5][]; (.targets // []) | length > 0)' "$sdash" >/dev/null; then
  echo "self-test: the first five panels of the dashboard must each carry a target (the mutants address panels by position)" >&2
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

# set_expr <panel index> <expr>: replace the expr of the first target of the panel at that index.
# The mutants address panels by position, so a dashboard MUST open with five panels that each
# carry a target (the stat panels come last).
set_expr() {
  printf '(.panels[%s].targets[0].expr) = %s' "$1" "$(jq -n --arg e "$2" '$e')"
}

# The first counter of the catalog that a dashboard query uses: the counter mutants rewrite a
# query around it.
counter=""
while read -r name type _; do
  if [ "$type" = "counter" ] &&
    jq -e --arg n "$name" '[.. | objects | select(has("expr")) | .expr] | any(.[]; contains($n))' "$sdash" >/dev/null; then
    counter="$name"
    break
  fi
done <"$scat"
if [ -z "$counter" ]; then
  echo "self-test: the dashboard uses no counter of the catalog, so the counter mutants cannot be built" >&2
  exit 1
fi
queue_depth="${prefix}queue_depth"

mutate wrong-uid D1 ".uid = \"${connector}-x\""
mutate wrong-title D1 '.title = "pg connector"'
mutate empty-time-from D1 '.time.from = ""'
mutate missing-refresh D1 'del(.refresh)'

mutate unknown-metric D2 '(.. | objects | select(has("expr")) | .expr) |= gsub("queue_depth"; "queue_depthz")'
mutate_catalog dropped-catalog-line D2 "/^${prefix}query_last_success_age_seconds /d"

mutate counter-outside-rate D3 "$(set_expr 0 "sum(${counter})")"
mutate counter-without-range D3 "$(set_expr 0 "sum(${counter}) + sum(increase(${counter}[1h]))")"
mutate suffix-on-gauge D3 '(.. | objects | select(has("expr")) | .expr) |= gsub("queue_depth"; "queue_depth_sum")'
mutate histogram-without-suffix D3 '(.. | objects | select(has("expr")) | .expr) |= gsub("request_duration_count"; "request_duration")'
mutate_catalog counter-retyped D3 "s/^(${queue_depth}) gauge/\\1 counter/"

# One mutant per topic of the list: rename every panel whose title names it, so D4 must fire.
# A topic that no title names fails the real inputs first, so each mutant really removes a panel.
topic_no=0
while read -r topic; do
  topic_no=$((topic_no + 1))
  phrase="$(jq -n -r --arg t "$topic" '$t | ascii_downcase | gsub("[-_]"; " ")')"
  mutate "drop-topic-${topic_no}" D4 "(.panels[] | select((.title // \"\" | ascii_downcase | gsub(\"[-_]\"; \" \")) | contains($(jq -n --arg p "$phrase" '$p'))) | .title) = \"Renamed panel\""
done < <(jq -r '.[]' <<<"$topics")

mutate missing-description D5 'del(.panels[1].description)'
mutate empty-description D5 '.panels[2].description = ""'

mutate target-loki D6 '.panels[3].targets[0].datasource = {"type": "loki", "uid": "loki"}'
mutate target-no-datasource D6 'del(.panels[4].targets[0].datasource)'

mutate gauge-under-sum D8 "$(set_expr 3 "sum by (class) (${queue_depth})")"
mutate bad-promql D7 "$(set_expr 3 "max by (class) (${queue_depth}))")"

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "OK: the $connector dashboard passes D1-D8 and $mutants mutants were each rejected by exactly the intended rule."
