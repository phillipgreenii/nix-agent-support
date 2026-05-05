# Shared script definitions for claude-status-line.
# Imported by both the home-manager module (default.nix) and flake.nix checks.
#
# colors (optional attrset): override ANSI escape sequences for named colors.
#   Recognized keys: reset, yellow, green, red, cyan, magenta, bold, dim
#   Values must be literal escape sequences (e.g. "\\033[32m" or truecolor
#   "\\033[38;2;166;227;161m"). Missing keys fall back to standard ANSI SGR codes.
#   Values are interpolated directly into printf format strings; printf expands
#   \033 as the ESC octal escape when it appears in the format string position.
{
  pkgs,
  lib,
  colors ? { },
}:
let
  ansiColors = ''
    RESET='${colors.reset or "\\033[0m"}'
    YELLOW='${colors.yellow or "\\033[33m"}'
    GREEN='${colors.green or "\\033[32m"}'
    RED='${colors.red or "\\033[31m"}'
    CYAN='${colors.cyan or "\\033[36m"}'
    MAGENTA='${colors.magenta or "\\033[35m"}'
    BOLD='${colors.bold or "\\033[1m"}'
    DIM='${colors.dim or "\\033[2m"}'
  '';

  envPart = pkgs.writeShellScript "claude-sl-env" ''
    ${ansiColors}
    if [ -n "''${CONTAINED_CLAUDE:-}" ]; then
      printf "''${BOLD}''${MAGENTA}C''${RESET}"
    else
      printf "''${BOLD}''${GREEN}H''${RESET}"
    fi
  '';

  sessionPart = pkgs.writeShellScript "claude-sl-session" ''
    ${ansiColors}
    if [ -n "$CLAUDE_SL_SESSION_NAME" ]; then
      printf "''${BOLD}%s''${RESET}" "$CLAUDE_SL_SESSION_NAME"
    elif [ -n "$CLAUDE_SL_SESSION_ID" ]; then
      printf "%s" "$CLAUDE_SL_SESSION_ID"
    else
      exit 1
    fi
  '';

  worktreePart = pkgs.writeShellScript "claude-sl-worktree" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_WORKTREE" ] || exit 1
    printf "''${BOLD}''${YELLOW}%s''${RESET}" "$CLAUDE_SL_WORKTREE"
  '';

  gitPart = pkgs.writeShellScript "claude-sl-git" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_BRANCH" ] || exit 1
    printf "''${GREEN}%s''${RESET}" "$CLAUDE_SL_BRANCH"
  '';

  modelPart = pkgs.writeShellScript "claude-sl-model" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_MODEL" ] || exit 1

    ctx_str=""
    if [ -n "$CLAUDE_SL_CONTEXT_USED_PCT" ]; then
      used_int=''${CLAUDE_SL_CONTEXT_USED_PCT%.*}
      if [ "$used_int" -ge 75 ] 2>/dev/null; then
        ctx_color="''${RED}"
      elif [ "$used_int" -ge 60 ] 2>/dev/null; then
        ctx_color="''${YELLOW}"
      else
        ctx_color="''${GREEN}"
      fi
      ctx_str=$(printf " ''${ctx_color}ctx:%s%%''${RESET}" "$CLAUDE_SL_CONTEXT_USED_PCT")
    fi

    printf "''${CYAN}%s''${RESET}%s" "$CLAUDE_SL_MODEL" "$ctx_str"
  '';

  versionPart = pkgs.writeShellScript "claude-sl-version" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_VERSION" ] || exit 1
    printf "''${DIM}%s''${RESET}" "$CLAUDE_SL_VERSION"
  '';

  # Build the wrapper script for a given list of part script store paths.
  # Parts are embedded at Nix eval time; each part is run with exported env vars.
  # A part that exits non-zero is silently skipped.
  mkWrapperScript =
    parts:
    pkgs.writeShellScript "claude-status-line-wrapper" ''
      input=$(cat)

      export CLAUDE_SL_SESSION_NAME
      export CLAUDE_SL_SESSION_ID
      export CLAUDE_SL_WORKTREE
      export CLAUDE_SL_BRANCH
      export CLAUDE_SL_VERSION
      export CLAUDE_SL_MODEL
      export CLAUDE_SL_CONTEXT_USED_PCT
      CLAUDE_SL_SESSION_NAME=$(printf '%s' "$input" | ${pkgs.jq}/bin/jq -r '.session_name // ""')
      CLAUDE_SL_SESSION_ID=$(printf '%s' "$input" | ${pkgs.jq}/bin/jq -r '.session_id // ""')
      CLAUDE_SL_WORKTREE=$(printf '%s' "$input" | ${pkgs.jq}/bin/jq -r '.worktree.name // .workspace.git_worktree // ""')
      CLAUDE_SL_BRANCH=$(printf '%s' "$input" | ${pkgs.jq}/bin/jq -r '.worktree.branch // ""')
      CLAUDE_SL_VERSION=$(printf '%s' "$input" | ${pkgs.jq}/bin/jq -r '.version // ""')
      CLAUDE_SL_MODEL=$(printf '%s' "$input" | ${pkgs.jq}/bin/jq -r '.model.display_name // ""')
      CLAUDE_SL_CONTEXT_USED_PCT=$(printf '%s' "$input" | ${pkgs.jq}/bin/jq -r '.context_window.used_percentage // ""')

      collected=()
      ${lib.concatMapStringsSep "\n" (part: ''
        output=$(${part} 2>/dev/null) || true
        if [ -n "$(printf '%s' "$output" | tr -d '[:space:]')" ]; then
          collected+=("$output")
        fi
      '') parts}

      result=""
      for segment in "''${collected[@]}"; do
        if [ -z "$result" ]; then
          result="$segment"
        else
          result="''${result} | ''${segment}"
        fi
      done

      printf "%b\n" "$result"
    '';

  defaultParts = [
    "${envPart}"
    "${sessionPart}"
    "${worktreePart}"
    "${gitPart}"
    "${modelPart}"
    "${versionPart}"
  ];
in
{
  inherit
    envPart
    sessionPart
    worktreePart
    gitPart
    modelPart
    versionPart
    mkWrapperScript
    defaultParts
    ;
}
