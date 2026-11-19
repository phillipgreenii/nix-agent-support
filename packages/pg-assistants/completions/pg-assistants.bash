# pg-assistants.bash
#
# Completes the operator and session commands. daemon is the long-running
# supervisor launchd starts; it is not offered as a command to type, but its
# flags still complete if typed.
_pg_assistants() {
  local cur cmd
  cur="${COMP_WORDS[COMP_CWORD]}"

  if [[ $COMP_CWORD -eq 1 ]]; then
    mapfile -t COMPREPLY < <(compgen -W "status attach exit trust config help --version --help" -- "$cur")
    return
  fi

  # A flag's value is a file: --config and --claude-json name files, not
  # directories, whatever the command's positional words are.
  case "${COMP_WORDS[COMP_CWORD - 1]}" in
  --config | --claude-json)
    mapfile -t COMPREPLY < <(compgen -f -- "$cur")
    return
    ;;
  esac

  cmd="${COMP_WORDS[1]}"
  case "$cmd" in
  status)
    mapfile -t COMPREPLY < <(compgen -W "--json --check --config --help" -- "$cur")
    ;;
  attach)
    mapfile -t COMPREPLY < <(compgen -W "--config --help" -- "$cur")
    ;;
  exit)
    mapfile -t COMPREPLY < <(compgen -W "--help" -- "$cur")
    ;;
  trust)
    if [[ $cur == -* ]]; then
      mapfile -t COMPREPLY < <(compgen -W "--claude-json --help" -- "$cur")
    else
      mapfile -t COMPREPLY < <(compgen -d -- "$cur")
    fi
    ;;
  daemon)
    mapfile -t COMPREPLY < <(compgen -W "--config --shutdown-deadline --help" -- "$cur")
    ;;
  config)
    if [[ $COMP_CWORD -eq 2 ]]; then
      mapfile -t COMPREPLY < <(compgen -W "check --help" -- "$cur")
    elif [[ ${COMP_WORDS[2]} == check ]]; then
      mapfile -t COMPREPLY < <(compgen -W "--config --help" -- "$cur")
    fi
    ;;
  esac
}
complete -F _pg_assistants pg-assistants
