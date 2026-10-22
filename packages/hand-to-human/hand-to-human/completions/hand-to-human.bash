# bash completion for hand-to-human

_hand_to_human() {
  local cur prev
  _init_completion || return

  case $prev in
  --bd-dir)
    _filedir -d
    return
    ;;
  --backend | --also-remove | --actor)
    return
    ;;
  esac

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--backend --bd-dir --also-remove --actor --help -h --version -v" -- "$cur")
  fi
}

complete -F _hand_to_human hand-to-human
