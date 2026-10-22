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

  bgkill = pkgs.callPackage ./bgkill {
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
    bgkill
    ;
  packages = bgrun.packages ++ bgcheck.packages ++ bgwait.packages ++ bgkill.packages;
  tldr = bgrun.tldr ++ bgcheck.tldr ++ bgwait.tldr ++ bgkill.tldr;
  checks = {
    test-bg-tools-lib = bg-tools-lib.check;
    test-bgrun = bgrun.check;
    test-bgcheck = bgcheck.check;
    test-bgwait = bgwait.check;
    test-bgkill = bgkill.check;
  };
}
