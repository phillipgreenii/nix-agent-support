{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "claude-tool-tracker";

  # Pattern A (phillipg-nix-repo-base ADR 0008): one standalone module at this
  # package dir, stdlib only, so there is no local `replace` and no modRoot.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  subPackages = [ "cmd/claude-tool-tracker" ];

  # This build is NOT the Go test gate (mkGoApp defaults doCheck = false): the
  # whole-module gate is checks.<system>.claude-tool-tracker-go-tests in flake.nix.

  meta = {
    description = "claude-tool-tracker: appends one tool-event line per call to the tracker's event log; hook mode for Claude Code command hooks";
    mainProgram = "claude-tool-tracker";
  };
}
