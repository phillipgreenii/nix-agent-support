# pg-git-lander.bash
_pg_git_lander() {
  local cur cmd
  cur="${COMP_WORDS[COMP_CWORD]}"

  if [[ $COMP_CWORD -eq 1 ]]; then
    mapfile -t COMPREPLY < <(compgen -W "rebase land status log config help --version --help" -- "$cur")
    return
  fi

  cmd="${COMP_WORDS[1]}"
  case "$cmd" in
  rebase)
    mapfile -t COMPREPLY < <(compgen -W "--onto --source-worktree --work-worktree --wait --context --json --continue --skip --abort --quit --repo --help" -- "$cur")
    ;;
  land)
    mapfile -t COMPREPLY < <(compgen -W "--into --target-worktree --source-worktree --work-worktree --keep-source-worktree --keep-source-branch --wait --context --json --continue --skip --abort --quit --repo --help" -- "$cur")
    ;;
  status)
    mapfile -t COMPREPLY < <(compgen -W "--json --repo --help" -- "$cur")
    ;;
  log)
    mapfile -t COMPREPLY < <(compgen -W "--tail --repo --help" -- "$cur")
    ;;
  config)
    if [[ $COMP_CWORD -eq 2 ]]; then
      mapfile -t COMPREPLY < <(compgen -W "check --help" -- "$cur")
    elif [[ ${COMP_WORDS[2]} == check ]]; then
      mapfile -t COMPREPLY < <(compgen -W "--file --json --repo --help" -- "$cur")
    fi
    ;;
  help) ;;
  esac
}
complete -F _pg_git_lander pg-git-lander
