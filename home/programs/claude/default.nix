{ lib, ... }:
{
  options.phillipgreenii.programs.claude.enable =
    lib.mkEnableOption "Claude Code AI assistant and associated tooling";
}
