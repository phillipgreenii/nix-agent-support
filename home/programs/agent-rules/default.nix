{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.claude;
  rulesFile = ./pgii-agent-rules.md;
in
{
  # Personal always-on Claude Code rules (pgii-agent-rules.md).
  #
  # PRIMARY delivery (pg2-44sj): the `agent-rules` HOOK PLUGIN in this repo's
  # nix-built local marketplace ships a SessionStart hook
  # (`agent-rules-session-start`) that emits the rules as `additionalContext`, so
  # they inject into the context of EVERY session — interactive and headless
  # `claude -p` alike — i.e. always-on. The plugin's `hooks.json` references the
  # hook by BARE name (ADR-0017), so this module installs the hook binary on
  # PATH for the command to resolve. The rules markdown is baked into that binary
  # as its AGENT_RULES_FILE, so `pgii-agent-rules.md` here is the single source
  # of truth for both deliveries.
  #
  # A plugin-root CLAUDE.md is NOT loaded by Claude Code (Skills(0)/~0 tokens),
  # and a skill body loads on-invoke (only its name+description are always-on) —
  # both are the wrong vehicle for always-on rules; hence the SessionStart hook.
  #
  # The `~/.claude/CLAUDE.md` ("user memory") write is retained as a redundant
  # fallback delivery. The interactive-vs-autonomous distinction lives in the
  # rules file itself (the leading note tells autonomous agents to ignore the
  # interactive section); it is intentionally NOT enforced by any hook, since no
  # reliable interactive-vs-`-p` signal exists for hooks.
  config = lib.mkIf cfg.enable {
    home = {
      file.".claude/CLAUDE.md".source = rulesFile;
      # Install the SessionStart hook binary so the marketplace's bare
      # `agent-rules-session-start` hook command resolves on PATH.
      packages = [ pkgs.agent-rules ];
    };
  };
}
