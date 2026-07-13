#!/usr/bin/env bash
# Standalone developer utility - not Nix-wrapped intentionally
#
# Thin wrapper: delegates to the shared gomod2nix dependency updater
# (packages/update-gomod2nix-deps.sh), which derives this package's name and
# the flake root from the directory passed below. Kept per-package so the
# documented `./update-deps.sh` entry point (README, update-locks.sh) still works.
set -euo pipefail

_pkg_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "${_pkg_dir}/../update-gomod2nix-deps.sh" "${_pkg_dir}"
