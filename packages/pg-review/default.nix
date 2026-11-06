{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-review";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at this
  # package dir, with go.mod and the committed gomod2nix.toml side by side. No
  # local `replace`, so no modRoot and no parent-rooted fileset.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  # Build every directory under cmd/, so later packets add binaries (the
  # providers) without editing this file. Each declares `var Version = "dev"`,
  # the mkGoApp default ldflag target.
  subPackages = map (d: "cmd/${d}") (
    builtins.attrNames (lib.filterAttrs (_: t: t == "directory") (builtins.readDir ./cmd))
  );

  # The whole-module Go test gate is checks.<system>.pg-review-go-tests.

  meta = {
    description = "Self-review: find, rule on and track improvements to how agents work";
    mainProgram = "pg-review";
  };
}
