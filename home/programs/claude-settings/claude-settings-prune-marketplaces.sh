# shellcheck shell=bash
#
# Prune stale DIRECTORY-source marketplace registrations from Claude Code's
# marketplace registry (~/.claude/plugins/known_marketplaces.json).
#
# Why this exists (pg2-rjfti): claude-settings-register-marketplace.sh
# registers/refreshes a directory marketplace declared in nix config, but
# activation had no counterpart for a marketplace REMOVED from nix
# declarations. Once decommissioned, its registry entry lingered forever,
# pointing at a directory that no longer exists, and every subsequent
# `claude plugin marketplace update` logged "N marketplace(s) could not be
# refreshed" / caused the activation's own
# `marketplace update failed (non-fatal)` warning for it (observed 2026-09-29:
# mobilecombackup-marketplace-local, decommissioned in an earlier session, kept
# failing to refresh on every zn-self-apply/zn-self-upgrade thereafter).
#
# Detection mirrors claude-settings-install-plugin.sh's stale-scope-entry
# prune as closely as makes sense: an entry counts as stale only when BOTH
# hold:
#   - it is a DIRECTORY-source entry (source.source == "directory") whose
#     source.path no longer exists on disk. github-source entries are
#     intentionally excluded — matching
#     claude-settings-register-marketplace.sh's own scope (see its header): a
#     github marketplace's installLocation not existing just means "not yet
#     cloned", which is not stale.
#   - its name is NOT present in the currently-declared marketplace name set
#     (i.e. no longer declared anywhere in nix's extraKnownMarketplaces for
#     this generation).
# jq emits every directory-source candidate; the dead-path test is done in
# bash so no filesystem logic leaks into jq (same split as the sibling
# stale-scope prune).
#
# Warn always; prune only when explicitly opted in via
# CLAUDE_SETTINGS_PRUNE_STALE_MARKETPLACES (unset/empty = warn only) — mirrors
# CLAUDE_SETTINGS_PRUNE_STALE_SCOPE's opt-in style so the default activation
# path never mutates known_marketplaces.json. A dedicated env var, distinct
# from CLAUDE_SETTINGS_PRUNE_STALE_SCOPE, because the two prune different
# files for different reasons (an installed_plugins.json wrong-scope entry vs.
# a known_marketplaces.json dead registration) and an operator may want one
# without the other.
#
# Usage:
#   claude-settings-prune-marketplaces.sh <known_marketplaces_path> <declared_names_json>
#
# <declared_names_json> is a JSON array of every marketplace name currently
# declared in nix's extraKnownMarketplaces (any source type) for this
# generation — the full declared set, not just directory-source names, so a
# marketplace whose source TYPE changed (rather than being removed outright)
# is never treated as stale.
#
# Always exits 0 (non-fatal, matching the sibling scripts' style) except on a
# caller usage error (exit 64).

_usage() {
  echo "usage: $0 <known_marketplaces_path> <declared_names_json>" >&2
}

if [ "$#" -ne 2 ]; then
  _usage
  exit 64
fi

known_marketplaces="$1"
declared_names_json="$2"

if ! printf '%s' "$declared_names_json" | jq -e 'type == "array"' >/dev/null 2>&1; then
  echo "$0: <declared_names_json> must be a JSON array, got '$declared_names_json'" >&2
  _usage
  exit 64
fi

[ -f "$known_marketplaces" ] || exit 0

candidates=$(jq -r \
  --argjson declared "$declared_names_json" '
  to_entries
  | map(select(
      (.value.source.source // "") == "directory"
      and (.value.source.path // null) != null
      and (.key as $k | $declared | index($k)) == null
    ))
  | .[]
  | "\(.key)\t\(.value.source.path)"
' "$known_marketplaces" 2>/dev/null || true)

if [ -n "$candidates" ]; then
  while IFS=$'\t' read -r name path; do
    [ -n "$name" ] || continue
    # Only a DEAD path counts as stale.
    [ -e "$path" ] && continue
    act_warn "WARNING marketplace $name is stale (no longer declared, dead path $path)" >&2
    if [ -n "${CLAUDE_SETTINGS_PRUNE_STALE_MARKETPLACES:-}" ]; then
      if jq --arg name "$name" 'del(.[$name])' "$known_marketplaces" >"$known_marketplaces.tmp" 2>/dev/null; then
        mv -f "$known_marketplaces.tmp" "$known_marketplaces"
        act_ok "pruned stale marketplace $name ($path)" >&2
      else
        rm -f "$known_marketplaces.tmp"
        act_warn "WARNING failed to prune stale marketplace $name (non-fatal)" >&2
      fi
    fi
  done <<<"$candidates"
fi

exit 0
