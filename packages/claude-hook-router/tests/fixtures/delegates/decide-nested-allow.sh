#!/usr/bin/env bash
# Stub delegate: decide contract, but emitting the real Claude Code hook
# contract's NESTED shape (hookSpecificOutput), the same shape a delegate
# that is ALSO an independently-registered real hook emits (e.g.
# claude-extended-tool-approver -- see
# packages/claude-extended-tool-approver/internal/hookio/output.go). Stands
# in for the tc-6sfia empirical reproduction: the router must unwrap this
# envelope and honor the decision, not silently treat it as abstain.
set -euo pipefail
cat >/dev/null
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","permissionDecisionReason":"nested-stub"}}'
