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

let
  cfg = config.phillipgreenii.programs.pg-task-focus;
  jsonFormat = pkgs.formats.json { };

  # The observability surface is declared at darwin/system scope (in
  # phillipgreenii-nix-support-apps). Read it null-safely from osConfig, the
  # way home/programs/pa-monitor does: absent osConfig, an absent
  # `phillipgreenii.observability`, or a stack without `mkEmitterEnv` all fall
  # back to "no OTel", and the daemon starts and serves with no collector.
  obs = if osConfig == null then { } else osConfig.phillipgreenii.observability or { };
  emitterEnv =
    if obs ? mkEmitterEnv then
      obs.mkEmitterEnv {
        serviceName = "pg-task-focus";
        protocol = "grpc";
      }
    else
      { };

  # Whether the composing flake imports personal's home module, which declares
  # the HM launchdServices registry. Read from the module `options` argument,
  # never from `config`/`pkgs` (using those to choose attribute NAMES is
  # infinite recursion); see home/programs/pa-monitor for the full reasoning.
  hasLaunchdRegistry = options.phillipgreenii.programs ? launchdServices;
  # Likewise for the pg-connector HM module, which this module registers the
  # connector backend with when `connector.enable` is set (same reasoning: a
  # definition of an undeclared option errors even under mkIf false).
  hasPgConnector = options.phillipgreenii.programs ? pg-connector;
  connectorBackend = "pg-connector-calendar-task-focus";
  isDarwin = pkgs.stdenv.hostPlatform.isDarwin;

  # The rendered configuration: the top-level keys the daemon reads itself
  # (listen_port, public_url) plus the freeform settings (defaults, profiles,
  # tasks, cycles, group_order), which the consuming flake owns because every
  # task, link and routine detail is configuration, never code in this public
  # repository. Validated at BUILD time by the package's own `config check`,
  # so a malformed configuration fails the build and never reaches launchd.
  configJson = jsonFormat.generate "pg-task-focus-config-unchecked.json" (
    cfg.settings
    // {
      listen_port = cfg.listenPort;
    }
    // lib.optionalAttrs (cfg.publicUrl != null) { public_url = cfg.publicUrl; }
  );
  configFile =
    pkgs.runCommand "pg-task-focus-config.json"
      {
        nativeBuildInputs = [ cfg.package ];
      }
      ''
        pg-task-focus config check ${configJson}
        cp ${configJson} "$out"
      '';

  stateDir = "${config.xdg.stateHome}/pg-task-focus";
