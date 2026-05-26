#!/usr/bin/env bash
# activation.sh — write managed [packs.<name>] blocks into one or more
# city.toml files. Called from home/programs/pgii-packs/default.nix during
# home-manager activation.
#
# Inputs:
#   --cities '<JSON array of city paths>'
#   --packs  '<JSON object: { "<pack-name>": "<store-path>", ... }>'
#   --reload  (optional: run `gc supervisor reload` per city if its
#              controller.sock exists and gc is on PATH)
#
# Marker format written/managed:
#
#   # BEGIN pgii-pack:<pack-name> (managed)
#   [packs.<pack-name>]
#   path = "/nix/store/..."
#   # END pgii-pack:<pack-name> (managed)
#
# Idempotent. Re-running with the same args is a no-op.

set -euo pipefail

CITIES_JSON=""
PACKS_JSON=""
# shellcheck disable=SC2034
RELOAD=0

usage() {
  cat >&2 <<EOF
usage: $0 --cities '<json>' --packs '<json>' [--reload]
EOF
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
  --cities)
    shift
    CITIES_JSON="${1:-}"
    [ -n "$CITIES_JSON" ] || {
      echo "--cities requires a value" >&2
      usage
    }
    shift
    ;;
  --packs)
    shift
    PACKS_JSON="${1:-}"
    [ -n "$PACKS_JSON" ] || {
      echo "--packs requires a value" >&2
      usage
    }
    shift
    ;;
  --reload)
    # shellcheck disable=SC2034
    RELOAD=1
    shift
    ;;
  -h | --help) usage ;;
  *)
    echo "unknown argument: $1" >&2
    usage
    ;;
  esac
done

[ -n "$CITIES_JSON" ] || {
  echo "missing --cities" >&2
  usage
}
[ -n "$PACKS_JSON" ] || {
  echo "missing --packs" >&2
  usage
}

# Validate JSON inputs early so a malformed arg never reaches city.toml.
jq -e 'type == "array"' <<<"$CITIES_JSON" >/dev/null || {
  echo "--cities must be a JSON array" >&2
  exit 2
}
jq -e 'type == "object"' <<<"$PACKS_JSON" >/dev/null || {
  echo "--packs must be a JSON object" >&2
  exit 2
}

# Parse into bash-native structures.
mapfile -t CITIES < <(jq -r '.[]' <<<"$CITIES_JSON")
declare -A PACKS
while IFS=$'\t' read -r name path; do
  PACKS["$name"]="$path"
done < <(jq -r 'to_entries[] | [.key, .value] | @tsv' <<<"$PACKS_JSON")

PACK_NAMES=("${!PACKS[@]}")

# Emit one managed block to stdout for a given pack.
emit_block() {
  local name="$1" path="$2"
  cat <<EOF

# BEGIN pgii-pack:$name (managed)
[packs.$name]
path = "$path"
# END pgii-pack:$name (managed)
EOF
}

# Return 0 if city.toml is already in the desired state for our pack set.
no_op_needed() {
  local city_toml="$1"

  # Names of pgii-pack:* blocks currently in the file.
  local -a current_names=()
  while IFS= read -r line; do
    current_names+=("$line")
  done < <(grep -oE '^# BEGIN pgii-pack:[^ ]+ \(managed\)$' "$city_toml" |
    sed -E 's/^# BEGIN pgii-pack:(.+) \(managed\)$/\1/' | sort -u)

  # Names of packs we want present.
  local -a desired_names=()
  while IFS= read -r line; do desired_names+=("$line"); done < <(printf '%s\n' "${PACK_NAMES[@]}" | sort -u)

  # Set equality check.
  [ "${#current_names[@]}" -eq "${#desired_names[@]}" ] || return 1
  local i
  for i in "${!current_names[@]}"; do
    [ "${current_names[$i]}" = "${desired_names[$i]}" ] || return 1
  done

  # Per-pack path check.
  for name in "${PACK_NAMES[@]}"; do
    local got want
    got=$(awk '
      BEGIN {
        begin = "# BEGIN pgii-pack:'"$name"' (managed)"
        end = "# END pgii-pack:'"$name"' (managed)"
      }
      $0 == begin { in_block = 1; next }
      $0 == end   { in_block = 0; next }
      in_block && /^path = / { gsub(/(^path = "|"$)/, ""); print; exit }
    ' "$city_toml")
    want="${PACKS[$name]}"
    [ "$got" = "$want" ] || return 1
  done

  return 0
}

# Process a single city.toml in-place.
process_city() {
  local city="$1"
  local city_toml="$city/city.toml"

  mkdir -p "$(dirname "$city_toml")"
  if [ ! -f "$city_toml" ]; then
    : >"$city_toml"
  fi

  # Fast path: if every pack we'd write is already present with the same
  # path, and the set of currently-managed pgii blocks equals our target
  # set, do nothing. This keeps `home-manager switch` from rewriting
  # city.toml on every no-op rebuild.
  if no_op_needed "$city_toml"; then
    return 0
  fi

  local tmp
  tmp="$(mktemp "$city_toml.XXXXXX")"

  # Strip all managed blocks belonging to packs we're about to write.
  # awk reads the file once, suppressing lines between BEGIN and END markers
  # for any pack in $PACK_NAMES. Other managed blocks (for packs we're not
  # currently managing) pass through untouched.
  local pack_pattern
  pack_pattern="$(printf '%s|' "${PACK_NAMES[@]}")"
  pack_pattern="${pack_pattern%|}"

  awk -v pack_pattern="$pack_pattern" '
    BEGIN {
      pattern = "^# BEGIN pgii-pack:(" pack_pattern ") \\(managed\\)$"
      end_pattern = "^# END pgii-pack:(" pack_pattern ") \\(managed\\)$"
    }
    $0 ~ pattern     { in_block = 1; next }
    in_block && $0 ~ end_pattern { in_block = 0; next }
    !in_block        { print }
  ' "$city_toml" >"$tmp"

  # Trim trailing blank lines so we do not accumulate them on each rewrite.
  awk '
    /^$/ { blanks++; next }
    { while (blanks-- > 0) print ""; blanks = 0; print }
  ' "$tmp" >"$tmp.trim"
  mv "$tmp.trim" "$tmp"

  # Append fresh blocks.
  for name in "${PACK_NAMES[@]}"; do
    local path="${PACKS[$name]}"
    emit_block "$name" "$path" >>"$tmp"
  done

  mv "$tmp" "$city_toml"
}

for city in "${CITIES[@]}"; do
  process_city "$city"
done

exit 0
