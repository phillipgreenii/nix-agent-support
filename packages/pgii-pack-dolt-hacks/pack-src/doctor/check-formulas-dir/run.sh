#!/bin/sh
# Doctor check: pgii-dolt-hacks must have a formulas/ directory.
#
# gascity v1.1.0 only adds a pack's formula layer when formulas/ physically
# exists (pack.go:699-705), and cmd_order.go:cityOrderRoots derives the
# pack's orders/ scan path from that formula layer. A pack with only
# orders/ (no formulas/) has its orders SILENTLY dropped from gc order list.
#
# This check fails if the workaround dir was removed. Re-create it in the
# pack source (under phillipgreenii-nix-agent-support):
#   mkdir -p packages/pgii-pack-dolt-hacks/pack-src/formulas
#   touch    packages/pgii-pack-dolt-hacks/pack-src/formulas/.gitkeep
# then rebuild and zn-self-apply.
#
# Retire this check when gascity decouples order discovery from formula
# layer presence.

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
PACK_ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)

if [ ! -d "$PACK_ROOT/formulas" ]; then
  echo "pgii-dolt-hacks/formulas/ missing — orders will be silently dropped"
  echo "Fix: in phillipgreenii-nix-agent-support, mkdir -p packages/pgii-pack-dolt-hacks/pack-src/formulas && touch packages/pgii-pack-dolt-hacks/pack-src/formulas/.gitkeep; then rebuild + zn-self-apply."
  exit 2
fi

echo "pgii-dolt-hacks/formulas/ present"
exit 0
