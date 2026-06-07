#!/usr/bin/env bash
# activation.sh — write managed import blocks into a city's pack.toml and/or
# city.toml. Called from home/programs/pgii-packs/default.nix during
# home-manager activation.
#
# Inputs:
#   --cities '<JSON array of city paths>'
#   --packs  '<JSON object: { "<pack-name>": "<store-path>", ... }>'
#   --reload  (optional: run `gc supervisor reload` per city if its
#              controller.sock exists and gc is on PATH)
#
# Marker formats written/managed per city:
#
#   City-scope pack → <city>/pack.toml:
#   # BEGIN pgii-pack:<pack-name> (managed)
#   [imports.<pack-name>]
#   source = "/nix/store/..."
#   export = true
#   # END pgii-pack:<pack-name> (managed)
#
#   Rig-scope pack → <city>/city.toml:
#   # BEGIN pgii-pack:<pack-name> (managed)
#   [defaults.rig.imports.<pack-name>]
#   source = "/nix/store/..."
#   export = true
#   # END pgii-pack:<pack-name> (managed)
#
# Why two target files (gascity 1.2.x split):
#   - City-scope imports stay in <city>/pack.toml. gascity treats
#     [packs.<name>] in city.toml as a remote git source; local file-system
#     imports go through [imports.<name>] in the top-level pack.toml.
#   - Rig-scope imports MUST live in <city>/city.toml. gascity 1.2.x rejects
#     [defaults.rig.imports.<name>] in pack.toml outright
#     ("[defaults.rig.imports] belongs in city.toml, not pack.toml (keeping
#     old config)"), which breaks gc bd / order parsing city-wide. The same
#     block in city.toml parses cleanly ("Config reloaded"). Verified
#     empirically + via the 1.2.1 binary strings.
#
# Earlier (gascity 1.1.0) both scopes lived in pack.toml; this script now
# routes by scope. The managed-block sentinels are identical in both files,
# so we only ever add/replace/remove our own
# `# BEGIN/END pgii-pack:<name> (managed)` blocks and never disturb
# hand-written config (city.toml's [workspace], [[rigs]], [orders], etc.).
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

