# bash completion for pg-disk-reclaimer
#
# list's --aggressiveness (pg2-txxyj.4) and reclaim's flags (pg2-txxyj.6)
# are completed below. validate takes only an optional registry-path
# positional (pg2-txxyj.5) and needs no completion of its own -- the
# _filedir fallback below already covers it. reclaim's item-id
# positional(s) complete against the registered ids in the XDG registry
# (pg2-txxyj.7) -- list takes no such positional (unexpected-argument
# error), so it gets no id completion. Update this together with the
# tldr page and show_help() whenever a subcommand's flags change.

# _pg_disk_reclaimer_item_ids: best-effort -- prints one registered item id
# per line, read from the same XDG registry path pg-disk-reclaimer.bash's
# pgdr_default_registry_path resolves. A missing/unreadable/malformed
# registry silently yields no ids rather than erroring the completion.
_pg_disk_reclaimer_item_ids() {
  local registry="${XDG_CONFIG_HOME:-$HOME/.config}/pg-disk-reclaimer/registry.json"
  [[ -r $registry ]] || return 0
  jq -r '.[].id' "$registry" 2>/dev/null
}

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
    else
      mapfile -t COMPREPLY < <(compgen -W "$(_pg_disk_reclaimer_item_ids)" -- "$cur")
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
