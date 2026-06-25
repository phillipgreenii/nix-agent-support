#!/usr/bin/env bash
#
# Validate cached manifests for a Claude Code plugin, then install it via the
# supplied claude binary and ALWAYS follow a successful install with an
# (idempotent) update. `plugin install` short-circuits on an already-installed
# plugin and never pulls a newer marketplace version, so without the trailing
# update a content-digest version bump would never take effect (pg2-cxwj). On
# install failure we fall back to update; stderr from install/update is
# surfaced only if both fail. A post-install update failure is non-fatal — the
# already-installed copy is left in place with a warning. Removes cached
# versions with corrupt manifests
# (anything that fails `jq -e '.name'` — i.e. unparseable JSON or a missing
# required `name`) before attempting install/update. `version` is NOT
# required: it is optional in the plugin manifest and lives in the
# marketplace's marketplace.json, so plugins pinned by git ref (e.g.
# caveman) or without a semver legitimately omit it.
#
# Usage:
#   install-plugin.sh <claude_bin> <plugin@marketplace> <cache_root>
#
# Cache layout assumed:
#   <cache_root>/<marketplace>/<plugin>/<version>/.claude-plugin/plugin.json

set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <claude_bin> <plugin@marketplace> <cache_root>" >&2
  exit 64
fi

claude_bin="$1"
spec="$2"
cache_root="$3"

plugin="${spec%@*}"
marketplace="${spec##*@}"

plugin_cache="$cache_root/$marketplace/$plugin"
if [ -d "$plugin_cache" ]; then
  for ver_dir in "$plugin_cache"/*/; do
    [ -d "$ver_dir" ] || continue
    manifest="$ver_dir.claude-plugin/plugin.json"
    if [ -f "$manifest" ]; then
      if ! jq -e '.name' <"$manifest" >/dev/null 2>&1; then
        echo "claude-settings: WARNING corrupt manifest at $manifest — removing $ver_dir" >&2
        rm -rf "$ver_dir"
      fi
    fi
  done
fi

install_out=$(mktemp)
update_out=$(mktemp)
trap 'rm -f "$install_out" "$update_out"' EXIT

if "$claude_bin" plugin install "$spec" --scope user >"$install_out" 2>&1; then
  echo "claude-settings: $spec installed"
  # `install` is a no-op for an already-installed plugin and does NOT pull a
  # newer marketplace version, so always follow with an (idempotent) update.
  # Non-fatal: a failed post-install update leaves the installed copy in place.
  if "$claude_bin" plugin update "$spec" --scope user >"$update_out" 2>&1; then
    echo "claude-settings: $spec updated"
  else
    echo "claude-settings: WARNING $spec post-install update failed (non-fatal)" >&2
    cat "$update_out" >&2
  fi
elif "$claude_bin" plugin update "$spec" --scope user >"$update_out" 2>&1; then
  echo "claude-settings: $spec updated"
else
  echo "claude-settings: WARNING $spec install/update failed" >&2
  echo "--- install output ---" >&2
  cat "$install_out" >&2
  echo "--- update output ---" >&2
  cat "$update_out" >&2
fi
