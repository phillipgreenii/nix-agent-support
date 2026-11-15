{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "pg-claude-code-recorder";

  # Pattern B (phillipg-nix-repo-base ADR 0008): go.mod locally replaces
  # ../pg-sqlite (the shared sqlite-open helper, ADR 0125), so the build sandbox
  # must contain both package dirs at their relative positions.
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../pg-sqlite
    ];
  };
  modRoot = "pg-claude-code-recorder";

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
