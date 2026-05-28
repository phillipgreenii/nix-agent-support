#!/bin/sh
# Doctor check: HACK 11 (mol-dog-jsonl schema-rename wrapper).
#
# HACK 11 retires when upstream `.gc/system/packs/maintenance/
# assets/scripts/jsonl-export.sh` is patched to use `issue_type`
# instead of `type` in its SCRUB_FILTER SQL.
#
# Exit 0 = HACK 11 still needed
# Exit 1 = HACK 11 retirement candidate

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
CITY_ROOT=$(cd "$SCRIPT_DIR/../../../../.." && pwd)
SYS="$CITY_ROOT/.gc/system/packs/maintenance/assets/scripts/jsonl-export.sh"

if [ ! -f "$SYS" ]; then
  echo "upstream jsonl-export.sh missing at $SYS — cannot compare"
  exit 0
fi

if grep -q 'WHERE issue_type NOT IN' "$SYS"; then
  echo "upstream jsonl-export.sh now uses issue_type — HACK 11 retirement candidate"
  echo "  1. Remove [[orders.overrides]] name='mol-dog-jsonl' from city.toml"
  echo "  2. In phillipgreenii-nix-agent-support, delete:"
  echo "       packages/pgii-pack-dolt-hacks/pack-src/orders/hack-mol-dog-jsonl.toml.template"
  echo "       packages/pgii-pack-dolt-hacks/pack-src/scripts/hack-mol-dog-jsonl.sh"
  echo "  3. Rebuild + zn-self-apply."
  exit 1
fi

if grep -q 'WHERE type NOT IN' "$SYS"; then
  echo 'upstream jsonl-export.sh still uses bare `type` column — HACK 11 still needed'
  exit 0
fi

echo "upstream jsonl-export.sh SCRUB_FILTER changed unexpectedly — review manually"
exit 1
