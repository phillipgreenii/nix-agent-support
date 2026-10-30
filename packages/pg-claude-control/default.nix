{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-claude-control";

  # Pattern A (phillipg-nix-repo-base ADR 0008): one standalone module at this
  # package dir with go.mod and the committed gomod2nix.toml side by side. No
  # local `replace`, so no modRoot.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-claude-control" ];

  # This build is NOT the Go test gate (mkGoApp defaults doCheck = false): the
  # whole-module gate is checks.<system>.pg-claude-control-go-tests in flake.nix.

  meta = {
    description = "pg-claude-control: send submit, interrupt, run, stop and read requests to a running Claude's control inbox";
    mainProgram = "pg-claude-control";
  };
}
