# bash completion for wtdone

_wtdone() {
  local cur prev
  _init_completion || return

  if [[ $prev == --cc ]]; then
    _filedir -d
    return
  fi

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--cc --help -h --version -v" -- "$cur")
  fi
}

complete -F _wtdone wtdone
