{
  mkBashScript,
  pkgs,
  claude-activity-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "claude-work-end";
  src = ./.;
  description = "Claude hook script for Stop event";
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
