{
  pkgs,
  bashBuilders,
}:
let
  pg-go-gate = pkgs.callPackage ./pg-go-gate {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };
in
{
  inherit pg-go-gate;
  inherit (pg-go-gate) packages tldr;
  checks = {
    test-pg-go-gate = pg-go-gate.check;
  };
}
