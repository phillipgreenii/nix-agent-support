{
  pkgs,
  bashBuilders,
}:
let
  integrate-branch-support = pkgs.callPackage ./integrate-branch-support {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };
in
{
  inherit integrate-branch-support;
  inherit (integrate-branch-support) packages tldr;
  checks = {
    test-integrate-branch-support = integrate-branch-support.check;
  };
}
