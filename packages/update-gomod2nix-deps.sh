#!/usr/bin/env bash
# Standalone developer utility - not Nix-wrapped intentionally
set -euo pipefail

# Shared Go dependency updater for this repo's gomod2nix packages.
#
# Refresh Go module dependencies and regenerate gomod2nix.toml for ONE package.
# These packages build on the gomod2nix engine (ADR 0008), so the dependency
# graph is pinned by a committed gomod2nix.toml beside go.mod — there is no
# vendorHash to rewrite. `gomod2nix generate` rewrites the toml in place; no
# nix-update, no fake-hash dance, no fsmonitor hack.
#
# The package name and flake root are derived from the package directory, so
# this single script serves every package. Each package's ./update-deps.sh is a
# thin wrapper that passes its own directory.
#
# Usage: update-gomod2nix-deps.sh <package-dir>

PKG_DIR="$(cd "${1:?usage: update-gomod2nix-deps.sh <package-dir>}" && pwd)"
PKG_NAME="$(basename "${PKG_DIR}")"
FLAKE_ROOT="$(cd "${PKG_DIR}/../.." && pwd)"

# Devbox may export GOEXPERIMENT from its Go; clear so Nix-managed Go isn't confused.
unset GOEXPERIMENT

cd "${PKG_DIR}"

echo "==> Tidying Go modules..."
go mod tidy

echo ""
echo "==> Regenerating gomod2nix.toml..."
nix run github:nix-community/gomod2nix -- generate

echo ""
echo "==> Verifying build..."
if (cd "${FLAKE_ROOT}" && nix build ".#${PKG_NAME}" --no-link); then
  echo ""
  echo "✓ Success! Dependencies updated and gomod2nix.toml regenerated."
  echo "  Updated: go.mod, go.sum"
  echo "  Updated: gomod2nix.toml"
else
  echo ""
  echo "✗ Build failed. Check the output above." >&2
  exit 1
fi
