# shellcheck shell=bash
#
# Garbage-collect stale plugin version directories under
# ~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/.
#
# WHY THIS EXISTS (pg2-x3a3t): the local nix-built marketplaces (ADR 0017)
# version each plugin as `<semver>-<hash>`, so every activation that changes a
# plugin's content mints a fresh version directory and NOTHING ever collects
# the previous one. `claude plugin prune` does not help — it removes only
# auto-installed *dependencies*, never cached plugin versions. Growth is
# unbounded and proportional to plugin edit frequency.
#
# RETENTION RULE — primary test, nix-store reachability:
# For each <marketplace>/<plugin> pair, if a LOCAL nix-built marketplace under
# <marketplaces_root>/<marketplace>/<plugin>/.claude-plugin/plugin.json exists,
# its `.version` field names the currently-declared version (e.g.
# "1.0.0+990632f6"); the corresponding cache directory name substitutes the
# first "+" with "-" (e.g. "1.0.0-990632f6", matching what Claude Code writes
# on disk). That single directory is kept; every sibling version directory is
# a removal candidate.
#
# FALLBACK RULE — when reachability cannot be determined:
# If no local marketplace manifest exists for a <marketplace>/<plugin> pair
# (a non-local marketplace such as superpowers-marketplace/beads-marketplace,
# whose versions are upstream semvers rather than content hashes; or a plugin
# no longer present in the current marketplace build), the newest 2 version
# directories BY MTIME are kept and the rest become removal candidates. Two,
# not one, so a rollback to the immediately-previous generation still hits
# cache. NOTE: a previous home-manager generation's marketplace may still be a
# live GC root even though it is invisible to the primary test (only the
# CURRENT generation's marketplace is read) — a rollback would then
# re-materialize a plugin this pass considered indeterminate. That is an
# accepted cost of the versions-only, current-generation-only scope; a
# rollback re-populates cache exactly as a fresh install would.
#
# MUST NOT remove, regardless of the above:
#   - any directory listed as an `installPath` in installed_plugins.json
#     (absolute-path match against every "installPath" value in the doc)
#   - any directory holding a live `.in_use` lock file — concurrent Claude
#     Code sessions create/hold these while a version is loaded (see pg2-5t1w,
#     where such a lock blocked a manual reinstall). The check is re-run
#     immediately before each `rm -rf` to shrink the window between the
#     candidate scan and the removal.
#
# OUT OF SCOPE (deliberately): whole orphaned PLUGIN directories (every
# version of a plugin no longer declared anywhere) are treated the same as any
# other indeterminate plugin — newest-2-by-mtime — rather than removed
# outright. Removing an entire plugin is a different risk class from trimming
# its old versions; that stays a deliberate `claude plugin uninstall` (which,
# as measured for pg2-x3a3t, does not itself reclaim cache).
#
# Usage:
#   claude-settings-gc-plugin-cache.sh [--dry-run] <cache_root> <marketplaces_root>
#
# <cache_root>        ~/.claude/plugins/cache
# <marketplaces_root> ~/.local/share/pgii-marketplaces (local nix-built
#                      marketplaces; see home/programs/claude-marketplaces)
#
# installed_plugins.json is assumed to live beside the cache dir, matching
# claude-settings-install-plugin.sh's convention:
#   <cache_root>/../installed_plugins.json
#
# --dry-run lists every removal candidate and the total reclaimable size,
# removes nothing, and reports skips exactly as a real run would.
#
# Always exits 0: this is a best-effort activation step and must never fail
# the activation (matching the non-fatal style of the sibling scripts), except
# on a caller usage error (exit 64).

_usage() {
  echo "usage: $0 [--dry-run] <cache_root> <marketplaces_root>" >&2
}

dry_run=0
while [ "$#" -gt 0 ]; do
  case "$1" in
  --dry-run)
    dry_run=1
    shift
    ;;
  --)
    shift
    break
    ;;
  -*)
    echo "$0: unknown option: $1" >&2
    _usage
    exit 64
    ;;
  *)
    break
    ;;
  esac
done

if [ "$#" -ne 2 ]; then
  _usage
  exit 64
fi

cache_root="$1"
marketplaces_root="$2"
installed_plugins="$(dirname "$cache_root")/installed_plugins.json"

# Portable mtime: try GNU stat syntax, fall back to BSD (macOS) syntax. Do NOT
# rely on runtimeDeps PATH ordering to pick a particular `stat` — runtimeDeps
# are appended (--suffix PATH), so an ambient stat earlier in PATH wins
# regardless of which coreutils this script declares.
_mtime() {
  stat -c '%Y' "$1" 2>/dev/null || stat -f '%m' "$1" 2>/dev/null || echo 0
}

# Directory size in whole KB; both GNU and BSD `du` accept -s and -k. Always
# exits 0 (the trailing `|| true` absorbs a pipefail-propagated `du` error,
# e.g. a concurrent removal) so a failure here can never abort under `set -e`;
# callers fall back to 0 on empty output.
_dir_size_kb() {
  du -sk "$1" 2>/dev/null | cut -f1 || true
}

# ---------------------------------------------------------------------------
# 1. installed_plugins.json installPath values — NEVER removed, any rule.
# ---------------------------------------------------------------------------
declare -A protected_paths=()
protected_list=""
if [ -f "$installed_plugins" ]; then
  protected_list="$(jq -r '.. | .installPath? // empty' "$installed_plugins" 2>/dev/null || true)"
