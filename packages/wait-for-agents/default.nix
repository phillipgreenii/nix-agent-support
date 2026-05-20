{
  pkgs,
  bashBuilders,
  pa-monitor,
}:
let
  wait-for-agents-to-finish = pkgs.callPackage ./wait-for-agents-to-finish {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pa-monitor;
  };
in
{
  inherit wait-for-agents-to-finish;
  inherit (wait-for-agents-to-finish) packages tldr;
  checks = {
    test-wait-for-agents = wait-for-agents-to-finish.check;
  };
}
