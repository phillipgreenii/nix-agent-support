#!/bin/sh
# Stand-in for the pg-connector-github client (`<bin> --config <file> <instance>`):
# reads ONE request from stdin, logs it, prints a canned answer. Generic
# fixtures only; the collector tests drive it through STANDIN_DIR.
#
# STANDIN_DIR  directory holding the canned answers and the call log.
# TICKS_FILE   when set, every changes_ack request is followed in calls.log by the
#              line count of that file (what the collector had written by then).
#
# calls.log gets one line "<instance> <request JSON>" per call. The answer of the
# first call for an (instance, op, query) is <instance>-<op>-<query>.json, the
# n-th call (n >= 2) reads <instance>-<op>-<query>.<n>.json when that exists and
# otherwise repeats the last file read. An answer holding an "error" exits 7 (the
# daemon's invalid_argument), any other answer exits 0.
dir=${STANDIN_DIR:?STANDIN_DIR is not set}
for instance in "$@"; do :; done
req=$(cat)
op=$(printf '%s' "$req" | jq -r '.op')
query=$(printf '%s' "$req" | jq -r '.args.query')
printf '%s %s\n' "$instance" "$req" >>"$dir/calls.log"
if [ "$op" = changes_ack ] && [ -n "${TICKS_FILE:-}" ]; then
  if [ -f "$TICKS_FILE" ]; then
    wc -l <"$TICKS_FILE" | tr -d ' ' >>"$dir/calls.log"
  else
    echo 0 >>"$dir/calls.log"
  fi
fi
key="$instance-$op-$query"
n=$(($(cat "$dir/.count-$key" 2>/dev/null || echo 0) + 1))
echo "$n" >"$dir/.count-$key"
file="$dir/$key.json"
if [ "$n" -ge 2 ]; then
  if [ -f "$dir/$key.$n.json" ]; then
    file="$dir/$key.$n.json"
  else
    file=$(cat "$dir/.last-$key")
  fi
fi
echo "$file" >"$dir/.last-$key"
if [ ! -f "$file" ]; then
  echo "standin: no canned answer $file" >&2
  exit 9
fi
cat "$file"
if jq -e 'has("error")' "$file" >/dev/null; then exit 7; fi
exit 0
