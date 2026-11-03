# bash completion for pg-decider-backfill-minted-by

_pg_decider_backfill_minted_by() {
  local cur prev
  _init_completion || return

  case $prev in
  --bd-dir)
    _filedir -d
    return
    ;;
  --actor | --expect)
    return
    ;;
  esac

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--bd-dir --actor --expect --apply --help -h --version -v" -- "$cur")
  fi
}

complete -F _pg_decider_backfill_minted_by pg-decider-backfill-minted-by
