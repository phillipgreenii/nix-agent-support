# bash completion for agent-activity-api

_agent_activity_api() {
  local cur prev opts
  COMPREPLY=()
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD - 1]}"

  # Commands
  local commands="list wait is-agent-active clean help version"

  # Wait command options
  local wait_opts="--maximum-wait --time-between-checks --caffeinate"

  # If first argument, complete commands
  if [[ ${COMP_CWORD} -eq 1 ]]; then
    COMPREPLY=($(compgen -W "${commands}" -- ${cur}))
    return 0
  fi

  # If previous was wait command, complete wait options
  if [[ ${COMP_WORDS[1]} == "wait" ]]; then
    case "${prev}" in
    --maximum-wait | --time-between-checks)
      # Numeric argument expected
      return 0
      ;;
    *)
      COMPREPLY=($(compgen -W "${wait_opts}" -- ${cur}))
      return 0
      ;;
    esac
  fi
}

complete -F _agent_activity_api agent-activity-api
