# shellcheck shell=bash
# pg-decider-backfill-pr-metadata - one-off backfill, bead pg2-eclw3j.5 (plan
# D4 of epic pg2-eclw3j). DRY-RUN BY DEFAULT: writes only with --apply.
#
# Safety design. (1) The run prints the tracker bd really resolved (TARGET) and
# refuses to continue unless it lies inside --bd-dir; inherited BEADS_*/BD_*
# variables are dropped. (2) Planning is a separate, write-free phase that
# collects EVERY problem (an unusable parent, a child whose title does not name
# the parent's PR, a malformed value) before reporting; any problem, zero
# candidates, or a planned-children count different from --expect ends the run
# with exit 1 BEFORE any write, in both modes. (3) Writes (--apply) stop at the
# first failure. (4) The read-back compares the set keys and dedup_key only:
# the decider may legitimately write another key of the same child between
# discovery and the update, which must not read as a failure; a false failure
# fails safe and a rerun completes the remaining children.
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

Usage: pg-decider-backfill-pr-metadata --bd-dir DIR --actor ID [--expect N] [--apply]

Discovers (read-only, no hard-coded ids) the decider-minted process-feedback
children (dedup_key of kind process-feedback, parent set) and, for each one
missing `repo` and/or `pr_number`, copies the value from its parent anchor
with `bd update --set-metadata key=value` (merge; never --metadata). A child
is refused unless its title is `process-feedback: <repo>#<pr_number>` for the
parent's values. Children that already carry both keys are skipped, so a
re-run changes nothing.

bd stores a numeric-looking value such as a PR number as a JSON number, and
--set-metadata cannot write a string. Existing beads on the tracker carry both
forms (string and number); the per-PR lookups match either.

Default is a DRY RUN: it prints TARGET (the tracker bd resolved), then per
child a CHILD<TAB>id<TAB>title line and PLAN<TAB>id<TAB>key<TAB>old<TAB>new
lines, and writes nothing. Planning collects every problem before reporting.
With --apply each child is updated and read back, stopping at the first
failure. A summary is always printed.

Options:
  --bd-dir DIR   Tracker root to run bd against (bd -C DIR). Required.
  --actor ID     Actor id passed on every write (--actor). Required; falls back
                 to $PG_BACKFILL_ACTOR. Never defaults to a person's name.
  --expect N     Fail (exit 1, nothing written) unless exactly N children are
                 planned. Zero candidates always fails.
  --apply        Perform the writes. Without it nothing is written.
  -h, --help     Show this help message
  -v, --version  Show version information
HELP
}

PB_BD_DIR=""
PB_ACTOR="${PG_BACKFILL_ACTOR:-}"
PB_APPLY=0
PB_EXPECT=""

# need_value <option> <value...>: the option's value must exist and not look
# like another option.
need_value() {
  [[ $# -ge 2 ]] || die "$1 needs a value"
  [[ $2 != -* ]] || die "$1 needs a value, got '$2'"
}

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    show_help
    exit 0
    ;;
  --bd-dir)
    need_value "$@"
    PB_BD_DIR="$2"
    shift
    ;;
  --actor)
    need_value "$@"
    PB_ACTOR="$2"
    shift
    ;;
  --expect)
    need_value "$@"
    [[ $2 =~ ^[0-9]+$ ]] || die "--expect needs a non-negative integer, got '$2'"
    PB_EXPECT="$2"
    shift
    ;;
  --apply) PB_APPLY=1 ;;
  *) die "unknown argument: $1" ;;
  esac
  shift
done

[[ -n $PB_BD_DIR ]] || die "--bd-dir is required"
[[ -n $PB_ACTOR ]] || die "--actor (or PG_BACKFILL_ACTOR) is required"
[[ $PB_ACTOR != -* ]] || die "--actor must not start with '-'"
command -v bd >/dev/null 2>&1 || die "bd is not on PATH"
command -v jq >/dev/null 2>&1 || die "jq is not on PATH"
export PB_BD_DIR PB_ACTOR

discovered=0 skipped=0 planned=0 updated=0 failed=0 problems=0
mode="dry-run"
((PB_APPLY)) && mode="apply"

summary() {
  echo "SUMMARY mode=$mode discovered=$discovered skipped=$skipped planned=$planned updated=$updated problems=$problems failed=$failed"
}

fail_run() { # message
  echo "pg-decider-backfill-pr-metadata: $1" >&2
  summary
  exit 1
}

pb_verify_target || fail_run "tracker target check failed"

candidates="$(pb_discover)" || fail_run "discovery failed"

# ---- Phase 1: plan (no writes) ----
plan_ids=() plan_meta=() plan_args=()
while IFS= read -r row; do
  [[ -n $row ]] || continue
  discovered=$((discovered + 1))
  id="$(jq -r '.id' <<<"$row")"
  parent="$(jq -r '.parent' <<<"$row")"
  title="$(jq -r '.title' <<<"$row")"
  meta="$(jq -c '.metadata' <<<"$row")"

  need=()
  pb_is_blank "$meta" repo && need+=(repo)
  pb_is_blank "$meta" pr_number && need+=(pr_number)
  if [[ ${#need[@]} -eq 0 ]]; then
    skipped=$((skipped + 1))
    continue
  fi

  if ! pb_parent_identity "$parent"; then
    problems=$((problems + 1))
    echo "PROBLEM $id: cannot take identity from parent $parent" >&2
    continue
  fi
  if [[ $title != "process-feedback: ${PB_P_REPO}#${PB_P_PR}" ]]; then
    problems=$((problems + 1))
    echo "PROBLEM $id: title '$title' does not match parent $parent (process-feedback: ${PB_P_REPO}#${PB_P_PR})" >&2
    continue
  fi

  printf 'CHILD\t%s\t%s\n' "$id" "$title"
  args=()
  for key in "${need[@]}"; do
    case $key in
    repo) new="$PB_P_REPO" ;;
    pr_number) new="$PB_P_PR" ;;
    esac
    printf 'PLAN\t%s\t%s\t%s\t%s\n' "$id" "$key" "$(pb_old_value "$meta" "$key")" "$new"
    args+=("$key" "$new")
  done
  plan_ids+=("$id")
  plan_meta+=("$meta")
  plan_args+=("${args[*]}")
  planned=$((planned + 1))
done <<<"$candidates"

((discovered > 0)) || {
  echo "WARN no candidates" >&2
  fail_run "no process-feedback children discovered"
}
((problems == 0)) || fail_run "$problems problem(s) found while planning; nothing written"
if [[ -n $PB_EXPECT && $planned -ne $PB_EXPECT ]]; then
  fail_run "planned $planned children but --expect is $PB_EXPECT; nothing written"
fi

# ---- Phase 2: writes (--apply only), stop at the first failure ----
if ((PB_APPLY)); then
  for i in "${!plan_ids[@]}"; do
    read -r -a kv <<<"${plan_args[$i]}"
    if pb_write_and_verify "${plan_ids[$i]}" "${plan_meta[$i]}" "${kv[@]}"; then
      updated=$((updated + 1))
      echo "OK ${plan_ids[$i]}"
    else
      failed=1
      echo "FAIL ${plan_ids[$i]}" >&2
      break
    fi
  done
fi

summary
((failed == 0)) || exit 1
