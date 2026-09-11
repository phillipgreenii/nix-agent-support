# bash completion for session-mode

_session_mode() {
  local cur
  _init_completion || return

  local subcommands="start set-status show hook"

  if [[ $COMP_CWORD -eq 1 ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v $subcommands" -- "$cur")
    return
  fi

  case "${COMP_WORDS[1]}" in
  start)
    if [[ $cur == -* ]]; then
      mapfile -t COMPREPLY < <(compgen -W "--detail --force" -- "$cur")
    fi
    ;;
  set-status)
    if [[ $COMP_CWORD -eq 2 ]]; then
      mapfile -t COMPREPLY < <(compgen -W "running stopping finished" -- "$cur")
    fi
    ;;
  hook)
    if [[ $COMP_CWORD -eq 2 ]]; then
      mapfile -t COMPREPLY < <(compgen -W "session-end" -- "$cur")
    fi
    ;;
  esac
}

complete -F _session_mode session-mode
