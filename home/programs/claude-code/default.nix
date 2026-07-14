{
  lib,
  pkgs,
  config,
  ...
}:
let
  cfg = config.phillipgreenii.programs.claude-code;
in
{
  # The single Claude Code FEATURE module (Plan 5): source of truth for the
  # binary and the suite gate. The claude-* suite modules (settings, status-line,
  # theme, marketplaces, plugins, agent-rules, …) gate on this option; the plugin
  # model lives in pgii-claude-plugins, not here.
  options.phillipgreenii.programs.claude-code.enable =
    lib.mkEnableOption "Claude Code AI assistant and associated tooling";

  config = lib.mkIf cfg.enable {
    # Sole installer of the claude binary (was nix-personal's claude-code module,
    # deleted in the same cutover). Sourced from llm-agents per the repo's
    # AI-agent package-sourcing order; matches the darwin module + beads idiom.
    home.packages = [ pkgs.llm-agentsPkgs.claude-code ];
  };
}
