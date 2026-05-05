{
  mkBashScript,
  pkgs,
  claude-activity-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "claude-activity-api";
  src = ./.;
  description = "CLI API for querying and managing Claude activity sessions";
  libraries = [ claude-activity-lib ];
  runtimeDeps = [
    pkgs.jq
    pkgs.coreutils
  ];
  testDeps = [
    pkgs.jq
    pkgs.coreutils
    pkgs.perl
  ];
  inherit testSupport;
}
