{
  pkgs,
  bashBuilders,
  integrate-branch-support,
}:
let
  wtnew = pkgs.callPackage ./wtnew {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs integrate-branch-support;
  };
in
{
  inherit wtnew;
  inherit (wtnew) packages tldr;
  checks = {
    test-wtnew = wtnew.check;
  };
}
