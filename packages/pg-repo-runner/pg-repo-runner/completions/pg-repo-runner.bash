# bash completion for pg-repo-runner

_pg_repo_runner() {
  local cur prev cmd
  _init_completion || return

  cmd="${COMP_WORDS[1]:-}"
  case $prev in
  --repo)
    return
    ;;
  esac

  if ((COMP_CWORD == 1)); then
    mapfile -t COMPREPLY < <(compgen -W "tips workspace-tips verify sync build status --help -h --version -v" -- "$cur")
    return
  fi

  if [[ $cmd == status && $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--json --repo" -- "$cur")
  fi
}

complete -F _pg_repo_runner pg-repo-runner
