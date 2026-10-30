{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-claude-code-recorder";

  # Pattern A (phillipg-nix-repo-base ADR 0008): one standalone module at this
  # package dir with go.mod and the committed gomod2nix.toml side by side. No
  # local `replace`, so no modRoot (a later packet that takes a replace to
  # ../claude-transcript switches to Pattern B per
  # .claude/rules/package-versioning.md).
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/pg-claude-code-recorder" ];

  # This build is NOT the Go test gate (mkGoApp defaults doCheck = false): the
  # whole-module gate is checks.<system>.pg-claude-code-recorder-go-tests in
  # flake.nix.

  meta = {
    description = "pg-claude-code-recorder: records Claude Code state (scaffold)";
    mainProgram = "pg-claude-code-recorder";
  };
}
