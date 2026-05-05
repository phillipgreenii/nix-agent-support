{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.claude.status-line-parts;
  cfgColors = config.phillipgreenii.programs.claude.status-line-colors;
  scripts = import ./scripts.nix {
    inherit pkgs lib;
    colors = cfgColors;
  };
  wrapperScript = scripts.mkWrapperScript cfg;
in
{
  options.phillipgreenii.programs.claude.status-line-parts = lib.mkOption {
    type = lib.types.listOf lib.types.str;
    default = [ ];
    description = ''
      Ordered list of status line part scripts to run. Each script:
        - receives Claude context via exported env vars (CLAUDE_SL_SESSION_NAME,
          CLAUDE_SL_SESSION_ID, CLAUDE_SL_WORKTREE, CLAUDE_SL_BRANCH,
          CLAUDE_SL_VERSION, CLAUDE_SL_MODEL, CLAUDE_SL_CONTEXT_USED_PCT)
        - prints a single formatted segment to stdout (ANSI colors allowed)
        - exits 0 to include the segment, non-zero to skip it silently
      Segments are joined with " | ".
    '';
  };

  options.phillipgreenii.programs.claude.status-line-colors = lib.mkOption {
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

  config = lib.mkIf config.phillipgreenii.programs.claude.enable {
    phillipgreenii.programs.claude.status-line-parts = scripts.defaultParts;

    phillipgreenii.programs.claude.settings.statusLine = lib.mkIf (cfg != [ ]) {
      type = "command";
      command = "bash ${wrapperScript}";
    };
  };
}
