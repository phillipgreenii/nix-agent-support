{
  mkBashLibrary,
  pkgs,
  testSupport ? null,
}:

mkBashLibrary {
  name = "agent-activity-lib";
  src = ./.;
  description = "Shared library for agent-activity orchestration";
  inherit testSupport;
  testDeps = [
    pkgs.jq
    pkgs.coreutils
  ];
}
