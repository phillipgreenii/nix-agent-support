# bash completion for gh-prreview

_gh_prreview() {
  local cur prev words cword
  _init_completion || return

  local commands="checkout remove list-local list-awaiting help"

  # Complete first argument (command)
  if [[ $cword -eq 1 ]]; then
    COMPREPLY=($(compgen -W "$commands" -- "$cur"))
    return
  fi

  # Complete based on command
  case "${words[1]}" in
  checkout)
    # Could complete PR numbers from list-awaiting, but that's expensive
    # Just let user type the number
    ;;
  remove)
    # Complete --closed flag
    if [[ $cur == -* ]]; then
      COMPREPLY=($(compgen -W "--closed" -- "$cur"))
    fi
    ;;
  list-awaiting)
    # Complete flags
    if [[ $cur == -* ]]; then
      COMPREPLY=($(compgen -W "--deep --debug --include-draft" -- "$cur"))
    fi
    ;;
  esac
}

complete -F _gh_prreview gh
complete -F _gh_prreview gh-prreview
