{
  config,
  lib,
  ...
}:
let
  cfg = config.phillipgreenii.programs.claude;
  marketplaceRoot = ".local/share/pgii-local-plugins";
  # Short marketplace/UI description. The full triggering description lives in
  # skills/bead-grooming/SKILL.md frontmatter (that is what Claude matches on).
  description = "Claude skill: review open beads (bd/beads) and bring them to plan-ready quality — acceptance criteria, light polish, and human-flagging.";
in
{
  config = lib.mkIf cfg.enable {
    phillipgreenii.programs.claude.plugins.local.plugins.bead-grooming = {
      inherit description;
      source = "bead-grooming";
      enabledByDefault = true;
    };

    home.file."${marketplaceRoot}/bead-grooming/.claude-plugin/plugin.json".text = builtins.toJSON {
      name = "bead-grooming";
      inherit (cfg.plugins.local) version;
      inherit description;
    };

    home.file."${marketplaceRoot}/bead-grooming/skills" = {
      source = ./skills;
      recursive = true;
    };
  };
}
