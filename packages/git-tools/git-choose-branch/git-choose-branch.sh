# shellcheck shell=bash

show_help() {
  cat <<EOF
Usage: git-choose-branch [OPTIONS]

Interactive branch selector using fzf with git log preview.

Displays all local branches (except current) sorted by last commit date.
Use arrow keys to navigate, Enter to checkout selected branch.

Options:
  -h, --help     Show this help message and exit
  -v, --version  Show version information

Requirements:
  - fzf (fuzzy finder)

Example:
  git-choose-branch
  # Opens interactive selector, checkout selected branch

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-support-apps/issues>
EOF
}

# Handle flags
case "${1:-}" in
-v | --version)
  show_version
  exit 0
  ;;
-h | --help)
  show_help
  exit 0
  ;;
esac

choose-branch() {
  local selections
  selections="$(render-selections)" || return 1
  fzf --ansi \
    --preview "echo '{}' | cut -d' ' -f1 | xargs -L1 git log -${LINES:-20}" \
    --preview-window 'right:65%' \
    <<<"${selections}"
}

list-branches() {
  git for-each-ref 'refs/heads/**' --format='%(refname:short)%09%(committerdate:unix)%09%(committerdate:relative)%09%(HEAD)' |
    sort -k2 -r
}

render-selections() {
  local branch lastcommit currmarker
  list-branches |
    while IFS=$'\t' read -r branch _ lastcommit currmarker; do
      [[ ${currmarker} == "*" ]] && continue
      printf "%s\t%s\n" "${branch}" "${lastcommit}"
    done | column -t -s$'\t'
}

sel="$(choose-branch)"
[[ -n ${sel} ]] || exit 1
git checkout "${sel%% *}"
