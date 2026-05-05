# Maps Stylix base16 colors to Claude Code custom theme token names.
#
# Called with: { colors = config.lib.stylix.colors; }
# where config.lib.stylix.colors.baseXX is a 6-char hex string (no #, lowercase).
#
# References:
#   Claude Code theme docs:     https://code.claude.com/docs/en/terminal-config
#   Theme system (v2.1.118):    https://github.com/anthropics/claude-code/releases/tag/v2.1.118
#   Full token list research:   https://github.com/Piebald-AI/claude-code-themes
#   Community token discussion: https://github.com/anthropics/claude-code/issues/34702
#   Base16 color role spec:     https://github.com/chriskempson/base16/blob/main/styling.md
{ colors }:
{
  # ---------------------------------------------------------------------------
  # PRIMARY ACCENT
  # base0E = Keywords/Storage/Selector/Purple. Chosen as the Claude accent
  # because it is the most scheme-distinctive color in base16 — it maps to the
  # primary "brand" color (e.g. Mauve #CBA6F7 in Catppuccin Mocha). The `claude`
  # token controls the Claude logo and interactive UI highlights.
  claude = "#${colors.base0E}";

  # Shimmer tokens are the second color in gradient/pulse animations. The
  # thinking spinner oscillates between `claude` and `claudeShimmer`, creating
  # a blue-purple gradient when base0D (blue) and base0E (purple) are paired.
  # See: https://github.com/anthropics/claude-code/issues/6038
  claudeShimmer = "#${colors.base0D}";

  # The system spinner token names explicitly include "Blue"; base0D is the
  # canonical blue slot in base16 (Functions/Methods/Attribute IDs/Headings).
  claudeBlue_FOR_SYSTEM_SPINNER = "#${colors.base0D}";

  # Shimmer companion to base0D. base0C (Cyan/Support) is spectrally adjacent
  # to base0D (Blue), giving a natural blue→cyan shimmer gradient.
  claudeBlueShimmer_FOR_SYSTEM_SPINNER = "#${colors.base0C}";

  # ---------------------------------------------------------------------------
  # TEXT
  text = "#${colors.base05}"; # Default Foreground — canonical primary text color
  # Inverse text appears on accent-colored surfaces (e.g. highlighted buttons).
  # On dark themes, that surface is accent-colored and the text should use the
  # background color so it reads as "inverted".
  inverseText = "#${colors.base00}"; # Default Background
  # base03 = Comments/Invisibles — the canonical "dimmed" slot in base16 editors.
  inactive = "#${colors.base03}";
  subtle = "#${colors.base04}"; # Dark Foreground — secondary text, status bars
  suggestion = "#${colors.base03}"; # Hints are as secondary as code comments
  # Memory highlights use the primary accent for visual consistency with the
  # Claude brand color.
  remember = "#${colors.base0E}";

  # ---------------------------------------------------------------------------
  # BACKGROUNDS
  background = "#${colors.base00}"; # Default Background
  # base01 = Lighter Background (used for status bars, line number gutters in
  # editors). A slight elevation above base00 for message containers.
  userMessageBackground = "#${colors.base01}";
  bashMessageBackgroundColor = "#${colors.base01}";
  memoryBackgroundColor = "#${colors.base01}";

  # ---------------------------------------------------------------------------
  # BORDERS / UI CHROME
  # Prompt border uses the accent color (base0E) to match the `claude` token,
  # creating a cohesive accent color for the input area.
  promptBorder = "#${colors.base0E}";
  promptBorderShimmer = "#${colors.base0D}"; # Blue shimmer companion
  # Bash block border uses base02 (Selection Background) — subtle, not accent.
  bashBorder = "#${colors.base02}";

  # ---------------------------------------------------------------------------
  # SEMANTIC STATUS COLORS
  success = "#${colors.base0B}"; # Strings/Diff Inserted/Green — universal
  error = "#${colors.base08}"; # Variables/Diff Deleted/Red — universal
  # base09 (Orange/Integers/Constants) chosen over base0A (Yellow) for warning:
  # orange signals "caution" more strongly than yellow in most terminal UIs.
  warning = "#${colors.base09}";
  warningShimmer = "#${colors.base0A}"; # Yellow — adjacent to orange for gradient

  # Permission prompts use orange (same rationale as warning: "proceed with caution").
  permission = "#${colors.base09}";
  permissionShimmer = "#${colors.base0A}";

  planMode = "#${colors.base0D}"; # Blue — informational, not error/warning
  ide = "#${colors.base0C}"; # Cyan — IDE indicator, distinct from primary blue

  # ---------------------------------------------------------------------------
  # DIFF VISUALIZATION
  diffAdded = "#${colors.base0B}"; # Green
  diffRemoved = "#${colors.base08}"; # Red
  diffAddedWord = "#${colors.base0B}";
  diffRemovedWord = "#${colors.base08}";
  # Dimmed diff variants: base16 has no "desaturated color" concept — it defines
  # 16 fixed semantic roles without tonal variants. base03 (Comments/Invisibles)
  # is the least-bad approximation since it is already the "muted" color in base16.
  # LIMITATION: these may not appear visually distinct from non-dimmed in all
  # schemes. A future improvement could arithmetically blend base0B/base08 with
  # base00 to compute a dimmed hex value.
  diffAddedDimmed = "#${colors.base03}";
  diffRemovedDimmed = "#${colors.base03}";
  diffAddedWordDimmed = "#${colors.base03}";
  diffRemovedWordDimmed = "#${colors.base03}";

  # ---------------------------------------------------------------------------
  # AGENT INDICATOR COLORS
  # These label parallel subagent processes in multi-agent mode. Token names
  # require the _FOR_SUBAGENTS_ONLY suffix (confirmed from Claude Code source).
  # Short names (red, blue, …) are silently ignored by Claude Code.
  red_FOR_SUBAGENTS_ONLY = "#${colors.base08}";
  blue_FOR_SUBAGENTS_ONLY = "#${colors.base0D}";
  green_FOR_SUBAGENTS_ONLY = "#${colors.base0B}";
  yellow_FOR_SUBAGENTS_ONLY = "#${colors.base0A}";
  purple_FOR_SUBAGENTS_ONLY = "#${colors.base0E}";
  orange_FOR_SUBAGENTS_ONLY = "#${colors.base09}";
  # LIMITATION: base16 has no pink slot. base0F = Deprecated/Opening Tags =
  # brown/rust in most schemes (e.g. #F2CDCD Flamingo in Catppuccin is pink, but
  # that mapping is scheme-specific). base08 (red) is the closest universal
  # approximation for pink.
  pink_FOR_SUBAGENTS_ONLY = "#${colors.base08}";
  cyan_FOR_SUBAGENTS_ONLY = "#${colors.base0C}";

  # ---------------------------------------------------------------------------
  # RATE LIMIT PROGRESS BAR
  # Controls a visual quota indicator showing consumed vs. available API capacity.
  # See: https://github.com/anthropics/claude-code/issues/34702
  rate_limit_fill = "#${colors.base08}"; # Red — consumed quota = danger
  rate_limit_empty = "#${colors.base02}"; # Selection Background — dark, non-prominent

  # ---------------------------------------------------------------------------
  # AUTO-ACCEPT MODE
  # autoAccept: indicator color for auto-accept mode. base0A (yellow) signals
  # "proceed with awareness" — confirmed/active but worth noticing.
  # autoAccept-shimmer: used for xhigh effort level display; base0B (green)
  # gives a yellow→green gradient for the shimmer animation.
  autoAccept = "#${colors.base0A}";
  "autoAccept-shimmer" = "#${colors.base0B}";

  # ---------------------------------------------------------------------------
  # RAINBOW ANIMATION TOKENS
  # Used for animated effects (e.g. rainbow border pulse). Each token maps to
  # the base16 slot sharing that color's semantic role. Shimmer is the
  # spectrally adjacent color to create a smooth gradient oscillation.
  rainbow_red = "#${colors.base08}";
  rainbow_red_shimmer = "#${colors.base09}"; # orange, spectral neighbor
  rainbow_orange = "#${colors.base09}";
  rainbow_orange_shimmer = "#${colors.base0A}"; # yellow
  rainbow_yellow = "#${colors.base0A}";
  rainbow_yellow_shimmer = "#${colors.base0B}"; # green
  rainbow_green = "#${colors.base0B}";
  rainbow_green_shimmer = "#${colors.base0C}"; # cyan
  rainbow_blue = "#${colors.base0D}";
  rainbow_blue_shimmer = "#${colors.base0C}"; # cyan, spectral neighbor
  rainbow_indigo = "#${colors.base0E}";
  rainbow_indigo_shimmer = "#${colors.base0D}"; # blue, spectral neighbor
  # LIMITATION: base16 has no violet slot. base0F = brown/rust in most schemes
  # (Catppuccin Flamingo). It is the least-bad approximation for violet.
  rainbow_violet = "#${colors.base0F}";
  rainbow_violet_shimmer = "#${colors.base0E}"; # purple, spectral neighbor

  # ---------------------------------------------------------------------------
  # CLAWD MASCOT
  # Clawd is the crab character shown on the startup screen.
  # See: https://github.com/anthropics/claude-code/issues/9246
  # Color changed from orange to blue in v2.0.67 (undocumented):
  # https://github.com/anthropics/claude-code/issues/13755
  clawd_body = "#${colors.base0E}"; # Primary accent
  clawd_background = "#${colors.base00}"; # Default Background

  # ---------------------------------------------------------------------------
  # PROFESSIONAL BLUE
  # Used for IDE/professional context accents; base0D is the canonical blue slot.
  professionalBlue = "#${colors.base0D}";
}
