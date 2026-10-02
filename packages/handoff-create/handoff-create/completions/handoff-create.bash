# bash completion for handoff-create

_handoff_create() {
  local cur prev
  _init_completion || return

  case $prev in
  --body-file)
    _filedir
    return
    ;;
  --bd-dir)
    _filedir -d
    return
    ;;
  --session-id | --title | --label | --actor)
    return
    ;;
  esac

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--attended --unattended --session-id --title --body-file --label --actor --bd-dir --help -h --version -v" -- "$cur")
  fi
}

complete -F _handoff_create handoff-create
