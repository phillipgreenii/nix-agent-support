{
  lib,
  stdenvNoCC,
  fetchurl,
  unzip,
  codeburn,
}:
# CodeBurn macOS menubar app (https://github.com/getagentseal/codeburn), taken from the upstream
# `mac-v<version>` GitHub release as a hash-pinned fixed-output fetch, so nothing is downloaded
# at activation time and the bytes are verified by nix, not by a checksum served next to the zip.
#
# The version tracks the nix-packaged CLI (`codeburn`): upstream publishes one menubar build per
# CLI version, and the menubar spawns that CLI. Releases from 0.9.19 and earlier were ad hoc
# signed (spctl rejected them); later ones are Developer ID signed and notarized. The bundle is
# installed UNMODIFIED — any rewrite would invalidate its code signature — so there is no
# fixupPhase (no strip/patchShebangs) and no quarantine handling is needed.
#
# Refresh on a version bump: set `version` to codeburn's, then
#   nix store prefetch-file https://github.com/getagentseal/codeburn/releases/download/mac-v<version>/CodeBurnMenubar-v<version>.zip
# and paste its hash below (the release also ships a .sha256 sidecar to cross-check against).
stdenvNoCC.mkDerivation (finalAttrs: {
  pname = "codeburn-menubar";
  inherit (codeburn) version;

  src = fetchurl {
    url = "https://github.com/getagentseal/codeburn/releases/download/mac-v${finalAttrs.version}/CodeBurnMenubar-v${finalAttrs.version}.zip";
    hash = "sha256-+P1759F6iURMW0Tep/NDir5tlkkb8ezacjDk8Fb+AfY=";
  };

  nativeBuildInputs = [ unzip ];

  sourceRoot = ".";

  installPhase = ''
    runHook preInstall
    mkdir -p "$out/Applications"
    cp -R CodeBurnMenubar.app "$out/Applications/"
    runHook postInstall
  '';

  dontFixup = true;

  meta = {
    description = "CodeBurn macOS menubar app (prebuilt, notarized upstream release)";
    homepage = "https://github.com/getagentseal/codeburn";
    license = lib.licenses.mit;
    platforms = lib.platforms.darwin;
    sourceProvenance = [ lib.sourceTypes.binaryNativeCode ];
  };
})
