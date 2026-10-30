# bash completion for pg-decider-backfill-pr-metadata

_pg_decider_backfill_pr_metadata() {
  local cur prev
  _init_completion || return

  case $prev in
  --bd-dir)
    _filedir -d
    return
    ;;
  --actor)
    return
    ;;
  esac

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--bd-dir --actor --apply --help -h --version -v" -- "$cur")
  fi
}

complete -F _pg_decider_backfill_pr_metadata pg-decider-backfill-pr-metadata
