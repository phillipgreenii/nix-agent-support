# shellcheck shell=bash
# pg-disk-reclaimer.bash - core subcommand logic, split out of
# pg-disk-reclaimer.sh so it can be unit-tested without going through
# argument parsing (see the bash-scripting skill's ".sh / .bash split").
#
# Every function here started as a STUB for the scaffold task (bead
# pg2-txxyj.1). Later tasks in the pg2-txxyj epic replace the bodies:
#   - pg2-txxyj.2: registry loading + schema validation engine (see
#     pgdr_default_registry_path / pgdr_validate_registry /
#     pgdr_read_registry below)
#   - pg2-txxyj.3: variant-selection algorithm (this task; see
#     pgdr_select_variants below)
#   - pg2-txxyj.4: cmd_list
#   - pg2-txxyj.5: cmd_validate
#   - pg2-txxyj.6: cmd_reclaim
# Names/signatures for cmd_list/cmd_validate/cmd_reclaim below are
# placeholders; later tasks MAY rename them.

# pgdr_default_registry_path: echoes the default registry file location,
# honoring XDG_CONFIG_HOME with the usual $HOME/.config fallback.
pgdr_default_registry_path() {
  printf '%s\n' "${XDG_CONFIG_HOME:-$HOME/.config}/pg-disk-reclaimer/registry.json"
}

