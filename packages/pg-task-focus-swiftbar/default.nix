{
  pkgs,
  bashBuilders,
}:
let
  pg-task-focus-swiftbar = pkgs.callPackage ./pg-task-focus-swiftbar {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };
in
{
  inherit pg-task-focus-swiftbar;
  inherit (pg-task-focus-swiftbar) packages;
  checks = {
    test-pg-task-focus-swiftbar = pg-task-focus-swiftbar.check;
  };
}
