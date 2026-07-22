{
  lib,
  buildNpmPackage,
  fetchurl,
  importNpmLock,
  nodejs_22,
}:
# codeburn (https://github.com/getagentseal/codeburn) — a published, tsup-bundled ESM CLI.
#
# This is the FIRST npm package in this repo (buildNpmPackage/importNpmLock otherwise only
# appears in phillipgreenii-nix-support-apps). We deliberately do NOT build from source:
# codeburn's `npm run build` runs `scripts/bundle-litellm.mjs` (fetches LiteLLM pricing over
# the network) and `build:dash` (a nested `cd dash && npm install`) — both fail in the Nix
# build sandbox. Instead we take the fully-built `dist/` from the published npm tarball and
# supply its runtime dependencies as node_modules — tsup externalizes ALL of a package's
# `dependencies` by default, so codeburn's bundle (dist/main.js) `import`s every one of its
# ten declared deps at runtime rather than inlining them.
#
# The in-tree package.json + package-lock.json (this directory) are a synthetic shim pinning
# exactly those runtime deps (at the versions codeburn's own lock resolves). Regenerate
# the lock after a version bump with:
#   nix shell nixpkgs#nodejs_22 -c npm install --package-lock-only --no-audit --no-fund
# and refresh `version` + the `distTarball` hash by hand (third-party deps bump manually here,
# per CLAUDE.md "Versioning of Custom Packages").
buildNpmPackage (finalAttrs: {
  pname = "codeburn";
  version = "0.9.19";

  # In-tree synthetic package. Keeping the lock in-tree lets importNpmLock read it without
  # import-from-derivation (mirrors nix-support-apps' jsonl-log-parser).
  src = ./.;

  # codeburn requires Node >= 22.13 — its CLI entry (dist/cli.js) is TypeScript run directly
  # via Node's type-stripping.
  nodejs = nodejs_22;

  npmDeps = importNpmLock { npmRoot = ./.; };
  inherit (importNpmLock) npmConfigHook;

  # The dist is prebuilt in the tarball; never run codeburn's own (networked) build, and skip
  # dependency lifecycle scripts (the pinned runtime closure is pure-JS; nothing to compile).
  dontNpmBuild = true;
  npmFlags = [ "--ignore-scripts" ];

  # Prebuilt, fully-bundled artifact from the npm registry (LiteLLM pricing + web dashboard
  # already built in). This is the ONLY remote hash to bump on a version change.
  distTarball = fetchurl {
    url = "https://registry.npmjs.org/codeburn/-/codeburn-${finalAttrs.version}.tgz";
    hash = "sha256-5TFPJQTDfn6Aeatw1NCBwU7/SBXnn7IKg2TPgVr+F+Q=";
  };

  # Replace the (absent) in-tree dist with the prebuilt one before pack/install. npm packs
  # only `dist` (package.json `files`), so the runtime deps resolved by importNpmLock land in
  # $out/lib/node_modules/codeburn/node_modules alongside it.
  postPatch = ''
    tar -xzf "$distTarball"
    rm -rf dist
    mv package/dist ./dist
    rm -rf package
  '';

  meta = {
    description = "Local AI coding token usage & cost tracker (terminal, web, macOS menubar)";
    homepage = "https://github.com/getagentseal/codeburn";
    license = lib.licenses.mit;
    mainProgram = "codeburn";
    platforms = lib.platforms.unix;
  };
})
