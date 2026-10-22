# bash completion for pg-rescue-fix-ci

_pg_rescue_fix_ci() {
  local cur prev
  _init_completion || return

  case $prev in
  --tracker-dir | --state-dir)
    _filedir -d
    return
    ;;
  esac

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--item-id --repo --pr-number --tracker-dir --ci-backend --state-dir --help -h --version -v" -- "$cur")
  elif [[ ${COMP_CWORD} -eq 1 ]]; then
    mapfile -t COMPREPLY < <(compgen -W "step" -- "$cur")
  fi
}

complete -F _pg_rescue_fix_ci pg-rescue-fix-ci
