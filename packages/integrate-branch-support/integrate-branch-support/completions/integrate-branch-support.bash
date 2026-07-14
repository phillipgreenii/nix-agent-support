# bash completion for integrate-branch-support

_integrate_branch_support() {
  local cur
  _init_completion || return

  # No positional arguments or custom flags -- only the framework-injected
  # --help/--version.
  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v" -- "$cur")
  fi
}

complete -F _integrate_branch_support integrate-branch-support
