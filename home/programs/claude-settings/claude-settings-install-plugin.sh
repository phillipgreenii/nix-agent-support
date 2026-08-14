# shellcheck shell=bash
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
# On a GENUINE failure (install fails AND the fallback update also fails) the
# warning additionally surfaces diagnostic context — the target scope, the
# `installed_plugins.json` entries recorded for the spec (scope + version),
# and the cached version dirs present under the plugin's cache — so the empty
# `Failed to clone repository:` failures (pg2-oklb) are debuggable. The normal
# install-fails-then-update-SUCCEEDS path stays QUIET (no warning).
#
# Independently of install success/failure, warns about a STALE non-user-scope
# entry for the spec (e.g. a `project`-scope entry whose `projectPath` no
# longer exists, like a removed external volume) while the plugin is enabled at
# user scope. Such an entry shadows the user-scope enable and is a common cause
# of confusing install behavior. Pruning of the stale entry is GATED behind the
# CLAUDE_SETTINGS_PRUNE_STALE_SCOPE env var (unset/empty = warn only) so the
# default activation path never mutates installed_plugins.json. The trailing
# update is NEVER skipped on account of a cached/enabled plugin — see pg2-cxwj.
#
# ENABLEMENT IS RE-ASSERTED LAST, when the optional <settings_path> +
# <declared_enabled> pair is supplied. `claude plugin install --scope <scope>`
# writes `.enabledPlugins["<spec>"] = true` into that scope's settings.json on
# EVERY successful invocation, so the installer is the LAST writer of enablement
# during activation and it silently overrides the value
# claude-settings-replace-managed-keys wrote from Nix EARLIER IN THE SAME
# activation (bead pg2-4q1qk). Measured 2026-08-13 in an isolated
# CLAUDE_CONFIG_DIR against BOTH claude binaries present (2.1.220, the
# nix-pinned one, and 2.1.228): starting from a declared `false`, `plugin
# install` flipped the key to `true` on a FRESH install, on an
# ALREADY-INSTALLED SAME-VERSION install (the "short-circuit" — so that path is
# NOT safe either), and on a REAL version bump. `plugin update` never touched
# enablement in any of those states, including a real 1.0.0 -> 2.0.0 upgrade.
# There is no install-without-enabling flag to prefer instead: `plugin install`
# accepts only --config and --scope (claude 2.1.228 `plugin install --help`).
#
# So the ONLY way a Nix-declared `false` survives an apply is to restore it
# after the install/update pair, which is what happens here — in the step that
# causes the drift rather than in a later step, so the guarantee does not depend
# on activation-block ordering that nothing enforces. The restore runs on every
# outcome (install ok, fallback update ok, both failed): the Nix declaration is
# authoritative regardless of how the CLI fared. It writes ONLY
# `.enabledPlugins["<spec>"]` — no other key, and never
# `extraKnownMarketplaces`, so it cannot fight
# claude-settings-register-marketplace (whose `marketplace add` writes
# `extraKnownMarketplaces` into the same file). It is idempotent, and stays
# silent unless it actually changed the value.
#
# Usage:
#   claude-settings-install-plugin.sh <claude_bin> <plugin@marketplace> <cache_root>
#   claude-settings-install-plugin.sh <claude_bin> <plugin@marketplace> <cache_root> \
#     <settings_path> <declared_enabled>
#
# The pair is optional because a plugin MAY be listed for install with no
# `enabledPlugins` declaration at all, in which case there is no declared value
# to assert and Claude Code's own default stands. 4 arguments is rejected rather
# than treated as "settings path, unspecified value": a half-supplied pair is a
# caller bug, and silently skipping the restore is the defect this exists to
# prevent.
#
# Cache layout assumed:
#   <cache_root>/<marketplace>/<plugin>/<version>/.claude-plugin/plugin.json
#
# installed_plugins.json is assumed to live beside the cache dir:
#   <cache_root>/../installed_plugins.json

_usage() {
  echo "usage: $0 <claude_bin> <plugin@marketplace> <cache_root> [<settings_path> <declared_enabled>]" >&2
}

