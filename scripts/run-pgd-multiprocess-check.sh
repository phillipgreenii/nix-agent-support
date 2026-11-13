#!/usr/bin/env bash
# Opt-in multi-process check of the pgd shared store: several real headless `claude -p` sessions
# against $.store. Not part of the unit gate or any hook (V-4); its own named nix check is
# test-pgd-multiprocess, which points the sessions at a local stub Messages API server, so no
# credential or network is needed there. By hand it needs the claude binary and credentials in the
# environment; run it with PGD_MULTIPROCESS=1 (about 10 minutes max).
set -euo pipefail
if [ "${PGD_MULTIPROCESS:-}" != "1" ]; then
  echo "skipped: set PGD_MULTIPROCESS=1 to run the multi-process shared-store check" >&2
  exit 0
fi
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
exec node --experimental-strip-types --test --test-timeout=540000 \
  "$root/claude-marketplace/pgd/hooks/sharedStore.multiprocess.test.ts"
