{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.claude-extended-tool-approver;
  pkg = cfg.package;
  hookPkg =
    if cfg.inputProcessor != null then
      pkgs.symlinkJoin {
        name = "${pkg.name}-wrapped";
        paths = [ pkg ];
        nativeBuildInputs = [ pkgs.makeWrapper ];
        postBuild = ''
          wrapProgram $out/bin/claude-extended-tool-approver \
            --set CETA_INPUT_PROCESSOR "${cfg.inputProcessor}"
        '';
      }
    else
      pkg;
in
{
  options.phillipgreenii.programs.claude-extended-tool-approver = {
    enable = lib.mkEnableOption "claude-extended-tool-approver permission evaluator";
    package = lib.mkPackageOption pkgs "claude-extended-tool-approver" { };
    inputProcessor = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        Command to rewrite bash commands before execution.
        Called as: <command> "<bash-command>".
        Exit 0 + stdout = rewritten command, exit 1+ = no rewrite.
      '';
    };
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude.enable && cfg.enable) {
    home = {
      # Plugin registration + content (plugin.json, skills, hooks/hooks.json) now
      # live in the committed claude-marketplace/ tree, built by the nix
      # marketplace package. The marketplace's hooks.json uses a BARE
      # `claude-extended-tool-approver` command; installing hookPkg here puts the
      # (possibly rtk/inputProcessor-wrapped) binary on PATH so that bare command
      # resolves to THIS binary.
      packages = [ hookPkg ];
    };
  };
}
