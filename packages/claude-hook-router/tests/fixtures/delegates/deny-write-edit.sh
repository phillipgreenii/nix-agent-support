#!/usr/bin/env bash
# Stub delegate standing in for ceta's real write-protection registration
# (ADR 0049/0051): registered match-all on PreToolUse -- i.e. NOT filtered
# by the router's own matcher field -- and instead decides for itself,
# inside the delegate, whether the call is a Write/Edit it must deny. This
# is the persisted regression scenario for "ADR 0049/0051's write-protection
# carve-out still works" (packet B3's Binding decisions).
set -euo pipefail
input="$(cat)"
tool_name="$(printf '%s' "$input" | jq -r '.tool_name // empty')"
if [[ $tool_name == "Write" || $tool_name == "Edit" ]]; then
  echo '{"permissionDecision":"deny"}'
else
  echo '{}'
fi
