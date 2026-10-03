# bash completion for sync-projects

_sync_projects() {
  local cur
  _init_completion || return

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v" -- "$cur")
  fi
}

complete -F _sync_projects sync-projects