in
{
  options.phillipgreenii.programs.pg-task-focus = {
    enable = lib.mkEnableOption ''
      pg-task-focus: a local daemon and command-line client that keep a written
      daily routine (day, week and sprint checklists and timed work cycles) as an
      append-only event log. Installs the CLI and renders the configuration file
    '';

    package = lib.mkPackageOption pkgs "pg-task-focus" { };

    daemon.enable = lib.mkEnableOption ''
      the pg-task-focus launchd user agent. On darwin it is registered right
      here, in this HM module, through
      `phillipgreenii.programs.launchdServices.userAgents` (the HM-scoped launchd
      pattern of `phillipgreenii-nix-personal` ADR 0055, as pa-monitor does), so
      the daemon, its audio session and its data live in the user's own scope.
      That registry is provided by phillipgreenii-nix-personal's home module,
      which the composing flake MUST also import. The system-scope observability
      registrations (metrics target, log source, alert rules, dashboard) live in
      darwin/modules/pg-task-focus and follow this flag across
      `home-manager.users.<u>`
    '';

    listenPort = lib.mkOption {
      type = lib.types.port;
      default = 49210;
      description = ''
        The loopback port the daemon listens on (127.0.0.1 only). It renders into
        the configuration file as `listen_port`, and the CLI finds the daemon
        through `PG_TASK_FOCUS_ADDR`, which this module sets to match. A change
        takes effect on restart. The default is a literal: this repo cannot read
        the workspace's port ledger, so a consuming flake that has one MUST
        check it.
      '';
    };

    connector.enable = lib.mkEnableOption ''
      the pg-connector calendar and attention backend for this daemon
      (`pg-connector-calendar-task-focus`). Registers it under
      `phillipgreenii.programs.pg-connector` as a `connector.calendar` backend
      and an `attention.sources` entry, with its `backends` config block: the
      daemon's address, derived from `listenPort` so the two cannot drift, and
      the named calendar query `running`, because `pg-connector calendar list`
      resolves named queries from the backend's config. Needs
      `phillipgreenii.programs.pg-connector` to be declared (this flake's home
      module declares it) and enabled by the consuming flake
    '';

    publicUrl = lib.mkOption {
      type = lib.types.nullOr (lib.types.strMatching "^https?://[^[:space:]/?#]+[^[:space:]]*$");
      default = null;
      example = "https://focus.example.test";
      description = ''
        The browser URL of the local reverse proxy that fronts the web UI. It
        renders into the configuration as `public_url`: the daemon builds every
        deep link from it and adds its host and origin to the Host and Origin
        allowlists. Registering the hostname in the proxy (its service list, the
        hosts entry and its dashboard test) is done in the consuming flake that
        owns the proxy; this module only documents the contract. The daemon works
        with no proxy.
      '';
    };

    settings = lib.mkOption {
      inherit (jsonFormat) type;
      default = { };
      example = lib.literalExpression ''
        {
          defaults = {
            cycle_minutes = 25;
            profile = "normal";
            alert = { sound = "Glass"; repeat_minutes = 5; };
          };
          profiles.normal = { daily = [ "plan-day" ]; weekly = [ ]; sprint = [ ]; cycles = [ "deep-work" ]; };
          tasks.plan-day = {
            title = "Plan the day";
            cadence = "daily";
            due = { at = "09:00"; tz = "America/New_York"; };
          };
          cycles.deep-work = { title = "Deep work"; minutes = 50; keys = [ ]; };
        }
      '';
      description = ''
        The routine: `defaults`, `group_order`, `profiles`, `tasks` and `cycles`,
        in the schema of `packages/pg-task-focus/schemas/config.schema.json`.
        There is no default zone anywhere: every due rule names its IANA `tz`.
        `listen_port` and `public_url` come from the options above and MUST NOT
        be set here. The rendered file is validated at build time
        (`pg-task-focus config check`).
      '';
    };

    dataDir = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        The data directory (absolute). Null means the daemon's own default,
        `$XDG_DATA_HOME/pg-task-focus` or `~/.local/share/pg-task-focus`.
      '';
    };

    internal.configFile = lib.mkOption {
      type = lib.types.path;
      internal = true;
      readOnly = true;
      default = configFile;
      defaultText = lib.literalMD "the rendered, build-time-validated configuration file";
      description = "The rendered configuration file in the store. Read-only.";
    };
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      home.packages = [ cfg.package ];

      # The CLI finds the daemon and the configuration through these. A shell
      # that predates an apply holds stale values (home.sessionVariables are
      # exported at shell init), so a post-apply check reads them from
      # hm-session-vars.sh, not from the running shell.
      home.sessionVariables = {
        PG_TASK_FOCUS_ADDR = "127.0.0.1:${toString cfg.listenPort}";
        PG_TASK_FOCUS_CONFIG = toString cfg.internal.configFile;
      };

      assertions = [
        {
          assertion = !(cfg.settings ? listen_port) && !(cfg.settings ? public_url);
          message = "phillipgreenii.programs.pg-task-focus.settings MUST NOT set listen_port or public_url: use the listenPort and publicUrl options.";
        }
      ];
    })

    # The daemon without the program: the agent runs the package by store path.
    (lib.mkIf cfg.daemon.enable {
      assertions = [
        {
          assertion = !(cfg.daemon.enable && isDarwin) || hasLaunchdRegistry;
          message = "phillipgreenii.programs.pg-task-focus.daemon.enable on darwin needs phillipgreenii.programs.launchdServices, which phillipgreenii-nix-personal's home module declares (personal ADR 0055); import it alongside this module.";
        }
      ];
    })

    # Registration of the connector backend with pg-connector (bead
    # pg2-t7me1.4): gated on the option's declaration at attribute level
    # (optionalAttrs) and on connector.enable at value level (mkIf). The lists
    # concatenate with whatever the consuming flake registers itself.
    (lib.optionalAttrs hasPgConnector {
      phillipgreenii.programs.pg-connector = lib.mkIf (cfg.enable && cfg.connector.enable) {
        connector.calendar = [ connectorBackend ];
        attention.sources = [ connectorBackend ];
        backends.${connectorBackend} = {
          base_url = "http://127.0.0.1:${toString cfg.listenPort}";
          # `calendar list` needs at least one named query. Each element is a
          # look-ahead duration; nothing exists in the future, so any window
          # answers with the open segment of the running cycle (if any).
          queries.running = [ "1h" ];
        };
      };
    })

    # LaunchAgent registration via the HM-scoped launchdServices registry
    # (personal ADR 0055), gated on daemon.enable at ENTRY level (mkIf) and on
    # the option's declaration at attribute level (optionalAttrs), exactly as
    # home/programs/pa-monitor does.
    (lib.optionalAttrs hasLaunchdRegistry {
      phillipgreenii.programs.launchdServices.userAgents.pg-task-focus =
        lib.mkIf (cfg.daemon.enable && isDarwin)
          {
            label = "com.phillipg.pg-task-focus";
            # The configuration is passed by store path, so a config-only change
            # changes this script, the wrapper and its hash, and the agent
            # restarts onto it (a SIGHUP would also do, but the restart also
            # picks up listen_port and public_url, which a reload cannot).
            script = ''
              exec ${cfg.package}/bin/pg-task-focus serve --config ${cfg.internal.configFile}${
                lib.optionalString (cfg.dataDir != null) " --data-dir ${lib.escapeShellArg cfg.dataDir}"
              }
            '';
            runAtLoad = true;
            keepAlive = true;
            serviceConfig = {
              # The daemon logs JSON lines on stdout: the *.jsonl name matches the
              # log source's default glob ${XDG_STATE_HOME}/pg-task-focus/*.jsonl
              # (and not its rotated archive or the stderr file).
              StandardOutPath = "${stateDir}/pg-task-focus.jsonl";
              StandardErrorPath = "${stateDir}/launchd-stderr.log";
              EnvironmentVariables = emitterEnv // {
                # launchd's bare default environment has no locale; the daemon
                # needs none, and nothing is inherited from the user's shell.
                HOME = config.home.homeDirectory;
              };
            };
          };
    })
  ];
}
