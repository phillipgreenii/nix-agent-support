#!/usr/bin/env bash
# activation.sh — write managed import blocks into one or more
# <city>/pack.toml files. Called from home/programs/pgii-packs/default.nix
# during home-manager activation.
#
# Inputs:
#   --cities '<JSON array of city paths>'
#   --packs  '<JSON object: { "<pack-name>": "<store-path>", ... }>'
#   --reload  (optional: run `gc supervisor reload` per city if its
#              controller.sock exists and gc is on PATH)
#
# Marker format written/managed in <city>/pack.toml:
#
#   City-scope pack:
#   # BEGIN pgii-pack:<pack-name> (managed)
#   [imports.<pack-name>]
#   source = "/nix/store/..."
#   export = true
#   # END pgii-pack:<pack-name> (managed)
#
#   Rig-scope pack:
#   # BEGIN pgii-pack:<pack-name> (managed)
#   [defaults.rig.imports.<pack-name>]
#   source = "/nix/store/..."
#   export = true
#   # END pgii-pack:<pack-name> (managed)
#
# Why pack.toml and not city.toml: gascity treats [packs.<name>] in city.toml
# as a remote git source. Local file-system imports go through
# [imports.<name>] in the city's top-level pack.toml. Verified empirically
# against gascity 1.1.0.
#
# Idempotent. Re-running with the same args is a no-op.

set -euo pipefail

CITIES_JSON=""
PACKS_JSON=""
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

# Validate JSON inputs early so a malformed arg never reaches pack.toml.
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

# pack_scope <store-path>
# Echoes "city" (default) or "rig" based on the pack's .pack-meta.json.
# Falls back to "city" if the meta file is absent (older packs built
# before mkPgiiPack started embedding scope; defensive default keeps
# behavior unchanged for them).
pack_scope() {
  local store_path="$1"
  local meta="$store_path/.pack-meta.json"
  if [ -f "$meta" ]; then
    jq -r '.scope // "city"' "$meta"
  else
    echo "city"
  fi
}

# Emit one managed block to stdout for a given pack.
emit_block() {
  local name="$1" path="$2"
  local scope
  scope="$(pack_scope "$path")"
  local header
  case "$scope" in
  rig) header="[defaults.rig.imports.$name]" ;;
  city) header="[imports.$name]" ;;
  *)
    echo "pgii-packs: ERROR: pack '$name' has unsupported scope '$scope' (expected city|rig)" >&2
    exit 4
    ;;
  esac
  cat <<EOF

# BEGIN pgii-pack:$name (managed)
$header
source = "$path"
export = true
# END pgii-pack:$name (managed)
EOF
}

