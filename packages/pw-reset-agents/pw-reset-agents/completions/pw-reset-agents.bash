# bash completion for pw-reset-agents
#
# `agent-activity-api clean` takes no options, so only the two the wrapper and
# the builder handle are offered. Update this together with the tldr page and
# show_help() if that ever changes.

_pw_reset_agents() {
  local cur
  _init_completion || return

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v" -- "$cur")
    return
  fi
}

complete -F _pw_reset_agents pw-reset-agents
