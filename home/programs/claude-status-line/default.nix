{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.claude.status-line-parts;
  cfgColors = config.phillipgreenii.programs.claude.status-line-colors;
  cfgReserve = config.phillipgreenii.programs.claude.status-line-notification-reserve;
  scripts = import ./scripts.nix {
    inherit pkgs lib;
    colors = cfgColors;
  };
  wrapperScript = scripts.mkWrapperScript {
    parts = cfg;
    reserve = cfgReserve;
  };
in
{
  options.phillipgreenii.programs.claude = {
    status-line-parts = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = ''
        Ordered list of status line part scripts to run. Each script:
          - receives Claude context via exported env vars (CLAUDE_SL_SESSION_NAME,
            CLAUDE_SL_SESSION_ID, CLAUDE_SL_WORKTREE, CLAUDE_SL_BRANCH,
            CLAUDE_SL_VERSION, CLAUDE_SL_MODEL, CLAUDE_SL_CONTEXT_USED_PCT,
            CLAUDE_SL_REPO_OWNER, CLAUDE_SL_REPO_NAME, CLAUDE_SL_PR_NUMBER,
            CLAUDE_SL_PR_URL, CLAUDE_SL_PR_REVIEW_STATE, CLAUDE_SL_EFFORT,
            CLAUDE_SL_THINKING, CLAUDE_SL_OUTPUT_STYLE, CLAUDE_SL_VIM_MODE,
            CLAUDE_SL_AGENT)
          - prints a single formatted segment to stdout (ANSI colors allowed)
          - exits 0 to include the segment, non-zero to skip it silently
        Segments are joined with " | ".
      '';
    };

    status-line-colors = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      description = ''
        Override ANSI escape sequences used by status-line segment scripts.
        Recognized keys: reset, yellow, green, red, cyan, magenta, bold, dim.
        Values must be literal escape sequences, e.g. "\\033[32m" (ANSI SGR)
        or "\\033[38;2;166;227;161m" (24-bit truecolor). Missing keys fall back to
        hardcoded standard ANSI SGR codes. This option is backwards-compatible —
        the empty default {} produces identical behavior to the previous hardcoded codes.
      '';
    };

    status-line-notification-reserve = lib.mkOption {
      type = lib.types.int;
      default = 20;
      description = ''
        Columns reserved on the right of EVERY status-line row for Claude Code's
        right-aligned system notifications (MCP server errors, auto-update notices).
        Claude Code does not expose whether a notification is present or how wide it is,
        so this is a fixed, conservative margin rather than a detected value. The wrapper
        breaks segments onto a new row when a row's visible width (ANSI stripped) would
        exceed (COLUMNS - this value). Applied uniformly to all rows because which row
        carries the notification is undocumented. Set to 0 to wrap only at the true COLUMNS
        edge. Wrapping is skipped entirely (single line, legacy behavior) when COLUMNS is
        unset, 0, or non-numeric.
      '';
    };
  };

  config = lib.mkIf config.phillipgreenii.programs.claude.enable {
    # Base parts occupy the default order band (1000). Downstream modules MUST place their
    # parts with lib.mkBefore / lib.mkAfter / lib.mkOrder (never plain assignment) so the
    # merged order stays deterministic as contributors grow.
    # See docs/adr/0020-status-line-parts-ordering-convention.md.
    phillipgreenii.programs.claude.status-line-parts = lib.mkOrder 1000 scripts.defaultParts;

    phillipgreenii.programs.claude.settings.statusLine = lib.mkIf (cfg != [ ]) {
      type = "command";
      command = "bash ${wrapperScript}";
    };
  };
}
