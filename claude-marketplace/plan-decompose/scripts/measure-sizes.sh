#!/usr/bin/env bash
# measure-sizes.sh
#
# Prints the two size figures a decomposer run report carries so the next
# measurement of model-written packet text needs no reconstruction (speedup
# plan "Measured after", follow-up 2; bead pg2-i56s4): the design's bytes, the
# bytes the model typed into packet drafts and acceptance files, and their
# ratio ("bytes per design byte").
set -euo pipefail

show_help() {
  cat <<'HELP'
measure-sizes.sh: design bytes, model-written draft bytes, and their ratio

Usage: measure-sizes.sh --design <design.md> --drafts-dir <dir> [--acceptance-dir <dir>]

Prints one line:

  design_bytes=N draft_bytes=M drafts=K bytes_per_design_byte=R

design_bytes        bytes of <design.md>
draft_bytes         summed bytes of <drafts-dir>/*.draft.md (directive lines
                    included: they are typed by the model, and tiny) plus, when
                    --acceptance-dir is given, <acceptance-dir>/*.acceptance.md
drafts              number of *.draft.md files counted
bytes_per_design_byte  draft_bytes / design_bytes, two decimals

Paste the line into the decomposition report. Measure the FINAL drafts, after
the last fix-loop edit, so the figure is the text the model wrote.

Options:
  --design FILE          Design text (the one file materialized for the run)
  --drafts-dir DIR       Directory holding <key>.draft.md files
  --acceptance-dir DIR   Directory holding <key>.acceptance.md files
  -h, --help             Show this help message

Exit codes:
  0  printed
  1  generic error (unreadable file or directory)
  2  usage error (bad flag, missing required flag, empty design, no drafts)
HELP
}

die() {
  echo "measure-sizes.sh: error: $1" >&2
  exit "${2:-1}"
}

DESIGN=""
DRAFTS_DIR=""
ACCEPT_DIR=""

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --design | --drafts-dir | --acceptance-dir)
    [[ $# -ge 2 ]] || die "$1 requires a value" 2
    case "$1" in
    --design) DESIGN="$2" ;;
    --drafts-dir) DRAFTS_DIR="$2" ;;
    --acceptance-dir) ACCEPT_DIR="$2" ;;
    esac
    shift 2
    ;;
  -*)
    die "unknown option: $1" 2
    ;;
  *)
    die "unexpected argument: $1" 2
    ;;
  esac
done

[[ -n $DESIGN ]] || die "--design is required" 2
[[ -n $DRAFTS_DIR ]] || die "--drafts-dir is required" 2
[[ -r $DESIGN && -f $DESIGN ]] || die "cannot read design: $DESIGN"
[[ -d $DRAFTS_DIR ]] || die "not a directory: $DRAFTS_DIR"
[[ -z $ACCEPT_DIR || -d $ACCEPT_DIR ]] || die "not a directory: $ACCEPT_DIR"

bytes_of() { wc -c <"$1" | tr -d ' '; }

design_bytes=$(bytes_of "$DESIGN")
[[ $design_bytes -gt 0 ]] || die "design is empty: $DESIGN" 2

draft_bytes=0
drafts=0
for f in "$DRAFTS_DIR"/*.draft.md; do
  [[ -f $f ]] || continue
  draft_bytes=$((draft_bytes + $(bytes_of "$f")))
  drafts=$((drafts + 1))
done
[[ $drafts -gt 0 ]] || die "no *.draft.md files in $DRAFTS_DIR" 2

if [[ -n $ACCEPT_DIR ]]; then
  for f in "$ACCEPT_DIR"/*.acceptance.md; do
    [[ -f $f ]] || continue
    draft_bytes=$((draft_bytes + $(bytes_of "$f")))
  done
fi

ratio=$(awk -v d="$draft_bytes" -v s="$design_bytes" 'BEGIN { printf "%.2f", d / s }')
echo "design_bytes=$design_bytes draft_bytes=$draft_bytes drafts=$drafts bytes_per_design_byte=$ratio"
