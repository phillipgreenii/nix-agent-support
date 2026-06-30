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
    {
      parts,
      reserve ? 20,
    }:
    pkgs.writeShellScript "claude-status-line-wrapper" ''
      input=$(cat)

      export CLAUDE_SL_SESSION_NAME
      export CLAUDE_SL_SESSION_ID
      export CLAUDE_SL_WORKTREE
      export CLAUDE_SL_BRANCH
      export CLAUDE_SL_VERSION
      export CLAUDE_SL_MODEL
      export CLAUDE_SL_CONTEXT_USED_PCT
      # Single jq invocation extracts every field at once (one process per render, not one
      # per field). jq emits shell-quoted `VAR=value` assignments via @sh; eval applies them.
      # @sh guarantees each value is safely quoted, so spaces / quotes / $() / backticks in
      # JSON values are preserved literally and never executed. The vars are pre-declared
      # exported above, so these plain assignments are still exported to the part scripts.
      eval "$(printf '%s' "$input" | ${pkgs.jq}/bin/jq -r '
        @sh "CLAUDE_SL_SESSION_NAME=\(.session_name // "")",
        @sh "CLAUDE_SL_SESSION_ID=\(.session_id // "")",
        @sh "CLAUDE_SL_WORKTREE=\(.worktree.name // .workspace.git_worktree // "")",
        @sh "CLAUDE_SL_BRANCH=\(.worktree.branch // "")",
        @sh "CLAUDE_SL_VERSION=\(.version // "")",
        @sh "CLAUDE_SL_MODEL=\(.model.display_name // "")",
        @sh "CLAUDE_SL_CONTEXT_USED_PCT=\(.context_window.used_percentage // "")"
      ')"

      collected=()
      ${lib.concatMapStringsSep "\n" (part: ''
        output=$(${part} 2>/dev/null) || true
        if [ -n "$(printf '%s' "$output" | tr -d '[:space:]')" ]; then
          collected+=("$output")
        fi
      '') parts}

      # Width-aware wrapping. budget = COLUMNS - reserve, applied uniformly to every row.
      # reserve: nix-baked default, overridable via CLAUDE_SL_RESERVE (test/power-use seam).
      # COLUMNS unset / 0 / non-numeric => budget 0 => no wrapping (single line, legacy).
      reserve=''${CLAUDE_SL_RESERVE:-${toString reserve}}
      cols=''${COLUMNS:-0}
      case "$cols" in (*[!0-9]*|"") cols=0 ;; esac
      if [ "$cols" -gt 0 ] && [ "$cols" -gt "$reserve" ]; then
        budget=$((cols - reserve))
      else
        budget=0
      fi

      # Visible width = segment with ANSI SGR escapes stripped. Pure bash, no subprocess.
      strip_ansi() {
        local s=$1 out=""
        while [ "$s" != "''${s#*$'\033['}" ]; do
          out=$out''${s%%$'\033['*}
          s=''${s#*$'\033['}
          s=''${s#*m}
        done
        printf '%s' "$out$s"
      }

      # Greedily pack segments into " | "-joined rows in list order. A segment is moved to
      # a new row only when appending it to a NON-empty row would exceed budget; the first
      # segment of any row is placed unconditionally, so an oversized segment is always
      # emitted whole on its own row (never split, never dropped).
      lines=()
      cur=""
      cur_w=0
      for seg in "''${collected[@]}"; do
        stripped=$(strip_ansi "$seg")
        seg_w=''${#stripped}
        if [ -z "$cur" ]; then
          cur=$seg
          cur_w=$seg_w
        elif [ "$budget" -gt 0 ] && [ $((cur_w + 3 + seg_w)) -gt "$budget" ]; then
          lines+=("$cur")
          cur=$seg
          cur_w=$seg_w
        else
          cur="$cur | $seg"
          cur_w=$((cur_w + 3 + seg_w))
        fi
      done
      [ -n "$cur" ] && lines+=("$cur")

      for line in "''${lines[@]}"; do
        printf '%s\n' "$line"
      done
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