# Return 0 if pack.toml is already in the desired state for our pack set.
no_op_needed() {
  local target="$1"

  # Names of pgii-pack:* blocks currently in the file.
  local -a current_names=()
  while IFS= read -r line; do
    current_names+=("$line")
  done < <(grep -oE '^# BEGIN pgii-pack:[^ ]+ \(managed\)$' "$target" |
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

  # Per-pack source and scope check.
  for name in "${PACK_NAMES[@]}"; do
    local got_source want_source got_header want_header scope
    got_source=$(awk -v begin="# BEGIN pgii-pack:$name (managed)" -v end="# END pgii-pack:$name (managed)" '
      $0 == begin { in_block = 1; next }
      $0 == end   { in_block = 0; next }
      in_block && /^source = / { gsub(/(^source = "|"$)/, ""); print; exit }
    ' "$target")
    want_source="${PACKS[$name]}"
    [ "$got_source" = "$want_source" ] || return 1

    # Also verify the header type matches the pack's current scope.
    scope="$(pack_scope "${PACKS[$name]}")"
    case "$scope" in
    rig) want_header="[defaults.rig.imports.$name]" ;;
    *) want_header="[imports.$name]" ;;
    esac
    got_header=$(awk -v begin="# BEGIN pgii-pack:$name (managed)" -v end="# END pgii-pack:$name (managed)" '
      $0 == begin { in_block = 1; next }
      $0 == end   { in_block = 0; next }
      in_block && /^\[/ { print; exit }
    ' "$target")
    [ "$got_header" = "$want_header" ] || return 1
  done

  return 0
}

# Process a single <city>/pack.toml in-place.
process_city() {
  local city="$1"
  local pack_toml="$city/pack.toml"

  mkdir -p "$(dirname "$pack_toml")"
  if [ ! -f "$pack_toml" ]; then
    : >"$pack_toml"
  fi

  # Fast path: if every pack we'd write is already present with the same
  # source path, and the set of currently-managed pgii blocks equals our
  # target set, do nothing. This keeps `home-manager switch` from rewriting
  # pack.toml on every no-op rebuild.
  if no_op_needed "$pack_toml"; then
    return 0
  fi

  # Pre-flight: for each pack we want to write, refuse if the relevant
  # import key exists in the file but is NOT bracketed by our managed sentinels.
  # City-scope packs use [imports.<name>]; rig-scope packs use
  # [defaults.rig.imports.<name>].
  for name in "${PACK_NAMES[@]}"; do
    local store_path scope toml_key toml_key_re
    store_path="${PACKS[$name]}"
    scope="$(pack_scope "$store_path")"
    case "$scope" in
    rig) toml_key="defaults.rig.imports.$name" ;;
    city) toml_key="imports.$name" ;;
    *)
      echo "pgii-packs: ERROR: pack '$name' has unsupported scope '$scope'" >&2
      exit 4
      ;;
    esac
    # Escape dots for regex matching.
    toml_key_re="${toml_key//./\\.}"

    # Does the file declare [<toml_key>] anywhere?
    if grep -Eq "^\[${toml_key_re}\]\$" "$pack_toml"; then
      # Is that declaration inside a managed block? Walk the file.
      local inside_managed
      inside_managed=$(awk -v name="$name" -v key="$toml_key" '
        BEGIN { in_block = 0; found = 0 }
        $0 == "# BEGIN pgii-pack:" name " (managed)" { in_block = 1; next }
        $0 == "# END pgii-pack:" name " (managed)"   { in_block = 0; next }
        $0 == "[" key "]" {
          if (in_block) { found = 1 }
          else { found = -1; exit }
        }
        END { print found }
      ' "$pack_toml")

      if [ "$inside_managed" = "-1" ]; then
        echo "pgii-packs: ERROR: Hand-written [$toml_key] exists in $pack_toml" >&2
        echo "  Either rename or delete the hand-written block, or remove" >&2
        echo "  phillipgreenii.programs.pgii.packs.$name from your config." >&2
        exit 3
      fi
    fi
  done

  local tmp
  tmp="$(mktemp "$pack_toml.XXXXXX")"

  # Strip all managed pgii-pack:* blocks. We re-emit only the ones we
  # want below, which gives us removal-on-disable for free.
  awk '
    /^# BEGIN pgii-pack:.* \(managed\)$/ { in_block = 1; next }
    in_block && /^# END pgii-pack:.* \(managed\)$/ { in_block = 0; next }
    !in_block { print }
  ' "$pack_toml" >"$tmp"

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

  mv "$tmp" "$pack_toml"
}

for city in "${CITIES[@]}"; do
  process_city "$city"
done

maybe_reload_city() {
  local city="$1"
  local sock="$city/.gc/controller.sock"

  if [ ! -S "$sock" ] && [ ! -f "$sock" ]; then
    return 0
  fi
  if ! command -v gc >/dev/null 2>&1; then
    echo "pgii-packs: WARN: $city has controller.sock but \`gc\` not on PATH; skipping reload" >&2
    return 0
  fi

  if ! gc --city "$city" supervisor reload; then
    echo "pgii-packs: WARN: \`gc --city $city supervisor reload\` failed; the next manual reload will pick up the changes" >&2
  fi
}

if [ "$RELOAD" -eq 1 ]; then
  for city in "${CITIES[@]}"; do
    maybe_reload_city "$city"
  done
fi

exit 0
