# pg-keep-awake.bash
#
# Completes the client and operator commands. keeper and anchor are
# long-running modes started by launchd and the keeper; they are not offered
# as commands to type, but their flags still complete if typed.
_pg_keep_awake() {
  local cur cmd
  cur="${COMP_WORDS[COMP_CWORD]}"

  if [[ $COMP_CWORD -eq 1 ]]; then
    mapfile -t COMPREPLY < <(compgen -W "acquire release run status pause resume config help --version --help" -- "$cur")
    return
  fi

  cmd="${COMP_WORDS[1]}"
  case "$cmd" in
  acquire)
    mapfile -t COMPREPLY < <(compgen -W "--owner --key --pid --for -d -i -m -s --help" -- "$cur")
    ;;
  release)
    mapfile -t COMPREPLY < <(compgen -W "--owner --key --all --help" -- "$cur")
    ;;
  run)
    mapfile -t COMPREPLY < <(compgen -W "--owner --key -d -i -m -s --help" -- "$cur")
    ;;
  status)
    mapfile -t COMPREPLY < <(compgen -W "--json --check --help" -- "$cur")
    ;;
  pause)
    mapfile -t COMPREPLY < <(compgen -W "--for --help" -- "$cur")
    ;;
  resume | keeper)
    mapfile -t COMPREPLY < <(compgen -W "--help" -- "$cur")
    ;;
  anchor)
    mapfile -t COMPREPLY < <(compgen -W "--release-pct --blind-pct --watchdog --heartbeat --interval --state-dir --help" -- "$cur")
    ;;
  config)
    if [[ $COMP_CWORD -eq 2 ]]; then
      mapfile -t COMPREPLY < <(compgen -W "check --help" -- "$cur")
    elif [[ ${COMP_WORDS[2]} == check ]]; then
      mapfile -t COMPREPLY < <(compgen -W "--config --help" -- "$cur")
    fi
    ;;
  help)
    if [[ $COMP_CWORD -eq 2 ]]; then
      mapfile -t COMPREPLY < <(compgen -W "acquire release run status pause resume config keeper anchor" -- "$cur")
    fi
    ;;
  esac
}
complete -F _pg_keep_awake pg-keep-awake
