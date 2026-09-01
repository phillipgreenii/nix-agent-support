#!/usr/bin/env bash
# chunk-for-bd-field.sh
#
# Splits a file into sequential chunks, each at most --max-bytes bytes
# (default 65000, safely under bd's 65,535-byte design/description field cap),
# breaking only on line boundaries so no line is split mid-content.
# Concatenating the chunks in order reproduces the input byte-for-byte,
# including a missing trailing newline on the last line.
#
# Exists so plan-decompose prepares `bd comment --file` parts for an
# over-cap design or report in ONE call instead of many ad hoc
# byte-count-and-slice steps.
set -euo pipefail

show_help() {
  cat <<'HELP'
chunk-for-bd-field.sh: split a file into byte-capped, line-safe chunks

Usage: chunk-for-bd-field.sh [OPTIONS] <input-file> <output-prefix>

Splits <input-file> into <output-prefix>.1, <output-prefix>.2, ... where each
chunk is at most --max-bytes bytes. Chunks break only on line boundaries;
concatenating them in order reproduces the input exactly. Prints the chunk
paths to stdout, one per line, in order.

A single line longer than --max-bytes cannot be split safely and is an error.

Options:
  -m, --max-bytes N   Maximum bytes per chunk (default: 65000)
  -h, --help          Show this help message
HELP
}

die() {
  echo "chunk-for-bd-field.sh: error: $1" >&2
  exit "${2:-1}"
}

MAX_BYTES=65000
ARGS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  -m | --max-bytes)
    [[ $# -ge 2 ]] || die "--max-bytes requires a value" 2
    MAX_BYTES="$2"
    shift 2
    ;;
  --)
    shift
    ARGS+=("$@")
    break
    ;;
  -*)
    die "unknown option: $1" 2
    ;;
  *)
    ARGS+=("$1")
    shift
    ;;
  esac
done

[[ ${#ARGS[@]} -eq 2 ]] || die "expected <input-file> <output-prefix>, got ${#ARGS[@]} argument(s)" 2
INPUT_FILE="${ARGS[0]}"
OUTPUT_PREFIX="${ARGS[1]}"

[[ $MAX_BYTES =~ ^[0-9]+$ ]] || die "--max-bytes must be a positive integer, got: $MAX_BYTES" 2
[[ $MAX_BYTES -gt 0 ]] || die "--max-bytes must be a positive integer, got: $MAX_BYTES" 2
[[ -f $INPUT_FILE ]] || die "no such file: $INPUT_FILE"

if [[ ! -s $INPUT_FILE ]]; then
  first_chunk="${OUTPUT_PREFIX}.1"
  : >"$first_chunk"
  echo "$first_chunk"
  exit 0
fi

# LC_ALL=C makes awk's length() count raw bytes, not decoded characters, so
# chunk boundaries are correct on multi-byte (UTF-8) content too.
#
# awk runs as a plain command (not inside a process substitution) so its
# exit status is checked directly: a process-substitution failure would
# otherwise go unnoticed and this script would exit 0 with no chunks.
if ! CHUNK_PATHS_RAW="$(
  LC_ALL=C awk -v max="$MAX_BYTES" -v prefix="$OUTPUT_PREFIX" '
    BEGIN { idx = 1; bytes = 0; path = prefix "." idx; paths[idx] = path }
    {
      linebytes = length($0) + 1
      if (linebytes > max) {
        printf "chunk-for-bd-field.sh: error: line %d (%d bytes) exceeds --max-bytes (%d) and cannot be split\n", NR, linebytes, max > "/dev/stderr"
        exit 1
      }
      if (bytes > 0 && bytes + linebytes > max) {
        idx++
        bytes = 0
        path = prefix "." idx
        paths[idx] = path
      }
      print $0 > path
      bytes += linebytes
    }
    END { for (i = 1; i <= idx; i++) print paths[i] }
  ' "$INPUT_FILE"
)"; then
  exit 1
fi
mapfile -t CHUNK_PATHS <<<"$CHUNK_PATHS_RAW"

# Preserve a missing trailing newline on the source's last line: awk's print
# always terminates a record, so if the source did not end in a newline, the
# last chunk has exactly one extra trailing byte to remove.
if [[ "$(tail -c1 "$INPUT_FILE" | wc -l)" -eq 0 ]]; then
  last_chunk="${CHUNK_PATHS[-1]}"
  truncate -s -1 "$last_chunk"
fi

printf '%s\n' "${CHUNK_PATHS[@]}"
