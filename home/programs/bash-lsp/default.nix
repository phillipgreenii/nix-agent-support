{
  config,
  lib,
  ...
}:

let
  cfg = config.phillipgreenii.programs.bash-lsp;
  marketplaceRoot = ".local/share/pgii-local-plugins";
in
{
  options.phillipgreenii.programs.bash-lsp = {
    enable = lib.mkEnableOption "Bash LSP plugin for Claude";
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
    phillipgreenii.programs.claude.plugins.local.plugins.bash-lsp = {
      description = "Bash/Shell language server for code intelligence";
      source = "bash-lsp";
      enabledByDefault = true;
    };

    home.file."${marketplaceRoot}/bash-lsp/.claude-plugin/plugin.json" = {
      text = builtins.toJSON {
        name = "bash-lsp";
        description = "Bash/Shell language server for code intelligence";
        inherit (config.phillipgreenii.programs.claude.plugins.local) version;
        lspServers = {
          bash = {
            command = "bash-language-server";
            args = [ "start" ];
            extensionToLanguage = {
              ".sh" = "shellscript";
              ".bash" = "shellscript";
              ".zsh" = "shellscript";
            };
          };
        };
      };
    };
  };
}
