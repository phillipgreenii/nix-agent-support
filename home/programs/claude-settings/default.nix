{
  config,
  lib,
  pkgs,
  mkBashBuildersFor,
  inputs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.claude-code.settings;

  # Build the shared act_* activation-output helpers locally (repo-base ADR 0014;
  # the library form cannot be a flake.lib output, so each consumer builds it from
  # repo-base's lib/activation). Mirrors modules/zm and github-nix-auth.
  bashBuilders = mkBashBuildersFor pkgs;
  activation-lib = bashBuilders.mkBashLibrary {
    name = "activation-lib";
    src = inputs.phillipgreenii-nix-base + "/lib/activation";
    description = "Shared act_* activation-output helpers (single source with system.activationScripts)";
  };
  scripts = import ./scripts.nix {
    inherit pkgs activation-lib;
    inherit (bashBuilders) mkBashScript;
  };

  filters =
    # extraSettings is the freeform passthrough escape hatch (pg2-hpwww): it is
    # merged in FIRST, so every enumerated option below emits its own
    # assignment/del LATER in this same `|` pipe and therefore always WINS over
    # a same-named extraSettings key — a typo in extraSettings can never
    # silently override a setting this module deliberately manages. This is
    # deliberate precedence, not an eval-time error: naming an enumerated key
    # in extraSettings is not rejected, it is simply superseded.
    # `enabledPlugins` / `extraKnownMarketplaces` are stripped from the merge
    # unconditionally: those two keys are wholesale-replaced by
    # claude-settings-replace-managed-keys.sh in a separate pass that already
    # ran before this jq invocation ever sees the file, so letting them
    # through here would let a plain attrset silently fight a script whose
    # entire job is owning those two keys outright.
    lib.optional (cfg.extraSettings != { })
      ". * ${
        builtins.toJSON (
          builtins.removeAttrs cfg.extraSettings [
            "enabledPlugins"
            "extraKnownMarketplaces"
          ]
        )
      }"
    ++
      # These top-level-key options are tri-state nullOr, but unlike promptCacheTtl
      # (below) `null` is a deliberate NO-OP: no filter is emitted, so a value a
      # previous non-null generation wrote is LEFT IN PLACE, not deleted (pg2-a6y3).
      # A promptCacheTtl-style scrub-on-null is intentionally NOT applied here because
      # (1) `theme`/`statusLine` are written by Claude Code itself at runtime
      # (`/theme`, `/statusline`), so del-on-null would fight an interactive choice;
      # and (2) these are top-level keys with no per-key escape hatch (promptCacheTtl
      # keys off `cfg.env ? KEY`), so an unconditional del would clobber a hand-set
      # value with no opt-out — and this list runs on EVERY activation (it is never
      # empty: noFlicker and the promptCacheTtl null-cleanup always append). A
      # `sandbox` scrub was considered and deferred for the same no-opt-out reason.
      lib.optional (cfg.statusLine != null) ".statusLine = ${builtins.toJSON cfg.statusLine}"
    ++ lib.optional (
      cfg.showClearContextOnPlanAccept != null
    ) ".showClearContextOnPlanAccept = ${builtins.toJSON cfg.showClearContextOnPlanAccept}"
    ++ lib.optional (
      cfg.showThinkingSummaries != null
    ) ".showThinkingSummaries = ${builtins.toJSON cfg.showThinkingSummaries}"
    ++ lib.optional (
      cfg.includeCoAuthoredBy != null
    ) ".includeCoAuthoredBy = ${builtins.toJSON cfg.includeCoAuthoredBy}"
    ++ lib.optional (
      cfg.disableClaudeAiConnectors != null
    ) ".disableClaudeAiConnectors = ${builtins.toJSON cfg.disableClaudeAiConnectors}"
    ++ lib.optional (cfg.sandbox != null) ".sandbox = ${builtins.toJSON cfg.sandbox}"
    ++ lib.optional (
      cfg.sandbox == null && cfg.sandboxEnabled != null
    ) ".sandbox.enabled = ${builtins.toJSON cfg.sandboxEnabled}"
    ++ lib.optional (cfg.theme != null) ".theme = ${builtins.toJSON cfg.theme}"
    # cleanupPeriodDays is the one option in this list with a NON-NULL default, so
    # unlike its siblings it emits a filter on every machine that has not opted out
    # (pg2-3sca9). That is the point: an unset key means Claude Code's 30-day sweep,
    # and the loss is silent and unrecoverable. `null` is still the sibling-style
    # no-op escape hatch.
    ++ lib.optional (
      cfg.cleanupPeriodDays != null
    ) ".cleanupPeriodDays = ${builtins.toJSON cfg.cleanupPeriodDays}"
    ++ lib.mapAttrsToList (name: val: ".env[\"${name}\"] = ${builtins.toJSON val}") cfg.env
    ++ [
      (
        if cfg.noFlicker then ".env.CLAUDE_CODE_NO_FLICKER = \"1\"" else "del(.env.CLAUDE_CODE_NO_FLICKER)"
      )
    ]
    # promptCacheTtl selects the Claude Code prompt-cache TTL via two mutually
    # exclusive env vars (docs: code.claude.com/docs/en/prompt-caching). Applied
    # after cfg.env so the dedicated option wins over any stray env entry, and
    # each non-null state deletes the opposite key so toggling never leaves a
    # stale value behind. When null (the default, incl. the option being unset)
    # both keys are deleted so a value a previous "1h"/"5m" generation wrote is
    # scrubbed — EXCEPT a key pinned via the generic `env` attrset, which is
    # preserved. That `cfg.env ? KEY` guard is the only eval-time signal that a
    # runtime key is user-owned rather than our leftover, so it is what makes the
    # generic `env` attrset a deliberate escape hatch (pg2-e46e).
    ++ lib.optionals (cfg.promptCacheTtl == "1h") [
      ".env.ENABLE_PROMPT_CACHING_1H = \"1\""
      "del(.env.FORCE_PROMPT_CACHING_5M)"
    ]
    ++ lib.optionals (cfg.promptCacheTtl == "5m") [
      ".env.FORCE_PROMPT_CACHING_5M = \"1\""
      "del(.env.ENABLE_PROMPT_CACHING_1H)"
    ]
    ++ lib.optional (
      cfg.promptCacheTtl == null && !(cfg.env ? ENABLE_PROMPT_CACHING_1H)
    ) "del(.env.ENABLE_PROMPT_CACHING_1H)"
    ++ lib.optional (
      cfg.promptCacheTtl == null && !(cfg.env ? FORCE_PROMPT_CACHING_5M)
    ) "del(.env.FORCE_PROMPT_CACHING_5M)";

  # Framework-built scripts (mkBashScript with libraries = [ activation-lib ]),
  # so they source the shared act_* helpers. Binary names preserved as
  # claude-settings-* (invocations below, the eval-check, and the bats tests all
  # depend on the stable names).
  replaceScript = scripts.replaceManagedKeys.script;
  installPluginScript = scripts.installPlugin.script;
  registerMarketplaceScript = scripts.registerMarketplace.script;

  # DIRECTORY-source marketplaces from extraKnownMarketplaces. `claude plugin
  # marketplace update` only refreshes marketplaces already in the registry, so
  # a freshly-added nix directory marketplace must be `marketplace add`ed before
  # the per-plugin install loop or `install <plugin>@<mkt>` fails "Plugin not
  # found". github-source marketplaces are intentionally excluded — they are
  # left to the existing update + install flow.
  directoryMarketplaces = lib.filterAttrs (
    _name: entry: (entry.source.source or null) == "directory" && (entry.source.path or null) != null
  ) cfg.extraKnownMarketplaces;

  hasManagedKeys = cfg.enabledPlugins != { } || cfg.extraKnownMarketplaces != { };

  hasSettings = filters != [ ];
  hasPlugins = cfg.plugins != [ ] && cfg.claudeCodePackage != null;
  hasAnything = hasSettings || hasPlugins || hasManagedKeys;
