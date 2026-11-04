# bash completion for wtaudit

_wtaudit() {
  local cur prev words cword
  _init_completion || return

  case "$prev" in
  --repo)
    _filedir -d
    return
    ;;
  --branch | --expect-tip)
    return
    ;;
  *) ;;
  esac

  local i sub=""
  for ((i = 1; i < cword; i++)); do
    case "${words[i]}" in
    scan | remove)
      sub="${words[i]}"
      break
      ;;
    *) ;;
    esac
  done

  if [[ -z $sub ]]; then
    mapfile -t COMPREPLY < <(compgen -W "scan remove --help -h --version -v" -- "$cur")
    return
  fi

  case "$sub" in
  scan) mapfile -t COMPREPLY < <(compgen -W "--repo --json --table --help -h" -- "$cur") ;;
  remove) mapfile -t COMPREPLY < <(compgen -W "--repo --branch --expect-tip --keep-branch --help -h" -- "$cur") ;;
  *) ;;
  esac
}

complete -F _wtaudit wtaudit
