{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "claude-tool-tracker";

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
  modRoot = "claude-tool-tracker";

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/claude-tool-tracker" ];

  # This build is NOT the Go test gate (mkGoApp defaults doCheck = false): the
  # whole-module gate is checks.<system>.claude-tool-tracker-go-tests in flake.nix.

  meta = {
    description = "claude-tool-tracker: appends one tool-event line per call to the tracker's event log; hook mode for Claude Code command hooks";
    mainProgram = "claude-tool-tracker";
  };
}
