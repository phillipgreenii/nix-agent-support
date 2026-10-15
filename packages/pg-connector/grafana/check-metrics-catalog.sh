#!/usr/bin/env bash
# Drift check between the pg-connector-github metric catalog and the Metric* constants of
# the daemon's telemetry package, plus mutant self-tests proving each rule can fail.
#
#   check-metrics-catalog.sh <catalog> <telemetry.go>
#   check-metrics-catalog.sh --self-test <catalog> <telemetry.go>
#
# The catalog has one line per metric, <prometheus_name> <type> <labels>: the name as
# Prometheus sees it (dots replaced by underscores, nothing appended), a type of gauge,
# counter or histogram, and a comma-separated list of label keys or - for none.
#
# Every failure line starts with RULE-Mn so a mutant can assert the exact rule that fired.
#
#   M1  the name set equals the Metric* constants of the Go file, dots replaced by
#       underscores (no extra, no missing, no duplicate)
#   M2  every line has exactly three fields and a type from the closed set
#   M3  no name ends in _total, _bucket, _sum or _count
#   M4  each label key is in the closed set of keys the daemon emits
#   M5  each line's type matches the instrument constructor registered for that constant
#
# Exit codes: 0 consistent (self-test: real inputs pass and every mutant fails with exactly
# its own rule), 1 a rule failed, 2 usage error or unreadable input.
set -euo pipefail

usage() {
  echo "usage: check-metrics-catalog.sh [--self-test] <catalog> <telemetry.go>" >&2
  exit 2
}

selftest=0
if [ "${1:-}" = "--self-test" ]; then
  selftest=1
  shift
fi
[ "$#" -eq 2 ] || usage
catalog="$1"
gofile="$2"
for f in "$catalog" "$gofile"; do
  if [ ! -r "$f" ] || [ ! -f "$f" ]; then
    echo "check-metrics-catalog.sh: cannot read $f" >&2
    exit 2
  fi
done

valid_labels=" bucket purpose group reason class op served_from role result kind origin consumer query "
valid_types=" gauge counter histogram "

# lint <catalog> <telemetry.go>: prints RULE-Mn lines to stderr, returns 1 when any fired.
lint() {
  local cat="$1" go="$2" status=0
  local consts ctors want have line name type labels extra n l

  # "<ConstName> <prometheus_name>" per Metric* constant.
  consts="$(sed -nE 's/^[[:space:]]*(Metric[A-Za-z0-9]+)[[:space:]]*=[[:space:]]*"([^"]*)".*/\1 \2/p' "$go" |
    while read -r c v; do echo "$c ${v//./_}"; done)"
  # "<ConstName> <Constructor>" per registration site.
  ctors="$(sed -nE 's/.*\.(Int64Counter|Float64Counter|Int64Histogram|Float64Histogram|Int64ObservableGauge|Float64ObservableGauge)\((Metric[A-Za-z0-9]+),.*/\2 \1/p' "$go")"

  # M1
  want="$(printf '%s\n' "$consts" | awk 'NF {print $2}' | sort)"
  have="$(awk 'NF {print $1}' "$cat" | sort)"
  if [ "$want" != "$have" ]; then
    echo "RULE-M1: catalog names differ from the Metric* constants of $(basename "$go"):" \
      "$(diff <(printf '%s\n' "$want") <(printf '%s\n' "$have") | grep '^[<>]' | tr '\n' ' ')" >&2
    status=1
  fi

  n=0
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1))
    # shellcheck disable=SC2086 # intentional word splitting into fields
    set -- $line
    # M2
    if [ "$#" -ne 3 ] || [[ ! $line =~ ^[^[:space:]]+\ [^[:space:]]+\ [^[:space:]]+$ ]]; then
      echo "RULE-M2: line $n must have exactly three space-separated fields: $line" >&2
      status=1
      continue
    fi
    name="$1" type="$2" labels="$3"
    if [[ $valid_types != *" $type "* ]]; then
      echo "RULE-M2: line $n has type '$type', not gauge, counter or histogram" >&2
      status=1
    fi
    # M3
    case "$name" in
    *_total | *_bucket | *_sum | *_count)
      echo "RULE-M3: line $n name $name ends in a suffix the translation never appends" >&2
      status=1
      ;;
    esac
    # M4
    if [ "$labels" != "-" ]; then
      while IFS= read -r l; do
        if [[ $valid_labels != *" $l "* ]]; then
          echo "RULE-M4: line $n label key '$l' is not one the daemon emits" >&2
          status=1
        fi
      done < <(printf '%s\n' "$labels" | tr ',' '\n')
    fi
    # M5
    extra="$(printf '%s\n' "$consts" | awk -v n="$name" '$2 == n {print $1; exit}')"
    if [ -n "$extra" ]; then
      want="$(printf '%s\n' "$ctors" | awk -v c="$extra" '$1 == c {print $2; exit}')"
      case "$want" in
      Int64Counter | Float64Counter) want=counter ;;
      Int64Histogram | Float64Histogram) want=histogram ;;
      Int64ObservableGauge | Float64ObservableGauge) want=gauge ;;
      *) want="" ;;
      esac
      if [ -z "$want" ]; then
        echo "RULE-M5: no instrument constructor found for $extra" >&2
        status=1
      elif [ "$want" != "$type" ]; then
        echo "RULE-M5: line $n $name is cataloged as $type but registered as $want" >&2
        status=1
      fi
    fi
  done <"$cat"
  return "$status"
}

