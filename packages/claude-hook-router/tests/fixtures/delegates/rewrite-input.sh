#!/usr/bin/env bash
# Stub delegate: rewrite contract. Always rewrites tool_input to a fixed
# command, regardless of the original input.
set -euo pipefail
cat >/dev/null
echo '{"updatedInput":{"command":"echo rewritten"}}'
