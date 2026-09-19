#!/usr/bin/env bash
# Stub delegate: annotate contract, run AFTER an earlier delegate abstains in
# the same chain. Its contribution must still make it into the merged
# output -- proving abstain never drops a later delegate's context.
set -euo pipefail
cat >/dev/null
echo '{"additionalContext":"contributed-after-abstain"}'
