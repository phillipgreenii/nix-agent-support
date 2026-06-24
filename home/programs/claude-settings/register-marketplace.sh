#!/usr/bin/env bash
#
# Register a nix-provided DIRECTORY-source marketplace into Claude Code's
# marketplace registry (~/.claude/plugins/known_marketplaces.json) BEFORE the
# per-plugin install loop runs.
#
# Why this exists:
#   `claude plugin marketplace update` only REFRESHES marketplaces already in
#   the registry; it never REGISTERS a new one. A nix-provided directory
#   marketplace that exists only in settings.json's extraKnownMarketplaces is
#   not in the registry on the first `pn workspace apply`, so
#   `claude plugin install <plugin>@<new-mkt>` fails "Plugin not found" until a
#   manual `marketplace add` or an interactive Claude startup reconciles it.
#   Running `marketplace add <path>` here scans the marketplace into the
#   registry so the subsequent install succeeds on the first apply.
#
# Behavior:
#   - `marketplace add <path>` is idempotent for a directory source already
#     present, but we don't rely on that: if `add` fails (e.g. already
#     registered on a re-apply), fall back to `marketplace update <name>` to
#     refresh it, then swallow any remaining error.
#   - Non-fatal: matches the activation's existing `|| true` style. If claude
#     or the network is unavailable the activation must not break.
#
# Usage:
#   register-marketplace.sh <claude_bin> <marketplace_name> <directory_path>
#
# github-source marketplaces are intentionally NOT handled here — they are left
# to the existing `claude plugin marketplace update` global call + install flow.

set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <claude_bin> <marketplace_name> <directory_path>" >&2
  exit 64
fi

claude_bin="$1"
name="$2"
path="$3"

if "$claude_bin" plugin marketplace add "$path" >/dev/null 2>&1; then
  echo "claude-settings: marketplace $name registered ($path)"
elif "$claude_bin" plugin marketplace update "$name" >/dev/null 2>&1; then
  echo "claude-settings: marketplace $name refreshed"
else
  echo "claude-settings: WARNING marketplace $name add/update skipped ($path)" >&2
fi

exit 0
