{
  pkgs,
  bashBuilders,
}:
let
  testSupport = ./test-support;

  claude-activity-lib = pkgs.callPackage ./lib {
    inherit (bashBuilders) mkBashLibrary;
    inherit pkgs testSupport;
  };

  claude-activity-api = pkgs.callPackage ./claude-activity-api {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs claude-activity-lib testSupport;
  };

  claude-work-start = pkgs.callPackage ./claude-work-start {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs claude-activity-lib testSupport;
  };

  claude-work-end = pkgs.callPackage ./claude-work-end {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs claude-activity-lib testSupport;
  };
in
{
  inherit
    claude-activity-lib
    claude-activity-api
    claude-work-start
    claude-work-end
    ;
  packages = claude-activity-api.packages ++ claude-work-start.packages ++ claude-work-end.packages;
  inherit (claude-activity-api) tldr;
  checks = {
    test-claude-activity-lib = claude-activity-lib.check;
    test-claude-activity-api = claude-activity-api.check;
    test-claude-work-start = claude-work-start.check;
    test-claude-work-end = claude-work-end.check;
  };
}
