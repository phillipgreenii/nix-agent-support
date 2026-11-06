#!/usr/bin/env bash
# Opt-in multi-process check of the pgd shared store: several real headless `claude -p` sessions
# against $.store. It needs the claude binary and credentials in the environment, so it is not part
# of any nix check or the unit gate; run it by hand with PGD_MULTIPROCESS=1 (about 10 minutes max).
set -euo pipefail
if [ "${PGD_MULTIPROCESS:-}" != "1" ]; then
  echo "skipped: set PGD_MULTIPROCESS=1 to run the multi-process shared-store check" >&2
  exit 0
fi
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
exec node --experimental-strip-types --test --test-timeout=540000 \
  "$root/claude-marketplace/pgd/hooks/sharedStore.multiprocess.test.ts"
