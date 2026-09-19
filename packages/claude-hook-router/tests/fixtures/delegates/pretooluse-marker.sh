#!/usr/bin/env bash
# Stub delegate: registered only on PreToolUse. Used with
# posttooluse-marker.sh to confirm the router routes strictly by
# hook_event_name.
set -euo pipefail
cat >/dev/null
echo '{"additionalContext":"pretooluse-marker"}'