# no_op_needed <target-file> [<pack-name>...]
# Return 0 if <target-file> already holds EXACTLY the desired set of managed
# pgii-pack:* blocks (the trailing pack-name args), each pointing at the
# current source path with the correct scope header — and nothing else. Any
# stray managed block (e.g. a rig-scope block lingering in pack.toml from the
# 1.1.0 layout, or a city-scope block in city.toml) forces a rewrite so the
# strip-and-re-emit step can relocate/remove it.
no_op_needed() {
  local target="$1"
  shift
  local -a want_names=("$@")

  # Names of ALL pgii-pack:* managed blocks currently in the file.
  local -a current_names=()
  while IFS= read -r line; do
    current_names+=("$line")
  done < <(grep -oE '^# BEGIN pgii-pack:[^ ]+ \(managed\)$' "$target" |
    sed -E 's/^# BEGIN pgii-pack:(.+) \(managed\)$/\1/' | sort -u)

  # Names of packs we want present in this target.
  local -a desired_names=()
  if [ "${#want_names[@]}" -gt 0 ]; then
    while IFS= read -r line; do desired_names+=("$line"); done < <(printf '%s\n' "${want_names[@]}" | sort -u)
  fi

  # Set equality check.
  [ "${#current_names[@]}" -eq "${#desired_names[@]}" ] || return 1
  local i
  for i in "${!current_names[@]}"; do
    [ "${current_names[$i]}" = "${desired_names[$i]}" ] || return 1
  done

  # Per-pack source and scope check.
  [ "${#want_names[@]}" -gt 0 ] || return 0
  local name
  for name in "${want_names[@]}"; do
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

# process_target <target-file> [<pack-name>...]
# Bring <target-file> into the desired state for the packs routed to it.
# All four phases below operate ONLY on the passed-in pack subset and on our
# own managed sentinels; hand-written content is never touched.
#
# The caller is responsible for deciding whether <target-file> should be
# processed at all (and for creating an empty one when appropriate). This
# function assumes <target-file> already exists.
process_target() {
  local target="$1"
  shift
  local -a names=("$@")

  # Fast path: if the file already holds exactly our desired managed blocks
  # (same set, sources and scope headers) and nothing else, do nothing. This
  # keeps `home-manager switch` from rewriting the file on every no-op rebuild.
  if no_op_needed "$target" "${names[@]}"; then
    return 0
  fi

  # Pre-flight: for each pack we want to write, refuse if the relevant import
  # key exists in the file but is NOT bracketed by our managed sentinels.
  # City-scope packs use [imports.<name>]; rig-scope packs use
  # [defaults.rig.imports.<name>].
  local name
  for name in "${names[@]}"; do
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
    if grep -Eq "^\[${toml_key_re}\]\$" "$target"; then
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
      ' "$target")

      if [ "$inside_managed" = "-1" ]; then
        echo "pgii-packs: ERROR: Hand-written [$toml_key] exists in $target" >&2
        echo "  Either rename or delete the hand-written block, or remove" >&2
        echo "  phillipgreenii.programs.pgii.packs.$name from your config." >&2
        exit 3
      fi
    fi
  done

  local tmp
  tmp="$(mktemp "$target.XXXXXX")"

  # Strip ALL managed pgii-pack:* blocks (including any block for a pack that
  # belongs in the OTHER target — e.g. a rig block lingering in pack.toml from
  # the 1.1.0 layout). We re-emit only the desired ones below, which gives us
  # removal-on-disable and relocation-on-scope-change for free. Only lines
  # between our sentinels are removed; hand-written content is preserved.
  awk '
    /^# BEGIN pgii-pack:.* \(managed\)$/ { in_block = 1; next }
    in_block && /^# END pgii-pack:.* \(managed\)$/ { in_block = 0; next }
    !in_block { print }
  ' "$target" >"$tmp"

  # Trim trailing blank lines so we do not accumulate them on each rewrite.
  # This only ever drops trailing empty lines; non-blank hand-written content
  # (and interior blank lines) is preserved verbatim.
  awk '
    /^$/ { blanks++; next }
    { while (blanks-- > 0) print ""; blanks = 0; print }
  ' "$tmp" >"$tmp.trim"
  mv "$tmp.trim" "$tmp"

  # Append fresh blocks for the packs routed to this target.
  for name in "${names[@]}"; do
    local path="${PACKS[$name]}"
    emit_block "$name" "$path" >>"$tmp"
  done

  mv "$tmp" "$target"
}

# file_has_managed_block <file> → exit 0 if the file exists and contains at
# least one `# BEGIN pgii-pack:* (managed)` sentinel.
file_has_managed_block() {
  local f="$1"
  [ -f "$f" ] && grep -Eq '^# BEGIN pgii-pack:[^ ]+ \(managed\)$' "$f"
}

# Process a single city: route packs to pack.toml (city-scope) and city.toml
# (rig-scope) and reconcile each file independently.
process_city() {
  local city="$1"
  local pack_toml="$city/pack.toml"
  local city_toml="$city/city.toml"

  # Partition the enabled packs by scope.
  local -a city_scope=() rig_scope=()
  local name scope
  if [ "${#PACK_NAMES[@]}" -gt 0 ]; then
    for name in "${PACK_NAMES[@]}"; do
      scope="$(pack_scope "${PACKS[$name]}")"
      case "$scope" in
      rig) rig_scope+=("$name") ;;
      city) city_scope+=("$name") ;;
      *)
        echo "pgii-packs: ERROR: pack '$name' has unsupported scope '$scope'" >&2
        exit 4
        ;;
      esac
    done
  fi

  # --- pack.toml (city-scope) ---
  # Always processed: even with no city-scope packs it must strip leftover
  # managed blocks (the packs-enabled → none transition). Preserve the
  # historical "create an empty pack.toml if missing" behavior.
  mkdir -p "$(dirname "$pack_toml")"
  if [ ! -f "$pack_toml" ]; then
    : >"$pack_toml"
  fi
  process_target "$pack_toml" "${city_scope[@]}"

  # --- city.toml (rig-scope) ---
  # city.toml is a hand-written config. Only touch it when there is real work:
  #   - rig-scope packs to write, OR
  #   - an existing city.toml that still carries a managed block we must strip
  #     (removal-on-disable / relocation of the last rig pack).
  # Never CREATE city.toml just to leave it empty — a pack.toml-only city does
  # not get a city.toml conjured from nothing.
  if [ "${#rig_scope[@]}" -gt 0 ]; then
    if [ ! -f "$city_toml" ]; then
      # Unusual: a city with rigs should already have a city.toml. Create it
      # so the rig imports have somewhere to live.
      : >"$city_toml"
    fi
    process_target "$city_toml" "${rig_scope[@]}"
  elif file_has_managed_block "$city_toml"; then
    # No rig packs this rebuild, but stale managed blocks remain — strip them.
    process_target "$city_toml"
  fi
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
