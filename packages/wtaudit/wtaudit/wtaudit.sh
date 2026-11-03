# shellcheck shell=bash
# wtaudit - deterministic classifier and remover for worktrees and branches:
# the mechanical half of the audit-worktrees sweep. `scan` is a read-only
# Query that prints every non-canonical worktree, coordinated-set member and
# dangling branch with a verdict; `remove` re-classifies one candidate as a
# Guard and deletes it with a compare-and-swap. See wtaudit.bash.
#
# nix build already sources wt-liveness-lib and wtaudit.bash ahead of this
# body; this guard only fires for a raw `bash wtaudit.sh` run (local bats),
# where nothing has sourced them yet.
if ! declare -F wtaudit_primary >/dev/null 2>&1; then
  _wtaudit_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  # shellcheck disable=SC1090,SC1091  # runtime-computed path, by design
  source "$_wtaudit_dir/wtaudit.bash"
  # shellcheck disable=SC1090,SC1091  # runtime-computed path, by design
  if ! declare -F wtdone_command_blocks >/dev/null 2>&1; then
    if [[ -n ${LIB_PATH:-} ]]; then
      if [[ -d $LIB_PATH ]]; then
        source "$LIB_PATH/wt-liveness-lib.bash"
      else
        source "${LIB_PATH%%:*}"
      fi
    else
      source "$_wtaudit_dir/../../wtdone/wt-liveness-lib/wt-liveness-lib.bash"
    fi
  fi
  unset _wtaudit_dir
fi

usage_error() {
  echo "wtaudit: $1" >&2
  exit 2
}

show_help() {
  cat <<'HELP'
wtaudit: Deterministic classifier and remover for worktrees and branches

Usage:
  wtaudit scan   [--repo DIR]... [--json | --table]
  wtaudit remove --repo DIR --branch NAME --expect-tip SHA
  wtaudit --help | --version

scan classifies every non-canonical worktree, coordinated-set member and
dangling branch of each repo, offline, and prints JSON (schema_version 1) by
default; --table prints a human summary instead. With no --repo it enumerates
the workspace repos from `pn workspace discover`; --repo DIR (repeatable)
overrides discovery. Every candidate appears exactly once; nothing is hidden.

remove re-classifies the one named candidate from scratch and proceeds only if
the verdict is still remove or reclaim AND the branch tip equals --expect-tip.
The worktree (if any) goes through wtdone (WTDONE_BIN); the branch is deleted
with `git update-ref -d refs/heads/<branch> <tip>` (compare-and-swap), never
`git branch -D`. Its stdout is always one JSON object:
{id, removed_worktree, removed_branch, exit, reason, message}.

Actions (the `action` field): remove, reclaim, keep, agent.
HELP
  local -a reasons
  local reason
  read -ra reasons <<<"$WTAUDIT_SCAN_REASONS"
  echo 'Verdict reasons (the `reason` field, a closed enum, in decision-table order):'
  for reason in "${reasons[@]}"; do printf '  %s\n' "$reason"; done
  printf 'Set-only reasons (sets[].reason): %s\n' "$WTAUDIT_SET_REASONS"
  printf 'Remove-only reasons: %s\n\n' "$WTAUDIT_REMOVE_REASONS"
  cat <<'HELP'
Exit codes:
  scan:   0 success (an unreadable repo or a failed bd call is a row, not an
          error); 2 usage error (or `pn workspace discover` failed); 3 a --repo
          argument is not a readable git repository
  remove: 0 the worktree (if any) and the branch are gone; 2 usage error
          (malformed --expect-tip, unreadable --repo); 10 refused before
          acting (reason verdict-changed or tip-moved; an unknown or vanished
          branch is verdict-changed); 11 the branch remains (reason
          branch-refused or tip-moved-late); 12 the candidate is a set member
          (reason set-member; remove a set with pnwf cleanup); 13 wtdone failed
          and the worktree is still present (reason teardown-refused)

Environment:
  WTDONE_BIN                 The wtdone executable remove runs (pinned by the
                             nix package).
  WTDONE_BLOCKING_COMMANDS   Process-name allow-list override shared with
                             wtdone (see wtdone --help).

Options:
  -h, --help     Show this help message
  -v, --version  Show version information

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

[[ $# -gt 0 ]] || usage_error "usage: wtaudit scan|remove ... (see --help)"

subcommand=""
case "$1" in
-h | --help)
  show_help
  exit 0
  ;;
scan | remove)
  subcommand="$1"
  shift
  ;;
-*) usage_error "unknown option: $1" ;;
*) usage_error "unknown subcommand: $1" ;;
esac

