{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.claude-extended-tool-approver;
  pkg = cfg.package;

  # knownAbsentRoots (pg2-fxu7k) is DELIBERATELY the SAME nix option that
  # home/programs/agent-rules already renders into the prose Absolute-Path
  # Provenance rule (A-1): `config.phillipgreenii.programs.claude-code.
  # knownAbsentRoots`, a Darwin-conditional `[ "/home" "/mnt" "/repo" ]` /
  # empty-on-Linux default. That prose rule is the retro's own measured
  # evidence that a MONTH of asking the model to self-check did not move the
  # failed-Read/Bash-call rate (pg2-5q1xj); this module feeds the identical
  # list into a MECHANICAL deny instead of inventing a second, parallel
  # "roots that don't exist on this machine" option that could drift from the
  # one A-1 already renders. A machine that needs a different list still
  # configures ONE option (knownAbsentRoots) to change both the prose
  # sentence and this mechanical guard together.
  knownAbsentRoots = config.phillipgreenii.programs.claude-code.knownAbsentRoots;

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
    ) ''--set CETA_EXTRA_READONLY_ROOTS "${lib.concatStringsSep ":" cfg.extraReadOnlyRoots}"''
    ++ lib.optional (
      knownAbsentRoots != [ ]
    ) ''--set CETA_DENIED_ROOTS "${lib.concatStringsSep ":" knownAbsentRoots}"'';

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

        It runs in a process group of its own with a 3s budget; on expiry the
        whole group is killed and the command is left unrewritten. A process the
        processor forks MUST NOT keep stdout open after the processor exits: the
        read is cut off 250ms later, the possibly-truncated rewrite is discarded
        (again leaving the command unrewritten), and the forked process is killed.
        Every outcome is reported on stderr; none of them block the tool call.
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
        ":"-separated list). Checked after all built-in zones. The option
        DEFAULT is empty so the option itself stays generic, but this module
        contributes a base set of home read-only inspection roots when enabled
        (see config, pg2-t76k8); definitions list-merge, so consumer/machine
        additions here are additive on top of that base set.
      '';
    };
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude-code.enable && cfg.enable) {
    # Base read-only inspection roots (pg2-t76k8): home dot-files/dirs that are
    # safe to READ for inspection but are deliberately NOT base-code path-safety
    # zones (broadening base zones was rejected in favor of this allow-list).
    # Fed through the existing extraReadOnlyRoots -> CETA_EXTRA_READONLY_ROOTS
    # plumbing; consumer definitions list-merge, so these stay present alongside
    # any org/machine additions. Absolute paths with ~ expanded to the HM home
    # directory, as CETA_EXTRA_READONLY_ROOTS expects (patheval symlink-resolves
    # each at runtime). Individual rc FILES are valid roots (pathContains
    # exact-matches a file). NOT ~/.config / ~/.gc / ~/.colima (secret-adjacent
    # or out of scope).
    phillipgreenii.programs.claude-extended-tool-approver.extraReadOnlyRoots = [
      "${config.home.homeDirectory}/.beads"
      "${config.home.homeDirectory}/.zshrc"
      "${config.home.homeDirectory}/.zshenv"
      "${config.home.homeDirectory}/.zprofile"
      "${config.home.homeDirectory}/.profile"
      "${config.home.homeDirectory}/.local/bin"
      "${config.home.homeDirectory}/.local/state"
    ];
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
