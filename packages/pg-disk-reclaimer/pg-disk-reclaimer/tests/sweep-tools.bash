# shellcheck shell=bash
# Shared probe for the tmp-sweep bats suite (bead pg2-sbvin).
#
# The guarded sweep (pgdr-tmp-sweep.bash) fails closed without a usable `ps` and
# `lsof`, and a darwin nix sandbox denies both. A case that needs them used to
# SKIP there, so a green nix check proved nothing about the sweep guards. Now:
#
#   - PGDR_REQUIRE_SWEEP_TOOLS=1 turns "the tools are unusable" into a FAILURE.
#     The non-sandboxed gate (the `pg-disk-reclaimer-tmp-sweep-test` pre-commit /
#     pre-land hook, scripts/run-tmp-sweep-bats.sh) and the Linux nix checks set
#     it. A Linux sandbox has a working ps (procps), so a skip there is a bug.
#   - Unset, the case skips, which is the only honest answer on a darwin nix
#     sandbox. Those runs are NOT the gate; the hook is.

# pgdr_sweep_tools_usable: 0 when ps and lsof both print something, else 1 with
# PGDR_SWEEP_TOOLS_WHY naming the first tool that does not.
pgdr_sweep_tools_usable() {
  local out
  PGDR_SWEEP_TOOLS_WHY=""
  if ! out="$(ps -axww -o command= 2>/dev/null)" || [[ -z $out ]]; then
    PGDR_SWEEP_TOOLS_WHY="ps is not usable here (sandbox?)"
    return 1
  fi
  if ! out="$(lsof -n -w -d cwd -Fn 2>/dev/null)" || [[ -z $out ]]; then
    PGDR_SWEEP_TOOLS_WHY="lsof is not usable here (sandbox?)"
    return 1
  fi
}

# pgdr_require_sweep_tools: call at the top of a case that sweeps. Returns 0 when
# the tools work; otherwise fails the case (PGDR_REQUIRE_SWEEP_TOOLS=1) or skips it.
pgdr_require_sweep_tools() {
  pgdr_sweep_tools_usable && return 0
  if [[ ${PGDR_REQUIRE_SWEEP_TOOLS:-} == 1 ]]; then
    echo "$PGDR_SWEEP_TOOLS_WHY; PGDR_REQUIRE_SWEEP_TOOLS=1 forbids skipping the sweep cases" >&2
    return 1
  fi
  skip "$PGDR_SWEEP_TOOLS_WHY: run the suite outside the sandbox too (pg-disk-reclaimer-tmp-sweep-test hook)"
}
