{
  config,
  lib,
  ...
}:
{
  # cx-claude is bundled with the cmux scripts package (pkgs.cx-scripts) which
  # the cmux module installs when cmux.enable = true. This module declares the
  # claude+cmux integration dependency so the relationship is explicit.
  config = lib.mkIf (
    config.phillipgreenii.programs.claude.enable
    && (config.phillipgreenii.programs.cmux.enable or false)
  ) { };
}
