#!/usr/bin/env bash
# hack-mol-dog-jsonl.sh — workaround wrapper for the type/issue_type
# schema-rename bug in upstream jsonl-export.sh.
#
# Upstream `.gc/system/packs/maintenance/assets/scripts/jsonl-export.sh`
# (mol-dog-jsonl's executor) builds:
#   SELECT * FROM `<db>`.issues WHERE type NOT IN ('message','event','wisp','agent') AND title NOT LIKE 'gc:%'
# but the `issues` table column is `issue_type`, not `type`. Dolt 1.86.x
# returns "Error 1105: column \"type\" could not be found" for every
# database — discovered 2026-05-19 by pgii-gastown.deacon (mail
# gc-wisp-56k). All 7 db exports fail every cycle; the jsonl-archive
# repo has zero commits since init.
#
# This wrapper sed-patches the script to a tempfile, symlinks its
# sibling dolt-target.sh next to it (the script does `. "$SCRIPT_DIR/
# dolt-target.sh"`), exports PACK_DIR so other system-pack references
# resolve, then execs. The system pack stays untouched.
#
# Retire when:
#   - upstream patches jsonl-export.sh (the doctor check fires), AND
#   - mol-dog-jsonl is re-enabled via city.toml [[orders.overrides]].
set -euo pipefail

SYS_PACK="${GC_CITY:-$HOME/gc}/.gc/system/packs/maintenance"
SYS_SCRIPTS="$SYS_PACK/assets/scripts"
SYS_SCRIPT="$SYS_SCRIPTS/jsonl-export.sh"
SYS_SIBLING="$SYS_SCRIPTS/dolt-target.sh"

if [ ! -f "$SYS_SCRIPT" ]; then
  echo "hack-mol-dog-jsonl: upstream script missing at $SYS_SCRIPT" >&2
  exit 1
fi
if [ ! -f "$SYS_SIBLING" ]; then
  echo "hack-mol-dog-jsonl: upstream dolt-target.sh missing at $SYS_SIBLING" >&2
  exit 1
fi

TMP=$(mktemp -d -t hack-mol-dog-jsonl.XXXXXX)

# Compute the archive repo this run will touch. Mirrors the upstream
# jsonl-export.sh resolution (PACK_STATE_DIR → ${PACK_STATE_DIR}/jsonl-archive).
# Used by the EXIT trap below to clean stale index.lock if we die mid-commit.
CITY="${GC_CITY:-$HOME/gc}"
ARCHIVE_REPO="${GC_JSONL_ARCHIVE_REPO:-${GC_PACK_STATE_DIR:-${GC_CITY_RUNTIME_DIR:-$CITY/.gc/runtime}/packs/pgii-dolt-hacks}/jsonl-archive}"
ARCHIVE_LOCK="$ARCHIVE_REPO/.git/index.lock"

cleanup() {
  local rc=$?
  rm -rf "$TMP"
  # If we're exiting non-zero AND the archive has a stale index.lock that
  # we (or our exec'd child) almost certainly left behind, drop it. Only
  # touch locks unheld by any process and at least 5s old — anything
  # newer/held could be a concurrent fire's in-flight commit we shouldn't
  # disrupt.
  if [ "$rc" -ne 0 ] && [ -f "$ARCHIVE_LOCK" ]; then
    if ! lsof "$ARCHIVE_LOCK" >/dev/null 2>&1; then
      local lock_mtime
      lock_mtime=$(stat -f %m "$ARCHIVE_LOCK" 2>/dev/null || stat -c %Y "$ARCHIVE_LOCK" 2>/dev/null || echo 0)
      local lock_age=$(($(date +%s) - lock_mtime))
      if [ "$lock_age" -ge 5 ]; then
        rm -f "$ARCHIVE_LOCK" &&
          echo "hack-mol-dog-jsonl: cleared stale archive index.lock ($ARCHIVE_LOCK, ${lock_age}s old) after non-zero exit ($rc)" >&2
      fi
    fi
  fi
  exit "$rc"
}
trap cleanup EXIT

# Sibling dependency — symlink so the patched script's
# `. "$SCRIPT_DIR/dolt-target.sh"` resolves.
ln -s "$SYS_SIBLING" "$TMP/dolt-target.sh"

# Patch two things, verbatim otherwise:
#   1. SCRUB_FILTER:  `type` → `issue_type` (the bug deacon found).
#   2. scrub_exported_issues' jq:  for inputs that don't have a `.rows`
#      array (i.e. empty databases — Dolt returns `{}`), emit
#      `{rows: []}` instead of passing the input through. Without this,
#      the downstream validate_exported_issues call rejects `{}` and
#      the script marks every dormant rig as failed.
sed 's/WHERE type NOT IN/WHERE issue_type NOT IN/' "$SYS_SCRIPT" |
  perl -0777 -pe 's/(scrub_exported_issues\(\) \{.*?else\n\s+)\.(\n\s+end)/$1\{rows: \[\]\}$2/s' \
    >"$TMP/jsonl-export.sh"
chmod +x "$TMP/jsonl-export.sh"

export PACK_DIR="$SYS_PACK"
# Run the patched script as a child (not exec) so our EXIT trap can fire
# and clean up any stale lock the child may have left on kill/timeout.
bash "$TMP/jsonl-export.sh" "$@"
