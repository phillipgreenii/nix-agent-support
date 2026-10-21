# shellcheck shell=bash
#
# Replace the nix-owned autoMode lists (environment, allow, soft_deny) in a
# Claude Code settings.json. For every key PRESENT in the desired JSON file the
# result is ["$defaults"] followed by the desired entries (an empty desired list
# therefore yields exactly ["$defaults"]); a key ABSENT from the desired file is
# not touched. Entries that exist in the settings file but are not in the
# desired list (and are not the literal "$defaults") are saved to a timestamped
# removal file before they are dropped, so nothing is lost silently.
#
# All prose reaches jq through --slurpfile/--arg, never interpolated into the
# jq program, so entries containing ', ", $, \ or * survive byte-for-byte.
#
# Usage:
#   claude-settings-replace-auto-mode.sh <settings_path> <desired_json_file> <removed_dir>
#
# Exit status: 0 success; 2 invalid JSON in either input (settings untouched);
# 64 usage.

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <settings_path> <desired_json_file> <removed_dir>" >&2
  exit 64
fi

settings_path="$1"
desired_file="$2"
removed_dir="$3"

# Validate the desired file: an object whose present owned keys are string arrays.
if ! jq -e '
  type == "object"
  and ([("environment", "allow", "soft_deny") as $k
        | (has($k) | not) or (.[$k] | type == "array" and all(type == "string"))]
       | all)
' "$desired_file" >/dev/null 2>&1; then
  echo "claude-settings-replace-auto-mode: invalid desired JSON in $desired_file" >&2
  exit 2
fi

# Validate the settings file (a missing or empty file is treated as {}).
if [ -s "$settings_path" ]; then
  if ! jq -e 'type == "object"' "$settings_path" >/dev/null 2>&1; then
    echo "claude-settings-replace-auto-mode: invalid JSON in $settings_path" >&2
    exit 2
  fi
  current_file="$settings_path"
else
  current_file=""
fi

mkdir -p "$(dirname "$settings_path")"
mkdir -p "$removed_dir"

work_dir=$(mktemp -d)
cleanup() {
  rm -rf "$work_dir"
  rm -f "$settings_path.tmp"
}
trap cleanup EXIT

if [ -z "$current_file" ]; then
  echo '{}' >"$work_dir/empty.json"
  current_file="$work_dir/empty.json"
fi

removed=$(jq -n \
  --slurpfile cur "$current_file" \
  --slurpfile des "$desired_file" \
  --arg defaults '$defaults' '
  ($des[0]) as $d
  | ($cur[0].autoMode // {}) as $am
  | reduce ("environment", "allow", "soft_deny") as $k ({};
      if ($d | has($k)) then
        ((if ($am[$k] | type) == "array" then $am[$k] else [] end)
          | map(select(. as $e | $e != $defaults and (($d[$k] | any(. == $e)) | not)))) as $x
        | if ($x | length) > 0 then .[$k] = $x else . end
      else . end)
  | if . == {} then empty else {autoMode: .} end
')

jq \
  --slurpfile des "$desired_file" \
  --arg defaults '$defaults' '
  reduce ("environment", "allow", "soft_deny") as $k (.;
    if ($des[0] | has($k)) then
      .autoMode[$k] = ([$defaults] + ($des[0][$k] | map(select(. != $defaults))))
    else . end)
' "$current_file" >"$settings_path.tmp"

# Save dropped entries only once the new settings have been produced, but
# before they replace the old file.
if [ -n "$removed" ]; then
  ts=$(date -u +%Y%m%dT%H%M%SZ)
  removed_file="$removed_dir/.claude-settings-removed-$ts-$$.json"
  echo "$removed" | jq . >"$removed_file"
  act_warn "autoMode entries REMOVED on activation (saved to $removed_file):" >&2
  echo "$removed" | jq . >&2
fi

mv -f "$settings_path.tmp" "$settings_path"
act_ok "autoMode lists replaced in $settings_path" >&2
