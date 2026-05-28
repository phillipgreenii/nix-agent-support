#!/usr/bin/env bash
# hack-autoclose-completed-mols.sh
#
# ────────────────────────────────────────────────────────────────────────
# THIS SCRIPT SHOULD NOT EXIST. THE GAS CITY ON_CLOSE HOOK SHOULD CASCADE.
# ────────────────────────────────────────────────────────────────────────
#
# In gascity 1.1.0, the bead store's on_close hook
# (.beads/hooks/on_close) calls `gc convoy autoclose <child-id>` after
# any bead closes. That command correctly walks up to the parent
# molecule and closes it when every parent-child sibling is closed.
# Proven on 2026-05-14 in mayor with two test beads:
#
#   bd dep add <child> <parent> --type=parent-child
#   bd close <child>
#     → ✓ Auto-closed completed molecule <parent>
#
# However, **`gc bd close` does NOT trigger the on_close hook**, while
# direct `bd close` does. Both should be equivalent passthroughs.
# Verified with a second identical test using `gc bd close <child>` —
# parent stayed OPEN.
#
# All ten builtin `mol-*` formulas with a "Close work and exit" report
# step instruct the dog to use `gc bd close <work-bead>`:
#   dolt/{mol-dog-doctor,mol-dog-phantom-db,mol-dog-backup,mol-dog-compactor}
#   gastown/{mol-refinery-patrol,mol-witness-patrol,mol-digest-generate}
#   maintenance/{mol-dog-jsonl,mol-dog-reaper,mol-shutdown-dance}
#
# Only `core/mol-polecat-commit` and `dolt/mol-dog-stale-db` escape the
# bug by calling `bd close` directly. (Their parents auto-close fine.)
#
# Consequence: the dog closes its report bead, but the parent molecule
# stays open forever. mol-dog-doctor fires every 5 minutes — that's
# ~288 zombie parents per day on a healthy city. Eventually the
# zombies block their own orders (single-flight gate by formula
# instance) and dispatch stalls — observed on 2026-05-14 when
# mol-dog-doctor was 133 hours overdue and the dog pool sat stopped.
#
# This script enumerates the safe pattern — molecules carrying an
# `order-run:<formula>` label whose every parent-child dependency
# points at a closed issue — and closes them. The `order-run:` label
# is added by the order dispatcher; user-authored molecules (epics,
# ad-hoc parents) don't carry it, so this cannot wedge non-dispatched
# work. "Closed" is interpreted strictly: deferred / in_progress /
# blocked children keep the parent open.
#
# ── Retirement criteria ──────────────────────────────────────────────
# Delete this script and orders/hack-autoclose-completed-mols.toml
# when EITHER of the following lands upstream:
#
#   1) `gc bd close` is made to passthrough the on_close hook chain
#      the same way direct `bd close` does. (Right place for the fix.)
#
#   2) The builtin `mol-*` formulas are updated to either:
#        (a) close the parent molecule explicitly in their report
#            step after closing the work bead, or
#        (b) switch the close call from `gc bd close` to `bd close`.
#
# Once retired, run once with `--dry-run` to confirm no zombies remain
# before removing the order, then `bd close $(this output)` any final
# stragglers and delete the two files.
#
# ── Audit: who/what this script touches ──────────────────────────────
# Reads:  hq.issues, hq.labels, hq.dependencies via dolt sql.
# Writes: `bd close <id> --reason "hack-autoclose ..."` on matches.
# Does not modify formulas, hooks, dolt schema, or env.

set -euo pipefail

DOLT_HOST="${BEADS_DOLT_SERVER_HOST:-127.0.0.1}"
DOLT_PORT="${BEADS_DOLT_SERVER_PORT:-24158}"

# Discovery: one SQL round-trip vs N bd-show calls. Filters:
#   - type=molecule, status=open
#   - has at least one order-run:* label
#   - has at least one parent-child child (i.e., is a real molecule
#     parent, not a childless one)
#   - has NO child whose status is anything other than 'closed'
candidates=$(
  DOLT_CLI_PASSWORD="${BEADS_DOLT_PASSWORD:-}" \
    dolt --host "$DOLT_HOST" --port "$DOLT_PORT" --user root --no-tls \
    sql -q "
    USE hq;
    SELECT i.id
    FROM issues i
    WHERE i.issue_type = 'molecule'
      AND i.status = 'open'
      AND EXISTS (
        SELECT 1 FROM labels l
        WHERE l.issue_id = i.id
          AND l.label LIKE 'order-run:%'
      )
      AND EXISTS (
        SELECT 1 FROM dependencies d
        WHERE d.depends_on_id = i.id
          AND d.type = 'parent-child'
      )
      AND NOT EXISTS (
        SELECT 1 FROM dependencies d
        JOIN issues c ON c.id = d.issue_id
        WHERE d.depends_on_id = i.id
          AND d.type = 'parent-child'
          AND c.status <> 'closed'
      );
  " --result-format=csv 2>/dev/null | tail -n +2 | tr -d '\r'
)

closed=0
failed=0

if [ -n "$candidates" ]; then
  while IFS= read -r id; do
    [ -z "$id" ] && continue
    if bd close "$id" --reason "hack-autoclose: order-run molecule with all children closed; gc bd close in formula report does not trigger on_close auto-close hook" >/dev/null 2>&1; then
      echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) closed $id"
      closed=$((closed + 1))
    else
      echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) failed to close $id" >&2
      failed=$((failed + 1))
    fi
  done <<<"$candidates"
fi

if [ "$closed" -gt 0 ] || [ "$failed" -gt 0 ]; then
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) hack-autoclose-completed-mols: closed=$closed failed=$failed"
fi
