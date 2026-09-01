# bash completion for wtnew

_wtnew() {
  local cur prev
  _init_completion || return

  if [[ $prev == --base ]]; then
    mapfile -t COMPREPLY < <(compgen -W "$(git branch --all --format='%(refname:short)' 2>/dev/null)" -- "$cur")
    return
  fi

  if [[ $prev == --branch ]]; then
    # No completion for a brand-new branch name.
    return
  fi

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--base --branch --help -h --version -v" -- "$cur")
  fi
}

complete -F _wtnew wtnew
