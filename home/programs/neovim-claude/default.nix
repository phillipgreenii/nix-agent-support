{
  config,
  lib,
  pkgs,
  ...
}:
{
  config = lib.mkIf (config.phillipgreenii.programs.claude.enable && config.programs.neovim.enable) {
    programs.neovim = {
      plugins = [
        pkgs.unstable.vimPlugins.claudecode-nvim
      ];

      extraLuaConfig = builtins.readFile ./config.lua;
    };
  };
}
