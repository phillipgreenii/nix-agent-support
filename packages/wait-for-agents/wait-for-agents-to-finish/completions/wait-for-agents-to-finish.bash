# bash completion for wait-for-agents-to-finish

_wait_for_agents_to_finish() {
  local cur prev words cword
  _init_completion || return

  local flags="--maximum-wait --time-between-checks --consecutive-idle-checks --caffeinate --help -h"

  # If current word starts with -, complete flags
  if [[ $cur == -* ]]; then
    COMPREPLY=($(compgen -W "$flags" -- "$cur"))
    return
  fi

  # If previous word needs a numeric argument
  case "$prev" in
  --maximum-wait | --time-between-checks | --consecutive-idle-checks)
    # Don't complete, let user type number
    COMPREPLY=()
    return
    ;;
  esac
}

complete -F _wait_for_agents_to_finish wait-for-agents-to-finish
