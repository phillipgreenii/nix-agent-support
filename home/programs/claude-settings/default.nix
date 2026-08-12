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
    ++ lib.optional (cfg.sandbox != null) ".sandbox = ${builtins.toJSON cfg.sandbox}"
    ++ lib.optional (
      cfg.sandbox == null && cfg.sandboxEnabled != null
    ) ".sandbox.enabled = ${builtins.toJSON cfg.sandboxEnabled}"
    ++ lib.optional (cfg.theme != null) ".theme = ${builtins.toJSON cfg.theme}"
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

    plugins = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Plugin keys (plugin@marketplace) to install or update";
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

        ${lib.concatStringsSep "\n" (
          map (plugin: ''
            ${installPluginScript}/bin/claude-settings-install-plugin \
              "$CLAUDE" \
              "${plugin}" \
              "$HOME/.claude/plugins/cache"
          '') cfg.plugins
        )}
      ''}
    '';
  };
}
