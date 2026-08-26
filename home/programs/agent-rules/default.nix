{
  config,
  lib,
  pkgs,
  ...
}@args:
let
  cfg = config.phillipgreenii.programs.claude-code;
  rulesFile = ./pgii-agent-rules.md;
  doltNoAutostartRule = ./beads-dolt-no-autostart-rule.md;
  doltLaunchdRule = ./beads-dolt-launchd-rule.md;
  # Read the machine-wide policy flags propagated from the agent-support darwin
  # beads module via `home-manager.extraSpecialArgs` (design's flag-propagation
  # decision). Read off the module argument set — a named `arg ? DEFAULT`
  # default is NOT honoured by the module system (it forces
  # `config._module.args.<arg>`), whereas `args.<arg> or DEFAULT` is a real
  # default when no consumer propagates the flag.
  #
  # forbidDoltAutoStart defaults TRUE (fail-safe / secure-by-default, design
  # P5): the generic no-autostart posture is safe on every machine class. The
  # same flag also gates the machine-wide `bd` wrapper's
  # `BEADS_DOLT_AUTO_START=0` export in the darwin module's consumers — this
  # module only reads it, it must not weaken that use.
  forbidDoltAutoStart = args.forbidDoltAutoStart or true;
  # localDoltLaunchdServer defaults FALSE (fail-safe in the other direction):
  # shipping no launchd text is safe, whereas shipping Mac-only launchd text
  # (org.nixos.beads-dolt-server, port 25252) on a machine with no such server
  # — e.g. a NixOS host whose workspace rules mandate a REMOTE dolt server —
  # actively contradicts that machine's real policy. Only the darwin beads
  # module propagates `true`.
  localDoltLaunchdServer = args.localDoltLaunchdServer or false;

  # Rule A-1's machine-roots sentence is machine-class-specific (the roots that
  # do not exist on a Mac are exactly where a Linux session's cwd lives), so
  # pgii-agent-rules.md carries the placeholder token
  # @KNOWN_ABSENT_ROOTS_SENTENCE@ and the sentence is composed here from
  # cfg.knownAbsentRoots. An empty list erases the token, leaving A-1 to end at
  # the observed-roots requirement.
  formatRoots =
    roots:
    let
      quoted = map (r: "`${r}`") roots;
    in
    if builtins.length quoted == 1 then
      builtins.head quoted
    else
      lib.concatStringsSep ", " (lib.init quoted) + " and " + lib.last quoted;
  knownAbsentRootsSentence = lib.optionalString (cfg.knownAbsentRoots != [ ]) (
    " The following roots are known NOT to exist on this machine: "
    + formatRoots cfg.knownAbsentRoots
    + " — producing one of them means the root was invented rather than observed."
  );
  rulesText =
    builtins.replaceStrings
      [ "@KNOWN_ABSENT_ROOTS_SENTENCE@" ]
      [
        knownAbsentRootsSentence
      ]
      (builtins.readFile rulesFile);
in
{
  # Option colocated with the module that uses it (repo convention), under the
  # claude-code feature namespace this module already gates on.
  options.phillipgreenii.programs.claude-code.knownAbsentRoots = lib.mkOption {
    type = lib.types.listOf lib.types.str;
    default =
      if pkgs.stdenv.isDarwin then
        [
          "/home"
          "/mnt"
          "/repo"
        ]
      else
        [ ];
    defaultText = lib.literalExpression ''if pkgs.stdenv.isDarwin then [ "/home" "/mnt" "/repo" ] else [ ]'';
    example = [
      "/home"
      "/mnt"
    ];
    description = ''
      Filesystem roots known NOT to exist on this machine, rendered into the
      Absolute-Path Provenance rule A-1 of the always-on agent rules so an
      agent can recognise a fabricated absolute-path root. The darwin default
      names the Linux-flavoured roots absent on macOS; on Linux the default is
      empty (the cwd itself lives under `/home`) and no sentence is rendered.
    '';
  };

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
  # PARTIAL SUPERSESSION (tc-ql0o Stage C, tc-ql0o.3, operator Decisions 1-3,
  # 2026-08-25/26): the "skill is the wrong vehicle" argument above still
  # holds for clauses with NO observable in-session trigger (bare
  # prohibitions, conversation-time rulings) — those stay in pgii-agent-rules.md,
  # unconditionally always-on, exactly as this comment originally argued. It
  # does NOT hold for clauses that key on an observable trigger a model can
  # gate on (a `bd` verb, a park/release/accept action, a worktree-review/human
  # label mutation): those moved to the `beads-lifecycle` skill
  # (claude-marketplace/beads-lifecycle), behind a short MUST-invoke tripwire
  # stub that DOES stay always-on here. The pg2-44sj context above is
  # preserved because it is still the reason the REMAINING core content can't
  # move the same way.
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
  #
  # Delivered as composed `.text` (not `.source`): a `.source` store path could
  # neither substitute the A-1 roots sentence nor include/exclude the
  # flag-gated sections. The composition is two flags over three files:
  #
  #   1. pgii-agent-rules.md — always, with @KNOWN_ABSENT_ROOTS_SENTENCE@
  #      substituted from cfg.knownAbsentRoots;
  #   2. beads-dolt-no-autostart-rule.md — appended when forbidDoltAutoStart
  #      (generic posture, valid on every machine class);
  #   3. beads-dolt-launchd-rule.md — appended when localDoltLaunchdServer
  #      (Mac-local launchd server specifics; darwin beads module opts in).
  #
  # A blank line keeps appended sections separated cleanly.
  config = lib.mkIf cfg.enable {
    home.file.".claude/CLAUDE.md".text =
      rulesText
      + lib.optionalString forbidDoltAutoStart ("\n" + builtins.readFile doltNoAutostartRule)
      + lib.optionalString localDoltLaunchdServer ("\n" + builtins.readFile doltLaunchdRule);
  };
}
