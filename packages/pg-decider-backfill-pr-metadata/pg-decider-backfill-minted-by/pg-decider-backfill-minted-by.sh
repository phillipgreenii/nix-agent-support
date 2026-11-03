# shellcheck shell=bash
# pg-decider-backfill-minted-by - one-off backfill: add the
# minted-by:pg-decider label to the beads the decider created before the mint
# stamp existed. DRY-RUN BY DEFAULT: writes only with --apply.
#
# Safety design, as pg-decider-backfill-pr-metadata. (1) The run prints the
# tracker bd really resolved (TARGET) and refuses to continue unless it lies
# inside --bd-dir; inherited BEADS_*/BD_* variables are dropped. (2) Planning is
# a separate, write-free phase working from ONE listing; zero candidates or a
# planned count different from --expect ends the run with exit 1 BEFORE any
# write, in both modes. (3) Writes (--apply) stop at the first failure. (4) The
# label is ADDED (--add-label); the read-back checks the label is present, no
# label was lost and dedup_key is unchanged.
#
# Only beads provably the decider's are planned; look-alikes are reported as a
# residual and never written (see PB_CLASSIFY in the .bash file).
#
# nix build sources the .bash ahead of this body; this guard only fires for a
# raw `bash ...sh` run (local bats).
if ! declare -F pb_bd >/dev/null 2>&1; then
  # shellcheck disable=SC1091  # sibling file resolved at runtime, by design
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pg-decider-backfill-minted-by.bash"
fi

die() {
  echo "pg-decider-backfill-minted-by: $1" >&2
  exit "${2:-2}"
}

show_help() {
  cat <<'HELP'
pg-decider-backfill-minted-by: Add the minted-by:pg-decider label to the decider's existing beads

Usage: pg-decider-backfill-minted-by --bd-dir DIR --actor ID [--expect N] [--apply]

Reads every bead once (read-only, no hard-coded ids) and plans the label for the
beads that are provably the decider's and do not carry it yet:

  - every bead whose dedup_key is shaped <type>:<ident>:<kind>[:suffix] with
    kind one of anchor, process-feedback, review-pr, fix-ci, resolve-conflict,
    focus-item (the decider's own key grammar), open or closed;
  - the decider's escalation beads (no dedup_key): an exact title
    `pg-decider: rule <rule> keeps failing on <type> <id>` or
    `pr not found: <id>` AND a parent that is such a keyed anchor.

Beads that merely LOOK like the decider's are never labelled: a keyless bead
titled `<kind>: <repo>#<n>` or `<repo>#<n>: ...` (the earlier tool's output, or a
hand-made bead), an escalation title without a keyed anchor parent, and a
dedup_key the decider cannot parse. They are reported as RESIDUAL (a count per
shape and status, and one RESIDUAL-OPEN line per open bead) for the reviewer.

The label is added with `bd update --add-label` (never replaced), so no other
label changes. A bead that already carries it is skipped, so a re-run changes
nothing.

Expect one pg-desk labels_changed change row per labelled bead (about 540 at the
last count): that is expected, not a fault.

Default is a DRY RUN: it prints TARGET (the tracker bd resolved), a
PLAN<TAB>id<TAB>kind<TAB>status<TAB>title line per bead to label, COUNT<TAB>kind<TAB>n
per kind, the RESIDUAL lines and a SUMMARY, and writes nothing. With --apply each
bead is updated and read back, stopping at the first failure.

Each bd listing may take over a minute on a large tracker: run it with a long
timeout or in the background, and never repeat a timed-out run unchanged.

Options:
  --bd-dir DIR   Tracker root to run bd against (bd -C DIR). Required.
  --actor ID     Actor id passed on every write (--actor). Required; falls back
                 to $PG_BACKFILL_ACTOR. Never defaults to a person's name.
  --expect N     Fail (exit 1, nothing written) unless exactly N beads are
                 planned. Matching nothing at all always fails.
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

skipped=0 planned=0 residual=0 updated=0 failed=0
mode="dry-run"
((PB_APPLY)) && mode="apply"

summary() {
  echo "SUMMARY mode=$mode skipped=$skipped planned=$planned residual=$residual updated=$updated failed=$failed"
}

fail_run() { # message
  echo "pg-decider-backfill-minted-by: $1" >&2
  summary
  exit 1
}

pb_verify_target || fail_run "tracker target check failed"

snapshot="$(pb_snapshot)" || fail_run "listing failed"
rows="$(pb_classify "$snapshot")" || fail_run "classification failed"

# ---- Phase 1: plan (no writes) ----
plan_ids=()
while IFS= read -r row; do
  [[ -n $row ]] || continue
  decision="$(jq -r '.decision' <<<"$row")"
  case $decision in
  skip) skipped=$((skipped + 1)) ;;
  plan)
    id="$(jq -r '.id' <<<"$row")"
    printf 'PLAN\t%s\t%s\t%s\t%s\n' "$id" "$(jq -r '.kind' <<<"$row")" "$(jq -r '.status' <<<"$row")" "$(jq -r '.title' <<<"$row")"
    plan_ids+=("$id")
    planned=$((planned + 1))
    ;;
  residual) residual=$((residual + 1)) ;;
  esac
done <<<"$rows"

if ((planned + skipped == 0)); then
  fail_run "no decider beads found; nothing to label"
fi

jq -r 'select(.decision == "plan") | .kind' <<<"$rows" | sort | uniq -c |
  while read -r n kind; do printf 'COUNT\t%s\t%s\n' "$kind" "$n"; done
jq -r 'select(.decision == "residual") | [.kind, (if .status == "closed" then "closed" else "open" end)] | @tsv' <<<"$rows" |
  sort | uniq -c |
  while read -r n kind state; do printf 'RESIDUAL\t%s\t%s\t%s\n' "$kind" "$state" "$n"; done
jq -r 'select(.decision == "residual" and .status != "closed") | ["RESIDUAL-OPEN", .id, .kind, .status, .title] | @tsv' <<<"$rows"

if [[ -n $PB_EXPECT && $planned -ne $PB_EXPECT ]]; then
  fail_run "planned $planned beads but --expect is $PB_EXPECT; nothing written"
fi

# ---- Phase 2: writes (--apply only), stop at the first failure ----
if ((PB_APPLY)); then
  for id in ${plan_ids[@]+"${plan_ids[@]}"}; do
    if pb_write_and_verify "$id"; then
      updated=$((updated + 1))
      echo "OK $id"
    else
      failed=1
      echo "FAIL $id" >&2
      break
    fi
  done
fi

summary
((failed == 0)) || exit 1
