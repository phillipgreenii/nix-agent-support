_command_name() {
  local cur
  _init_completion || return

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v --flag" -- "$cur")
    return
  fi

  _filedir
}

complete -F _command_name command-name
