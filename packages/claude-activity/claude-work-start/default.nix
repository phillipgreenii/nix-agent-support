{
  mkBashScript,
  pkgs,
  claude-activity-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "claude-work-start";
  src = ./.;
  description = "Claude hook script for UserPromptSubmit event";
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
