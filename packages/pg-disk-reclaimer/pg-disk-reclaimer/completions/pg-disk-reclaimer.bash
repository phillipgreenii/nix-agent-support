# bash completion for pg-disk-reclaimer
#
# Subcommand-only for validate: it has no stable option surface yet
# (pg2-txxyj.5 adds it later). list's flag (pg2-txxyj.4) and reclaim's
# flags (pg2-txxyj.6) are completed below. Update this together with the
# tldr page and show_help() whenever a subcommand's flags change.

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
