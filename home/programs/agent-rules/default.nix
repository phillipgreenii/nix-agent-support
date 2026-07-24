{
  config,
  lib,
  ...
}@args:
let
  cfg = config.phillipgreenii.programs.claude-code;
  rulesFile = ./pgii-agent-rules.md;
  beadsDoltRule = ./beads-dolt-rule.md;
  # Read the machine-wide policy flag propagated from the agent-support darwin
  # module via `home-manager.extraSpecialArgs` (design §6.1). Read off the
  # module argument set with a fail-safe default of `true` (secure-by-default,
  # design P5): a named `arg ? true` default is NOT honoured by the module
  # system (it forces `config._module.args.<arg>`), whereas `args.<arg> or true`
  # is a real default when no consumer propagates the flag.
  forbidDoltAutoStart = args.forbidDoltAutoStart or true;
in
{
  # Personal always-on Claude Code rules, delivered as the user-level
  # ~/.claude/CLAUDE.md ("user memory"). Claude Code loads this file into the
  # context of EVERY session unconditionally — interactive and headless
  # `claude -p` alike (verified against 2.1.186) — which is exactly the
  # always-on semantics these rules need.
  #
  # These rules previously shipped as the `agent-rules@pgii-local-plugins`
  # plugin via a plugin-root CLAUDE.md, but a plugin-root CLAUDE.md is NOT
  # loaded by Claude Code (Skills(0)/~0 tokens), so they were inert. A skill
  # is also the wrong vehicle: a skill's body loads on-invoke, so only its
  # name+description are ever always-on. See pg2-44sj and
  # docs/superpowers/specs/2026-06-25-agent-rules-delivery-design.md.
  #
  # agent-rules is NOT a plugin: a SessionStart hook plugin was briefly added
  # (pg2-44sj, commit 63a696b) during the marketplace migration but injected the
  # same rules a second time (double-injection); it was removed (pg2-qewh). The
  # user-level CLAUDE.md is the single, canonical always-on delivery.
  #
  # The interactive-vs-autonomous distinction lives in the rules file itself
  # (the leading note tells autonomous agents to ignore the interactive
  # section); it is intentionally NOT enforced by any hook/mechanism, since
  # no reliable interactive-vs-`-p` signal exists for hooks.
  # Delivered as composed `.text` (not `.source`) so the flag-gated beads/dolt
  # section can be conditionally appended — a `.source` store path cannot
  # include/exclude a section (design §6.3.1). When the machine forbids dolt
  # auto-start, the beads/dolt no-autostart rule is appended (a blank line keeps
  # the sections separated cleanly).
  config = lib.mkIf cfg.enable {
    home.file.".claude/CLAUDE.md".text =
      builtins.readFile rulesFile
      + lib.optionalString forbidDoltAutoStart ("\n" + builtins.readFile beadsDoltRule);
  };
}
