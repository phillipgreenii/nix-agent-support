{
  pkgs,
  bashBuilders,
  agent-activity,
}:
let
  pw-reset-agents = pkgs.callPackage ./pw-reset-agents {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs agent-activity;
  };
in
{
  inherit pw-reset-agents;
  inherit (pw-reset-agents) packages tldr;
  checks = {
    test-pw-reset-agents = pw-reset-agents.check;
  };
}
