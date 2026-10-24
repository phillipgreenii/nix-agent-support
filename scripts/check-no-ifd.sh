#!/usr/bin/env bash
# Whole-repo import-from-derivation (IFD) guard (bead pg2-8l6hx).
#
# Evaluates this flake's `checks`, `packages` and `devShells` for one system as
# derivation paths with `--option allow-import-from-derivation false`: a real
# `nix eval`, no builds. Any import from derivation (evaluation having to build
# or fetch a derivation to continue) makes nix fail at the first such site, which
# this script reports. This is the same evaluation the eval-only land check runs
# with `--no-ifd` (`pn workspace flake-check --no-build --no-ifd`, `pre-land-eval
# --no-ifd`); use whichever is installed, this script needs only `nix`.
#
# Usage: check-no-ifd.sh [--root <flake-dir>] [--system <system>]
#                        [--override-input <name>=<flake-ref>]... [--set <name>]...
#   --root            the flake to evaluate (default: the repo this script lives in)
#   --system          default: the current system (`nix eval --impure` builtins.currentSystem)
#   --override-input  repeatable; passed to nix as `--override-input <name> <ref>`
#                     (workspace siblings, e.g. phillipgreenii-nix-base=git+file:///abs/nix-repo-base)
#   --set             repeatable; limit to checks, packages or devShells (default: all three)
#
# Nix stops at the FIRST IFD site per set, so a failure lists the first site, not
# necessarily all of them: fix it and re-run. A run takes one to three minutes
# (it evaluates every check); run it through `pg-nix-log-wrapped` and a timeout
# or `bgrun`. The one in-sandbox guard that runs under `nix build` is
# `checks.<system>.test-claude-hook-router-no-ifd`; a full drvPath evaluation
# needs every flake input fetched, which a build sandbox cannot do.
#
# Exits 0 when every selected set evaluates; 1 when a set fails (nix's first
# error lines are printed); 2 on a usage error.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
system=""
overrides=()
sets=()
while [ "$#" -gt 0 ]; do
  case "$1" in
  --root)
    root="${2:?--root needs a directory}"
    shift 2
    ;;
  --system)
    system="${2:?--system needs a value}"
    shift 2
    ;;
  --override-input)
    pair="${2:?--override-input needs <name>=<flake-ref>}"
    case "$pair" in
    *=*) ;;
    *)
      echo "check-no-ifd: --override-input expects <name>=<flake-ref>, got '$pair'" >&2
      exit 2
      ;;
    esac
    overrides+=(--override-input "${pair%%=*}" "${pair#*=}")
    shift 2
    ;;
  --set)
    case "${2:-}" in
    checks | packages | devShells) sets+=("$2") ;;
    *)
      echo "check-no-ifd: --set must be checks, packages or devShells" >&2
      exit 2
      ;;
    esac
    shift 2
    ;;
  -h | --help)
    sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
    exit 0
    ;;
  *)
    echo "check-no-ifd: unknown argument '$1'" >&2
    exit 2
    ;;
  esac
done

[ -f "$root/flake.nix" ] || {
  echo "check-no-ifd: no flake.nix under '$root'" >&2
  exit 2
}
[ "${#sets[@]}" -gt 0 ] || sets=(checks packages devShells)
if [ -z "$system" ]; then
  system="$(nix eval --impure --raw --expr builtins.currentSystem)"
fi

fail=0
for set in "${sets[@]}"; do
  err="$(mktemp)"
  if nix eval --no-write-lock-file --option allow-import-from-derivation false --json \
    "git+file://$root#$set.$system" "${overrides[@]}" \
    --apply 'set: builtins.mapAttrs (n: v: v.drvPath or null) set' >/dev/null 2>"$err"; then
    echo "ok    $set.$system"
  else
    echo "FAIL  $set.$system" >&2
    grep -E '^error|cannot build|allow-import-from-derivation' "$err" | head -5 >&2 || head -5 "$err" >&2
    fail=1
  fi
  rm -f "$err"
done
exit "$fail"