case "$#" in
3)
  settings_path=""
  declared_enabled=""
  ;;
5)
  settings_path="$4"
  declared_enabled="$5"
  ;;
*)
  _usage
  exit 64
  ;;
esac

# Keyed on settings_path, not on declared_enabled: an EMPTY declared value must
# also be rejected, or the restore below would hand jq invalid JSON and degrade
# into a warning instead of naming the caller bug.
if [ -n "$settings_path" ] && [ "$declared_enabled" != "true" ] && [ "$declared_enabled" != "false" ]; then
  echo "$0: <declared_enabled> must be 'true' or 'false', got '$declared_enabled'" >&2
  _usage
  exit 64
fi

claude_bin="$1"
spec="$2"
cache_root="$3"

scope="user"

plugin="${spec%@*}"
marketplace="${spec##*@}"

plugin_cache="$cache_root/$marketplace/$plugin"
installed_plugins="$(dirname "$cache_root")/installed_plugins.json"

if [ -d "$plugin_cache" ]; then
  for ver_dir in "$plugin_cache"/*/; do
    [ -d "$ver_dir" ] || continue
    manifest="$ver_dir.claude-plugin/plugin.json"
    if [ -f "$manifest" ]; then
      if ! jq -e '.name' <"$manifest" >/dev/null 2>&1; then
        act_warn "WARNING corrupt manifest at $manifest — removing $ver_dir" >&2
        rm -rf "$ver_dir"
      fi
    fi
  done
fi

# Detect a stale NON-target-scope entry for the spec: an entry whose scope is
# not the target scope and whose recorded projectPath no longer exists on disk.
# Such an entry shadows the target (user) scope enable and is a frequent source
# of confusing install/update behavior. Warn always; prune only when explicitly
# opted in via CLAUDE_SETTINGS_PRUNE_STALE_SCOPE. This NEVER short-circuits the
# install/update below — `plugin install` does not pull newer marketplace
# versions, so the trailing update must still run to apply version bumps
# (pg2-cxwj). jq emits every candidate (non-target-scope, projectPath set);
# the dead-path test is done in bash so no filesystem logic leaks into jq.
if [ -f "$installed_plugins" ]; then
  candidates=$(jq -r \
    --arg spec "$spec" \
    --arg scope "$scope" '
    (.plugins[$spec] // [])
    | map(select(.scope != $scope and (.projectPath // null) != null))
    | .[]
    | "\(.scope)\t\(.projectPath)\t\(.version // "?")"
  ' "$installed_plugins" 2>/dev/null || true)

  if [ -n "$candidates" ]; then
    while IFS=$'\t' read -r entry_scope entry_path entry_ver; do
      [ -n "$entry_path" ] || continue
      # Only a DEAD projectPath counts as stale.
      [ -e "$entry_path" ] && continue
      act_warn "WARNING $spec has a stale $entry_scope-scope entry (version $entry_ver) for dead path $entry_path — it shadows the $scope-scope enable" >&2
      if [ -n "${CLAUDE_SETTINGS_PRUNE_STALE_SCOPE:-}" ]; then
        if jq \
          --arg spec "$spec" \
          --arg scope "$entry_scope" \
          --arg path "$entry_path" '
          .plugins[$spec] |= (
            map(select((.scope == $scope and (.projectPath // "") == $path) | not))
          )
        ' "$installed_plugins" >"$installed_plugins.tmp" 2>/dev/null; then
          mv -f "$installed_plugins.tmp" "$installed_plugins"
          act_ok "pruned stale $entry_scope-scope entry for $spec ($entry_path)" >&2
        else
          rm -f "$installed_plugins.tmp"
          act_warn "WARNING failed to prune stale entry for $spec (non-fatal)" >&2
        fi
      fi
    done <<<"$candidates"
  fi
fi

install_out=$(mktemp)
update_out=$(mktemp)
trap 'rm -f "$install_out" "$update_out"' EXIT

# Emit the install/update failure context for a GENUINE failure (both commands
# failed): target scope, the installed_plugins.json entries recorded for the
# spec (scope + version), and the cached version dirs present. Only called from
# the both-fail branch, so the install-fails-then-update-SUCCEEDS path stays
# quiet.
_emit_failure_context() {
  echo "--- context ---" >&2
  echo "target scope: $scope" >&2

  echo "installed_plugins.json entries for $spec:" >&2
  if [ -f "$installed_plugins" ]; then
    local entries
    entries=$(jq -r \
      --arg spec "$spec" '
      (.plugins[$spec] // [])
      | if length == 0 then "  (none)"
        else .[] | "  scope=\(.scope) version=\(.version // "?")\(if .projectPath then " projectPath=\(.projectPath)" else "" end)"
        end
    ' "$installed_plugins" 2>/dev/null || true)
    if [ -n "$entries" ]; then
      echo "$entries" >&2
    else
      echo "  (could not parse $installed_plugins)" >&2
    fi
  else
    echo "  (no installed_plugins.json at $installed_plugins)" >&2
  fi

  echo "cached version dirs under $plugin_cache:" >&2
  if [ -d "$plugin_cache" ]; then
    local found=""
    for ver_dir in "$plugin_cache"/*/; do
      [ -d "$ver_dir" ] || continue
      echo "  $(basename "$ver_dir")" >&2
      found=1
    done
    [ -n "$found" ] || echo "  (none)" >&2
  else
    echo "  (no cache dir)" >&2
  fi
}

