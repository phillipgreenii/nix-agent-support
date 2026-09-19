#!/usr/bin/env bash
# Stub delegate: always abstains ({}), regardless of input. Used to prove a
# mid-chain abstain does not drop a later delegate's contribution (the
# Abstain-does-not-drop-context defect this whole design fixes).
set -euo pipefail
cat >/dev/null
echo '{}'
