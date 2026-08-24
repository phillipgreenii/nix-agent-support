# shellcheck shell=bash
# pw-agent-activity - thin wrapper delegating to `agent-activity-api wait`.
#
# PASSTHROUGH CONTRACT: every argument is forwarded UNCHANGED to
# `agent-activity-api wait`, so the option surface belongs to agent-activity-api,
# not to this wrapper. Two exceptions, both additive:
#   * `-h`/`--help` is answered here -- `agent-activity-api wait` defines no help
#     option and rejects one with exit 2, so nothing that used to work changes.
#   * `-v`/`--version` is intercepted by the mkBashBuilders-injected handler
#     before this body runs (the builder reserves both spellings).
# There is deliberately NO local option table: enumerating agent-activity-api's
# `wait` options here would drift silently the next time that tool gains,
# renames, or drops one -- the exact failure mode wait-for-agents-to-finish had
# to document against pa-monitor.

show_help() {
  cat <<'HELP'
pw-agent-activity: Wait for all AI agents to finish

Usage: pw-agent-activity [OPTIONS]

Every option is forwarded verbatim to `agent-activity-api wait`, which owns the
option surface. At the time of writing it accepts:

  --maximum-wait SECONDS      Maximum wait time (default: 7200 = 2h)
  --time-between-checks SECS  Interval between checks (default: 5)
  --caffeinate                Keep the Mac awake while waiting (macOS only)

Handled by this wrapper:

  -h, --help                  Show this help message
  -v, --version               Show version information

Exit codes:
  0  all agents finished
  1  timeout (--maximum-wait elapsed)
  2  error (invalid arguments)

Run `agent-activity-api help` for the authoritative option list.

Report bugs to: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support/issues>
HELP
}

# SCAN the arguments without consuming them: help is answered here, everything
# else is forwarded byte-for-byte. `for arg in "$@"` rather than collecting into
# an array on purpose -- "$@" is safe with zero arguments under the injected
# `set -u`, whereas `"${arr[@]}"` on an empty array is an unbound-variable error
# on bash 3.2, which is exactly the interpreter a stripped-PATH caller lands on
# (launchd hands down PATH=/usr/bin:/bin, where /bin/bash is 3.2 on macOS).
for arg in "$@"; do
  case "$arg" in
  -h | --help)
    show_help
    exit 0
    ;;
  esac
done

exec agent-activity-api wait "$@"
