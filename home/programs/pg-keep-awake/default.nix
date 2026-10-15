{
  config,
  lib,
  pkgs,
  options,
  # Set by the nix-darwin / NixOS home-manager integration; null for a
  # standalone home-manager activation (no system scope to read from).
  osConfig ? null,
  ...
}:

# pg-keep-awake keeper (docket pg2-8bgxg.2, packet 17a): renders the keeper's
# config.toml, validates it at build time with `pg-keep-awake config check`, and
# registers the keeper as an HM-scoped launchd user agent through
# `phillipgreenii.programs.launchdServices.userAgents` (the pa-monitor,
# pg-task-focus and pg-connector precedents in this repo's CLAUDE.md,
# "HM-scoped launchd agents"). The shim wiring (the `caffeinate` symlink,
# `shim.enable`, `home.packages`, the claude-fleet capability and the tldr page)
# is packet 17b and lands in this same file.

let
  cfg = config.phillipgreenii.programs.pg-keep-awake;
  tomlFormat = pkgs.formats.toml { };
  isDarwin = pkgs.stdenv.hostPlatform.isDarwin;

  # The observability surface is declared at darwin/system scope (in
  # phillipgreenii-nix-support-apps). Read it null-safely from osConfig, the
  # way home/programs/pg-task-focus does: absent osConfig, an absent
  # `phillipgreenii.observability`, or a stack without `mkEmitterEnv` all fall
  # back to "no OTel", and the keeper holds the assertion with no collector.
  obs = if osConfig == null then { } else osConfig.phillipgreenii.observability or { };
  emitterEnv =
    if obs ? mkEmitterEnv then
      obs.mkEmitterEnv {
        serviceName = "pg-keep-awake";
        protocol = "grpc";
      }
    else
      { };
  # `mkEmitterEnv` yields an empty set when the stack is disabled, so the
  # endpoint's presence is the gate (never a hardcoded address).
  otelEndpoint = emitterEnv.OTEL_EXPORTER_OTLP_ENDPOINT or null;

  # Whether the composing flake imports personal's home module, which declares
  # the HM launchdServices registry. Read from the module `options` argument,
  # never from `config`/`pkgs` (using those to choose attribute NAMES is
  # infinite recursion); see home/programs/pa-monitor for the full reasoning.
  hasLaunchdRegistry = options.phillipgreenii.programs ? launchdServices;

  secs = n: "${toString n}s";

  # The rendered keeper configuration (design: Configuration). Durations are
  # whole seconds in the options so the two relations asserted below are plain
  # integer arithmetic.
  settings = {
    heartbeat_interval = secs cfg.heartbeatIntervalSeconds;
    watchdog_timeout = secs cfg.watchdogTimeoutSeconds;
    linger = secs cfg.lingerSeconds;
    battery = {
      hold_pct = cfg.battery.holdPct;
      release_pct = cfg.battery.releasePct;
      hysteresis_pct = cfg.battery.hysteresisPct;
    };
  }
  // lib.optionalAttrs (otelEndpoint != null) { otel.endpoint = otelEndpoint; };

  configUnchecked = tomlFormat.generate "pg-keep-awake-config-unchecked.toml" settings;

  # Validated at BUILD time by the package's own `config check`, so a malformed
  # or inconsistent file fails the build and never reaches launchd.
  configFile =
    pkgs.runCommand "pg-keep-awake-config.toml"
      {
        nativeBuildInputs = [ cfg.package ];
      }
      ''
        pg-keep-awake config check --config ${configUnchecked}
        cp ${configUnchecked} "$out"
      '';

  # A hash of the rendered config for the job definition. Nothing restarts a
  # launchd agent just because a file it read at startup changed, so this value
  # rides in the agent's environment: a different config is a different job
  # definition, and the apply restarts the keeper onto it (the restarted
  # keeper re-evaluates the anchor through the make-before-break replacement).
  # It is the hash of the generated file's store path, which is itself a pure
  # function of the file's content, so no build is forced at evaluation time.
  configHash = builtins.hashString "sha256" (
    builtins.unsafeDiscardStringContext (toString configUnchecked)
  );

  # The launchd stdout/stderr files live OUTSIDE the keeper's state directory
  # ($XDG_STATE_HOME/pg-keep-awake): the keeper refuses a state directory that
  # is not mode 0700, and launchd would create a missing parent with the
  # default mode.
  logDir = "${config.xdg.stateHome}/pg-keep-awake-launchd";
