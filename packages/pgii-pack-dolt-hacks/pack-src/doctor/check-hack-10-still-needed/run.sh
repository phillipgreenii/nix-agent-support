#!/bin/sh
# Doctor check: HACK 10 (hack-archive-and-compact daily lifecycle).
#
# HACK 10 retires when one of:
#   - mol-dog-compactor ships a working executor (daemon or pool agent)
#   - bd ships a built-in archive lifecycle (export + prune + GC)
#   - The city migrates off dolt to a different bd backend
#
# Exit 0 = HACK 10 still needed
# Exit 1 = HACK 10 retirement candidate

# Signal 1: dedicated compactor agent in resolved config
if gc config show 2>/dev/null | grep -qE '^name = "compactor"\b'; then
  echo "compactor agent detected in resolved config — HACK 10 retirement candidate"
  exit 1
fi

# Signal 2: gc service list shows a compactor service
if gc service list 2>/dev/null | grep -qi compactor; then
  echo "compactor service detected — HACK 10 retirement candidate"
  exit 1
fi

# Signal 3: bd ships archive lifecycle subcommand
if bd --help 2>/dev/null | grep -qE '^\s+(archive|lifecycle)\s'; then
  echo "bd has built-in archive/lifecycle subcommand — HACK 10 retirement candidate"
  exit 1
fi

bd_version=$(bd --version 2>/dev/null | head -1 | awk '{print $3}')
gc_version=$(gc version 2>/dev/null | head -1)
echo "bd $bd_version, gc $gc_version: no compactor executor, no bd archive lifecycle"
echo "HACK 10 (hack-archive-and-compact daily lifecycle) still needed"
exit 0
