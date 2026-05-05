{
  mkBashScript,
  pkgs,
  agent-activity-lib,
  claude-activity,
  testSupport ? null,
}:

mkBashScript {
  name = "agent-activity-api";
  src = ./.;
  description = "Unified API for AI agent activity management";
  libraries = [ agent-activity-lib ];
  runtimeDeps = [
    pkgs.jq
    pkgs.coreutils
    claude-activity
  ];
  testDeps = [
    pkgs.jq
    pkgs.coreutils
    pkgs.perl
    claude-activity
  ];
  inherit testSupport;
}
