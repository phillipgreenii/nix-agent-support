{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.claude.settings;

  filters =
    lib.optional (cfg.statusLine != null) ".statusLine = ${builtins.toJSON cfg.statusLine}"
    ++ lib.mapAttrsToList (
      name: val: ".extraKnownMarketplaces[\"${name}\"] = ${builtins.toJSON val}"
    ) cfg.extraKnownMarketplaces
    ++ lib.optional (
      cfg.enabledPlugins != { }
    ) ".enabledPlugins *= ${builtins.toJSON cfg.enabledPlugins}"
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
    ++ [
      (
        if cfg.noFlicker then ".env.CLAUDE_CODE_NO_FLICKER = \"1\"" else "del(.env.CLAUDE_CODE_NO_FLICKER)"
      )
    ];

  hasSettings = filters != [ ];
  hasPlugins = cfg.plugins != [ ] && cfg.claudeCodePackage != null;
  hasAnything = hasSettings || hasPlugins;
in
{
  options.phillipgreenii.programs.claude.settings = {
    claudeCodePackage = lib.mkOption {
      type = lib.types.nullOr lib.types.package;
      default = null;
      description = "The claude-code package; marketplace update and plugin install/update are skipped if null";
    };

    statusLine = lib.mkOption {
      type = lib.types.nullOr (lib.types.attrsOf lib.types.anything);
      default = null;
      description = "statusLine config object to set in ~/.claude/settings.json";
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
      description = "Whether to show the clear-context option when accepting a plan";
    };

    showThinkingSummaries = lib.mkOption {
      type = lib.types.nullOr lib.types.bool;
      default = null;
      description = "Whether to show thinking summaries in interactive sessions (off by default since Claude Code 2.x)";
    };

    includeCoAuthoredBy = lib.mkOption {
      type = lib.types.nullOr lib.types.bool;
      default = null;
      description = "Whether Claude Code adds Co-Authored-By trailers to git commits";
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
      '';
    };

    sandboxEnabled = lib.mkOption {
      type = lib.types.nullOr lib.types.bool;
      default = null;
      description = ''
        Convenience toggle for `sandbox.enabled`. When set and the
        `sandbox` option is null, writes only `.sandbox.enabled` into
        settings.json. Ignored if `sandbox` is set (use `sandbox.enabled`
        there instead).
      '';
    };

    noFlicker = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Set CLAUDE_CODE_NO_FLICKER=1 in ~/.claude/settings.json env to suppress terminal flicker";
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
      '';
    };

    plugins = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Plugin keys (plugin@marketplace) to install or update";
    };
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude.enable && hasAnything) {
    home.activation.claude-settings = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
      SETTINGS="$HOME/.claude/settings.json"

      mkdir -p "$HOME/.claude"
      [ -f "$SETTINGS" ] || echo '{}' > "$SETTINGS"

      ${lib.optionalString hasSettings ''
        ${pkgs.jq}/bin/jq '
          ${lib.concatStringsSep " |\n    " filters}
        ' "$SETTINGS" > "$SETTINGS.tmp" && mv -f "$SETTINGS.tmp" "$SETTINGS"
        echo "claude-settings: settings.json updated"
      ''}

      ${lib.optionalString hasPlugins ''
        CLAUDE="${cfg.claudeCodePackage}/bin/claude"

        echo "claude-settings: updating marketplaces"
        $CLAUDE plugin marketplace update 2>/dev/null || true

        ${lib.concatStringsSep "\n" (
          map (plugin: ''
            if $CLAUDE plugin install "${plugin}" --scope user 2>/dev/null; then
              echo "claude-settings: ${plugin} installed"
            elif $CLAUDE plugin update "${plugin}" --scope user 2>/dev/null; then
              echo "claude-settings: ${plugin} updated"
            else
              echo "claude-settings: ${plugin} install/update failed (non-fatal)"
            fi
          '') cfg.plugins
        )}
      ''}
    '';
  };
}
