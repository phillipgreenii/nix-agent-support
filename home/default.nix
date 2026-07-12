{ ... }:
{
  imports = [
    ./programs/claude
    ./programs/claude-settings
    ./programs/claude-status-line
    ./programs/claude-theme
    ./programs/pgii-claude-plugins
    ./programs/claude-marketplaces
    ./programs/agent-rules
    ./programs/agentsview
    ./programs/ccusage
    ./programs/ollama
    ./programs/opencode
    ./programs/tuicr
    ./programs/neovim-claude
    ./programs/cmux-claude
    ./programs/agent-activity
    ./programs/claude-activity
    ./programs/pa-monitor
    ./programs/ccpool
    ./programs/pr-pool
    ./programs/pb
    ./programs/gc-dolt-maintenance
    ./programs/claude-extended-tool-approver
    ./programs/git-tools
    ./programs/pg-pr
    ./programs/wait-for-agents
    # NOTE: ./programs/beads is intentionally NOT bundled here. nix-personal's
    # `agent-tools` capability now owns the canonical phillipgreenii.programs.beads
    # module; bundling this repo's copy too made consumers that import BOTH (e.g.
    # homelab tcadmin) declare the option twice ("already declared" eval error).
    # The module file remains for standalone/darwin use; compose it explicitly if a
    # consumer needs this repo's extra beads config (dolt, BD_JSON_ENVELOPE) and does
    # NOT also get beads from nix-personal. (tc-4uyk3, Plan 4b R5.)
    ./programs/serena
    ./programs/pw-reset-agents
    ./programs/pw-agent-activity
  ];
}
