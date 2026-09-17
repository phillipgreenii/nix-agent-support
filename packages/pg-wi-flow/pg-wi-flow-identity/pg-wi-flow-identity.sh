# shellcheck shell=bash

# nix build already sources pg-wi-flow-identity.bash ahead of this body
# (mkBashScript's hasSupportBash injection); this guard only fires for a raw
# `bash pg-wi-flow-identity.sh` run (e.g. local bats), where nothing has
# sourced it yet.
if ! declare -F pgwfi_compose_ident >/dev/null 2>&1; then
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pg-wi-flow-identity.bash"
fi

show_help() {
  cat <<'HELP'
pg-wi-flow-identity: ceta input processor -- stamps PG_WI_FLOW_IDENT

Usage: pg-wi-flow-identity COMMAND

This is a ceta input processor (see
phillipgreenii.programs.claude-extended-tool-approver.inputProcessors), not
a tool meant for interactive use. It is installed on PATH, and wired into
ceta's inputProcessors list, by pg-wi-flow's home-manager module.

Contract:
  COMMAND is the bash command ceta is about to run. CETA_SESSION_ID,
  CETA_AGENT_ID, CETA_AGENT_TYPE come from ceta's own environment.

  - Declines (exit 1, no stdout) if CETA_SESSION_ID is empty.
  - Declines (exit 1, no stdout) if COMMAND does not invoke `pg-wi-flow` as
    a command word.
  - Otherwise prints, and exits 0:
      export PG_WI_FLOW_IDENT=<session8>-<agent-or-main>-<agenttype-or-main>; COMMAND
    session8 is the first 8 characters of CETA_SESSION_ID; agent/agenttype
    fall back to the literal "main" when ceta's own env carries them empty
    (the main-session convention -- bead tc-7m85u).

Options:
  -h, --help     Show this help message
  -v, --version  Show version information

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

case "${1:-}" in
-h | --help)
  show_help
  exit 0
  ;;
esac

if [[ $# -lt 1 ]]; then
  echo "pg-wi-flow-identity: missing COMMAND argument" >&2
  exit 1
fi

command="$1"

if [[ -z ${CETA_SESSION_ID:-} ]]; then
  exit 1
fi

if ! pgwfi_invokes_pg_wi_flow "$command"; then
  exit 1
fi

ident="$(pgwfi_compose_ident "$CETA_SESSION_ID" "${CETA_AGENT_ID:-}" "${CETA_AGENT_TYPE:-}")"
printf 'export PG_WI_FLOW_IDENT=%q; %s\n' "$ident" "$command"
