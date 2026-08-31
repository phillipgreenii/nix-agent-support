_bgrun() {
  local cur
  _init_completion || return

  # Nothing to complete after the -- separator: the payload is any command.
  local i
  for i in "${!COMP_WORDS[@]}"; do
    if [[ ${COMP_WORDS[i]} == "--" && $i -lt $COMP_CWORD ]]; then
      return
    fi
  done

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v --dir -d" -- "$cur")
    return
  fi
}

complete -F _bgrun bgrun
