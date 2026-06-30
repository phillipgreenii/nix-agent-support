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

  repoPart = pkgs.writeShellScript "claude-sl-repo" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_REPO_OWNER" ] && [ -n "$CLAUDE_SL_REPO_NAME" ] || exit 1
    printf "''${MAGENTA}%s/%s''${RESET}" "$CLAUDE_SL_REPO_OWNER" "$CLAUDE_SL_REPO_NAME"
  '';

  # PR number, colored by review state. The full URL is exported as CLAUDE_SL_PR_URL for
  # custom parts to consume but is not rendered here (a URL would blow the width budget).
  prPart = pkgs.writeShellScript "claude-sl-pr" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_PR_NUMBER" ] || exit 1
    case "$CLAUDE_SL_PR_REVIEW_STATE" in
      approved)          color="''${GREEN}" ;;
      changes_requested) color="''${RED}" ;;
      pending)           color="''${YELLOW}" ;;
      draft)             color="''${DIM}" ;;
      *)                 color="" ;;
    esac
    printf "''${color}PR#%s''${RESET}" "$CLAUDE_SL_PR_NUMBER"
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

  # Reasoning effort level, abbreviated. Skipped when the model doesn't expose effort.
  effortPart = pkgs.writeShellScript "claude-sl-effort" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_EFFORT" ] || exit 1
    case "$CLAUDE_SL_EFFORT" in
      low)    abbr=lo ;;
      medium) abbr=med ;;
      high)   abbr=hi ;;
      xhigh)  abbr=xhi ;;
      max)    abbr=max ;;
      *)      abbr="$CLAUDE_SL_EFFORT" ;;
    esac
    printf "''${DIM}eff:%s''${RESET}" "$abbr"
  '';

  # Extended-thinking indicator. Only rendered while thinking is on.
  thinkingPart = pkgs.writeShellScript "claude-sl-thinking" ''
    ${ansiColors}
    [ "$CLAUDE_SL_THINKING" = "true" ] || exit 1
    printf "''${MAGENTA}think''${RESET}"
  '';

  # Output style name. Hidden for the implicit "default" style (noise) and when absent.
  outputStylePart = pkgs.writeShellScript "claude-sl-output-style" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_OUTPUT_STYLE" ] || exit 1
    [ "$CLAUDE_SL_OUTPUT_STYLE" = "default" ] && exit 1
    printf "''${DIM}style:%s''${RESET}" "$CLAUDE_SL_OUTPUT_STYLE"
  '';

  # Vim mode, abbreviated and colored. Skipped when vim mode is disabled.
  vimPart = pkgs.writeShellScript "claude-sl-vim" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_VIM_MODE" ] || exit 1
    case "$CLAUDE_SL_VIM_MODE" in
      INSERT)        m=I;  color="''${GREEN}" ;;
      NORMAL)        m=N;  color="''${CYAN}" ;;
      VISUAL)        m=V;  color="''${YELLOW}" ;;
      "VISUAL LINE") m=VL; color="''${YELLOW}" ;;
      *)             m="$CLAUDE_SL_VIM_MODE"; color="''${CYAN}" ;;
    esac
    printf "''${color}vim:%s''${RESET}" "$m"
  '';

  # Active subagent name, @-prefixed. Skipped when no agent is running.
  agentPart = pkgs.writeShellScript "claude-sl-agent" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_AGENT" ] || exit 1
    printf "''${BOLD}@%s''${RESET}" "$CLAUDE_SL_AGENT"
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
      export CLAUDE_SL_REPO_OWNER
      export CLAUDE_SL_REPO_NAME
      export CLAUDE_SL_PR_NUMBER
      export CLAUDE_SL_PR_URL
      export CLAUDE_SL_PR_REVIEW_STATE
      export CLAUDE_SL_EFFORT
      export CLAUDE_SL_THINKING
      export CLAUDE_SL_OUTPUT_STYLE
      export CLAUDE_SL_VIM_MODE
      export CLAUDE_SL_AGENT
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
        @sh "CLAUDE_SL_CONTEXT_USED_PCT=\(.context_window.used_percentage // "")",
        @sh "CLAUDE_SL_REPO_OWNER=\(.workspace.repo.owner // "")",
        @sh "CLAUDE_SL_REPO_NAME=\(.workspace.repo.name // "")",
        @sh "CLAUDE_SL_PR_NUMBER=\(.pr.number // "")",
        @sh "CLAUDE_SL_PR_URL=\(.pr.url // "")",
        @sh "CLAUDE_SL_PR_REVIEW_STATE=\(.pr.review_state // "")",
        @sh "CLAUDE_SL_EFFORT=\(.effort.level // "")",
        @sh "CLAUDE_SL_THINKING=\(.thinking.enabled // false | tostring)",
        @sh "CLAUDE_SL_OUTPUT_STYLE=\(.output_style.name // "")",
        @sh "CLAUDE_SL_VIM_MODE=\(.vim.mode // "")",
        @sh "CLAUDE_SL_AGENT=\(.agent.name // "")"
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
    "${repoPart}"
    "${prPart}"
    "${modelPart}"
    "${versionPart}"
    "${effortPart}"
    "${thinkingPart}"
    "${outputStylePart}"
    "${vimPart}"
    "${agentPart}"
  ];
in
{
  inherit
    envPart
    sessionPart
    worktreePart
    gitPart
    repoPart
    prPart
    modelPart
    versionPart
    effortPart
    thinkingPart
    outputStylePart
    vimPart
    agentPart
    mkWrapperScript
    defaultParts
    ;
}
