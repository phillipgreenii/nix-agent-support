# bash completion for pg-decider-backfill-minted-by

_pg_decider_backfill_minted_by() {
  local cur prev
  _init_completion || return

  case $prev in
  --bd-dir)
    _filedir -d
    return
    ;;
  --actor | --expect | --limit-per-kind)
    return
    ;;
  esac

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--bd-dir --actor --expect --limit-per-kind --include-open-keyless --apply --help -h --version -v" -- "$cur")
  fi
}

complete -F _pg_decider_backfill_minted_by pg-decider-backfill-minted-by
