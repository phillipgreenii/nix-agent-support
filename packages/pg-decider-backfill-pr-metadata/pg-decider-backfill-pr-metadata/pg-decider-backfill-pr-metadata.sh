# shellcheck shell=bash
# pg-decider-backfill-pr-metadata - one-off backfill, bead pg2-eclw3j.5 (plan
# D4 of epic pg2-eclw3j). DRY-RUN BY DEFAULT: writes only with --apply.
#
# nix build sources the .bash ahead of this body; this guard only fires for a
# raw `bash ...sh` run (local bats).
if ! declare -F pb_bd >/dev/null 2>&1; then
  # shellcheck disable=SC1091  # sibling file resolved at runtime, by design
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pg-decider-backfill-pr-metadata.bash"
fi

die() {
  echo "pg-decider-backfill-pr-metadata: $1" >&2
  exit "${2:-2}"
}

show_help() {
  cat <<'HELP'
pg-decider-backfill-pr-metadata: Copy repo and pr_number from the parent anchor onto process-feedback children that lack them

Usage: pg-decider-backfill-pr-metadata --bd-dir DIR --actor ID [--apply]

Discovers (read-only, no hard-coded ids) the decider-minted process-feedback
children (dedup_key of kind process-feedback, parent set) and, for each one
missing `repo` and/or `pr_number`, copies the value from its parent anchor with
`bd update --set-metadata key=value` (merge; never --metadata). pr_number is
sent as the plain decimal text (bd stores it as a JSON number, like the parent anchors). Children that already carry both keys are skipped, so a
re-run changes nothing.

Default is a DRY RUN: it prints one PLAN line per change
(PLAN<TAB>id<TAB>key<TAB>old<TAB>new) and writes nothing. With --apply each
child is updated, read back, and the run stops at the first failure. A summary
is always printed.

Options:
  --bd-dir DIR   Tracker root to run bd against (bd -C DIR). Required.
  --actor ID     Actor id passed on every write (--actor). Required; falls back
                 to $PG_BACKFILL_ACTOR. Never defaults to a person's name.
  --apply        Perform the writes. Without it nothing is written.
  -h, --help     Show this help message
  -v, --version  Show version information
HELP
}

PB_BD_DIR=""
PB_ACTOR="${PG_BACKFILL_ACTOR:-}"
PB_APPLY=0

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    show_help
    exit 0
    ;;
  --bd-dir)
    [[ $# -ge 2 ]] || die "--bd-dir needs a value"
    PB_BD_DIR="$2"
    shift
    ;;
  --actor)
    [[ $# -ge 2 ]] || die "--actor needs a value"
    PB_ACTOR="$2"
    shift
    ;;
  --apply) PB_APPLY=1 ;;
  *) die "unknown argument: $1" ;;
  esac
  shift
done

[[ -n $PB_BD_DIR ]] || die "--bd-dir is required"
[[ -n $PB_ACTOR ]] || die "--actor (or PG_BACKFILL_ACTOR) is required"
export PB_BD_DIR PB_ACTOR

discovered=0 skipped=0 planned=0 updated=0 failed=0
mode="dry-run"
((PB_APPLY)) && mode="apply"

summary() {
  echo "SUMMARY mode=$mode discovered=$discovered skipped=$skipped planned=$planned updated=$updated failed=$failed"
}

candidates="$(pb_discover)" || {
  failed=1
  summary
  die "discovery failed" 1
}

while IFS= read -r row; do
  [[ -n $row ]] || continue
  discovered=$((discovered + 1))
  id="$(jq -r '.id' <<<"$row")"
  parent="$(jq -r '.parent' <<<"$row")"
  meta="$(jq -c '.metadata' <<<"$row")"

  need=()
  pb_is_blank "$meta" repo && need+=(repo)
  pb_is_blank "$meta" pr_number && need+=(pr_number)
  if [[ ${#need[@]} -eq 0 ]]; then
    skipped=$((skipped + 1))
    continue
  fi

  if ! ident="$(pb_parent_identity "$parent")"; then
    failed=1
    echo "FAIL $id: cannot take identity from parent $parent" >&2
    break
  fi
  p_repo="${ident%%$'\t'*}"
  p_pr="${ident#*$'\t'}"

  args=()
  for key in "${need[@]}"; do
    case $key in
    repo) new="$p_repo" ;;
    pr_number) new="$p_pr" ;;
    esac
    printf 'PLAN\t%s\t%s\t%s\t%s\n' "$id" "$key" "$(pb_old_value "$meta" "$key")" "$new"
    args+=("$key" "$new")
  done
  planned=$((planned + ${#need[@]}))

  if ((PB_APPLY)); then
    if pb_write_and_verify "$id" "$meta" "${args[@]}"; then
      updated=$((updated + 1))
      echo "OK $id"
    else
      failed=1
      echo "FAIL $id" >&2
      break
    fi
  fi
done <<<"$candidates"

summary
((failed == 0)) || exit 1