# pgdr_validate_registry: shared schema-validation engine for the registry
# JSON file at PATH.
#
# Interface (deliberately simple, so it suits both current and future
# callers without either having to work around it):
#   pgdr_validate_registry <path>
#   - Checks, IN ORDER, FAILING FAST on the first category that fails:
#       a. the file is valid JSON at all
#       b. every item has a non-empty id/description/path/displayCommand,
#          and id is unique across the whole registry
#       c. every item's variants[] (which MAY legitimately be empty -- that
#          means "informational-only, never reclaimable") has unique,
#          non-negative aggressiveness values within that item, and each
#          variant has a non-empty variantDescription/dryRunCommand/
#          removeCommand
#   - On the first violation found anywhere, prints exactly ONE descriptive
#     message to stderr (naming the failing item by its array index) and
#     returns 1. Prints nothing and returns 0 on success.
#   - Deliberately does NOT print a "registry is valid" success banner:
#     a caller that wants a summary (e.g. a future `validate` subcommand,
#     bead pg2-txxyj.5) can layer its own reporting on top without
#     fighting this function's output; a caller that just wants a
#     fail-loud gate (e.g. pgdr_read_registry below) can use it as a plain
#     condition.
pgdr_validate_registry() {
  local path="$1"
  local err

  # (a) valid JSON at all
  if ! err=$(jq empty "$path" 2>&1); then
    echo "pg-disk-reclaimer: registry '$path' is not valid JSON: $err" >&2
    return 1
  fi

  # (b) root must be a JSON array
  if ! jq -e 'type == "array"' "$path" >/dev/null 2>&1; then
    echo "pg-disk-reclaimer: registry '$path' must contain a JSON array" >&2
    return 1
  fi

  # (b) every item has a non-empty id/description/path/displayCommand
  local bad_item
  bad_item=$(jq -r '
    to_entries
    | map(select(
        [.value.id, .value.description, .value.path, .value.displayCommand]
        | any(. == null or . == "" or type != "string")
      ))
    | .[0].key // ""
  ' "$path")
  if [[ -n $bad_item ]]; then
    echo "pg-disk-reclaimer: registry '$path' item at index $bad_item is missing a non-empty id/description/path/displayCommand" >&2
    return 1
  fi

  # (b) id must be unique across the whole registry
  local dup_ids
  dup_ids=$(jq -r '
    [.[].id] | group_by(.) | map(select(length > 1) | .[0]) | unique | join(", ")
  ' "$path")
  if [[ -n $dup_ids ]]; then
    echo "pg-disk-reclaimer: registry '$path' has duplicate id(s): $dup_ids" >&2
    return 1
  fi

  # (c) every variant has a non-empty variantDescription/dryRunCommand/
  # removeCommand and a non-negative aggressiveness. An empty variants[]
  # array is valid (informational-only item) and matches nothing here.
  local bad_variant
  bad_variant=$(jq -r '
    to_entries
    | map(select(
        (.value.variants // [])
        | any(
            ([.variantDescription, .dryRunCommand, .removeCommand] | any(. == null or . == "" or type != "string"))
            or (.aggressiveness == null)
            or ((.aggressiveness | type) != "number")
            or (.aggressiveness < 0)
          )
      ))
    | .[0].key // ""
  ' "$path")
  if [[ -n $bad_variant ]]; then
    echo "pg-disk-reclaimer: registry '$path' item at index $bad_variant has an invalid variant (missing non-empty variantDescription/dryRunCommand/removeCommand, or a missing/non-numeric/negative aggressiveness)" >&2
    return 1
  fi

  # (c) aggressiveness must be unique within one item's variants
  local dup_aggr
  dup_aggr=$(jq -r '
    to_entries
    | map(select(
        (((.value.variants // []) | map(.aggressiveness) | group_by(.) | map(select(length > 1))) | length) > 0
      ))
    | .[0].key // ""
  ' "$path")
  if [[ -n $dup_aggr ]]; then
    echo "pg-disk-reclaimer: registry '$path' item at index $dup_aggr has duplicate variant aggressiveness values" >&2
    return 1
  fi

  return 0
}

# pgdr_read_registry: reads (via jq) and validates the registry JSON at
# PATH (default: pgdr_default_registry_path). This is the load path used by
# cmd_list/cmd_reclaim (pg2-txxyj.4/.6): it fails loudly (non-zero exit,
# descriptive stderr message courtesy of pgdr_validate_registry) on a
# malformed registry rather than silently skipping bad items, and prints
# the jq-normalized registry JSON to stdout on success.
pgdr_read_registry() {
  local path="${1:-$(pgdr_default_registry_path)}"

  if [[ ! -f $path ]]; then
    echo "pg-disk-reclaimer: registry file not found: $path" >&2
    return 1
  fi

  if ! pgdr_validate_registry "$path"; then
    return 1
  fi

  jq '.' "$path"
}

# pgdr_select_variants: selects which reclaim variant to use for each
# candidate item in an ALREADY-VALIDATED registry at PATH, given a ceiling
# MAX_AGGRESSIVENESS. This function does NOT re-validate the registry --
# validation (pgdr_validate_registry / pgdr_read_registry) is the caller's
# job, done once before selection, so this function reads the registry
# directly with jq rather than paying that cost again.
#
# Usage: pgdr_select_variants <registry-path> <max-aggressiveness> [id...]
#
# With no ids (Case A): selects every item that has at least one variant
# with aggressiveness <= MAX_AGGRESSIVENESS, choosing -- for each selected
# item -- the variant with the HIGHEST aggressiveness <= MAX_AGGRESSIVENESS
# (never just any qualifying variant). Items with no qualifying variant,
# including a zero-variant (informational-only) item, are silently
# EXCLUDED; an empty result ([]) is valid success.
#
# With one or more explicit ids (Case B): processes ONLY those ids. Each
# requested id is checked, IN THE ORDER GIVEN ON THE COMMAND LINE, for three
# failure categories -- unknown id, informational-only (empty variants[]),
# and every variant's aggressiveness exceeding MAX_AGGRESSIVENESS -- and
# this function fails fast (one message to stderr, no stdout, return 1) on
# the FIRST id that hits any of them. If every requested id passes, the
# selection output is built in REGISTRY order (not command-line order),
# using the same "highest qualifying variant" rule as Case A.
#
# On success, prints a JSON array to stdout: one flattened object per
# selected item -- the item's own id/description/path plus the CHOSEN
# variant's aggressiveness/variantDescription/dryRunCommand/removeCommand
# merged in directly (no nested variants[], no displayCommand). Returns 0.
pgdr_select_variants() {
  local path="$1"
  local max_aggressiveness="$2"
  shift 2

  # Case A: no ids -- select every item with at least one qualifying
  # variant, silently excluding items with none (including zero-variant
  # informational-only items). An empty result ([]) is valid success.
  if [[ $# -eq 0 ]]; then
    jq --argjson n "$max_aggressiveness" '
      [
        .[]
        | . as $item
        | ($item.variants // []) as $variants
        | ($variants | map(select(.aggressiveness <= $n))) as $qualifying
        | select(($qualifying | length) > 0)
        | ($qualifying | max_by(.aggressiveness)) as $chosen
        | ($item | {id, description, path})
          + ($chosen | {aggressiveness, variantDescription, dryRunCommand, removeCommand})
      ]
    ' "$path"
    return 0
  fi

  # Case B: explicit ids. Check every requested id, IN THE ORDER GIVEN ON
  # THE COMMAND LINE, before producing any output -- fail fast (one stderr
  # message, no stdout, return 1) on the first one that is unknown,
  # informational-only, or entirely above the aggressiveness ceiling.
  local id
  for id in "$@"; do
    local item_json
    item_json=$(jq --arg id "$id" '[.[] | select(.id == $id)][0] // empty' "$path")
    if [[ -z $item_json ]]; then
      echo "pg-disk-reclaimer: unknown item id '$id'" >&2
      return 1
    fi

    if [[ $(jq '(.variants // []) | length' <<<"$item_json") -eq 0 ]]; then
      echo "pg-disk-reclaimer: item '$id' is informational-only (no variants) and cannot be selected" >&2
      return 1
    fi

    if [[ $(jq --argjson n "$max_aggressiveness" '(.variants | map(.aggressiveness) | min) <= $n' <<<"$item_json") != "true" ]]; then
      local min_aggressiveness
      min_aggressiveness=$(jq '.variants | map(.aggressiveness) | min' <<<"$item_json")
      echo "pg-disk-reclaimer: item '$id' requires aggressiveness >= $min_aggressiveness, but --aggressiveness $max_aggressiveness was given" >&2
      return 1
    fi
  done

  local ids_json
  ids_json=$(printf '%s\n' "$@" | jq -R . | jq -s .)

  jq --argjson n "$max_aggressiveness" --argjson ids "$ids_json" '
    [
      .[]
      | select(.id as $id | $ids | index($id) != null)
      | . as $item
      | ($item.variants // []) as $variants
      | ($variants | map(select(.aggressiveness <= $n))) as $qualifying
      | select(($qualifying | length) > 0)
      | ($qualifying | max_by(.aggressiveness)) as $chosen
      | ($item | {id, description, path})
        + ($chosen | {aggressiveness, variantDescription, dryRunCommand, removeCommand})
    ]
  ' "$path"
}

# cmd_list: implements the `list` subcommand. Args: any list-specific
# options/positionals (e.g. --aggressiveness), already stripped of the
# "list" token itself.
cmd_list() {
  echo "pg-disk-reclaimer: 'list' is not implemented yet" >&2
  return 1
}

# cmd_validate: implements the `validate` subcommand. Args: an optional
# registry path positional.
cmd_validate() {
  echo "pg-disk-reclaimer: 'validate' is not implemented yet" >&2
  return 1
}

# cmd_reclaim: implements the `reclaim` subcommand. Args: any
# reclaim-specific options/positionals (e.g. --aggressiveness, --apply,
# item ids), already stripped of the "reclaim" token itself.
cmd_reclaim() {
  echo "pg-disk-reclaimer: 'reclaim' is not implemented yet" >&2
  return 1
}
