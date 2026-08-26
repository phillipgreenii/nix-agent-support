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

# _pg_disk_reclaimer_complete_item_ids: fills COMPREPLY with the ids from
# _pg_disk_reclaimer_item_ids that prefix-match CUR. Deliberately does NOT
# route those ids through `compgen -W "$ids" -- "$cur"`: compgen -W splits
# its wordlist and then EXPANDS each resultant word -- including command
# substitution -- so a registry item whose id contains e.g. `$(...)` would
# execute the moment a user tab-completes `reclaim <TAB>`, without ever
# running any command themselves. Matching ids by hand in [[ ]] never
# re-expands their content as shell code.
_pg_disk_reclaimer_complete_item_ids() {
  local cur="$1"
  local -a ids
  mapfile -t ids < <(_pg_disk_reclaimer_item_ids)
  COMPREPLY=()
  local id
  for id in "${ids[@]}"; do
    [[ $id == "$cur"* ]] && COMPREPLY+=("$id")
  done
  return 0
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
      _pg_disk_reclaimer_complete_item_ids "$cur"
    fi
    return
  fi

  if [[ ${words[1]} == list ]]; then
    if [[ $prev == --aggressiveness ]]; then
      # No completion for the numeric ceiling itself.
      return
    fi
    if [[ $cur == -* ]]; then
      mapfile -t COMPREPLY < <(compgen -W "--aggressiveness --verbose -v --help -h --version" -- "$cur")
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
