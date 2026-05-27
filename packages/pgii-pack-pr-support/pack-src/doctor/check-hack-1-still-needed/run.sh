#!/bin/sh
# Doctor check: HACK 1 (hack-wake-on-work).
#
# HACK 1 retires when the gascity supervisor reconciler materializes
# (and respawns) on_demand named sessions when their work_query returns
# hits. Currently broken in gc 1.1.0 — on_demand sessions stay
# reserved-unmaterialized even with ready work.
#
# Exit 0 = HACK 1 still needed
# Exit 1 = HACK 1 retirement candidate

gc_version=$(gc version 2>/dev/null | head -1)

# gc dev builds report version as "dev" (no semver). Treat dev as 1.1.x
# for this check — the supervisor reconciler hasn't been fixed yet in
# any released build that I'm aware of.
case "$gc_version" in
1.1.* | dev)
  echo "gc $gc_version: HACK 1 still needed"
  echo "(supervisor reconciler does not materialize on_demand sessions in gc 1.1.x / dev — verified 2026-05-14)"
  exit 0
  ;;
*)
  echo "gc $gc_version: HACK 1 retirement candidate"
  echo "Test before removing:"
  echo '  1. Ensure pr-self-fixer has \>= 1 ready bead in its work_query'
  echo "  2. Stop pr-self-fixer (gc session close <id>)"
  echo "  3. Disable hack-wake-on-work order"
  echo "  4. Wait one supervisor patrol tick (default 30s)"
  echo "  5. If pr-self-fixer auto-materializes, supervisor is fixed"
  echo "  6. If confirmed, delete orders/hack-wake-on-work.toml + script"
  exit 1
  ;;
esac
