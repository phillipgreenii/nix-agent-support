# bash completion for git-branch-maintenance

_git_branch_maintenance() {
  local cur prev words cword
  _init_completion || return

  local flags="--ff --rebase --delete-merged --delete-merged-worktrees --protect-branch --protect-worktree --dry-run --force --help"

  # If current word starts with -, complete flags
  if [[ $cur == -* ]]; then
    COMPREPLY=($(compgen -W "$flags" -- "$cur"))
    return
  fi

  # If previous word needs an argument
  case "$prev" in
  --protect-branch)
    # Complete with branch names
    local branches=$(git for-each-ref --format='%(refname:short)' refs/heads 2>/dev/null)
    COMPREPLY=($(compgen -W "$branches" -- "$cur"))
    return
    ;;
  --protect-worktree)
    # Complete with directory paths
    _filedir -d
    return
    ;;
  esac

  # Otherwise complete with branch names
  local branches=$(git for-each-ref --format='%(refname:short)' refs/heads 2>/dev/null)
  COMPREPLY=($(compgen -W "$branches" -- "$cur"))
}

complete -F _git_branch_maintenance git-branch-maintenance
