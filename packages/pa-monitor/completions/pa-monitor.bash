# pa-monitor.bash
_pa_monitor() {
  local cur prev cmd
  cur="${COMP_WORDS[COMP_CWORD]}"

  # When completing the first arg, offer subcommands. Otherwise leave it
  # to the user (subcommand-specific flags vary).
  if [[ $COMP_CWORD -eq 1 ]]; then
    COMPREPLY=($(compgen -W "daemon tui status caffeinate nudge info agents-busy-check wait-until-agents-finished config cmux-bridge --version --help" -- "$cur"))
    return
  fi

  cmd="${COMP_WORDS[1]}"
  case "$cmd" in
  caffeinate)
    COMPREPLY=($(compgen -W "on off toggle" -- "$cur"))
    ;;
  config)
    COMPREPLY=($(compgen -W "show" -- "$cur"))
    ;;
  daemon)
    COMPREPLY=($(compgen -W "--socket --pidfile --tick-seconds --no-poller" -- "$cur"))
    ;;
  agents-busy-check)
    COMPREPLY=($(compgen -W "--consider-daemon-down-as-busy" -- "$cur"))
    ;;
  wait-until-agents-finished)
    COMPREPLY=($(compgen -W "--maximum-wait --consecutive-idle-checks --reconnect-grace" -- "$cur"))
    ;;
  esac
}
complete -F _pa_monitor pa-monitor
