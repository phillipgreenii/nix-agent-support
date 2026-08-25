# shellcheck shell=bash
# pg-disk-reclaimer.bash - core subcommand logic, split out of
# pg-disk-reclaimer.sh so it can be unit-tested without going through
# argument parsing (see the bash-scripting skill's ".sh / .bash split").
#
# Every function here started as a STUB for the scaffold task (bead
# pg2-txxyj.1). Later tasks in the pg2-txxyj epic replace the bodies:
#   - pg2-txxyj.2: registry loading + schema validation engine (this task;
#     see pgdr_default_registry_path / pgdr_validate_registry /
#     pgdr_read_registry below)
#   - pg2-txxyj.3: variant-selection algorithm
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
