# shellcheck shell=bash
# by http://github.com/jehiah
# this prints out some branch status (similar to the '... ahead' info you get from git status)

# example:
# $ git branch-status
# dns_check (ahead 1) | (behind 112) origin/master
# master (ahead 2) | (behind 0) origin/master

show_help() {
  cat <<EOF
Usage: git-branch-status [OPTIONS]

Show branch status for all local branches compared to main.

Displays how many commits each branch is ahead/behind the main branch.

Options:
  -h, --help     Show this help message and exit
  -v, --version  Show version information

Example:
  git-branch-status
  # Output:
  # feature-branch (ahead 3) | (behind 5) main
  # bugfix-branch (ahead 1) | (behind 0) main

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

# update main (may fail if remote is unavailable)
git fetch origin main:main 2>/dev/null || true

git for-each-ref --format="%(refname:short)" refs/heads |
  while read -r local; do
    TMP_FILE=$(mktemp)
    git rev-list --left-right "${local}...main" -- >"$TMP_FILE" || {
      rm -f "$TMP_FILE"
      echo "Rev-list failed for ${local}...main" >&2
      continue
    }
    LEFT_AHEAD=$(grep -c '^<' "$TMP_FILE" || true)
    RIGHT_AHEAD=$(grep -c '^>' "$TMP_FILE" || true)
    rm -f "$TMP_FILE"
    echo "$local (ahead $LEFT_AHEAD) | (behind $RIGHT_AHEAD) main"
  done
