# bash completion for pg-rescue-push-branch

_pg_rescue_push_branch() {
  local cur prev
  _init_completion || return

  case $prev in
  --worktree)
    _filedir -d
    return
    ;;
  esac

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--worktree --branch --base --expected-sha --remote --help -h --version -v" -- "$cur")
  fi
}

complete -F _pg_rescue_push_branch pg-rescue-push-branch