if "$claude_bin" plugin install "$spec" --scope "$scope" >"$install_out" 2>&1; then
  act_ok "$spec installed"
  # `install` is a no-op for an already-installed plugin and does NOT pull a
  # newer marketplace version, so always follow with an (idempotent) update.
  # Non-fatal: a failed post-install update leaves the installed copy in place.
  if "$claude_bin" plugin update "$spec" --scope "$scope" >"$update_out" 2>&1; then
    act_ok "$spec updated"
  else
    act_warn "WARNING $spec post-install update failed (non-fatal)" >&2
    cat "$update_out" >&2
  fi
elif "$claude_bin" plugin update "$spec" --scope "$scope" >"$update_out" 2>&1; then
  act_ok "$spec updated"
else
  act_warn "WARNING $spec install/update failed" >&2
  echo "--- install output ---" >&2
  cat "$install_out" >&2
  echo "--- update output ---" >&2
  cat "$update_out" >&2
  _emit_failure_context
fi

# Re-assert the Nix-declared enablement for THIS spec, after both CLI commands.
# See the "ENABLEMENT IS RE-ASSERTED LAST" note in the header for the measured
# `plugin install` behavior this undoes (pg2-4q1qk). Non-fatal throughout: a
# failure here must not fail activation, and it is reported so a silent no-op is
# impossible to mistake for success.
if [ -n "$settings_path" ]; then
  if [ -f "$settings_path" ]; then
    # "unset" distinguishes an absent key from a present `false`; both differ
    # from a declared "true", so both are corrected.
    current=$(jq -r \
      --arg s "$spec" \
      '.enabledPlugins[$s] | if . == null then "unset" else tostring end' \
      "$settings_path" 2>/dev/null || echo "unreadable")

    if [ "$current" = "$declared_enabled" ]; then
      : # Already the declared value — nothing to write, nothing to report.
    elif jq \
      --arg s "$spec" \
      --argjson v "$declared_enabled" \
      '.enabledPlugins[$s] = $v' \
      "$settings_path" >"$settings_path.tmp" 2>/dev/null; then
      mv -f "$settings_path.tmp" "$settings_path"
      act_ok "$spec enablement restored to $declared_enabled at $scope scope (was $current)"
    else
      rm -f "$settings_path.tmp"
      act_warn "WARNING $spec enablement could not be restored to $declared_enabled in $settings_path (non-fatal)" >&2
    fi
  else
    act_warn "WARNING $spec enablement not restored: no settings file at $settings_path (non-fatal)" >&2
  fi
fi
