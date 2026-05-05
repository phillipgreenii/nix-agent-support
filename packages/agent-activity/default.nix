{
  pkgs,
  bashBuilders,
  claude-activity,
}:
let
  testSupport = ./test-support;

  agent-activity-lib = pkgs.callPackage ./lib {
    inherit (bashBuilders) mkBashLibrary;
    inherit pkgs testSupport;
  };

  agent-activity-api = pkgs.callPackage ./agent-activity-api {
    inherit (bashBuilders) mkBashScript;
    inherit
      pkgs
      agent-activity-lib
      claude-activity
      testSupport
      ;
  };
in
{
  inherit
    agent-activity-lib
    agent-activity-api
    ;
  inherit (agent-activity-api) packages;
  inherit (agent-activity-api) tldr;
  checks = {
    test-agent-activity-lib = agent-activity-lib.check;
    test-agent-activity-api = agent-activity-api.check;
  };
}
