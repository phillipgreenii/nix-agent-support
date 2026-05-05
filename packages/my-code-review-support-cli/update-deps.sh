#!/usr/bin/env bash
# Standalone developer utility - not Nix-wrapped intentionally
set -euo pipefail

# Update Go module dependencies and refresh vendorHash in default.nix.
# Uses nix-update to rewrite the hash in place (no fake-hash dance, no error
# scraping). Safe to run from the package directory or via the top-level
# update-locks.sh (which handles fsmonitor itself).
#
# Usage: ./update-deps.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FLAKE_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
PKG_NAME="my-code-review-support-cli"

# Devbox may export GOEXPERIMENT from its Go; clear so Nix-managed Go isn't confused.
unset GOEXPERIMENT

# nix-update trips on git fsmonitor's .git/fsmonitor--daemon.ipc socket.
# When invoked standalone, suspend fsmonitor for the duration. When invoked
# from update-locks.sh, fsmonitor is already off — this becomes a no-op.
_fsmonitor_was_active="$(git -C "${FLAKE_ROOT}" config core.fsmonitor 2>/dev/null || echo false)"
if [ "${_fsmonitor_was_active}" = "true" ]; then
  git -C "${FLAKE_ROOT}" config core.fsmonitor false
  git -C "${FLAKE_ROOT}" fsmonitor--daemon stop 2>/dev/null || true
  trap '
    if [ "${_fsmonitor_was_active}" = "true" ]; then
      git -C "'"${FLAKE_ROOT}"'" config core.fsmonitor true
    fi
  ' EXIT
fi

cd "${SCRIPT_DIR}"

echo "==> Tidying Go modules..."
go mod tidy

echo ""
echo "==> Refreshing vendorHash via nix-update..."
(
  cd "${FLAKE_ROOT}"
  nix run nixpkgs#nix-update -- -F --no-src --version=skip "${PKG_NAME}"
)

echo ""
echo "==> Verifying build..."
if (cd "${FLAKE_ROOT}" && nix build ".#${PKG_NAME}" --no-link); then
  echo ""
  echo "✓ Success! Dependencies updated and vendorHash refreshed."
  echo "  Updated: go.mod, go.sum"
  echo "  Updated: default.nix (vendorHash)"
else
  echo ""
  echo "✗ Build failed. Check the output above." >&2
  exit 1
fi
