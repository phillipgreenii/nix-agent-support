# shellcheck shell=bash
# sync-projects - rebase every workspace repo onto its origin under pg-rescue,
# then publish with `pn workspace push` (bead pg2-zr3jf; design:
# docs/superpowers/specs/2026-10-02-pg-rescue-design.md section 11).
#
# A lightweight chore, NOT a replacement for /pn-workspace-sync (workforests,
# validation, landing). If no rebase conflicts, no agent is involved.

show_help() {
  cat <<'HELP'
sync-projects: Rebase every workspace repo onto its origin, then push

Usage: sync-projects [OPTIONS]

For each repo listed by `pn workspace discover`, runs

  pg-rescue --chain sync -C <repo> --context "sync-projects: rebase onto origin" \
    -- git pull --rebase

so a conflicted rebase is handed to pg-rescue's `sync` handler chain. When
every repo is rebased (or a handler resolved the conflict), runs
`pn workspace push`.

The loop STOPS before the push, leaving the working copy as-is, on the first
repo whose pg-rescue run does not exit 0:
  75      a handler deferred the work (a follow-up item exists)
  other   the failure was not handled; pg-rescue's own exit code is passed on
The script exits with that code. Repos after the failing one are not touched.

Handlers never push; this script does. Run it from inside the workspace (or
with PN_WORKSPACE_ROOT set). It takes no arguments.

Requires `pg-rescue` (with a `sync` chain configured) and `pn` on PATH.

Options:
  -h, --help     Show this help message
  -v, --version  Show version information

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

die() {
  echo "sync-projects: $1" >&2
  exit "${2:-1}"
}

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    show_help
    exit 0
    ;;
  *) die "unexpected argument: $1 (see --help)" ;;
  esac
done

# Capture first, parse second: a `pn` failure inside `< <(...)` would be
# invisible to `set -e`.
discovered=$(pn workspace discover) || die "'pn workspace discover' failed"

# discover prints `name<TAB>url<TAB>path` per repo; the path is the third field.
workspace_repos=()
while IFS=$'\t' read -r _name _url repo_path _; do
  [[ -z $repo_path ]] || workspace_repos+=("$repo_path")
done <<<"$discovered"

[[ ${#workspace_repos[@]} -gt 0 ]] || die "'pn workspace discover' listed no repos"

for repo in "${workspace_repos[@]}"; do
  rc=0
  # -C, not `git -C`: with pg-rescue's own -C the report and the handlers' cwd
  # are the conflicted repo.
  pg-rescue --chain sync -C "$repo" --context "sync-projects: rebase onto origin" \
    -- git pull --rebase || rc=$?
  if [[ $rc -ne 0 ]]; then
    # Stopping here, before the push, is intended (design section 11).
    echo "sync-projects: stopped at $repo (pg-rescue exit $rc); nothing was pushed" >&2
    exit "$rc"
  fi
done

pn workspace push
