_pg_wi_flow_identity() {
  local cur
  _init_completion || return

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v" -- "$cur")
    return
  fi

  # The single positional argument is an arbitrary bash command string (the
  # command ceta is about to run), not a file -- nothing sensible to offer.
}

complete -F _pg_wi_flow_identity pg-wi-flow-identity
