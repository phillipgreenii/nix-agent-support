# bash completion for pg-disk-reclaimer
#
# list's --aggressiveness (pg2-txxyj.4) and reclaim's flags (pg2-txxyj.6)
# are completed below. validate takes only an optional registry-path
# positional (pg2-txxyj.5) and needs no completion of its own -- the
# _filedir fallback below already covers it. Update this together with
# the tldr page and show_help() whenever a subcommand's flags change.

_pg_disk_reclaimer() {
  local cur prev words
  _init_completion || return

  if [[ $COMP_CWORD -eq 1 ]]; then
    mapfile -t COMPREPLY < <(compgen -W "list validate reclaim --help -h --version -v" -- "$cur")
    return
  fi

  if [[ ${words[1]} == reclaim ]]; then
    if [[ $prev == --aggressiveness ]]; then
      # No completion for the numeric ceiling itself.
      return
    fi
    if [[ $cur == -* ]]; then
      mapfile -t COMPREPLY < <(compgen -W "--aggressiveness --apply --help -h --version -v" -- "$cur")
    fi
    return
  fi

  if [[ ${words[1]} == list ]]; then
    if [[ $prev == --aggressiveness ]]; then
      # No completion for the numeric ceiling itself.
      return
    fi
    if [[ $cur == -* ]]; then
      mapfile -t COMPREPLY < <(compgen -W "--aggressiveness --help -h --version -v" -- "$cur")
    fi
    return
  fi

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v" -- "$cur")
    return
  fi

  _filedir
}

complete -F _pg_disk_reclaimer pg-disk-reclaimer
