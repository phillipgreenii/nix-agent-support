{
  pkgs,
  bashBuilders,
}:
let
  testSupport = ./test-support;

  bg-tools-lib = pkgs.callPackage ./lib {
    inherit (bashBuilders) mkBashLibrary;
    inherit pkgs testSupport;
  };

  bgrun = pkgs.callPackage ./bgrun {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs bg-tools-lib testSupport;
  };

  bgcheck = pkgs.callPackage ./bgcheck {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs bg-tools-lib testSupport;
  };

  bgwait = pkgs.callPackage ./bgwait {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs bg-tools-lib testSupport;
  };
in
{
  inherit
    bg-tools-lib
    bgrun
    bgcheck
    bgwait
    ;
  packages = bgrun.packages ++ bgcheck.packages ++ bgwait.packages;
  tldr = bgrun.tldr ++ bgcheck.tldr ++ bgwait.tldr;
  checks = {
    test-bg-tools-lib = bg-tools-lib.check;
    test-bgrun = bgrun.check;
    test-bgcheck = bgcheck.check;
    test-bgwait = bgwait.check;
  };
}
