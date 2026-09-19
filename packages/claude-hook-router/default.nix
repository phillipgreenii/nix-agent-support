{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "claude-hook-router";

  src = lib.cleanSource ./.;

  subPackages = [ "cmd/claude-hook-router" ];

  gomod2nixToml = ./gomod2nix.toml;

  meta = {
    description = "Claude Code hook router: dispatch/merge runtime for the claude-hook-router plugin (ADR 0071)";
    mainProgram = "claude-hook-router";
  };
}