fi
if [ -n "$protected_list" ]; then
  while IFS= read -r p; do
    [ -n "$p" ] || continue
    protected_paths["$p"]=1
  done <<<"$protected_list"
fi

# ---------------------------------------------------------------------------
# 2. Live version per <marketplace>/<plugin>, from local nix-built
#    marketplaces (primary reachability test).
# ---------------------------------------------------------------------------
declare -A live_version=() # key "<marketplace>/<plugin>" -> version dir name
if [ -d "$marketplaces_root" ]; then
  for mkt_dir in "$marketplaces_root"/*/; do
    [ -d "$mkt_dir" ] || continue
    mkt="$(basename "$mkt_dir")"
    for plugin_dir in "$mkt_dir"*/; do
      [ -d "$plugin_dir" ] || continue
      manifest="${plugin_dir}.claude-plugin/plugin.json"
      [ -f "$manifest" ] || continue
      plugin="$(basename "$plugin_dir")"
      version="$(jq -r '.version // empty' "$manifest" 2>/dev/null || true)"
      [ -n "$version" ] || continue
      # nix content-hash versions are "<semver>+<hash>" in plugin.json; the
      # cache directory Claude Code writes uses "-" in place of the first "+".
      live_version["$mkt/$plugin"]="${version/+/-}"
    done
  done
fi

# ---------------------------------------------------------------------------
# 3. Sweep the cache, applying (primary rule || fallback) minus the two
#    unconditional protections above.
# ---------------------------------------------------------------------------
reclaimed_kb=0
removed_count=0
skipped_inuse=0

_gc_plugin() {
  local mkt="$1" plugin="$2"
  # Strip a trailing slash: callers pass a `for … in "$dir"*/` glob match,
  # which retains one, and `$plugin_cache/$v` below would otherwise double it
  # — breaking the exact-string match against installPath (pg2-x3a3t).
  local plugin_cache="${3%/}"
  local key="$mkt/$plugin"
  local -a versions=()
  local v
  for v in "$plugin_cache"/*/; do
    [ -d "$v" ] || continue
    versions+=("$(basename "$v")")
  done
  [ "${#versions[@]}" -gt 0 ] || return 0

  local -A keep=()
  if [ -n "${live_version[$key]:-}" ]; then
    keep["${live_version[$key]}"]=1
  else
    local by_mtime v2
    by_mtime="$(
      for v2 in "${versions[@]}"; do
        printf '%s\t%s\n' "$(_mtime "$plugin_cache/$v2")" "$v2"
      done | sort -t $'\t' -k1,1 -rn | head -2 | cut -f2
    )"
    while IFS= read -r v2; do
      [ -n "$v2" ] || continue
      keep["$v2"]=1
    done <<<"$by_mtime"
  fi

  for v in "${versions[@]}"; do
    [ -n "${keep[$v]:-}" ] && continue
    local verdir="$plugin_cache/$v"
    [ -n "${protected_paths[$verdir]:-}" ] && continue

    if [ -e "$verdir/.in_use" ]; then
      act_warn "WARNING skipping $mkt/$plugin/$v: held by a live .in_use lock" >&2
      skipped_inuse=$((skipped_inuse + 1))
      continue
    fi

    local size_kb
    size_kb="$(_dir_size_kb "$verdir")"
    size_kb="${size_kb:-0}"

    if [ "$dry_run" = 1 ]; then
      act_info "would remove $mkt/$plugin/$v (${size_kb} KB)"
    else
      # Re-check immediately before removal to shrink the TOCTOU window
      # against a session that just started using this version.
      if [ -e "$verdir/.in_use" ]; then
        act_warn "WARNING skipping $mkt/$plugin/$v: held by a live .in_use lock" >&2
        skipped_inuse=$((skipped_inuse + 1))
        continue
      fi
      rm -rf "$verdir"
      act_ok "removed $mkt/$plugin/$v (${size_kb} KB)"
    fi
    reclaimed_kb=$((reclaimed_kb + size_kb))
    removed_count=$((removed_count + 1))
  done
}

if [ -d "$cache_root" ]; then
  for mkt_dir in "$cache_root"/*/; do
    [ -d "$mkt_dir" ] || continue
    mkt="$(basename "$mkt_dir")"
    for plugin_dir in "$mkt_dir"*/; do
      [ -d "$plugin_dir" ] || continue
      plugin="$(basename "$plugin_dir")"
      _gc_plugin "$mkt" "$plugin" "$plugin_dir"
    done
  done
fi

reclaimed_bytes=$((reclaimed_kb * 1024))
if [ "$dry_run" = 1 ]; then
  act_info "dry-run: would reclaim ${reclaimed_bytes} bytes across $removed_count version dir(s)"
elif [ "$removed_count" -gt 0 ]; then
  act_ok "plugin cache GC: reclaimed ${reclaimed_bytes} bytes across $removed_count version dir(s)"
fi
if [ "$skipped_inuse" -gt 0 ]; then
  act_warn "WARNING plugin cache GC: skipped $skipped_inuse version dir(s) held by a live .in_use lock" >&2
fi

exit 0
