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

  # effectiveInputProcessors (bead tc-7m85u item 1) is the ORDERED list ceta
  # actually receives: the deprecated scalar inputProcessor, if set, is
  # PREPENDED to inputProcessors rather than replacing it, so a machine that
  # has not migrated off the scalar keeps running its single processor FIRST
  # once it (or another consumer) also sets the list — exactly the behavior it
  # had before the list existed, plus whatever the list adds after it.
  effectiveInputProcessors =
    lib.optional (cfg.inputProcessor != null) cfg.inputProcessor ++ cfg.inputProcessors;

  # wrapProgram flags, contributed only by the settings that are active. The
  # binary is wrapped iff at least one flag is present; otherwise the unwrapped
  # package is used directly.
  wrapArgs =
    lib.optional (
      effectiveInputProcessors != [ ]
      # Newline-joined, matching internal/inputproc's CETA_INPUT_PROCESSORS
      # contract (a `:`-separated list was rejected there: a processor command
      # commonly embeds spaces in its own argv). Embedding a literal newline
      # inside this double-quoted wrapProgram argument is fine: it becomes a
      # real newline byte in the generated wrapper script's `export
      # CETA_INPUT_PROCESSORS="…"` line, which bash preserves verbatim inside
      # double quotes.
    ) ''--set CETA_INPUT_PROCESSORS "${lib.concatStringsSep "\n" effectiveInputProcessors}"''
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
    inputProcessors = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = ''
        ORDERED list of commands to rewrite bash commands before execution.
        Each is called as: <command> "<bash-command>" -- except that every
        processor AFTER the first receives the PREVIOUS processor's output as
        its "<bash-command>" instead of the original, so the list composes
        into a single pipeline rather than each processor seeing the same
        input independently. This is the ONE place a bash command may be
        rewritten (bead tc-7m85u): Claude Code runs every matching
        PreToolUse hook in parallel and keeps only the LAST hook's
        updatedInput wholesale, so a second, independent rewriting hook would
        silently lose to ceta -- anything that needs to rewrite a command
        MUST enter through this list instead of registering a hook of its
        own.

        Exit 0 + non-empty stdout = rewrite; exit 1+ or empty stdout =
        decline, which passes the PRIOR text through UNCHANGED to the next
        processor in the list (a decline is not the same as removing the
        processor -- the remaining processors still run).

        Each processor runs in a process group of its own with its OWN 3s
        budget (the budget is per processor, not shared across the list); on
        expiry the whole group for THAT processor is killed, that processor
        is treated as a decline, and the chain continues with the remaining
        processors. A process a processor forks MUST NOT keep stdout open
        after the processor exits: the read is cut off 250ms later, the
        possibly-truncated rewrite is discarded (again a decline), and the
        forked process is killed. Every outcome is reported on stderr; none
        of them block the tool call.

        Each processor's environment additionally carries CETA_SESSION_ID,
        CETA_AGENT_ID, CETA_AGENT_TYPE and CETA_CWD, taken from the hook
        payload ceta already parsed. CETA_AGENT_ID/CETA_AGENT_TYPE are
        present but EMPTY in the main session -- only a subagent invocation
        populates them -- so a processor must treat an empty value as "the
        main session," never as "unknown agent."

        LIMITATION -- processors run only when ceta's own verdict is Approve
        or Ask, never on NoOpinion (abstain). Measured 2026-09-16: Claude
        Code matches its settings/--allowedTools allowlist against the
        REWRITTEN command, so rewriting an abstained (allowlisted) command
        can turn a silent auto-run into an interactive prompt -- an `export
        X=1;` / `X=1 <cmd>` prefix on an otherwise-allowlisted command needed
        `Bash(export:*)` to still auto-run. See `phillipgreenii-nix-agent-support`
        ADR 0070.
      '';
    };
    inputProcessor = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        DEPRECATED: use `inputProcessors` instead. If set, this single
        command is PREPENDED to `inputProcessors` at evaluation time -- it
        does not replace the list, so an existing single-processor
        configuration keeps running first, unchanged, once `inputProcessors`
        also gains entries. Setting this option emits a warning.
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
    # mkRenamedOptionModule itself does not fit here: it requires the old and
    # new options to share a type, and inputProcessor (a nullOr str) is being
    # folded into inputProcessors (a listOf str) rather than simply renamed --
    # see effectiveInputProcessors above for the prepend semantics this
    # warning describes.
    warnings = lib.optional (cfg.inputProcessor != null) ''
      phillipgreenii.programs.claude-extended-tool-approver.inputProcessor is deprecated;
      use inputProcessors instead. The configured value is being prepended to
      inputProcessors for now.
    '';

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