repos=()
format="json"
branch=""
expect_tip=""

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --repo)
    [[ -n ${2:-} ]] || usage_error "--repo requires a value"
    repos+=("$2")
    shift 2
    ;;
  --json)
    [[ $subcommand == scan ]] || usage_error "--json is only valid for scan"
    format="json"
    shift
    ;;
  --table)
    [[ $subcommand == scan ]] || usage_error "--table is only valid for scan"
    format="table"
    shift
    ;;
  --branch)
    [[ $subcommand == remove ]] || usage_error "--branch is only valid for remove"
    [[ -n ${2:-} ]] || usage_error "--branch requires a value"
    branch="$2"
    shift 2
    ;;
  --expect-tip)
    [[ $subcommand == remove ]] || usage_error "--expect-tip is only valid for remove"
    [[ -n ${2:-} ]] || usage_error "--expect-tip requires a value"
    expect_tip="$2"
    shift 2
    ;;
  *) usage_error "unknown argument: $1" ;;
  esac
done

if [[ $subcommand == scan ]]; then
  names=()
  paths=()
  if [[ ${#repos[@]} -gt 0 ]]; then
    for r in "${repos[@]}"; do
      if ! wtaudit_repo_readable "$r"; then
        echo "wtaudit: --repo is not a readable git repository: $r" >&2
        exit 3
      fi
      top="$(wtaudit_repo_toplevel "$r")"
      names+=("$(basename "$top")")
      paths+=("$top")
    done
  else
    if ! discovered="$(pn workspace discover 2>/dev/null)"; then
      echo "wtaudit: 'pn workspace discover' failed; pass --repo DIR to scan explicitly" >&2
      exit 2
    fi
    while IFS=$'\t' read -r name _url path; do
      [[ -n $name && -n $path ]] || continue
      names+=("$name")
      paths+=("$path")
    done <<<"$discovered"
  fi

  lines=""
  for i in "${!paths[@]}"; do
    lines+="$(wtaudit_scan_repo "${names[$i]}" "${paths[$i]}")"$'\n'
  done
  doc="$(printf '%s' "$lines" | wtaudit_scan_document)"
  if [[ $format == table ]]; then
    wtaudit_render_table <<<"$doc"
  else
    printf '%s\n' "$doc"
  fi
  exit 0
fi

# remove
[[ ${#repos[@]} -eq 1 ]] || {
  wtaudit_remove_json "" false false 2 usage "remove needs exactly one --repo DIR"
  exit 2
}
[[ -n $branch ]] || {
  wtaudit_remove_json "" false false 2 usage "remove needs --branch NAME"
  exit 2
}
if [[ ! $expect_tip =~ ^([0-9a-f]{40}|[0-9a-f]{64})$ ]]; then
  wtaudit_remove_json "" false false 2 usage "--expect-tip must be a full 40 or 64 character lowercase hex sha"
  exit 2
fi
if ! wtaudit_repo_readable "${repos[0]}"; then
  wtaudit_remove_json "" false false 2 usage "--repo is not a readable git repository: ${repos[0]}"
  exit 2
fi
rc=0
# WTDONE_BIN is injected as config by the nix build (a raw run exports it); it is
# read here, after the injected assignment, and handed to the function.
wtaudit_remove "${repos[0]}" "$branch" "$expect_tip" "${WTDONE_BIN:-}" || rc=$?
exit "$rc"
