{
  config,
  lib,
  pkgs,
  ...
}:
{
  config =
    lib.mkIf (config.phillipgreenii.programs.claude-code.enable && config.programs.neovim.enable)
      {
        programs.neovim = {
          plugins = [
            pkgs.unstable.vimPlugins.claudecode-nvim
          ];

          initLua = builtins.readFile ./config.lua;
        };
      };
}
