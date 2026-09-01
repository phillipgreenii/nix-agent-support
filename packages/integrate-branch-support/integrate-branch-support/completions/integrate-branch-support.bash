# bash completion for integrate-branch-support

_integrate_branch_support() {
  local cur
  _init_completion || return

  # No positional arguments -- --facts is the only custom flag; the rest are
  # the framework-injected --help/--version.
  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--facts --help -h --version -v" -- "$cur")
  fi
}

complete -F _integrate_branch_support integrate-branch-support
