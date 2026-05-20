{
  mkBashScript,
  pkgs,
  pa-monitor,
}:
mkBashScript {
  name = "wait-for-agents-to-finish";
  src = ./.;
  description = "Wait for AI agents to finish working";
  runtimeDeps = [
    pkgs.coreutils
    pa-monitor
  ];
  testDeps = [
    pkgs.coreutils
    pa-monitor
  ];
}
