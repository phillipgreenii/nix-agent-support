# shellcheck shell=bash
#
# Delete exact-string entries from `.permissions.allow` of a Claude Code
# settings file. The entries to delete come from a JSON array of strings in a
# separate file; they are only ever passed to jq with --slurpfile, never
# interpolated into a jq program. All other keys, and the order of the
# remaining allow entries, are preserved.
#
# When at least one entry would be removed (and --dry-run is not given) the
# settings file is first copied to <settings_path>.pre-allow-prune.<UTC ts>.
# The write goes through a temp file in the same directory plus `mv -f`.
#
# Output (stdout): `removed=<n> not-found=<m>` followed by one
# `removed: <entry>` line per removed entry. Idempotent: a second run prints
# `removed=0`. Exit 2 on invalid JSON in either file (settings untouched).
#
# Usage:
#   claude-settings-prune-allow.sh <settings_path> <removal_json_file> [--dry-run]

usage="usage: $0 <settings_path> <removal_json_file> [--dry-run]"

dry_run=0
positional=()
for arg in "$@"; do
  case "$arg" in
  --dry-run) dry_run=1 ;;
  *) positional+=("$arg") ;;
  esac
done

if [ "${#positional[@]}" -ne 2 ]; then
  echo "$usage" >&2
  exit 64
fi

settings_path="${positional[0]}"
removal_file="${positional[1]}"

if ! jq -e 'type == "object"' "$settings_path" >/dev/null 2>&1; then
  act_fail "settings file is missing or not a valid JSON object: $settings_path" >&2
  exit 2
fi
if ! jq -e 'type == "array" and all(.[]; type == "string")' "$removal_file" >/dev/null 2>&1; then
  act_fail "removal file is not a valid JSON array of strings: $removal_file" >&2
  exit 2
fi
if ! jq -e '(.permissions.allow // []) | type == "array"' "$settings_path" >/dev/null 2>&1; then
  act_fail ".permissions.allow is not an array in: $settings_path" >&2
  exit 2
fi

# Entries (deduplicated) that are present in .permissions.allow.
removed_entries=$(jq -r --slurpfile rm "$removal_file" '
  (.permissions.allow // []) as $a
  | $rm[0] | unique[]
  | select(. as $e | any($a[]; . == $e))
' "$settings_path")

total=$(jq -r --slurpfile rm "$removal_file" -n '$rm[0] | unique | length')
removed=$(jq -r --slurpfile rm "$removal_file" '
  (.permissions.allow // []) as $a
  | [$rm[0] | unique[] | select(. as $e | any($a[]; . == $e))] | length
' "$settings_path")
not_found=$((total - removed))

echo "removed=$removed not-found=$not_found"
if [ "$removed" -gt 0 ]; then
  while IFS= read -r entry; do
    printf 'removed: %s\n' "$entry"
  done <<<"$removed_entries"
fi

if [ "$removed" -eq 0 ] || [ "$dry_run" -eq 1 ]; then
  exit 0
fi

ts=$(date -u +%Y%m%dT%H%M%SZ)
cp -p "$settings_path" "$settings_path.pre-allow-prune.$ts"

tmp_path="$settings_path.tmp.$$"
jq --slurpfile rm "$removal_file" '
  if (.permissions | type) == "object" and (.permissions.allow | type) == "array"
  then .permissions.allow |= map(select(. as $e | any($rm[0][]; . == $e) | not))
  else .
  end
' "$settings_path" >"$tmp_path"
mv -f "$tmp_path" "$settings_path"
