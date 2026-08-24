# shellcheck shell=bash
# pw-reset-agents - thin wrapper delegating to `agent-activity-api clean`.
#
# PASSTHROUGH CONTRACT: every argument is forwarded UNCHANGED to
# `agent-activity-api clean`, so the option surface belongs to
# agent-activity-api, not to this wrapper. Two exceptions, both additive:
#   * `-h`/`--help` is answered here -- `agent-activity-api clean` defines no
#     help option, so nothing that used to work changes.
#   * `-v`/`--version` is intercepted by the mkBashBuilders-injected handler
#     before this body runs (the builder reserves both spellings).
# There is deliberately NO local option table: `clean` takes no options today,
# and enumerating one here would drift silently the moment that changes.

show_help() {
  cat <<'HELP'
pw-reset-agents: Clear stale AI agent activity markers

Usage: pw-reset-agents [OPTIONS]

Delegates to `agent-activity-api clean`, which removes the activity markers left
behind by sessions that are no longer running, so waiters (pw-agent-activity)
stop counting them as busy. Arguments are forwarded verbatim; `clean` takes no
options of its own today.

Handled by this wrapper:

  -h, --help                  Show this help message
  -v, --version               Show version information

Exit codes:
  0  markers cleaned (the count is reported on stdout)
  non-zero  error from agent-activity-api

Run `agent-activity-api help` for the authoritative command list.

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

exec agent-activity-api clean "$@"
