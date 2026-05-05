# bash completion for claude-activity-api

_claude_activity_api() {
  local cur
  _init_completion || return

  local commands="list is-agent-active clean help"

  # If current word starts with -, complete flags
  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v" -- "$cur")
    return
  fi

  # Complete commands
  mapfile -t COMPREPLY < <(compgen -W "$commands" -- "$cur")
}

complete -F _claude_activity_api claude-activity-api
