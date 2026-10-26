# bash completion for escalation-watchdog

_escalation_watchdog() {
  local cur prev
  _init_completion || return

  case $prev in
  --bd-dir)
    _filedir -d
    return
    ;;
  --stale-after | --quiet-for | --max-per-tick)
    return
    ;;
  esac

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--bd-dir --stale-after --max-per-tick --quiet-for --dry-run --help -h --version -v" -- "$cur")
  fi
}

complete -F _escalation_watchdog escalation-watchdog
