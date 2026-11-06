#!/bin/sh
# Prints the rows file named by the first argument and records its own
# arguments next to it (<rows file>.args, one per line).
rows="$1"
shift
printf '%s\n' "$@" >"$rows.args"
cat "$rows"
