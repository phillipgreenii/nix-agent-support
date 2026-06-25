# shellcheck shell=bash

# Claude Code SessionStart hook: inject the always-on personal agent rules into
# EVERY session's context (interactive and headless `claude -p` alike).
#
# A SessionStart hook that exits 0 and prints
#   {"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"…"}}
# has its additionalContext added verbatim to the model's context before the
# first turn (Claude Code hooks spec, code.claude.com/docs/en/hooks.md, verified
# v2.1.186). This is the always-on delivery the rules need: a plugin-root
# CLAUDE.md is not loaded, and a skill body is on-invoke only.
#
# The rules content is the single source of truth: pgii-agent-rules.md, supplied
# via AGENT_RULES_FILE (injected at build time by mkBashScript `config`; settable
# in tests). The hook ignores its stdin payload — the rules are unconditional.

show_help() {
  cat <<EOF
Usage: agent-rules-session-start [OPTIONS]

Internal Claude Code SessionStart hook. Prints the always-on agent rules as
JSON additionalContext so they load into every session's context.

This script is invoked automatically by Claude Code via the agent-rules plugin's
hooks.json and is not meant to be run manually. Any stdin payload is ignored.

Options:
  -h, --help     Show this help message and exit
  -v, --version  Show version information

The rules content is read from \$AGENT_RULES_FILE (the bundled pgii-agent-rules.md).
EOF
}

case "${1:-}" in
-h | --help)
  show_help
  exit 0
  ;;
esac

# Drain any stdin payload so the hook never blocks on a pipe; its content is
# irrelevant (the rules are always-on).
if [[ ! -t 0 ]]; then
  cat >/dev/null 2>&1 || true
fi

if [[ -z ${AGENT_RULES_FILE:-} ]]; then
  echo "agent-rules-session-start: AGENT_RULES_FILE is not set" >&2
  exit 1
fi
if [[ ! -f ${AGENT_RULES_FILE} ]]; then
  echo "agent-rules-session-start: rules file not found: ${AGENT_RULES_FILE}" >&2
  exit 1
fi

# Emit the SessionStart additionalContext envelope. --rawfile reads the markdown
# verbatim (preserving newlines and special characters) and jq encodes it as a
# JSON string, so the output is always well-formed regardless of the content.
jq -n --rawfile rules "${AGENT_RULES_FILE}" \
  '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: $rules}}'
