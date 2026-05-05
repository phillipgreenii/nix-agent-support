#!/usr/bin/env bash
# Test runner for claude-extended-tool-approver — uses go run to stay auto-approved.
# Usage: scripts/test-hook.sh [subcommand] [args...]
# Examples:
#   echo '{"tool_name":"Bash",...}' | scripts/test-hook.sh
#   scripts/test-hook.sh evaluate --settings=path/to/settings.local.json
#   scripts/test-hook.sh compare --settings=... --baseline=...
#   scripts/test-hook.sh baseline --settings=... --output=...
set -euo pipefail
cd "$(dirname "$0")/.."
exec go run ./cmd/claude-extended-tool-approver/ "$@"
