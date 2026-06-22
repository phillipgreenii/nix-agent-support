# pa-monitor.bash
_pa_monitor() {
  local cur cmd
  cur="${COMP_WORDS[COMP_CWORD]}"

  # When completing the first arg, offer subcommands. Otherwise leave it
  # to the user (subcommand-specific flags vary).
  if [[ $COMP_CWORD -eq 1 ]]; then
    mapfile -t COMPREPLY < <(compgen -W "daemon tui status caffeinate nudge info agents-busy-check wait-until-agents-finished config cmux-bridge --version --help" -- "$cur")
    return
  fi

  cmd="${COMP_WORDS[1]}"
  case "$cmd" in
  caffeinate)
    mapfile -t COMPREPLY < <(compgen -W "on off toggle" -- "$cur")
    ;;
  config)
    mapfile -t COMPREPLY < <(compgen -W "show" -- "$cur")
    ;;
  daemon)
    mapfile -t COMPREPLY < <(compgen -W "--socket --pidfile --tick-seconds --no-poller" -- "$cur")
    ;;
  agents-busy-check)
    mapfile -t COMPREPLY < <(compgen -W "--consider-daemon-down-as-busy" -- "$cur")
    ;;
  wait-until-agents-finished)
    mapfile -t COMPREPLY < <(compgen -W "--maximum-wait --consecutive-idle-checks --reconnect-grace" -- "$cur")
    ;;
  esac
}
complete -F _pa_monitor pa-monitor
