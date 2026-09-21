_ccpool() {
  local cur
  _init_completion || return

  # Kept in sync by hand with the `subcommands` registry in
  # packages/ccpool/cmd/ccpool/dispatch.go (pg2-htmkq) -- see completions/_ccpool
  # for why there is no generator to derive this from.
  local -a commands=(attach attend cancel close doctor hook list meta new reap reap-all reply result state tail trust version)

  if [[ $COMP_CWORD -eq 1 ]]; then
    if [[ $cur == -* ]]; then
      mapfile -t COMPREPLY < <(compgen -W "--help -h --pool" -- "$cur")
    else
      mapfile -t COMPREPLY < <(compgen -W "${commands[*]}" -- "$cur")
    fi
    return
  fi

  # Per-subcommand flag completion is intentionally out of scope: this
  # completes the subcommand SET (matching ccpool --help's top-level
  # listing), not each subcommand's own flags.
}

complete -F _ccpool ccpool
