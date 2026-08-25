# shellcheck shell=bash
# pg-disk-reclaimer.bash - core subcommand logic, split out of
# pg-disk-reclaimer.sh so it can be unit-tested without going through
# argument parsing (see the bash-scripting skill's ".sh / .bash split").
#
# Every function here is a STUB for this scaffold task (bead pg2-txxyj.1).
# Later tasks in the pg2-txxyj epic replace the bodies:
#   - pg2-txxyj.2: registry loading + schema validation engine
#   - pg2-txxyj.3: variant-selection algorithm
#   - pg2-txxyj.4: cmd_list
#   - pg2-txxyj.5: cmd_validate
#   - pg2-txxyj.6: cmd_reclaim
# Names/signatures below are placeholders; later tasks MAY rename them.

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
