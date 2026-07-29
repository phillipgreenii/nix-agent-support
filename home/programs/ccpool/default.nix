{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.ccpool;
  tomlFormat = pkgs.formats.toml { };
  defaultCwd = lib.attrByPath [ "claude" "default_cwd" ] "" cfg.settings;
in
{
  options.phillipgreenii.programs.ccpool = {
    enable = lib.mkEnableOption "ccpool (Claude Code session pool manager)";
    package = lib.mkPackageOption pkgs "ccpool" { };

    reap.enable = lib.mkEnableOption ''
      the ccpool-reap timer (LaunchAgent on darwin / systemd user timer on
      linux) that runs `ccpool reap-all` periodically — governing the default
      pool plus every registered named pool in one pass, not just the default.
      The unit itself is registered by the parallel darwin/nixos module (per
      ADR 0049); this is the public-facing enable flag.
    '';
    reap.intervalSeconds = lib.mkOption {
      type = lib.types.int;
      default = 300;
      description = "How often (seconds) to run `ccpool reap-all`.";
    };

    settings = lib.mkOption {
      inherit (tomlFormat) type;
      default = { };
      description = "Contents of ccpool's config.toml (merged over plugin_dir).";
      example = {
        pool.max_sessions = 6;
        notify.adapter = "desktop";
      };
    };
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude-code.enable && cfg.enable) {
    home.packages = [ cfg.package ];

    # config.toml: the module always pins plugin_dir to the rendered store path;
    # everything else comes from cfg.settings.
    xdg.configFile."ccpool/config.toml".source = tomlFormat.generate "ccpool-config.toml" (
      lib.recursiveUpdate cfg.settings {
        claude.plugin_dir = "${cfg.package}/share/ccpool-plugin";
      }
    );

    # Pre-trust default_cwd in ~/.claude.json — set
    # `.projects["<resolved default_cwd>"].hasTrustDialogAccepted = true` before
    # any launch. An untrusted cwd stalls `claude` on the interactive
    # folder-trust prompt, which BLOCKS the REPL from starting, so SessionStart
    # never fires and ccpool's waiter hangs forever; trust is therefore
    # guaranteed before launch, never detected after.
    #
    # This activation is the PRIMARY, non-racy trust path. ccpool's runtime
    # `ensure` fallback (`packages/ccpool/internal/trust`) exists only for ad-hoc
    # `--cwd` values and is deliberately kept near-zero-frequency: ~/.claude.json
    # is Claude-owned, is rewritten on Claude's own schedule, and does NOT honour
    # ccpool's flock, so its read-merge-rename has an unavoidable lost-update
    # window and a lost write degrades right back to the trust-prompt hang.
    #
    # Best effort — a trust-write failure must not break activation; the runtime
    # path covers it. Rationale for the cwd being a trust-boundary input at all:
    # ADR 0038's Context.
    home.activation = lib.mkIf (defaultCwd != "") {
      ccpoolTrust = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
        $DRY_RUN_CMD ${cfg.package}/bin/ccpool trust ${lib.escapeShellArg defaultCwd} || true
      '';
    };
  };
}
