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
in
{
  inherit bg-tools-lib bgrun bgcheck;
  packages = bgrun.packages ++ bgcheck.packages;
  tldr = bgrun.tldr ++ bgcheck.tldr;
  checks = {
    test-bg-tools-lib = bg-tools-lib.check;
    test-bgrun = bgrun.check;
    test-bgcheck = bgcheck.check;
  };
}
