{
  mkBashLibrary,
  pkgs,
  testSupport ? null,
}:

mkBashLibrary {
  name = "claude-activity-lib";
  src = ./.;
  description = "Shared library for Claude activity tracking";
  inherit testSupport;
  testDeps = [
    pkgs.jq
    pkgs.coreutils
    pkgs.perl
  ];
}
