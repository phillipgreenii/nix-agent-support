# shellcheck shell=bash
# pg-disk-reclaimer - data-driven disk-space-reclaim CLI.
#
# Entry point: help + subcommand dispatch only. cmd_list/cmd_validate/
# cmd_reclaim (pg-disk-reclaimer.bash) hold the real logic, filled in by
# later tasks in the pg2-txxyj epic.
#
# nix build already sources pg-disk-reclaimer.bash ahead of this body
# (mkBashScript's hasSupportBash injection); this guard only fires for a raw
# `bash pg-disk-reclaimer.sh` run (e.g. local bats), where nothing has
# sourced it yet.
if ! declare -F cmd_list >/dev/null 2>&1; then
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pg-disk-reclaimer.bash"
fi

show_help() {
  cat <<'HELP'
pg-disk-reclaimer: Data-driven disk-space-reclaim CLI

Usage: pg-disk-reclaimer <command> [OPTIONS] [ARGS]

Commands:
  list [--aggressiveness N]            List reclaimable registry items
  validate [PATH]                      Validate a registry file
  reclaim --aggressiveness N [ID...]   Reclaim disk space (dry-run unless --apply)

Options:
  -h, --help     Show this help message
  -v, --version  Show version information

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

if [[ $# -eq 0 ]]; then
  show_help
  exit 1
fi

case "$1" in
-h | --help)
  show_help
  exit 0
  ;;
esac

cmd="$1"
shift

case "$cmd" in
list)
  cmd_list "$@"
  ;;
validate)
  cmd_validate "$@"
  ;;
reclaim)
  cmd_reclaim "$@"
  ;;
*)
  echo "pg-disk-reclaimer: unknown command '$cmd'" >&2
  show_help >&2
  exit 1
  ;;
esac
