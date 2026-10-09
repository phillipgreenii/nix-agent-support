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
#   - pg2-txxyj.4: cmd_list (this task; see cmd_list below) -- its
#     name/signature are now final, not a placeholder
#   - pg2-txxyj.5: cmd_validate (this task; see cmd_validate /
#     pgdr_command_exists / pgdr_validate_commands_exist below) -- its
#     name/signature are now final, not a placeholder
#   - pg2-txxyj.6: cmd_reclaim (this task; see cmd_reclaim / pgdr_confirm
#     below) -- its name/signature are now final, not a placeholder

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
#          id is unique across the whole registry, and the OPTIONAL
#          displayTimeoutSeconds (per-item override of the list display
#          ceiling, see PGDR_DISPLAY_TIMEOUT_SECONDS) is, when present, a
#          positive integer
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

  # (b) sizeCommand is optional (bead pg2-es6fn); when present it MUST be a
  # non-empty string (a shell command that prints the reclaimable size in
  # KiB as its first field -- see pgdr_size_kb).
  local bad_size_command
  bad_size_command=$(jq -r '
    to_entries
    | map(select(.value | has("sizeCommand") and (((.sizeCommand | type) != "string") or .sizeCommand == "")))
    | .[0].key // ""
  ' "$path")
  if [[ -n $bad_size_command ]]; then
    echo "pg-disk-reclaimer: registry '$path' item at index $bad_size_command has an invalid sizeCommand (must be a non-empty string when present)" >&2
    return 1
  fi

  # (b) displayTimeoutSeconds is optional; when the key is present it MUST
  # be a positive integer (a JSON number, not a string/bool/null, > 0, with
  # no fractional part).
  local bad_timeout
  bad_timeout=$(jq -r '
    to_entries
    | map(select(
        .value | has("displayTimeoutSeconds") and (
          .displayTimeoutSeconds
          | (type != "number") or (. <= 0) or (. != floor)
        )
      ))
    | .[0].key // ""
  ' "$path")
  if [[ -n $bad_timeout ]]; then
    echo "pg-disk-reclaimer: registry '$path' item at index $bad_timeout has an invalid displayTimeoutSeconds (must be a positive integer)" >&2
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
# and every variant's aggressiveness exceeding MAX_AGGRESSIVENESS. A bad id
# is reported (one stderr message each, in command-line order) and then
# SKIPPED -- it does NOT stop the other requested ids from being checked and
# selected (bug fix, bead pg2-qt7ep: the previous fail-fast-on-first-bad-id
# behavior meant one bad id among several valid ones produced no selection
# at all, so `reclaim --aggressiveness N id1 bad-id id2` never attempted
# id1 or id2 either). The selection output for every GOOD id is built in
# REGISTRY order (not command-line order), using the same "highest
# qualifying variant" rule as Case A. If ANY requested id was bad, this
# function still prints the good ids' selection to stdout but returns 1, so
# a caller (cmd_reclaim) can still attempt every good id while the overall
# exit status reflects the partial failure.
#
# On success (every requested id good, or no ids given), prints a JSON
# array to stdout: one flattened object per selected item -- the item's own
# id/description/path plus the CHOSEN variant's
# aggressiveness/variantDescription/dryRunCommand/removeCommand merged in
# directly (no nested variants[], no displayCommand). Returns 0.
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
          + (if $item.sizeCommand then {sizeCommand: $item.sizeCommand} else {} end)
          + ($chosen | {aggressiveness, variantDescription, dryRunCommand, removeCommand})
      ]
    ' "$path"
    return 0
  fi

  # Case B: explicit ids. Check every requested id, IN THE ORDER GIVEN ON
  # THE COMMAND LINE -- a bad one (unknown, informational-only, or entirely
  # above the aggressiveness ceiling) gets its own stderr message and is
  # SKIPPED, but does NOT stop the remaining requested ids from being
  # checked (bug fix, bead pg2-qt7ep -- see the doc comment above).
  local id
  local -a good_ids=()
  local had_bad_id=0
  for id in "$@"; do
    local item_json
    item_json=$(jq --arg id "$id" '[.[] | select(.id == $id)][0] // empty' "$path")
    if [[ -z $item_json ]]; then
      echo "pg-disk-reclaimer: unknown item id '$id'" >&2
      had_bad_id=1
      continue
    fi

    if [[ $(jq '(.variants // []) | length' <<<"$item_json") -eq 0 ]]; then
      echo "pg-disk-reclaimer: item '$id' is informational-only (no variants) and cannot be selected" >&2
      had_bad_id=1
      continue
    fi

    if [[ $(jq --argjson n "$max_aggressiveness" '(.variants | map(.aggressiveness) | min) <= $n' <<<"$item_json") != "true" ]]; then
      local min_aggressiveness
      min_aggressiveness=$(jq '.variants | map(.aggressiveness) | min' <<<"$item_json")
      echo "pg-disk-reclaimer: item '$id' requires aggressiveness >= $min_aggressiveness, but --aggressiveness $max_aggressiveness was given" >&2
      had_bad_id=1
      continue
    fi

    good_ids+=("$id")
  done

  local ids_json
  if [[ ${#good_ids[@]} -eq 0 ]]; then
    ids_json='[]'
  else
    ids_json=$(printf '%s\n' "${good_ids[@]}" | jq -R . | jq -s .)
  fi

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
        + (if $item.sizeCommand then {sizeCommand: $item.sizeCommand} else {} end)
        + ($chosen | {aggressiveness, variantDescription, dryRunCommand, removeCommand})
    ]
  ' "$path"

  if [[ $had_bad_id -eq 1 ]]; then
    return 1
  fi
  return 0
}

# pgdr_path_exists: true if PATH (a trusted, operator-authored registry `path`
# string, e.g. "~/.cache/uv") currently exists on disk. Deliberately
# unquoted inside eval -- this is what lets `~` expand; quoting $1 here
# would silently break tilde expansion and make every ~-based item look
# nonexistent. Same trust model as this file's other eval usages
# (dryRunCommand/removeCommand): registry data, not user input.
pgdr_path_exists() {
  eval "[[ -e $1 ]]" 2>/dev/null
}

# PGDR_DISPLAY_TIMEOUT_SECONDS: per-item ceiling (wall-clock seconds) on how
# long cmd_list will wait on one item's displayCommand. Overridable via the
# environment (tests use a short value so a deliberately-slow fixture
# doesn't make the suite slow). A single displayCommand -- e.g. `du` over a
# large/networked/permission-restricted volume -- has been observed to take
# 1-2 minutes on its own; this bounds that cost per item instead of letting
# one pathological item dominate the whole listing. An item MAY override this
# ceiling with its own registry field displayTimeoutSeconds (a positive
# integer, enforced by pgdr_validate_registry) -- for a known-slow item that
# is worth waiting on; items without the field keep this global ceiling.
: "${PGDR_DISPLAY_TIMEOUT_SECONDS:=10}"

# pgdr_display_output: runs displayCommand under a timeout ceiling -- the
# optional second argument (the item's displayTimeoutSeconds), else
# PGDR_DISPLAY_TIMEOUT_SECONDS -- and ALWAYS prints something usable to stdout, returning 0
# regardless of what displayCommand did -- a display command is purely
# informational (see cmd_list below), so its failure or slowness must never
# stop or abort the listing of other items.
#
# `timeout` needs an actual child process to police, so this runs the
# (trusted, operator-authored) command string via `bash -c` rather than
# `eval` in the current shell -- `eval` has no separate process for
# `timeout` to kill, and a bare `$(eval "$cmd")` assignment is exactly what
# used to make one failing displayCommand (a `du` that hit a
# permission-denied subdirectory) abort the ENTIRE script: this function
# runs under the nix wrapper's `set -euo pipefail`, where a failing command
# substitution used as a plain assignment is fatal, so the exit status is
# always captured and checked explicitly here rather than left to trigger
# that.
#
# stdout and stderr are captured together: on failure/timeout the command's
# own partial output (e.g. `du`'s real total alongside its permission
# warnings) is still shown, labeled with the exit status, rather than
# thrown away -- seeing a plausible number beats seeing nothing.
pgdr_display_output() {
  local display_command="$1"
  local timeout_seconds="${2:-$PGDR_DISPLAY_TIMEOUT_SECONDS}"
  local out status

  # NOT `out=$(...); status=$?` -- that bare assignment is a plain simple
  # command whose exit status is the command substitution's, and it is
  # exactly the shape that made cmd_list's OLD implementation abort the
  # whole script under `set -e` on a non-zero-exit display command. Putting
  # it as the condition of an `if` is what actually makes the failure
  # non-fatal (the same reason cmd_reclaim already wraps its evals in
  # `if ! eval ...`): commands tested by `if`/`!` are exempt from
  # triggering `errexit`. (Caught by manually running this against the real
  # registry under `set -euo pipefail` -- like the nix-wrapped binary runs
  # it -- since bats' own `run` helper neutralizes `errexit` and so cannot
  # exercise this failure mode at all.)
  if out=$(timeout "$timeout_seconds" bash -c "$display_command" 2>&1); then
    status=0
  else
    status=$?
  fi

  if [[ $status -eq 124 ]]; then
    printf '(display command timed out after %ss)\n' "$timeout_seconds"
  elif [[ $status -ne 0 ]]; then
    printf '(display command exited %s)\n' "$status"
    [[ -n $out ]] && printf '%s\n' "$out"
  else
    printf '%s\n' "$out"
  fi

  return 0
}

# cmd_list: implements the `list` subcommand.
#
# Grammar: list [--aggressiveness N] [-v|--verbose]
#   --aggressiveness N (optional): a selection ceiling. With no ceiling,
#     every registered item is listed, including zero-variant
#     (informational-only) items. With a ceiling, the listing is
#     restricted to items with at least one variant <= N -- reusing
#     pgdr_select_variants' own Case A "at least one qualifying variant"
#     filter (rather than re-implementing the <= N logic by hand) to
#     decide INCLUSION only. A zero-variant item never has a qualifying
#     variant under any ceiling, so it is naturally excluded once a
#     ceiling is given, exactly like pgdr_select_variants' own Case A
#     behavior.
#   -v|--verbose (optional): show items whose `path` does not currently
#     exist on disk. Without this flag such items are skipped entirely
#     (no output at all) -- see the path-existence guard below.
#
# For each included item whose `path` exists, this prints a header block
# (ID/DESCRIPTION/AGGRESSIVENESS -- EVERY aggressiveness value the item has
# a variant for, not just the single highest-qualifying variant
# pgdr_select_variants would choose; a zero-variant item shows "-"), then
# the verbatim output of running its displayCommand (via
# pgdr_display_output, bounded by the item's displayTimeoutSeconds or else
# PGDR_DISPLAY_TIMEOUT_SECONDS, and never fatal to the run), then a "---" separator line and a blank line before
# the next item. A fixed-width table was tried here before, but
# Every included item's displayCommand runs CONCURRENTLY (each into its own
# temp file, then printed in registry order), so the listing's wall time is
# roughly the slowest item rather than the sum of all of them.
# displayCommand strings have no contract to produce single-line output --
# one registry entry's `find ... -exec du -sh {} +` legitimately prints one
# line PER matched file -- so a table row is the wrong shape; this
# header/output/separator block has no such assumption.
#
# Path-existence guard (operator-reported dogfooding feedback): every
# registry item's `path` is the generic, cheap "is there anything here at
# all" signal -- if it doesn't exist on this machine, there is nothing to
# reclaim for that item, full stop. Without --verbose such an item is
# skipped entirely (no header, no separator -- silence, since "print
# nothing when there's nothing to reclaim" was the reported expectation).
# With --verbose the header block still prints, followed by one
# "(path '...' does not exist -- nothing to do)" line INSTEAD of running
# displayCommand -- this also avoids running e.g. `du -sh` against a path
# that isn't there, whose stderr (`du: cannot access ...`) was the other
# half of the reported noise.
#
# Exit status: 0 on success (including an empty listing when a ceiling
# excludes everything, every included item's path is missing and --verbose
# was not given, or when every included item's displayCommand itself
# failed/timed out -- those are reported inline, not treated as a listing
# failure); 1 if the registry fails to load/validate, or an option is
# malformed.
cmd_list() {
  local max_aggressiveness=""
  local verbose=0

  while [[ $# -gt 0 ]]; do
    case "$1" in
    --aggressiveness)
      if [[ -z ${2:-} ]]; then
        echo "pg-disk-reclaimer: --aggressiveness requires a value" >&2
        return 1
      fi
      max_aggressiveness="$2"
      shift 2
      ;;
    -v | --verbose)
      verbose=1
      shift
      ;;
    -*)
      echo "pg-disk-reclaimer: unknown option '$1'" >&2
      return 1
      ;;
    *)
      echo "pg-disk-reclaimer: unexpected argument '$1'" >&2
      return 1
      ;;
    esac
  done

  local registry_path
  registry_path="$(pgdr_default_registry_path)"

  if ! pgdr_read_registry "$registry_path" >/dev/null; then
    return 1
  fi

  # Which item ids to include. No ceiling: every item. A ceiling: reuse
  # pgdr_select_variants' Case A qualifying-variant filter to decide
  # inclusion (its output already silently excludes non-qualifying and
  # zero-variant items) -- we only need the resulting ids here, not its
  # single-chosen-variant fields.
  local included_ids_json
  if [[ -n $max_aggressiveness ]]; then
    local selected
    if ! selected=$(pgdr_select_variants "$registry_path" "$max_aggressiveness"); then
      return 1
    fi
    included_ids_json=$(jq -c '[.[].id]' <<<"$selected")
  else
    included_ids_json=$(jq -c '[.[].id]' "$registry_path")
  fi

  # Build display rows straight from the registry (not from
  # pgdr_select_variants' output): the listing shows every aggressiveness
  # value an item HAS variants for, not just the one chosen variant
  # pgdr_select_variants would pick.
  local rows
  rows=$(jq -c --argjson ids "$included_ids_json" '
    [
      .[]
      | select(.id as $id | $ids | index($id) != null)
      | {
          id,
          description,
          path,
          displayCommand,
          displayTimeoutSeconds: (
            if .displayTimeoutSeconds == null then "" else (.displayTimeoutSeconds | floor | tostring) end
          ),
          aggressiveness: ((.variants // []) | map(.aggressiveness))
        }
    ]
  ' "$registry_path")

  # Pass 1: start every existing-path item's displayCommand in the
  # background, each writing to its own file under out_dir (named by row
  # index). pgdr_display_output always returns 0, so a failing/slow item
  # never disturbs its siblings.
  local out_dir
  out_dir=$(mktemp -d)
  local idx=0 row path display_command item_timeout
  while IFS= read -r row; do
    path=$(jq -r '.path' <<<"$row")
    if pgdr_path_exists "$path"; then
      display_command=$(jq -r '.displayCommand' <<<"$row")
      item_timeout=$(jq -r '.displayTimeoutSeconds' <<<"$row")
      pgdr_display_output "$display_command" "${item_timeout:-$PGDR_DISPLAY_TIMEOUT_SECONDS}" \
        >"$out_dir/$idx" </dev/null &
    fi
    idx=$((idx + 1))
  done < <(jq -c '.[]' <<<"$rows")
  wait || true

  # Pass 2: print in registry order.
  local id description aggressiveness_display
  idx=0
  while IFS= read -r row; do
    id=$(jq -r '.id' <<<"$row")
    description=$(jq -r '.description' <<<"$row")
    path=$(jq -r '.path' <<<"$row")
    aggressiveness_display=$(jq -r '
      .aggressiveness
      | if length == 0 then "-" else (map(tostring) | join(",")) end
    ' <<<"$row")

    if pgdr_path_exists "$path"; then
      printf 'ID: %s\n' "$id"
      printf 'DESCRIPTION: %s\n' "$description"
      printf 'AGGRESSIVENESS: %s\n' "$aggressiveness_display"
      cat "$out_dir/$idx"
      printf -- '---\n\n'
    elif [[ $verbose -eq 1 ]]; then
      printf 'ID: %s\n' "$id"
      printf 'DESCRIPTION: %s\n' "$description"
      printf 'AGGRESSIVENESS: %s\n' "$aggressiveness_display"
      printf "(path '%s' does not exist -- nothing to do)\n" "$path"
      printf -- '---\n\n'
    fi
    idx=$((idx + 1))
  done < <(jq -c '.[]' <<<"$rows")

  rm -rf "$out_dir"
  return 0
}

# pgdr_command_exists: returns 0 if TOKEN resolves as something bash could
# actually invoke -- a binary on PATH, a builtin, or a function currently
# defined in this shell (which includes every pgdr_*/cmd_* function, since
# pg-disk-reclaimer.bash is always sourced before any subcommand runs) --
# and 1 otherwise.
#
# `command -v` alone already resolves a currently-defined function (bash
# feature, not POSIX-guaranteed), so the `declare -F` fallback below is
# belt-and-suspenders rather than load-bearing today -- kept because the
# bead's contract is phrased as the disjunction of the two, and dropping it
# would make that contract rely on an unstated bash-only behavior of
# `command -v`.
pgdr_command_exists() {
  local token="$1"
  command -v "$token" >/dev/null 2>&1 && return 0
  declare -F "$token" >/dev/null 2>&1 && return 0
  return 1
}

# pgdr_validate_commands_exist: best-effort 4th check, layered on top of an
# ALREADY schema-validated registry at PATH (pgdr_validate_registry's
# checks 1-3) -- for every command string in the registry (each item's
# displayCommand, and each variant's dryRunCommand/removeCommand), extracts
# its leading whitespace-delimited token and confirms it resolves via
# pgdr_command_exists.
#
# Deliberately best-effort: it CANNOT validate arbitrary shell logic inside
# a command string -- a pipe, a subshell, or a later command in a `&&`
# chain -- only that the first invoked command/function exists. See also
# cmd_validate below, --help, and the tldr page.
#
# Fails fast: on the first item/field whose leading token does not
# resolve, prints one descriptive message to stderr (naming the item id,
# the field, and the token) and returns 1. Prints nothing and returns 0 on
# success.
pgdr_validate_commands_exist() {
  local path="$1"
  local entry

  while IFS= read -r entry; do
    local id field cmd token
    id=$(jq -r '.id' <<<"$entry")
    field=$(jq -r '.field' <<<"$entry")
    cmd=$(jq -r '.cmd' <<<"$entry")

    read -r token _ <<<"$cmd"

    if ! pgdr_command_exists "$token"; then
      echo "pg-disk-reclaimer: registry '$path' item '$id' has a $field whose command does not exist: '$token'" >&2
      return 1
    fi
  done < <(jq -c '
    .[] as $item
    | ($item.id) as $id
    | (
        [{id: $id, field: "displayCommand", cmd: $item.displayCommand}]
        + (if $item.sizeCommand then [{id: $id, field: "sizeCommand", cmd: $item.sizeCommand}] else [] end)
        + (($item.variants // []) | to_entries | map({
            id: $id,
            field: ("variants[" + (.key | tostring) + "].dryRunCommand"),
            cmd: .value.dryRunCommand
          }))
        + (($item.variants // []) | to_entries | map({
            id: $id,
            field: ("variants[" + (.key | tostring) + "].removeCommand"),
            cmd: .value.removeCommand
          }))
      )[]
  ' "$path")

  return 0
}

# cmd_validate: implements the `validate` subcommand.
#
# Grammar: validate [path]
#   [path] (optional): registry file to validate. Defaults to
#     pgdr_default_registry_path when omitted, so `pg-disk-reclaimer
#     validate` with no args checks the registry that list/reclaim would
#     actually load.
#
# Runs pgdr_validate_registry's checks 1-3 (JSON parses; every item has its
# required fields and a unique id; every variant has its required fields
# and a unique, non-negative aggressiveness) FIRST, then
# pgdr_validate_commands_exist's best-effort command-existence check --
# unlike checks 1-3, this 4th check is validate-only and NOT shared with
# the list/reclaim load path, since a malformed command string doesn't
# stop list/reclaim from loading the registry itself.
#
# On the first failure from either stage, that stage's own descriptive
# stderr message is left as-is and cmd_validate returns 1 without adding
# anything further. On success, prints one confirmation line to stdout and
# returns 0 -- pgdr_validate_registry deliberately stays silent on success
# so callers can layer their own reporting on top; this is that reporting.
cmd_validate() {
  local path="${1:-$(pgdr_default_registry_path)}"

  if [[ ! -f $path ]]; then
    echo "pg-disk-reclaimer: registry file not found: $path" >&2
    return 1
  fi

  if ! pgdr_validate_registry "$path"; then
    return 1
  fi

  if ! pgdr_validate_commands_exist "$path"; then
    return 1
  fi

  echo "pg-disk-reclaimer: registry '$path' is valid"
  return 0
}

# pgdr_confirm: prompts PROMPT and reads a y/N confirmation directly from
# the controlling terminal (/dev/tty), returning 0 for an explicit y/yes
# answer and 1 for anything else -- including no controlling terminal at
# all (read fails, reply stays empty). "No" is the safe default.
#
# Deliberately reads /dev/tty rather than stdin: cmd_reclaim's aggressive
# (>=4) confirmation gate below MUST NOT be bypassable by piping an answer
# in (e.g. `yes | pg-disk-reclaimer reclaim --apply --aggressiveness 5`),
# since that pipe is exactly the non-interactive path -- cron/Taskfile/
# CI -- the gate exists to keep impossible (operator decision, final; see
# cmd_reclaim below). Reading /dev/tty means such a context simply has no
# controlling terminal to read from and always gets the safe "no".
#
# Kept as its own small function precisely so a caller/bats test can
# override/stub it out instead of ever touching a real terminal -- tests
# MUST do this rather than exercise the real read, which would hang
# waiting on input that never arrives in a non-interactive test run.
pgdr_confirm() {
  local prompt="$1"
  local reply=""

  read -r -p "$prompt" reply </dev/tty 2>/dev/null || true

  case "$reply" in
  [yY] | [yY][eE][sS])
    return 0
    ;;
  *)
    return 1
    ;;
  esac
}

# PGDR_SIZE_TIMEOUT_SECONDS: ceiling (wall-clock seconds) on how long
# cmd_reclaim waits to size ONE item (bead pg2-es6fn). Separate from
# PGDR_DISPLAY_TIMEOUT_SECONDS because a reclaim is a deliberate action where
# the operator wants the number for the biggest trees (a `du` over a
# 65 GB cache), not a quick listing. Resolved per item by
# pgdr_item_size_timeout so a per-item override can be added in one place.
: "${PGDR_SIZE_TIMEOUT_SECONDS:=60}"

# PGDR_LONG_LINE_CHARS: a dry-run output line longer than this is collapsed to
# a short summary unless -v is given (bead pg2-es6fn). `go clean -n -cache`
# prints ONE `rm -rf` line naming all 256 hash dirs.
: "${PGDR_LONG_LINE_CHARS:=200}"

# pgdr_item_size_timeout: echoes the size-computation ceiling for one item.
# Today this is just PGDR_SIZE_TIMEOUT_SECONDS for every item; it takes the
# item's selection JSON ($1) so a per-item registry override (bead
# pg2-m6bsb's displayTimeoutSeconds) can slot in here without touching
# cmd_reclaim.
pgdr_item_size_timeout() {
  printf '%s\n' "$PGDR_SIZE_TIMEOUT_SECONDS"
}

# pgdr_format_kb: renders a KiB count as a short human size (K/M/G/T, one
# decimal above 1 MiB). Pure integer arithmetic -- no awk/locale dependence.
pgdr_format_kb() {
  local kb="$1"
  local -a units=(K M G T)
  local i=0
  local tenths

  if ((kb < 1024)); then
    printf '%dK\n' "$kb"
    return 0
  fi

  tenths=$((kb * 10))
  while ((tenths >= 10240 && i < 3)); do
    tenths=$((tenths / 1024))
    i=$((i + 1))
  done
  printf '%d.%d%s\n' $((tenths / 10)) $((tenths % 10)) "${units[$i]}"
}

# pgdr_size_kb: computes the reclaimable size of one item, in KiB.
#
# Usage: pgdr_size_kb <path> <size-command-or-empty> <timeout-seconds>
#
# With a non-empty size command (the item's optional `sizeCommand`, for items
# where `du` over the path is not meaningful -- e.g. nix-store-gc, whose path
# is the whole store) that command is the size source; otherwise it is
# `du -sk <path>`. Both are trusted operator-authored registry strings, run
# via `bash -c` under `timeout` (same trust model and reasoning as
# pgdr_display_output). The first whitespace-delimited field of the first
# output line is the KiB count; a nonzero exit is tolerated when that field
# is numeric (du prints a real total alongside its permission warnings).
#
# On success prints the integer KiB to stdout and returns 0. On failure
# prints a short human reason to stdout (e.g. "timed out after 60s") and
# returns 1 -- never silent, so the caller can print an explicit unknown
# marker. Always safe under `set -e` (statuses are captured in `if`).
pgdr_size_kb() {
  local path="$1" size_command="$2" timeout_seconds="$3"
  local cmd out status first

  if [[ -n $size_command ]]; then
    cmd="$size_command"
  else
    cmd="du -sk $path"
  fi

  if out=$(timeout "$timeout_seconds" bash -c "$cmd" 2>/dev/null); then
    status=0
  else
    status=$?
  fi

  if [[ $status -eq 124 ]]; then
    printf 'timed out after %ss\n' "$timeout_seconds"
    return 1
  fi

  first=""
  read -r first _ <<<"$out" || true
  if [[ $first =~ ^[0-9]+$ ]]; then
    printf '%s\n' "$first"
    return 0
  fi

  if [[ -n $size_command ]]; then
    printf 'size command gave no number\n'
  else
    printf 'du gave no number\n'
  fi
  return 1
}

# pgdr_print_dry_run_output: prints a dry-run command's captured output
# (OUT, $3), collapsing any line longer than PGDR_LONG_LINE_CHARS unless
# VERBOSE ($2) is 1. A long `rm ...` line becomes
# "would remove N paths under <item path>" (N = non-flag words after `rm`);
# any other long line is truncated with its char count. Under VERBOSE the
# raw line is printed as well (instead of only the truncation for non-rm
# lines).
pgdr_print_dry_run_output() {
  local item_path="$1" verbose="$2" out="$3"
  local line n w
  local -a words

  [[ -z $out ]] && return 0

  while IFS= read -r line; do
    if ((${#line} <= PGDR_LONG_LINE_CHARS)); then
      printf '%s\n' "$line"
    elif [[ $line == "rm "* ]]; then
      read -ra words <<<"$line"
      n=0
      for w in "${words[@]:1}"; do
        [[ $w == -* ]] || n=$((n + 1))
      done
      printf 'would remove %s paths under %s\n' "$n" "$item_path"
      [[ $verbose -eq 1 ]] && printf '%s\n' "$line"
    elif [[ $verbose -eq 1 ]]; then
      printf '%s\n' "$line"
    else
      printf '%s... [%s chars; use -v for the raw line]\n' "${line:0:100}" "${#line}"
    fi
  done <<<"$out"

  return 0
}

# cmd_reclaim: implements the `reclaim` subcommand.
#
# Reclaimable-size reporting (bead pg2-es6fn): every item cmd_reclaim runs
# (dry run or --apply) gets a "<id>: size: <size>" line -- or an explicit
# "size: unknown (<reason>)" marker, never silence -- computed by
# pgdr_size_kb (the item's optional `sizeCommand`, else `du -sk` over its
# path) under pgdr_item_size_timeout, and the run ends with a
# "total reclaimable|reclaimed: <size> (N sized, M unknown)" line that sums
# ONLY the known sizes. Skipped items (missing path, declined confirm gate)
# and items whose command failed get no total contribution. A dry run's
# captured output goes through pgdr_print_dry_run_output (long-line
# collapse, raw under -v).
#
# Grammar: reclaim --aggressiveness N [id...] [--apply] [-v|--verbose]
#   --aggressiveness N (REQUIRED): the selection ceiling. Passed straight
#     through to pgdr_select_variants, which does the actual selection --
#     see its doc comment above for the no-ids/explicit-ids semantics.
#     cmd_reclaim does NOT re-validate ids itself; pgdr_select_variants's
#     errors (unknown id, informational-only id, id above the ceiling)
#     surface as-is. A bad id among several explicit ids does NOT prevent
#     the other (good) ids from being attempted -- pgdr_select_variants
#     itself still returns a selection for the good ones (see its own doc
#     comment); cmd_reclaim runs that selection regardless of
#     pgdr_select_variants' own return status, folding a bad-id failure
#     into overall_status alongside any command failure below (bug fix,
#     bead pg2-qt7ep).
#   [id...]: explicit item ids narrowing the selection (also passed
#     straight through to pgdr_select_variants).
#   --apply: switches from the default dry run (each selected variant's
#     dryRunCommand) to the real reclaim (each selected variant's
#     removeCommand).
#   -v|--verbose (optional): show a note when a selected item's path does
#     not currently exist on disk. Without this flag such an item is
#     skipped entirely (no output at all) -- see the path-existence guard
#     below.
#
# Path-existence guard (same operator-reported dogfooding feedback as
# cmd_list's guard): a selected item's `path` is the generic, cheap "is
# there anything here at all" signal -- if it doesn't exist on this
# machine, there is nothing to reclaim for that item, full stop. This is
# checked immediately before running dryRunCommand (dry-run branch) or
# removeCommand (--apply branch), in both cases before the aggressiveness
# confirm gate below (no point prompting to confirm removal of nothing).
# Without --verbose the item is skipped silently (no output, and NOT
# counted against overall_status -- matching how a declined confirm-gate
# is already handled). With --verbose one line is printed to stderr and
# the item is still skipped, still not a failure.
#
# Aggressiveness >= 4 confirmation gate (operator decision, final): any
# selected variant with aggressiveness >= 4 is gated behind an
# interactive pgdr_confirm prompt immediately before its removeCommand
# runs -- ONLY under --apply. A dry run never prompts, at any
# aggressiveness, since it never reaches a real removeCommand. Declining
# skips running removeCommand for THAT item only (not counted as a
# failure -- it is a deliberate choice, not an error); other selected
# items still proceed. There is NO bypass of any kind (no flag, no env
# var, no non-interactive path) -- do not add one.
#
# dryRunCommand/removeCommand are trusted operator-authored strings from
# the registry JSON (not user input), so running them via `eval` is safe
# and matches this repo's existing pattern for trusted command strings
# (e.g. claude-status-line's scripts.nix, agent-script.nix).
#
# Exit status: 0 if every dry-run/remove command that actually ran
# exited 0 AND every explicitly-requested id was valid (an item skipped
# via decline does not count against this); 1 if --aggressiveness was
# missing, at least one explicitly-requested id was bad (unknown,
# informational-only, or above the ceiling), or any command that ran
# exited non-zero. Neither a bad id nor a failing item's command stops
# any OTHER selected item from being attempted.
cmd_reclaim() {
  local max_aggressiveness=""
  local apply=0
  local verbose=0
  local ids=()

  while [[ $# -gt 0 ]]; do
    case "$1" in
    --aggressiveness)
      if [[ -z ${2:-} ]]; then
        echo "pg-disk-reclaimer: --aggressiveness requires a value" >&2
        return 1
      fi
      max_aggressiveness="$2"
      shift 2
      ;;
    --apply)
      apply=1
      shift
      ;;
    -v | --verbose)
      verbose=1
      shift
      ;;
    --)
      shift
      ids+=("$@")
      break
      ;;
    -*)
      echo "pg-disk-reclaimer: unknown option '$1'" >&2
      return 1
      ;;
    *)
      ids+=("$1")
      shift
      ;;
    esac
  done

  if [[ -z $max_aggressiveness ]]; then
    echo "pg-disk-reclaimer: 'reclaim' requires --aggressiveness N" >&2
    return 1
  fi

  local registry_path
  registry_path="$(pgdr_default_registry_path)"

  if ! pgdr_read_registry "$registry_path" >/dev/null; then
    return 1
  fi

  # NOT `if ! selected=$(...); then return 1; fi` -- pgdr_select_variants
  # now returns 1 for a bad explicit id while STILL printing a selection
  # for the good ones (see its doc comment); returning early here on that
  # non-zero status would throw away that selection and reproduce the
  # exact bug this fix addresses (bead pg2-qt7ep). overall_status is
  # seeded from pgdr_select_variants' status instead, and the loop below
  # always runs over whatever selection it produced.
  local selected
  local overall_status=0
  if ! selected=$(pgdr_select_variants "$registry_path" "$max_aggressiveness" "${ids[@]}"); then
    overall_status=1
  fi

  local total_kb=0 sized_count=0 unknown_count=0
  local item
  while IFS= read -r item; do
    local id aggressiveness dry_run_command remove_command path size_command
    local size_kb size_result size_label dry_out
    id=$(jq -r '.id' <<<"$item")
    aggressiveness=$(jq -r '.aggressiveness' <<<"$item")
    dry_run_command=$(jq -r '.dryRunCommand' <<<"$item")
    remove_command=$(jq -r '.removeCommand' <<<"$item")
    path=$(jq -r '.path' <<<"$item")
    size_command=$(jq -r '.sizeCommand // ""' <<<"$item")

    if ! pgdr_path_exists "$path"; then
      if [[ $verbose -eq 1 ]]; then
        echo "pg-disk-reclaimer: '$id' path '$path' does not exist -- nothing to do, skipping" >&2
      fi
      continue
    fi

    # Size the item before anything runs, so the operator sees it ahead of
    # an --apply confirm prompt. NOT a bare $(...) assignment: a failing
    # size computation must be an explicit "unknown" marker, never fatal
    # under the nix wrapper's `set -euo pipefail`.
    size_kb=""
    if size_result=$(pgdr_size_kb "$path" "$size_command" "$(pgdr_item_size_timeout "$item")"); then
      size_kb="$size_result"
      size_label=$(pgdr_format_kb "$size_kb")
    else
      size_label="unknown ($size_result)"
    fi

    if [[ $apply -eq 0 ]]; then
      printf '%s: size: %s\n' "$id" "$size_label"

      # stdout+stderr captured together so a long single-line dry run (go
      # clean -n prints its `rm -rf` line on a stream we should not guess)
      # can be collapsed; see pgdr_print_dry_run_output.
      if dry_out=$(eval "$dry_run_command" 2>&1); then
        pgdr_print_dry_run_output "$path" "$verbose" "$dry_out"
        if [[ -n $size_kb ]]; then
          total_kb=$((total_kb + size_kb))
          sized_count=$((sized_count + 1))
        else
          unknown_count=$((unknown_count + 1))
        fi
      else
        pgdr_print_dry_run_output "$path" "$verbose" "$dry_out"
        echo "pg-disk-reclaimer: dry-run command for '$id' exited non-zero" >&2
        overall_status=1
      fi
      continue
    fi

    printf '%s: size: %s\n' "$id" "$size_label"

    if [[ $aggressiveness -ge 4 ]]; then
      if ! pgdr_confirm "pg-disk-reclaimer: reclaim '$id' at aggressiveness $aggressiveness -- run its removeCommand? [y/N] "; then
        echo "pg-disk-reclaimer: skipping '$id' (not confirmed)" >&2
        continue
      fi
    fi

    if eval "$remove_command"; then
      if [[ -n $size_kb ]]; then
        total_kb=$((total_kb + size_kb))
        sized_count=$((sized_count + 1))
      else
        unknown_count=$((unknown_count + 1))
      fi
    else
      echo "pg-disk-reclaimer: remove command for '$id' exited non-zero" >&2
      overall_status=1
    fi
  done < <(jq -c '.[]' <<<"$selected")

  # No total when nothing ran (a run where every item was skipped stays
  # silent, matching the quiet-by-default missing-path contract).
  if ((sized_count + unknown_count > 0)); then
    local total_word="reclaimable"
    [[ $apply -eq 1 ]] && total_word="reclaimed"
    printf 'total %s: %s (%s sized, %s unknown)\n' \
      "$total_word" "$(pgdr_format_kb "$total_kb")" "$sized_count" "$unknown_count"
  fi

  return "$overall_status"
}
