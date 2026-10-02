# bash completion for integrate-branch-support

_integrate_branch_support() {
  local cur
  _init_completion || return

  # No positional arguments -- --facts, --prek-branch-diff and --bundle-refresh are the only
  # custom flags; the rest are --help and the framework-injected --version.
  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--facts --prek-branch-diff --bundle-refresh --help -h --version -v" -- "$cur")
  fi
}

complete -F _integrate_branch_support integrate-branch-support
