_pg-go-gate() {
  local cur
  _init_completion || return

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v --slots -s --wait -w --nice -n --dir -d" -- "$cur")
    return
  fi
  _command_offset 1
}

complete -F _pg-go-gate pg-go-gate
