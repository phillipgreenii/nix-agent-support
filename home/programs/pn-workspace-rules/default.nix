{
  config,
  lib,
  ...
}:
let
  cfg = config.phillipgreenii.programs.claude;
  rulesFile = ./pn-workspace-rules.md;
in
{
  config = lib.mkIf cfg.enable {
    phillipgreenii.programs.claude.plugins.local.plugins.pn-workspace-rules = {
      description = "pn-workspace conventions for AI agents (cardinal rules + command surface cheat-sheet)";
      source = "pn-workspace-rules";
      enabledByDefault = true;
    };

    home.file.".local/share/pgii-local-plugins/pn-workspace-rules/CLAUDE.md".source = rulesFile;

    home.file.".local/share/pgii-local-plugins/pn-workspace-rules/.claude-plugin/plugin.json".text =
      builtins.toJSON
        {
          name = "pn-workspace-rules";
          inherit (cfg.plugins.local) version;
          description = "pn-workspace conventions for AI agents";
        };
  };
}