in
{
  options.phillipgreenii.programs.claude-code.settings = {
    claudeCodePackage = lib.mkOption {
      type = lib.types.nullOr lib.types.package;
      default = null;
      description = "The claude-code package; marketplace update and plugin install/update are skipped if null";
    };

    statusLine = lib.mkOption {
      type = lib.types.nullOr (lib.types.attrsOf lib.types.anything);
      default = null;
      description = ''
        statusLine config object to set in ~/.claude/settings.json. `null`
        (default) is a deliberate no-op: a previously written `.statusLine` is
        left in place, not deleted (unlike `promptCacheTtl`). Claude Code's
        `/statusline` command writes `.statusLine` itself, so a scrub-on-null
        would fight an interactive change.
      '';
    };

    extraKnownMarketplaces = lib.mkOption {
      type = lib.types.attrsOf (lib.types.attrsOf lib.types.anything);
      default = { };
      description = "Marketplace name to source config to merge into extraKnownMarketplaces";
    };

    enabledPlugins = lib.mkOption {
      type = lib.types.attrsOf lib.types.bool;
      default = { };
      description = "Map of plugin@marketplace to bool to merge into enabledPlugins";
    };

    showClearContextOnPlanAccept = lib.mkOption {
      type = lib.types.nullOr lib.types.bool;
      default = null;
      description = ''
        Whether to show the clear-context option when accepting a plan. `null`
        (default) is a deliberate no-op — a previously written
        `.showClearContextOnPlanAccept` is left in place, not deleted (top-level
        key, no escape hatch, unlike `promptCacheTtl`).
      '';
    };

    showThinkingSummaries = lib.mkOption {
      type = lib.types.nullOr lib.types.bool;
      default = null;
      description = ''
        Whether to show thinking summaries in interactive sessions (off by
        default since Claude Code 2.x). `null` (default) is a deliberate no-op —
        a previously written `.showThinkingSummaries` is left in place, not
        deleted (top-level key, no escape hatch, unlike `promptCacheTtl`).
      '';
    };

    includeCoAuthoredBy = lib.mkOption {
      type = lib.types.nullOr lib.types.bool;
      default = null;
      description = ''
        Whether Claude Code adds Co-Authored-By trailers to git commits. `null`
        (default) is a deliberate no-op — a previously written
        `.includeCoAuthoredBy` is left in place, not deleted (top-level key, no
        escape hatch, unlike `promptCacheTtl`).
      '';
    };

    disableClaudeAiConnectors = lib.mkOption {
      type = lib.types.nullOr lib.types.bool;
      default = null;
      description = ''
        Whether to stop auto-fetching the claude.ai account's MCP cloud
        connectors. Claude Code's own settings schema defines the key as: "When
        true in any settings source, claude.ai MCP cloud connectors are not
        auto-fetched or connected. Only gates auto-fetched connectors — a
        claudeai-proxy server passed explicitly (e.g. via `--mcp-config` or the
        SDK `mcpServers` option) still follows the normal MCP config trust flow.
        Any-source-true wins: a project can opt out, but a project-level false
        cannot override a user-level true."

        Consequences a consumer MUST weigh before setting `true`:

        - It is ALL-OR-NOTHING. There is no per-connector form, so every
          claude.ai connector goes inert together. Selective suppression exists
          only as the enterprise `allowedMcpServers` / `deniedMcpServers` policy
          keys, which require a root-owned `managed-settings.json`.
        - A connector still WANTED after opting out MUST be re-declared as an
          ordinary MCP server (`claude mcp add --scope user …`). Explicitly
          configured servers are not gated by this key, so that is the supported
          escape hatch.
        - Servers from a project's own `.mcp.json` are unaffected — this gates
          only the account-level auto-fetch.

        `null` (default) is a deliberate no-op — a previously written
        `.disableClaudeAiConnectors` is left in place, not deleted (top-level
        key, no escape hatch, unlike `promptCacheTtl`).
      '';
    };

    sandbox = lib.mkOption {
      type = lib.types.nullOr (lib.types.attrsOf lib.types.anything);
      default = null;
      example = lib.literalExpression ''
        {
          enabled = true;
          filesystem.allowWrite = [ "~/.cache" "/tmp/build" ];
          network.allowedDomains = [ "github.com" "registry.npmjs.org" ];
          excludedCommands = [ "docker" "watchman" ];
        }
      '';
      description = ''
        Full Claude Code `sandbox` config object merged into
        `~/.claude/settings.json`. Replaces any existing top-level
        `.sandbox` value (jq assignment semantics — not a deep merge).
        See https://code.claude.com/docs/en/sandboxing.md for the
        supported keys (`enabled`, `filesystem.*`, `network.*`,
        `excludedCommands`, `allowUnsandboxedCommands`,
        `failIfUnavailable`).

        Requires Seatbelt on macOS (built-in) or bubblewrap + socat on
        Linux. macOS-only in our setup today.

        `null` (default) is a deliberate no-op: a previously written `.sandbox`
        is left in place, not deleted (unlike `promptCacheTtl`). A scrub-on-null
        was considered — Claude Code never writes `.sandbox` itself — but
        deferred: this filter list runs on every activation with no per-key
        escape hatch, so an unconditional `del(.sandbox)` would clobber a
        hand-crafted policy with no opt-out (pg2-a6y3).
      '';
    };

    sandboxEnabled = lib.mkOption {
      type = lib.types.nullOr lib.types.bool;
      default = null;
      description = ''
        Convenience toggle for `sandbox.enabled`. When set and the
        `sandbox` option is null, writes only `.sandbox.enabled` into
        settings.json. Ignored if `sandbox` is set (use `sandbox.enabled`
        there instead). `null` is a deliberate no-op: it never writes or
        deletes a top-level `.sandboxEnabled` key (only `.sandbox.enabled`),
        and a previously written `.sandbox.enabled` is left in place, not
        deleted (pg2-a6y3).
      '';
    };

    noFlicker = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Set CLAUDE_CODE_NO_FLICKER=1 in ~/.claude/settings.json env to suppress terminal flicker";
    };

    env = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      example = lib.literalExpression ''
        {
          BASH_DEFAULT_TIMEOUT_MS = "600000";
        }
      '';
      description = ''
        Extra environment variables to merge into `.env` in `~/.claude/settings.json`.
        Each entry is applied as a jq assignment, so keys overwrite any existing
        value at that path. `CLAUDE_CODE_NO_FLICKER` is still controlled by the
        `noFlicker` option and applied after this attrset. For the prompt-cache
        TTL vars (`ENABLE_PROMPT_CACHING_1H` / `FORCE_PROMPT_CACHING_5M`) prefer
        the dedicated `promptCacheTtl` option. Both are applied after this
        attrset: a non-null `promptCacheTtl` overrides a conflicting cache var set
        here, while a null `promptCacheTtl` preserves one set here (its cleanup
        deliberately skips a cache key present in this attrset). So pinning a cache
        var here is the supported way to keep it independent of `promptCacheTtl`.
      '';
    };

    promptCacheTtl = lib.mkOption {
      type = lib.types.nullOr (
        lib.types.enum [
          "1h"
          "5m"
        ]
      );
      default = null;
      example = "1h";
      description = ''
        Claude Code prompt-cache TTL preference, written into `.env` of
        `~/.claude/settings.json`. Three states:

        - `null` (default, including leaving the option unset): write neither
          cache env var, so Claude Code's auth-based default applies (1h on a
          Claude subscription, 5m on an API key / Bedrock / Vertex).
        - `"1h"`: set `ENABLE_PROMPT_CACHING_1H=1` (force the 1-hour TTL).
        - `"5m"`: set `FORCE_PROMPT_CACHING_5M=1` (the documented short-TTL
          override; force the 5-minute TTL).

        The two env vars are mutually exclusive, so selecting one deletes the
        other. Unlike the other `nullOr` options here, `null` is not a no-op: it
        deletes BOTH keys on every activation, scrubbing a value a previous
        `"1h"`/`"5m"` generation wrote. The one exception is a cache key pinned
        through the generic `env` option — the `null` cleanup skips a key present
        in `env`, so declare it there if you want to keep one of these vars by
        hand. A value hand-edited into `settings.json` but NOT declared via `env`
        is removed on the next activation. Prefer this dedicated option over
        setting the cache vars through `env`.
        See https://code.claude.com/docs/en/prompt-caching.
      '';
    };

    theme = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        Theme name to write into ~/.claude/settings.json.
        Built-in presets: "dark", "light", "dark-daltonized", "light-daltonized",
        "dark-ansi", "light-ansi".
        Custom themes use "custom:<slug>" where <slug> is the filename (without
        .json) of a file in ~/.claude/themes/. For example, a theme file at
        ~/.claude/themes/stylix.json is selected with "custom:stylix".
        See: https://code.claude.com/docs/en/terminal-config

        `null` (default, including leaving the option unset) is a deliberate
        no-op: a previously written `.theme` is left in place, not deleted
        (unlike `promptCacheTtl`). Claude Code writes `.theme` itself when you
        pick a theme with `/theme`, and there is no per-key escape hatch for
        top-level settings, so a scrub-on-null would fight an interactive theme
        choice.
      '';
    };

    cleanupPeriodDays = lib.mkOption {
      type = lib.types.nullOr lib.types.ints.positive;
      default = 365;
      example = 180;
      description = ''
        How long Claude Code retains local chat transcripts, by LAST ACTIVITY date,
        written as `.cleanupPeriodDays` in `~/.claude/settings.json`. Claude Code's
        own default is 30 days.

        This is the ONLY option here with a non-null default, and the deviation is
        deliberate (pg2-3sca9). The sibling top-level options default to `null`
        because Claude Code writes them itself at runtime (`/theme`,
        `/statusline`), so a default would fight an interactive choice. Nothing in
        Claude Code writes `.cleanupPeriodDays`, so there is no interactive choice
        to fight — and leaving it unset is not neutral: it silently deletes
        transcript history. Measured on the darwin machine 2026-08-12: 686
        transcripts aged 0-30d and ZERO older, oldest surviving file exactly 30
        days back. With no APFS local snapshot and no Time Machine destination,
        that data is unrecoverable. A non-null default is therefore what makes
        every machine that enables claude-code retain history with no per-machine
        wiring.

        The sweep covers more than the transcripts: `~/.claude/projects` (including
        each session's `subagents/` and `tool-results/`), plus `~/.claude/tasks/`,
        `~/.claude/shell-snapshots/` and `~/.claude/backups/`.

        Disk cost scales linearly and is worth checking before raising this: 1.5GB
        per 30-day window measured on the darwin machine (~50MB/day), so 365 is
        ~18GB. A very large value is NOT free — 3650 would be ~180GB.

        The type is `ints.positive` because Claude Code REJECTS
        `cleanupPeriodDays: 0` with a validation error (it once silently disabled
        persistence), so 0 is caught at eval rather than breaking the app.

        `null` is the sibling-style opt-out no-op: no filter is emitted, so a value
        a previous generation wrote is left in place, not deleted.
      '';
    };

    plugins = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Plugin keys (plugin@marketplace) to install or update";
    };

    extraSettings = lib.mkOption {
      type = lib.types.attrsOf lib.types.anything;
      default = { };
      example = lib.literalExpression ''
        {
          skillListingBudgetFraction = 0.05;
        }
      '';
      description = ''
        Freeform passthrough for any Claude Code `settings.json` key this
        module does not enumerate as its own option (concrete motivating
        case: `skillListingBudgetFraction`; see
        https://code.claude.com/docs/en/settings for the full schema).

        Merged into `~/.claude/settings.json` FIRST, before every enumerated
        option above emits its own filter. Every enumerated option's filter
        therefore runs strictly AFTER this merge in the same jq pipe, so an
        enumerated option's value always WINS over a same-named key here — a
        typo in `extraSettings` can never silently override a setting this
        module deliberately manages. This is deliberate precedence, not an
        eval-time error: naming an enumerated key here is not rejected, it is
        simply superseded.

        `enabledPlugins` and `extraKnownMarketplaces` are the one exception:
        both are stripped from this merge unconditionally (they never reach
        settings.json via this option, regardless of value), because those
        two keys are wholesale-replaced by
        `claude-settings-replace-managed-keys.sh` in a separate pass that
        already ran before this jq invocation ever sees the file. Use the
        dedicated `enabledPlugins` / `extraKnownMarketplaces` options for
        those instead.
      '';
    };
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude-code.enable && hasAnything) {
    home.activation.claude-settings = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
      # Source act_* so the inline status lines below match the chained scripts'
      # output. Same byte-identical source as the activation-lib mkBashLibrary
      # those scripts consume (repo-base readFile single source).
      ${inputs.phillipgreenii-nix-base.lib.activationHelpers}
      SETTINGS="$HOME/.claude/settings.json"

      mkdir -p "$HOME/.claude"
      [ -f "$SETTINGS" ] || echo '{}' > "$SETTINGS"

      ${lib.optionalString hasManagedKeys ''
        ${replaceScript}/bin/claude-settings-replace-managed-keys \
          "$SETTINGS" \
          '${builtins.toJSON cfg.enabledPlugins}' \
          '${builtins.toJSON cfg.extraKnownMarketplaces}' \
          "$HOME/.claude"
        act_ok "enabledPlugins and extraKnownMarketplaces replaced"
      ''}

      ${lib.optionalString hasSettings ''
        ${pkgs.jq}/bin/jq '
          ${lib.concatStringsSep " |\n    " filters}
        ' "$SETTINGS" > "$SETTINGS.tmp" && mv -f "$SETTINGS.tmp" "$SETTINGS"
        act_ok "settings.json updated"
      ''}

      ${lib.optionalString hasPlugins ''
        CLAUDE="${cfg.claudeCodePackage}/bin/claude"

        # This block runs INLINE in the activation script, so it does NOT get the
        # wrapped scripts' runtimeDeps PATH — and home-manager REPLACES PATH with a
        # set containing no git (git is home-manager-provided on darwin, so there is
        # no system fallback). `plugin marketplace update` and `plugin install` both
        # clone via `git` BY NAME, so without this prepend every github/url-source
        # marketplace and plugin fails. See bead pg2-ly6a6.
        PATH="${pkgs.git}/bin:$PATH"
        export PATH

        act_info "updating marketplaces"
        # Non-fatal (a transient network failure MUST NOT fail activation), but the
        # reason is no longer discarded: this previously ran `2>/dev/null || true`,
        # which hid the git-missing failure above for every github-source
        # marketplace, silently, for as long as the defect existed.
        $CLAUDE plugin marketplace update || act_warn "marketplace update failed (non-fatal)"

        ${lib.optionalString (directoryMarketplaces != { }) ''
          act_info "registering directory marketplaces"
          ${lib.concatStringsSep "\n" (
            lib.mapAttrsToList (name: entry: ''
              ${registerMarketplaceScript}/bin/claude-settings-register-marketplace \
                "$CLAUDE" \
                "${name}" \
                "${entry.source.path}"
            '') directoryMarketplaces
          )}
        ''}

        # Each install is followed, INSIDE the install script, by a re-assert of
        # this plugin's Nix-declared `enabledPlugins` value — because
        # `claude plugin install --scope user` sets `.enabledPlugins[spec] =
        # true` on every successful invocation and so silently overrides the
        # value replace-managed-keys wrote at the top of this same activation
        # (pg2-4q1qk; measured behavior and the reasoning are in the install
        # script's header). The settings path + declared value are passed only
        # for a plugin that HAS a declaration; a plugin listed for install with
        # no `enabledPlugins` entry keeps Claude Code's own default. Guarded by
        # checks.<system>.test-claude-settings-activation-enablement-restore,
        # which asserts on the generated invocations.
        ${lib.concatStringsSep "\n" (
          map (
            plugin:
            let
              # Assembled as a LIST joined with an explicit escaped line
              # continuation rather than written as an indented Nix string with
              # a conditional tail: nixfmt reflows the interior indentation of a
              # multi-line `''…''`, so the generated shell kept shifting. The
              # arguments are shell-quoted here, once, at the point they are
              # built.
              words = [
                "${installPluginScript}/bin/claude-settings-install-plugin"
                ''"$CLAUDE"''
                ''"${plugin}"''
                ''"$HOME/.claude/plugins/cache"''
              ]
              ++ lib.optionals (builtins.hasAttr plugin cfg.enabledPlugins) [
                ''"$SETTINGS"''
                ''"${lib.boolToString cfg.enabledPlugins.${plugin}}"''
              ];
            in
            lib.concatStringsSep " \\\n  " words
          ) cfg.plugins
        )}
      ''}
    '';
  };
}
