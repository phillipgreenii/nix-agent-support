# bash completion for pw-agent-activity
#
# The forwarded options mirror `agent-activity-api wait`. Completions are
# inherently enumerative, so this list is the one place a copy is unavoidable --
# update it together with the tldr page and show_help() when that tool's wait
# options change.

_pw_agent_activity() {
  local cur prev
  _init_completion || return

  local flags="--maximum-wait --time-between-checks --caffeinate --help -h --version -v"

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "$flags" -- "$cur")
    return
  fi

  case "$prev" in
  --maximum-wait | --time-between-checks)
    # Numeric argument; nothing sensible to complete.
    COMPREPLY=()
    return
    ;;
  esac
}

complete -F _pw_agent_activity pw-agent-activity