if [ "$selftest" -eq 0 ]; then
  lint "$catalog" "$gofile"
  exit $?
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The real inputs must pass, silently.
if ! out="$(lint "$catalog" "$gofile" 2>&1)"; then
  echo "self-test: the real inputs fail:" >&2
  echo "$out" >&2
  exit 1
fi

fail=0
# mutant <rule> <description> <sed-script-for-catalog> <sed-script-for-go>
# An empty script leaves that copy unchanged. The mutant must fail with exactly <rule>.
mutant() {
  local rule="$1" desc="$2" csed="$3" gsed="$4" dir ids
  dir="$(mktemp -d "$work/m.XXXXXX")"
  cat "$catalog" >"$dir/catalog.txt" # not cp: a store input is read-only and cp keeps the mode
  cat "$gofile" >"$dir/telemetry.go"
  if [ -n "$csed" ]; then
    sed -E "$csed" "$catalog" >"$dir/catalog.txt"
    if cmp -s "$catalog" "$dir/catalog.txt"; then
      echo "self-test: mutant $rule ($desc) did not change the catalog" >&2
      fail=1
      return
    fi
  fi
  if [ -n "$gsed" ]; then
    sed -E "$gsed" "$gofile" >"$dir/telemetry.go"
    if cmp -s "$gofile" "$dir/telemetry.go"; then
      echo "self-test: mutant $rule ($desc) did not change the Go file" >&2
      fail=1
      return
    fi
  fi
  if ids="$(lint "$dir/catalog.txt" "$dir/telemetry.go" 2>&1 >/dev/null)"; then
    echo "self-test: mutant $rule ($desc) passed" >&2
    fail=1
    return
  fi
  ids="$(printf '%s\n' "$ids" | grep -o '^RULE-M[0-9]*' | sort -u | tr '\n' ' ')"
  if [ "$ids" != "RULE-$rule " ]; then
    echo "self-test: mutant $rule ($desc) fired '${ids}' instead of only RULE-$rule" >&2
    fail=1
  fi
}

mutant M1 "a catalog line removed" '/^pg_connector_github_store_bytes /d' ''
mutant M2 "a line without its labels field" 's/^(pg_connector_github_queue_depth gauge) class$/\1/' ''
mutant M3 "a name with a _total suffix (catalog and Go constant renamed together)" \
  's/^pg_connector_github_budget_spent /pg_connector_github_budget_spent_total /' \
  's/"pg_connector_github\.budget\.spent"/"pg_connector_github.budget.spent_total"/'
mutant M4 "a label key the daemon never emits" 's/^(pg_connector_github_queue_depth gauge) class$/\1 colour/' ''
mutant M5 "a counter cataloged as a gauge" 's/^(pg_connector_github_budget_spent) counter /\1 gauge /' ''

[ "$fail" -eq 0 ]
