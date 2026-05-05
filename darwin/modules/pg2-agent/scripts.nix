# Pure script builders for pg2-agent module
# Uses mkBashBuilders from phillipgreenii-nix-base
{
  pkgs,
  bashBuilders,
  agents,
}:
let
  pg2-agent = pkgs.callPackage ./pg2-agent {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs agents;
  };
in
{
  inherit pg2-agent;
  inherit (pg2-agent) packages tldr;
  checks = {
    test-pg2-agent = pg2-agent.check;
  };
}
