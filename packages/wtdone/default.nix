{
  pkgs,
  bashBuilders,
}:
let
  # Shared hermetic-by-construction bats git-fixture harness (pg2-hpurf;
  # design pg2-gucfd), consumed BY REFERENCE from phillipg-nix-repo-base's
  # `git-fixture-harness` package (pkgs.git-fixture-harness via this flake's
  # overlay; pg2-xy4w7) -- no vendored copy to drift.
  testSupport = "${pkgs.git-fixture-harness}/lib/scripts";

  wt-liveness-lib = pkgs.callPackage ./wt-liveness-lib {
    inherit (bashBuilders) mkBashLibrary;
    inherit pkgs;
  };

  wtdone = pkgs.callPackage ./wtdone {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs testSupport wt-liveness-lib;
  };
in
{
  inherit wt-liveness-lib wtdone;
  inherit (wtdone) packages tldr;
  checks = {
    test-wt-liveness-lib = wt-liveness-lib.check;
    test-wtdone = wtdone.check;
  };
}
