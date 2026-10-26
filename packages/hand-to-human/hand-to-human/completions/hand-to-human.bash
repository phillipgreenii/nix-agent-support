# bash completion for hand-to-human

_hand_to_human() {
  local cur prev
  _init_completion || return

  case $prev in
  --bd-dir)
    _filedir -d
    return
    ;;
  --backend | --also-remove | --also-add | --note | --actor | --if-assignee | --if-status)
    return
    ;;
  esac

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--backend --bd-dir --also-remove --also-add --note --actor --if-assignee --if-status --help -h --version -v" -- "$cur")
  fi
}

complete -F _hand_to_human hand-to-human
