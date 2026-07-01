# Shared script definitions for claude-status-line.
# Imported by both the home-manager module (default.nix) and flake.nix checks.
#
# colors (optional attrset): override ANSI escape sequences for named colors.
#   Recognized keys: reset, yellow, green, red, cyan, magenta, bold, dim
#   Values must be literal escape sequences (e.g. "\\033[32m" or truecolor
#   "\\033[38;2;166;227;161m"). Missing keys fall back to standard ANSI SGR codes.
#   Values are interpolated directly into printf format strings; printf expands
#   \033 as the ESC octal escape when it appears in the format string position.
#
# nerdFont (bool, default false): when true, parts emit Material Design Icon (MDI) glyphs
#   (plane-15 PUA, U+F0xxx) as precomputed raw UTF-8 bytes via the bash builtin printf
#   (locale-independent; see the utf8Bytes comment for why `\U` is deliberately avoided).
#   When false, parts emit plain-text fallbacks. The choice is baked at Nix EVAL time (no
#   runtime shell branch); the glyph/text literals below are selected with
#   `if nerdFont then ... else ...`.
{
  pkgs,
  lib,
  colors ? { },
  nerdFont ? false,
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

  # MDI glyph emission (nerd-font on). B2: MDI glyphs are ASSUMED single display-width.
  #
  # The glyphs are all in Unicode plane 15 (U+F0000..U+FFFFF), which is always a 4-byte UTF-8
  # sequence. We emit the precomputed UTF-8 BYTES via `printf '\xNN...'` rather than the
  # codepoint escape `printf '\U000Fxxxx'`. Rationale (deviation from bead pg2-nhm2 note N2/B5
  # which named `\U`): bash's `\U` escape must transcode the codepoint through the C library
  # under the *active* locale; when that locale is not UTF-8 (e.g. the nix build sandbox on
  # darwin has NO usable locale, and a user shell may be LC_ALL=C) `\U` emits nothing or a
  # literal `\U000Fxxxx`. Raw byte escapes are locale-independent and produce identical output
  # in every environment. Width is still measured under a forced UTF-8 locale (see B1 below),
  # so the 4 bytes still count as one character. `printf` is the bash BUILTIN (bash >= 5.0).
  # cp is a DECIMAL codepoint (Nix has no hex integer literals). For plane-15 codepoints the
  # UTF-8 encoding is always the 4-byte form 11110xxx 10xxxxxx 10xxxxxx 10xxxxxx.
  utf8Bytes =
    cp:
    let
      b0 = 240 + (cp / 262144);
      b1 = 128 + ((cp / 4096) - ((cp / 262144) * 64));
      b2 = 128 + ((cp / 64) - ((cp / 4096) * 64));
      b3 = 128 + (cp - ((cp / 64) * 64));
      hx = n: "\\x" + lib.toLower (lib.fixedWidthString 2 "0" (lib.toHexString n));
    in
    hx b0 + hx b1 + hx b2 + hx b3;
  glyph = cp: "$(printf '${utf8Bytes cp}')";

  # Marker glyphs. Nerd-on prefix markers bake a SINGLE TRAILING SPACE so the marker sits one
  # space from its following value (e.g. `<repoG> owner/name`, `<ctxG> 42%`, `<5hG> <slice>`).
  # Nerd-off text labels stay tight (`ctx:`, `5h:`, `7d:`) — unaffected by the glyph spacing.
  # Decimal codepoints (see comments for the U+ hex values):
  glyphRepo = if nerdFont then glyph 983714 + " " else ""; # U+F02A2
  glyphWorktree = if nerdFont then glyph 983627 + " " else ""; # U+F024B
  glyphBranch = if nerdFont then glyph 984620 + " " else ""; # U+F062C
  glyphCtx = if nerdFont then glyph 983899 + " " else "ctx:"; # U+F035B
  glyph5h = if nerdFont then glyph 983376 + " " else "5h:"; # U+F0150
  glyph7d = if nerdFont then glyph 984697 + " " else "7d:"; # U+F0679

  # Thinking cog is a SUFFIX marker; the space in front of it is supplied by the model part
  # (`$out $cog`), so the glyph itself carries NO extra space. Text fallback is `[thinking]`.
  glyphThinking = if nerdFont then glyph 984211 else "[thinking]"; # U+F0493

  # 200k alert is a SUFFIX marker appended after the ctx percentage; it bakes a single LEADING
  # space so it reads `... 42% <alert>`. Nerd-off `(!)` stays tight (text unaffected).
  glyphAlert = if nerdFont then " " + glyph 983080 else "(!)"; # U+F0028

  # The session NAME is its own segment (bold). Shown only when session_name present.
  sessionNamePart = pkgs.writeShellScript "claude-sl-session-name" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_SESSION_NAME" ] || exit 1
    printf "''${BOLD}%s''${RESET}" "$CLAUDE_SL_SESSION_NAME"
  '';

  # The session ID is its own segment, always shown when present (after the name segment).
  sessionIdPart = pkgs.writeShellScript "claude-sl-session-id" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_SESSION_ID" ] || exit 1
    printf "%s" "$CLAUDE_SL_SESSION_ID"
  '';

  # LOCATION: repo (dim) + worktree (bold yellow) + branch (green) + PR (colored by review
  # state), space-separated in ONE segment. Each sub-part appears only when its data is
  # present; the whole segment is hidden when none are present. Worktree comes ONLY from
  # worktree.name (B8: no git_worktree fallback). The PR sub-part is appended LAST, after
  # branch, with NO glyph prefix (just `PR#<n>`).
  locationPart = pkgs.writeShellScript "claude-sl-location" ''
    ${ansiColors}
    parts=()
    if [ -n "$CLAUDE_SL_REPO_OWNER" ] && [ -n "$CLAUDE_SL_REPO_NAME" ]; then
      parts+=("$(printf "''${DIM}${glyphRepo}%s/%s''${RESET}" "$CLAUDE_SL_REPO_OWNER" "$CLAUDE_SL_REPO_NAME")")
    fi
    if [ -n "$CLAUDE_SL_WORKTREE" ]; then
      parts+=("$(printf "''${BOLD}''${YELLOW}${glyphWorktree}%s''${RESET}" "$CLAUDE_SL_WORKTREE")")
    fi
    if [ -n "$CLAUDE_SL_BRANCH" ]; then
      parts+=("$(printf "''${GREEN}${glyphBranch}%s''${RESET}" "$CLAUDE_SL_BRANCH")")
    fi
    if [ -n "$CLAUDE_SL_PR_NUMBER" ]; then
      # Colored by review state; full URL is exported as CLAUDE_SL_PR_URL for custom parts to
      # consume but is not rendered here (a URL would blow the width budget). No glyph prefix.
      case "$CLAUDE_SL_PR_REVIEW_STATE" in
        approved)          pr_color="''${GREEN}" ;;
        changes_requested) pr_color="''${RED}" ;;
        pending)           pr_color="''${YELLOW}" ;;
        draft)             pr_color="''${DIM}" ;;
        *)                 pr_color="" ;;
      esac
      parts+=("$(printf "''${pr_color}PR#%s''${RESET}" "$CLAUDE_SL_PR_NUMBER")")
    fi
    [ ''${#parts[@]} -gt 0 ] || exit 1
    out=$parts
    for p in "''${parts[@]:1}"; do
      out="$out $p"
    done
    printf '%s' "$out"
  '';

  # MODEL: full display_name (cyan) + folded effort abbreviation in parens + thinking glyph.
  #   effort.level -> abbrev (lo/med/hi/xhi/max), only when present.
  #   thinking glyph appended only when thinking.enabled == true.
  modelPart = pkgs.writeShellScript "claude-sl-model" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_MODEL" ] || exit 1

    out=$(printf "''${CYAN}%s''${RESET}" "$CLAUDE_SL_MODEL")

    if [ -n "$CLAUDE_SL_EFFORT" ]; then
      case "$CLAUDE_SL_EFFORT" in
        low)    abbr=lo ;;
        medium) abbr=med ;;
        high)   abbr=hi ;;
        xhigh)  abbr=xhi ;;
        max)    abbr=max ;;
        *)      abbr="$CLAUDE_SL_EFFORT" ;;
      esac
      out="$out $(printf "''${DIM}(%s)''${RESET}" "$abbr")"
    fi

    if [ "$CLAUDE_SL_THINKING" = "true" ]; then
      out="$out $(printf "''${MAGENTA}${glyphThinking}''${RESET}")"
    fi

    printf '%s' "$out"
  '';

  # CONTEXT: simple colored percentage (number always shown) + 200k alert glyph.
  #   green (<60) / yellow (60-74) / red (>=75) by used_percentage. Hidden when absent.
  #   Appends a RED alert marker when exceeds_200k_tokens == true.
  contextPart = pkgs.writeShellScript "claude-sl-context" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_CONTEXT_USED_PCT" ] || exit 1
    used_int=''${CLAUDE_SL_CONTEXT_USED_PCT%.*}
    if [ "$used_int" -ge 75 ] 2>/dev/null; then
      ctx_color="''${RED}"
    elif [ "$used_int" -ge 60 ] 2>/dev/null; then
      ctx_color="''${YELLOW}"
    else
      ctx_color="''${GREEN}"
    fi
    out=$(printf "''${ctx_color}${glyphCtx}%s%%''${RESET}" "$CLAUDE_SL_CONTEXT_USED_PCT")
    if [ "$CLAUDE_SL_EXCEEDS_200K" = "true" ]; then
      out="$out$(printf "''${RED}${glyphAlert}''${RESET}")"
    fi
    printf '%s' "$out"
  '';

  versionPart = pkgs.writeShellScript "claude-sl-version" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_VERSION" ] || exit 1
    printf "''${DIM}%s''${RESET}" "$CLAUDE_SL_VERSION"
  '';

  # Vim mode, abbreviated and colored. Skipped when vim mode is disabled. Rendered FIRST.
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

  # Active subagent name, @-prefixed. Skipped when no agent is running. After model, before ctx.
  agentPart = pkgs.writeShellScript "claude-sl-agent" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_AGENT" ] || exit 1
    printf "''${BOLD}@%s''${RESET}" "$CLAUDE_SL_AGENT"
  '';

  # LIMITS: 5h + 7d rate limits combined into ONE space-separated segment. Whole segment
  # hidden when rate_limits is absent; each sub-part hidden when its used_percentage is absent.
  #
  # Per sub-part (see bead pg2-nhm2 REVIEW RESOLUTIONS B3/B4/B5):
  #   used% < 80: circle-slice fill glyph (nerd on) / numeric % (nerd off), NO number when glyph.
  #     color = DEFAULT at-or-under pace, YELLOW when floor(used%) > percent-through-block (ptb).
  #     ptb = clamp((block_len - rem)*100/block_len, 0, 100); rem = resets_at - $EPOCHSECONDS.
  #     If resets_at missing OR rem <= 0: no pace (never yellow) AND no countdown.
  #   used% >= 80: RED number + reset countdown in parens (5h `Hh Mm`, 7d `Dd Hh`).
  #     Countdown omitted when resets_at missing / rem <= 0.
  #   circle-slice idx = clamp(floor(used%/10)+1, 1, 8); glyph = U+(0xF0A9E + idx-1).
  limitsPart = pkgs.writeShellScript "claude-sl-limits" ''
    ${ansiColors}
    [ -n "$CLAUDE_SL_5H_PCT" ] || [ -n "$CLAUDE_SL_7D_PCT" ] || exit 1

    # Render one sub-part. $1 marker literal, $2 used%, $3 resets_at, $4 block_len, $5 kind (5h|7d).
    render_limit() {
      local marker=$1 used=$2 reset=$3 blk=$4 kind=$5
      local used_int=''${used%.*}
      local now=$EPOCHSECONDS

      if [ "$used_int" -ge 80 ] 2>/dev/null; then
        # RED number + countdown.
        local cd=""
        if [ -n "$reset" ]; then
          local rem=$((reset - now))
          if [ "$rem" -gt 0 ]; then
            if [ "$kind" = "5h" ]; then
              local h=$((rem / 3600))
              local mnt=$(((rem % 3600) / 60))
              cd=" (''${h}h ''${mnt}m)"
            else
              local d=$((rem / 86400))
              local hh=$(((rem % 86400) / 3600))
              cd=" (''${d}d ''${hh}h)"
            fi
          fi
        fi
        printf "''${RED}%s%s%%%s''${RESET}" "$marker" "$used" "$cd"
        return 0
      fi

      # Below red: pace color + fill glyph (nerd on) / numeric % (nerd off).
      local color="" ptb=-1
      if [ -n "$reset" ]; then
        local rem=$((reset - now))
        if [ "$rem" -gt 0 ]; then
          ptb=$(((blk - rem) * 100 / blk))
          [ "$ptb" -lt 0 ] && ptb=0
          [ "$ptb" -gt 100 ] && ptb=100
        fi
      fi
      if [ "$ptb" -ge 0 ] && [ "$used_int" -gt "$ptb" ] 2>/dev/null; then
        color="''${YELLOW}"
      fi

      ${
        if nerdFont then
          ''
            # circle-slice fill glyph (no number). idx = clamp(floor(used%/10)+1, 1, 8).
            # glyph codepoint = U+F0A9E (985758 decimal) + (idx-1). Emit its 4 UTF-8 bytes
            # arithmetically (locale-independent) rather than printf '\U' which needs a
            # UTF-8 locale. All these codepoints are plane-15 -> always 4-byte UTF-8.
            local idx=$((used_int / 10 + 1))
            [ "$idx" -lt 1 ] && idx=1
            [ "$idx" -gt 8 ] && idx=8
            local cp=$((985758 + idx - 1))
            # Build the \xNN escape STRING first, then interpret it with printf %b. A single
            # `printf '\x%02x' N` does NOT work (printf won't re-interpret substituted digits).
            local esc fill
            esc=$(printf '\\x%02x\\x%02x\\x%02x\\x%02x' \
              $((240 + cp / 262144)) \
              $((128 + (cp / 4096) - (cp / 262144) * 64)) \
              $((128 + (cp / 64) - (cp / 4096) * 64)) \
              $((128 + cp - (cp / 64) * 64)))
            fill=$(printf '%b' "$esc")
            printf "''${color}%s%s''${RESET}" "$marker" "$fill"
          ''
        else
          ''
            printf "''${color}%s%s%%''${RESET}" "$marker" "$used"
          ''
      }
    }

    parts=()
    if [ -n "$CLAUDE_SL_5H_PCT" ]; then
      parts+=("$(render_limit "${glyph5h}" "$CLAUDE_SL_5H_PCT" "$CLAUDE_SL_5H_RESET" 18000 5h)")
    fi
    if [ -n "$CLAUDE_SL_7D_PCT" ]; then
      parts+=("$(render_limit "${glyph7d}" "$CLAUDE_SL_7D_PCT" "$CLAUDE_SL_7D_RESET" 604800 7d)")
    fi
    [ ''${#parts[@]} -gt 0 ] || exit 1
    out=$parts
    for p in "''${parts[@]:1}"; do
      out="$out $p"
    done
    printf '%s' "$out"
  '';

  # Build the wrapper script for a given list of part script store paths.
  # Parts are embedded at Nix eval time; each part is run with exported env vars.
  # A part that exits non-zero is silently skipped.
  #
  # utf8Locale: baked platform-appropriate UTF-8 locale used ONLY for the visible-width math
  # (B1). A glyph must count as ${#}=1; under a non-UTF-8 active locale it counts as its byte
  # length and over-wraps. On darwin C.UTF-8 may be absent, so fall back to en_US.UTF-8.
  utf8Locale = if pkgs.stdenv.isDarwin then "en_US.UTF-8" else "C.UTF-8";

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
      export CLAUDE_SL_EXCEEDS_200K
      export CLAUDE_SL_REPO_OWNER
      export CLAUDE_SL_REPO_NAME
      export CLAUDE_SL_PR_NUMBER
      export CLAUDE_SL_PR_URL
      export CLAUDE_SL_PR_REVIEW_STATE
      export CLAUDE_SL_EFFORT
      export CLAUDE_SL_THINKING
      export CLAUDE_SL_VIM_MODE
      export CLAUDE_SL_AGENT
      export CLAUDE_SL_5H_PCT
      export CLAUDE_SL_5H_RESET
      export CLAUDE_SL_7D_PCT
      export CLAUDE_SL_7D_RESET
      # Single jq invocation extracts every field at once (one process per render, not one
      # per field). jq emits shell-quoted `VAR=value` assignments via @sh; eval applies them.
      # @sh guarantees each value is safely quoted, so spaces / quotes / $() / backticks in
      # JSON values are preserved literally and never executed. The vars are pre-declared
      # exported above, so these plain assignments are still exported to the part scripts.
      eval "$(printf '%s' "$input" | ${pkgs.jq}/bin/jq -r '
        @sh "CLAUDE_SL_SESSION_NAME=\(.session_name // "")",
        @sh "CLAUDE_SL_SESSION_ID=\(.session_id // "")",
        @sh "CLAUDE_SL_WORKTREE=\(.worktree.name // "")",
        @sh "CLAUDE_SL_BRANCH=\(.worktree.branch // "")",
        @sh "CLAUDE_SL_VERSION=\(.version // "")",
        @sh "CLAUDE_SL_MODEL=\(.model.display_name // "")",
        @sh "CLAUDE_SL_CONTEXT_USED_PCT=\(.context_window.used_percentage // "")",
        @sh "CLAUDE_SL_EXCEEDS_200K=\(.exceeds_200k_tokens // false | tostring)",
        @sh "CLAUDE_SL_REPO_OWNER=\(.workspace.repo.owner // "")",
        @sh "CLAUDE_SL_REPO_NAME=\(.workspace.repo.name // "")",
        @sh "CLAUDE_SL_PR_NUMBER=\(.pr.number // "")",
        @sh "CLAUDE_SL_PR_URL=\(.pr.url // "")",
        @sh "CLAUDE_SL_PR_REVIEW_STATE=\(.pr.review_state // "")",
        @sh "CLAUDE_SL_EFFORT=\(.effort.level // "")",
        @sh "CLAUDE_SL_THINKING=\(.thinking.enabled // false | tostring)",
        @sh "CLAUDE_SL_VIM_MODE=\(.vim.mode // "")",
        @sh "CLAUDE_SL_AGENT=\(.agent.name // "")",
        @sh "CLAUDE_SL_5H_PCT=\(.rate_limits.five_hour.used_percentage // "")",
        @sh "CLAUDE_SL_5H_RESET=\(.rate_limits.five_hour.resets_at // "")",
        @sh "CLAUDE_SL_7D_PCT=\(.rate_limits.seven_day.used_percentage // "")",
        @sh "CLAUDE_SL_7D_RESET=\(.rate_limits.seven_day.resets_at // "")",
        @sh "_sl_cwd=\(.workspace.current_dir // .cwd // "")"
      ')"

      # Branch fallback: Claude only populates worktree.branch inside a worktree session,
      # so a normal checkout has no branch. Derive it from the repo's .git/HEAD by walking
      # up from cwd. Uses the `read` builtin (no git subprocess) to honor the
      # single-process-per-render goal; only runs when the JSON carried no branch.
      if [ -z "$CLAUDE_SL_BRANCH" ] && [ -n "$_sl_cwd" ]; then
        d=$_sl_cwd
        while [ -n "$d" ]; do
          if [ -d "$d/.git" ]; then
            gitdir="$d/.git"
          elif [ -f "$d/.git" ]; then
            # Linked worktree / submodule: ".git" is a file holding "gitdir: <path>".
            read -r gl <"$d/.git" || gl=""
            gitdir=''${gl#gitdir: }
            case "$gitdir" in /*) ;; *) gitdir="$d/$gitdir" ;; esac
          else
            d=''${d%/*}
            continue
          fi
          if [ -r "$gitdir/HEAD" ]; then
            read -r hl <"$gitdir/HEAD" || hl=""
            case "$hl" in
              "ref: refs/heads/"*) CLAUDE_SL_BRANCH=''${hl#ref: refs/heads/} ;;
            esac
          fi
          break
        done
      fi

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

      # B1: visible-width math MUST run under a UTF-8 locale so a plane-15 glyph counts as a
      # single character (''${#var} == 1). Under a non-UTF-8 active locale (e.g. LC_ALL=C) the
      # glyph counts as its multi-byte length and forces spurious wraps. Force a UTF-8 locale
      # for the width computation only when the active LC_CTYPE isn't already UTF-8. The locale
      # value is baked at build time (C.UTF-8 generally; en_US.UTF-8 on darwin).
      _active_ctype=''${LC_ALL:-''${LC_CTYPE:-''${LANG:-}}}
      case "$_active_ctype" in
        *[Uu][Tt][Ff]8* | *[Uu][Tt][Ff]-8*) ;;
        *) export LC_ALL='${utf8Locale}' ;;
      esac

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

  # Segment order (bead pg2-nhm2 FINAL DESIGN):
  #   vim, session name?, session id, location, model, agent, context, limits, version.
  defaultParts = [
    "${vimPart}"
    "${sessionNamePart}"
    "${sessionIdPart}"
    "${locationPart}"
    "${modelPart}"
    "${agentPart}"
    "${contextPart}"
    "${limitsPart}"
    "${versionPart}"
  ];
in
{
  inherit
    sessionNamePart
    sessionIdPart
    locationPart
    modelPart
    contextPart
    versionPart
    vimPart
    agentPart
    limitsPart
    mkWrapperScript
    defaultParts
    ;
}
