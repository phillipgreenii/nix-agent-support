{
  mkBashScript,
  pkgs,
  claude-agents-tui,
}:
mkBashScript {
  name = "wait-for-agents-to-finish";
  src = ./.;
  description = "Wait for AI agents to finish working";
  runtimeDeps = [
    pkgs.coreutils
    claude-agents-tui
  ];
  testDeps = [
    pkgs.coreutils
    claude-agents-tui
  ];
}
