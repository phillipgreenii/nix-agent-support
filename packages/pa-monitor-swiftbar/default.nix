{
  pkgs,
  bashBuilders,
}:
let
  pa-monitor-swiftbar = pkgs.callPackage ./pa-monitor-swiftbar {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };
in
{
  inherit pa-monitor-swiftbar;
  inherit (pa-monitor-swiftbar) packages;
  checks = {
    test-pa-monitor-swiftbar = pa-monitor-swiftbar.check;
  };
}
