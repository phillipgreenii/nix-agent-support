{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.claude-extended-tool-approver;
  pkg = cfg.package;

  # wrapProgram flags, contributed only by the settings that are active. The
  # binary is wrapped iff at least one flag is present; otherwise the unwrapped
  # package is used directly.
  wrapArgs =
    lib.optional (cfg.inputProcessor != null) ''--set CETA_INPUT_PROCESSOR "${cfg.inputProcessor}"''
    ++ lib.optional (
      cfg.extraReadWriteRoots != [ ]
    ) ''--set CETA_EXTRA_READWRITE_ROOTS "${lib.concatStringsSep ":" cfg.extraReadWriteRoots}"''
    ++ lib.optional (
      cfg.extraReadOnlyRoots != [ ]
    ) ''--set CETA_EXTRA_READONLY_ROOTS "${lib.concatStringsSep ":" cfg.extraReadOnlyRoots}"'';

  hookPkg =
    if wrapArgs == [ ] then
      pkg
    else
      pkgs.symlinkJoin {
        name = "${pkg.name}-wrapped";
        paths = [ pkg ];
        nativeBuildInputs = [ pkgs.makeWrapper ];
        postBuild = ''
          wrapProgram $out/bin/claude-extended-tool-approver \
            ${lib.concatStringsSep " " wrapArgs}
        '';
      };
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
    extraReadWriteRoots = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = ''
        Additional absolute paths whose subtrees the path-safety evaluator
        classifies read-write (exported as CETA_EXTRA_READWRITE_ROOTS, a
        ":"-separated list). Checked after all built-in zones. Empty by default
        so this repo stays generic; set org/machine paths in the consuming flake.
      '';
    };
    extraReadOnlyRoots = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = ''
        Additional absolute paths whose subtrees the path-safety evaluator
        classifies read-only (exported as CETA_EXTRA_READONLY_ROOTS, a
        ":"-separated list). Checked after all built-in zones. Empty by default
        so this repo stays generic; set org/machine paths in the consuming flake.
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
