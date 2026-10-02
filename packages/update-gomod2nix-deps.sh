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
# `generate --with-deps` is REQUIRED (phillipg-nix-repo-base ADR 0031): it writes
# the `cachePackages` list that primes mkGoApp's Go build cache. A plain
# `generate` silently REMOVES `cachePackages`, so every build compiles the whole
# dependency graph cold again.
#
# gomod2nix is PINNED to the rev this repo's flake.lock locks (an unpinned
# `nix run github:nix-community/gomod2nix` follows HEAD and can generate a toml
# the locked builder does not match). The script refuses to run if the pin
# drifts from flake.lock; bump both together.
#
# The package name and flake root are derived from the package directory, so
# this single script serves every package. Each package's ./update-deps.sh is a
# thin wrapper that passes its own directory.
#
# Usage: update-gomod2nix-deps.sh <package-dir>

GOMOD2NIX_REV="1201ddd1279c35497754f016ef33d5e060f3da8d"

PKG_DIR="$(cd "${1:?usage: update-gomod2nix-deps.sh <package-dir>}" && pwd)"
PKG_NAME="$(basename "${PKG_DIR}")"
FLAKE_ROOT="$(cd "${PKG_DIR}/../.." && pwd)"

# Devbox may export GOEXPERIMENT from its Go; clear so Nix-managed Go isn't confused.
unset GOEXPERIMENT

# Refuse to generate with a gomod2nix that differs from the locked builder.
LOCKED_REV="$(jq -r '.nodes[.nodes.root.inputs.gomod2nix].locked.rev // empty' "${FLAKE_ROOT}/flake.lock")"
if [ "${LOCKED_REV}" != "${GOMOD2NIX_REV}" ]; then
  echo "ERROR: GOMOD2NIX_REV (${GOMOD2NIX_REV}) does not match flake.lock's gomod2nix rev (${LOCKED_REV:-<none>})." >&2
  echo "       Update GOMOD2NIX_REV in $0 to the locked rev." >&2
  exit 1
fi

cd "${PKG_DIR}"

echo "==> Tidying Go modules..."
go mod tidy

echo ""
echo "==> Regenerating gomod2nix.toml (--with-deps, gomod2nix ${GOMOD2NIX_REV})..."
nix run "github:nix-community/gomod2nix/${GOMOD2NIX_REV}" -- generate --with-deps

# Library modules (e.g. claude-transcript) have no flake package attr, so there
# is nothing to `nix build`; skip the verify step for them.
SYSTEM="$(nix eval --impure --raw --expr builtins.currentSystem)"
HAS_ATTR="$(cd "${FLAKE_ROOT}" && nix eval --json ".#packages.${SYSTEM}" --apply "builtins.hasAttr \"${PKG_NAME}\"")"

echo ""
if [ "${HAS_ATTR}" != "true" ]; then
  echo "==> No flake package attr '${PKG_NAME}' (library module); skipping build verification."
  echo ""
  echo "✓ Success! Dependencies updated and gomod2nix.toml regenerated."
  echo "  Updated: go.mod, go.sum"
  echo "  Updated: gomod2nix.toml"
  exit 0
fi

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
