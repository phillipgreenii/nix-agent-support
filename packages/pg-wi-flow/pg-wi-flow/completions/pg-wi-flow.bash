_pg_wi_flow() {
  local cur prev
  _init_completion || return

  local -a commands=(query list next claim release)

  if [[ $COMP_CWORD -eq 1 ]]; then
    if [[ $cur == -* ]]; then
      mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v --actor" -- "$cur")
    else
      mapfile -t COMPREPLY < <(compgen -W "${commands[*]}" -- "$cur")
    fi
    return
  fi

  prev="${COMP_WORDS[COMP_CWORD - 1]}"
  case "$prev" in
  --stage)
    return
    ;;
  --days | --reserved-hours | --actor)
    return
    ;;
  esac

  case "${COMP_WORDS[1]}" in
  query)
    mapfile -t COMPREPLY < <(compgen -W "--stage --attended" -- "$cur")
    ;;
  list)
    mapfile -t COMPREPLY < <(compgen -W "--stage --attended --questions --unpooled --stale --days --reserved-hours" -- "$cur")
    ;;
  next)
    mapfile -t COMPREPLY < <(compgen -W "--stage" -- "$cur")
    ;;
  claim | release)
    # positional ID -- nothing sensible to offer without a live tracker.
    ;;
  esac
}

complete -F _pg_wi_flow pg-wi-flow