in
{
  options.phillipgreenii.programs.pg-keep-awake = {
    enable = lib.mkEnableOption ''
      pg-keep-awake: the single holder of the Mac keep-awake assertion. Renders
      the keeper's `config.toml` (validated at build time with
      `pg-keep-awake config check`) and, on darwin, registers the keeper as a
      keep-alive launchd user agent through
      `phillipgreenii.programs.launchdServices.userAgents` (the HM-scoped
      launchd pattern of `phillipgreenii-nix-personal` ADR 0055). That registry
      is provided by phillipgreenii-nix-personal's home module, which the
      composing flake MUST also import
    '';

    package = lib.mkPackageOption pkgs "pg-keep-awake" { };

    heartbeatIntervalSeconds = lib.mkOption {
      type = lib.types.ints.positive;
      default = 10;
      description = ''
        The keeper tick (heartbeat) period, in seconds. Renders as
        `heartbeat_interval`.
      '';
    };

    watchdogTimeoutSeconds = lib.mkOption {
      type = lib.types.ints.positive;
      default = 600;
      description = ''
        How long an anchor survives without a heartbeat, in seconds. Renders as
        `watchdog_timeout`. MUST be at least 6 times `heartbeatIntervalSeconds`
        (asserted).
      '';
    };

    lingerSeconds = lib.mkOption {
      type = lib.types.ints.unsigned;
      default = 30;
      description = ''
        How long the assertion is held after the last lease ends, in seconds.
        Renders as `linger`.
      '';
    };

    battery = {
      holdPct = lib.mkOption {
        type = lib.types.ints.between 0 100;
        default = 30;
        description = "Charge percent below which, on battery, the assertion stops being held. Renders as `battery.hold_pct`.";
      };
      releasePct = lib.mkOption {
        type = lib.types.ints.between 0 100;
        default = 20;
        description = "Charge percent at which a held assertion is released. MUST NOT exceed `holdPct` (asserted). Renders as `battery.release_pct`.";
      };
      hysteresisPct = lib.mkOption {
        type = lib.types.ints.between 0 100;
        default = 5;
        description = "Hysteresis band, in percent, before the hold resumes. Renders as `battery.hysteresis_pct`.";
      };
    };

    internal = {
      configFile = lib.mkOption {
        type = lib.types.path;
        internal = true;
        readOnly = true;
        default = configFile;
        defaultText = lib.literalMD "the rendered, build-time-validated config.toml";
        description = "The rendered configuration file in the store. Read-only.";
      };

      configUnchecked = lib.mkOption {
        type = lib.types.path;
        internal = true;
        readOnly = true;
        default = configUnchecked;
        defaultText = lib.literalMD "the rendered config.toml before `config check` runs on it";
        description = "The rendered configuration file, before build-time validation. Read-only; used by tests.";
      };

      configHash = lib.mkOption {
        type = lib.types.str;
        internal = true;
        readOnly = true;
        default = configHash;
        defaultText = lib.literalMD "the hash carried in the launchd job definition";
        description = "The config hash the launchd job definition carries. Read-only.";
      };
    };
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      # The keeper reads ${XDG_CONFIG_HOME:-~/.config}/pg-keep-awake/config.toml
      # at startup; this renders it there (config is managed by home-manager).
      xdg.configFile."pg-keep-awake/config.toml".source = configFile;

      assertions = [
        {
          assertion = cfg.watchdogTimeoutSeconds >= 6 * cfg.heartbeatIntervalSeconds;
          message = "phillipgreenii.programs.pg-keep-awake.watchdogTimeoutSeconds (${toString cfg.watchdogTimeoutSeconds}) must be at least 6 * heartbeatIntervalSeconds (${
            toString (6 * cfg.heartbeatIntervalSeconds)
          }): a slower keeper tick than the watchdog allows would let a live keeper's anchor expire.";
        }
        {
          assertion = cfg.battery.releasePct <= cfg.battery.holdPct;
          message = "phillipgreenii.programs.pg-keep-awake.battery.releasePct (${toString cfg.battery.releasePct}) must not exceed battery.holdPct (${toString cfg.battery.holdPct}): the release threshold would stop a hold the hold threshold never restricted.";
        }
        {
          assertion = !isDarwin || hasLaunchdRegistry;
          message = "phillipgreenii.programs.pg-keep-awake.enable on darwin needs phillipgreenii.programs.launchdServices, which phillipgreenii-nix-personal's home module declares (personal ADR 0055); import it alongside this module.";
        }
      ];
    })

    # LaunchAgent registration via the HM-scoped launchdServices registry
    # (personal ADR 0055), gated on `enable` and the platform at ENTRY level
    # (mkIf) and on the option's declaration at attribute level (optionalAttrs),
    # exactly as home/programs/pa-monitor and pg-task-focus do. A non-darwin
    # host gets no entry and no failure.
    (lib.optionalAttrs hasLaunchdRegistry {
      phillipgreenii.programs.launchdServices.userAgents.pg-keep-awake =
        lib.mkIf (cfg.enable && isDarwin)
          {
            label = "com.phillipg.pg-keep-awake";
            # No config flag: the keeper reads the XDG config path this module
            # renders. The binary and the config hash are what a changed apply
            # changes (the script embeds the store path; the hash is below).
            script = ''
              exec ${cfg.package}/bin/pg-keep-awake keeper
            '';
            runAtLoad = true;
            keepAlive = true;
            serviceConfig = {
              StandardOutPath = "${logDir}/launchd-stdout.log";
              StandardErrorPath = "${logDir}/launchd-stderr.log";
              # The keeper's anchor (the one `caffeinate` holder) is a child in the
              # keeper's process group. launchd.plist(5): when a job dies launchd
              # kills any remaining processes with the same process group ID unless
              # this key is true. A keeper restart MUST NOT drop a held assertion
              # (goal 4): the restarted keeper adopts the running anchor, or
              # replaces it make-before-break. Same key and reason as
              # darwin/modules/pg-router (pg2-e5u06).
              AbandonProcessGroup = true;
              EnvironmentVariables = {
                # launchd's bare default environment has no locale; the keeper needs
                # none, and nothing is inherited from the user's shell. HOME is how
                # it finds ~/.config and ~/.local/state.
                HOME = config.home.homeDirectory;
                # The hash of the rendered config (see configHash above).
                PG_KEEP_AWAKE_CONFIG_HASH = configHash;
              };
            };
          };
    })
  ];
}
