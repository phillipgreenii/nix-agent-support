# bash completion for pg-rescue-resolve-conflict

_pg_rescue_resolve_conflict() {
  local cur prev
  _init_completion || return

  case $prev in
  --worktree-root | --tracker-dir)
    _filedir -d
    return
    ;;
  esac

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--item-id --repo --branch --base --head-sha --repo-dir-map --worktree-root --tracker-dir --help -h --version -v" -- "$cur")
  fi
}

complete -F _pg_rescue_resolve_conflict pg-rescue-resolve-conflict
