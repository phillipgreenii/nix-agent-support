#!/bin/sh
# Doctor check: pgii-gastown vs the upstream gastown system pack.
#
# pgii-gastown carries a city-customized mayor (~53 lines vs gastown's
# ~237) and a verbatim copy of gastown's deacon/mol-deacon-patrol. The
# pack exists to avoid the all-or-nothing import of the gastown system
# pack (which would also try to manage other agents).
#
# Retire pgii-gastown if/when:
#   - The city's mayor prompt converges with gastown's, AND
#   - The deacon copy stays in sync with upstream, AND
#   - Enabling the gastown system pack via [imports.gastown] doesn't
#     break other city-level customizations.
#
# Exit 0 = still diverged (pack still needed)
# Exit 1 = retirement candidate (consider enabling gastown system pack)

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
PACK_ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)
CITY_ROOT=$(cd "$PACK_ROOT/../../.." && pwd)
GASTOWN="$CITY_ROOT/.gc/system/packs/gastown"

if [ ! -d "$GASTOWN" ]; then
  echo "gastown system pack not found at $GASTOWN — cannot compare"
  exit 1
fi

LOCAL_MAYOR="$PACK_ROOT/agents/mayor/prompt.md"
SYS_MAYOR="$GASTOWN/agents/mayor/prompt.template.md"
LOCAL_DEACON="$PACK_ROOT/agents/deacon/prompt.template.md"
SYS_DEACON="$GASTOWN/agents/deacon/prompt.template.md"
LOCAL_FORMULA="$PACK_ROOT/formulas/mol-deacon-patrol.toml"
SYS_FORMULA="$GASTOWN/formulas/mol-deacon-patrol.toml"

drift=0
for pair in "mayor:$LOCAL_MAYOR:$SYS_MAYOR" "deacon:$LOCAL_DEACON:$SYS_DEACON" "mol-deacon-patrol:$LOCAL_FORMULA:$SYS_FORMULA"; do
  name=$(echo "$pair" | cut -d: -f1)
  local_f=$(echo "$pair" | cut -d: -f2)
  sys_f=$(echo "$pair" | cut -d: -f3)
  if [ ! -f "$local_f" ] || [ ! -f "$sys_f" ]; then
    echo "$name: missing file (local=$local_f sys=$sys_f) — skipping"
    continue
  fi
  if diff -q "$local_f" "$sys_f" >/dev/null 2>&1; then
    echo "$name: CONVERGED with upstream"
  else
    local_n=$(wc -l <"$local_f")
    sys_n=$(wc -l <"$sys_f")
    echo "$name: diverged (local=${local_n}L, system=${sys_n}L)"
    drift=$((drift + 1))
  fi
done

if [ "$drift" -eq 0 ]; then
  echo "All pgii-gastown content matches upstream — retirement candidate"
  exit 1
fi
echo "pgii-gastown still diverged from gastown system pack ($drift item(s))"
exit 0
