{
  pkgs,
  bashBuilders,
}:
let
  pg-disk-reclaimer = pkgs.callPackage ./pg-disk-reclaimer {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };
in
{
  inherit pg-disk-reclaimer;
  inherit (pg-disk-reclaimer) packages tldr;
  checks = {
    test-pg-disk-reclaimer = pg-disk-reclaimer.check;
  };
}
