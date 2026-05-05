{
  config,
  lib,
  ...
}:
let
  cfg = config.phillipgreenii.programs.claude;
  rulesFile = ./pgii-agent-rules.md;
in
{
  config = lib.mkIf cfg.enable {
    phillipgreenii.programs.claude.plugins.local.plugins.agent-rules = {
      description = "Personal Claude Code agent rules (CLAUDE.md equivalent)";
      source = "agent-rules";
      enabledByDefault = true;
    };

    home.file.".local/share/pgii-local-plugins/agent-rules/CLAUDE.md".source = rulesFile;

    home.file.".local/share/pgii-local-plugins/agent-rules/plugin.json".text = builtins.toJSON {
      name = "agent-rules";
      inherit (cfg.plugins.local) version;
      description = "Personal Claude Code agent rules";
      type = "prompt";
    };
  };
}
