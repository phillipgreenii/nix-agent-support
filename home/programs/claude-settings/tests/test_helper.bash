# shellcheck shell=bash
#
# Shared resolver for the claude-settings activation helper scripts under test.
#
# The three claude-settings-*.sh sources obtain the act_* activation-output
# helpers (repo-base ADR 0014) from the mkBashScript framework, which sources
# the composed activation-lib into the built binary AND prepends a shebang. The
# RAW source under tests/.. therefore has no shebang and no act_* definitions,
# so running it directly (a manual `bats tests/` outside the Nix sandbox) fails:
# an exec of a shebang-less file falls back to /bin/sh and each act_* call is an
# undefined command.
#
# resolve_claude_settings_script bridges both run modes, mirroring the
# beads-provision-config test_helper's run_with_lib wrapper:
#   - CI / Nix (checks.testBashScripts): the framework-built binary is on PATH,
#     so `command -v <name>` wins and the packaged binary is used verbatim.
#   - Local dev (`bats tests/`): no binary on PATH, so it emits an executable
#     wrapper that (a) carries a real shebang, (b) defines minimal act_*
#     fallbacks, (c) sources the composed library from LIB_PATH when a check
#     provides one (the real helpers then override the fallbacks), and
#     (d) sources the raw sibling <name>.sh, forwarding all positional args.
# The returned path is drop-in for the existing `run "$SCRIPT" …` call sites.

# Usage: SCRIPT="$(resolve_claude_settings_script claude-settings-<name>)"
resolve_claude_settings_script() {
  local name="$1"
  local packaged
  packaged="$(command -v "$name" || true)"
  if [ -n "$packaged" ]; then
    printf '%s\n' "$packaged"
    return 0
  fi

  # No packaged binary on PATH: build a lib-sourcing wrapper around the raw
  # source for a local dev run. Placed under BATS_RUN_TMPDIR so bats cleans it
  # up; falls back to mktemp if that is not set.
  local raw wrapper
  raw="${BATS_TEST_DIRNAME}/../${name}.sh"
  if [ -n "${BATS_RUN_TMPDIR:-}" ]; then
    wrapper="${BATS_RUN_TMPDIR}/run_with_lib-${name}"
  else
    wrapper="$(mktemp)"
  fi

  cat >"$wrapper" <<WRAPPER
#!/usr/bin/env bash
# Minimal act_* fallbacks keep a local run (no LIB_PATH) working; the composed
# library overrides them when a Nix check exports LIB_PATH.
act_ok() { printf '  %s\n' "\$*"; }
act_warn() { printf '  %s\n' "\$*"; }
act_info() { printf '    %s\n' "\$*"; }
act_fail() { printf '  %s\n' "\$*"; }
act_detail() { printf '  %s\n' "\$*"; }
lib_path="\${LIB_PATH%%:*}"
if [ -n "\$lib_path" ] && [ -f "\$lib_path" ]; then
  source "\$lib_path"
fi
source "${raw}" "\$@"
WRAPPER
  chmod +x "$wrapper"
  printf '%s\n' "$wrapper"
}
