#!/usr/bin/env bash
# Standalone developer utility - not Nix-wrapped intentionally
set -euo pipefail

# Refresh Go module dependencies and regenerate gomod2nix.toml.
# This package builds on the gomod2nix engine (ADR 0008), so the dependency
# graph is pinned by a committed gomod2nix.toml beside go.mod — there is no
# vendorHash to rewrite. `gomod2nix generate` rewrites the toml in place; no
# nix-update, no fake-hash dance, no fsmonitor hack.
#
# Usage: ./update-deps.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FLAKE_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
PKG_NAME="pg-pr"

# Devbox may export GOEXPERIMENT from its Go; clear so Nix-managed Go isn't confused.
unset GOEXPERIMENT

cd "${SCRIPT_DIR}"

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
