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
  list [--aggressiveness N] [-v|--verbose]
      List reclaimable registry items
  validate [PATH]
      Validate a registry file
  reclaim --aggressiveness N [ID...] [--apply] [-v|--verbose]
      Reclaim disk space (dry-run unless --apply)

Options:
  -h, --help     Show this help message
  -v, --version  Show version information

'list' options:
  -v, --verbose  Also show items whose path does not exist on this
                 machine (skipped silently by default). This is a
                 list-only flag, unrelated to the top-level -v/--version
                 above, which is parsed before subcommand dispatch.

'reclaim' options:
  -v, --verbose  Also show a note when a selected item's path does not
                 exist on this machine (skipped silently, without
                 running its dryRunCommand/removeCommand, by default),
                 and keep very long single-line dry-run output (e.g.
                 `go clean -n -cache`'s one `rm -rf` line naming every
                 hash dir) instead of collapsing it to a summary.
                 This is a reclaim-only flag, unrelated to the top-level
                 -v/--version above, which is parsed before subcommand
                 dispatch.

  Every item reclaim runs prints a "<id>: size: <size>" line (from the
  registry item's optional sizeCommand, else `du -sk` over its path), or
  "size: unknown (<reason>, e.g. timed out after 60s)" when the size could
  not be computed in time, and the run ends with a total that sums only
  the known sizes and counts the unknown ones. The per-item size ceiling
  is PGDR_SIZE_TIMEOUT_SECONDS (default 60). A sizeCommand prints a bare
  KiB integer; only the first whitespace-delimited field of its FIRST
  output line is read. On macOS a size command (or the du default) that
  exits non-zero with "Operation not permitted" (a TCC denial: the terminal
  lacks Full Disk Access, e.g. for ~/.Trash) is reported as
  "size: unknown (no Full Disk Access)", never as the misleading 0 du
  prints; `list` likewise shows "size unavailable (no Full Disk Access)".
  A dry-run command that exits non-zero with the same denial prints
  "(dry run unavailable (no Full Disk Access))" in place of its partial
  output (e.g. the "0" of `ls -la ~/.Trash | wc -l`).

Size contract (what a printed size means):
  A size says what the number means. Each registry item MAY declare,
  as static metadata (all optional, item level):
    sizeKind         exact (default) | estimate | upper_bound | lower_bound
    sizeMethod       short tag for how the size is derived (default du)
    sizeBasis        documentation only, never printed; REQUIRED whenever
                     sizeKind is not exact (what the number measures, why
                     the approximation is acceptable, what to revisit)
    heldSizeCommand  bare KiB like sizeCommand, for data that is removable
                     but for a safety guard (e.g. a dirty or locked
                     worktree); run only after the main size succeeded,
                     under its own timeout
  Rendering ("exact" gets no suffix, so an all-exact run reads as it
  always did):
    size: 65.3G                                   exact
    size: ~972.8M (estimate: sqlite-closure)      estimate
    size: <=3.0G (upper_bound: du)                upper bound
    size: >=1.0G (lower_bound: git-scan)          lower bound
    <id>: held, not removable: 4.6G               held (omitted when 0)
  The total never merges kinds into one number: one bucket per kind,
  empty buckets omitted, held reported separately and never added in:
    total reclaimable: 65.3G exact + ~972.8M estimate (1 exact, 1 estimated, 1 unknown); held, not removable: 4.6G
  When every sized item is exact the plain "(N sized, M unknown)" total is
  printed. A held timeout or failure prints "held, not removable: unknown
  (<reason>)" and never voids the item's main size. `validate` rejects an
  unknown sizeKind, an empty sizeMethod/sizeBasis/heldSizeCommand, and a
  non-exact sizeKind without a sizeBasis.
  When an item's dry-run (or, with --apply, remove) command exits non-zero,
  the item still prints its own size and held lines but is NOT summed into
  the total (its sizes, kind bucket and held): the command did not complete,
  so the total makes no claim about it. The total says so instead of
  silently disagreeing with the per-item lines:
    total reclaimable: 1.0M (1 sized, 0 unknown); held, not removable: 1.0M; 1 item(s) failed, excluded from total
  A run where every item failed still prints that marked total.

Aggressiveness scale:
  --aggressiveness N is a CEILING: every item with a variant at level <= N
  is selected, and an item with several qualifying variants runs the one
  with the highest level <= N.
    0  tidy cruft              clutter whose removal costs nothing
    1  tidy                    finished work (merged/stale worktrees)
    2  caches                  cheap caches that refill in seconds
    3  deep                    local-only refill that is expensive
    4  costly or irreversible  network refill, lost rollback/history,
                               or live shared state
    5  hardest to recover      slow, manual rebuild of user-visible state
  Under --apply, a selected variant at level >= 4 asks for interactive
  confirmation (read from the terminal; there is no bypass). A dry run
  never prompts. The authoritative definition is
  packages/pg-disk-reclaimer/pg-disk-reclaimer/docs/aggressiveness-scale.md
  in the phillipgreenii-nix-agent-support repository.

Notes:
  'validate' checks the registry's JSON schema, then does a best-effort
  check that each command string's leading command/function exists. It
  cannot validate arbitrary shell logic inside a command string (pipes,
  subshells, a later command in a `&&` chain) -- only that the first
  invoked command/function exists.

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
