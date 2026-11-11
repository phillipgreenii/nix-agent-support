{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-git-lander";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at this
  # package dir, with go.mod and the committed gomod2nix.toml side by side. No
  # local `replace`, so no modRoot and no parent-rooted fileset.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  # `var Version = "dev"` in cmd/pg-git-lander is the mkGoApp default ldflag
  # target (main.Version). This build is NOT the Go test gate: that is
  # checks.<system>.pg-git-lander-go-tests in flake.nix.
  subPackages = [ "cmd/pg-git-lander" ];

  meta = {
    description = "pg-git-lander: lock-safe rebase and land tool";
    mainProgram = "pg-git-lander";
  };
}
