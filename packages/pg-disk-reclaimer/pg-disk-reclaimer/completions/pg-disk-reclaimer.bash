# bash completion for pg-disk-reclaimer
#
# Subcommand-only completion for now: list/validate/reclaim have no stable
# option surface yet (later tasks in the pg2-txxyj epic add
# --aggressiveness/--apply/etc.). Update this together with the tldr page
# and show_help() once those land.

_pg_disk_reclaimer() {
  local cur
  _init_completion || return

  if [[ $COMP_CWORD -eq 1 ]]; then
    mapfile -t COMPREPLY < <(compgen -W "list validate reclaim --help -h --version -v" -- "$cur")
    return
  fi

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v" -- "$cur")
    return
  fi

  _filedir
}

complete -F _pg_disk_reclaimer pg-disk-reclaimer
