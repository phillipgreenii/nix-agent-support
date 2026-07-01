# shellcheck shell=bash
#
# Replace `enabledPlugins` and `extraKnownMarketplaces` in a Claude Code
# settings.json with the Nix-declared sets. Any pre-existing entries that
# are not in the new sets are captured to a timestamped JSON file and
# echoed to stderr before the replace happens.
#
# Usage:
#   claude-settings-replace-managed-keys.sh <settings_path> <new_enabled_json> <new_mkts_json> <removed_dir>

if [ "$#" -ne 4 ]; then
  echo "usage: $0 <settings_path> <new_enabled_json> <new_mkts_json> <removed_dir>" >&2
  exit 64
fi

settings_path="$1"
new_enabled_json="$2"
new_mkts_json="$3"
removed_dir="$4"

mkdir -p "$(dirname "$settings_path")"
mkdir -p "$removed_dir"
[ -f "$settings_path" ] || echo '{}' >"$settings_path"

removed=$(jq -n \
  --slurpfile cur "$settings_path" \
  --argjson newEnabled "$new_enabled_json" \
  --argjson newMkts "$new_mkts_json" '
  {
    enabledPlugins:
      (($cur[0].enabledPlugins // {})
        | with_entries(select(.key as $k | ($newEnabled | has($k) | not)))),
    extraKnownMarketplaces:
      (($cur[0].extraKnownMarketplaces // {})
        | with_entries(select(.key as $k | ($newMkts | has($k) | not))))
  }
  | with_entries(select(.value != {}))
')

if [ "$removed" != "{}" ]; then
  ts=$(date -u +%Y%m%dT%H%M%SZ)
  removed_file="$removed_dir/.claude-settings-removed-$ts-$$.json"
  act_warn "REMOVED on activation (saved to $removed_file):" >&2
  echo "$removed" | jq . >&2
  echo "$removed" >"$removed_file"
fi

jq \
  --argjson newEnabled "$new_enabled_json" \
  --argjson newMkts "$new_mkts_json" \
  '.enabledPlugins = $newEnabled | .extraKnownMarketplaces = $newMkts' \
  "$settings_path" >"$settings_path.tmp"
mv -f "$settings_path.tmp" "$settings_path"
