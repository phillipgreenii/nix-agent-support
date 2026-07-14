{ lib, ... }:
{
  options.phillipgreenii.programs.claude-code.enable =
    lib.mkEnableOption "Claude Code AI assistant and associated tooling";
}
