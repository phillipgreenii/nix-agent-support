{
  pkgs,
  bashBuilders,
  agent-activity,
}:
let
  pw-agent-activity = pkgs.callPackage ./pw-agent-activity {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs agent-activity;
  };
in
{
  inherit pw-agent-activity;
  inherit (pw-agent-activity) packages tldr;
  checks = {
    test-pw-agent-activity = pw-agent-activity.check;
  };
}
