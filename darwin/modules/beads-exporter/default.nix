{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.services.beads-exporter;
  obs = config.phillipgreenii.observability;

  # XDG_STATE_HOME default for macOS user agents, matching every other
  # generic services.* launchd module in this repo (pg-desk-serve, pg-router).
  primaryUser = config.system.primaryUser or null;
  homeDir = if primaryUser != null then "/Users/${primaryUser}" else "/tmp/beads-exporter";
  stateHome = "${homeDir}/.local/state";
  stateDir = "${stateHome}/beads-exporter";

  # The binary's stdout is JSON lines (slog), so the stdout file is named
  # *.jsonl: the logSources entry below keeps its default glob
  # ${XDG_STATE_HOME}/beads-exporter/*.jsonl, which matches this file and not
  # its rotated archive (.1) or the stderr file.
  stdoutLog = "${stateDir}/beads-exporter.jsonl";
  stderrLog = "${stateDir}/launchd-stderr.log";

  # The queue definitions are read at eval time from the committed mirror, never
  # from a runtime plugin cache.
  queues = builtins.fromJSON (builtins.readFile ../../../claude-marketplace/pb/queues.json);

  # PATH of every bd child: bash and coreutils only. git is deliberately absent.
  childPath = lib.makeBinPath [
    pkgs.bash
    pkgs.coreutils
  ];

  configJson = pkgs.writeText "beads-exporter-config-unchecked.json" (
    builtins.toJSON {
      bdPath = "${cfg.bdPackage}/bin/bd";
      inherit childPath;
      beadsDirs = lib.mapAttrs (_: db: db.beadsDir) cfg.dbs;
      inherit (cfg)
        claudeDir
        operatorNames
        port
        pollIntervalSeconds
        strandedIntervalSeconds
        staleClaimHours
        commandTimeoutSeconds
        labelCap
        ;
      inherit queues;
    }
  );

  # Validated at build time, so a malformed configuration fails the build and
  # never reaches launchd. The wrapper references this file, so a config-only
  # change changes the wrapper and restarts the agent.
  configFile =
    pkgs.runCommand "beads-exporter-config.json"
      {
        nativeBuildInputs = [ cfg.package ];
      }
      ''
        beads-exporter -check-config ${configJson}
        cp ${configJson} "$out"
      '';

  gate = cfg.enable && (obs.enable or false) && cfg.dbs != { };
in
{
  options.phillipgreenii.services.beads-exporter = {
    enable = lib.mkEnableOption "the beads-exporter Prometheus exporter as a launchd user agent";

    package = lib.mkPackageOption pkgs "beads-exporter" { };

    bdPackage = lib.mkOption {
      type = lib.types.package;
      description = ''
        The bd package the exporter runs, by absolute path. There is no default:
        this MUST be the machine's own wrapped bd, because an unwrapped bd would
        not see the machine's configuration.
      '';
    };

    dbs = lib.mkOption {
      type = lib.types.attrsOf (
        lib.types.submodule {
          options.beadsDir = lib.mkOption {
            type = lib.types.strMatching "^/.*";
            description = "Absolute path of the database's .beads directory.";
          };
        }
      );
      default = { };
      description = ''
        The bead databases to export, keyed by database name (the `db` label
        value). The module is inert while this is empty.
      '';
    };

    claudeDir = lib.mkOption {
      type = lib.types.strMatching "^/.*";
      description = ''
        The Claude state directory (absolute). There is no default: it is
        required whenever the module is active, and the machine layer sets it
        to the operator's own Claude directory.
      '';
    };

    operatorNames = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Assignee values that mean a bead was claimed in the operator's own name.";
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 9146;
      description = ''
        Loopback listen port of the metrics endpoint. The default is the
        port reserved for this exporter in the shared port ledger; this repo
        cannot read that ledger, so the value is a literal here.
      '';
    };

    pollIntervalSeconds = lib.mkOption {
      type = lib.types.ints.positive;
      default = 120;
      description = "Period of the main collection pass.";
    };

    strandedIntervalSeconds = lib.mkOption {
      type = lib.types.ints.positive;
      default = 600;
      description = "Period of the throughput and stranded-claim passes.";
    };

    staleClaimHours = lib.mkOption {
      type = lib.types.ints.positive;
      default = 6;
      description = "Age after which a claim with no live owner counts as stale.";
    };

    commandTimeoutSeconds = lib.mkOption {
      type = lib.types.ints.positive;
      default = 30;
      description = "Bound on each bd call.";
    };

    labelCap = lib.mkOption {
      type = lib.types.ints.positive;
      default = 500;
      description = "How many bead labels get their own series; the rest are counted together.";
    };

    internal.configFile = lib.mkOption {
      type = lib.types.path;
      internal = true;
      readOnly = true;
      default = configFile;
      defaultText = lib.literalMD "the rendered, build-time-validated configuration file";
      description = ''
        The rendered configuration file in the store. Read-only; the machine
        wiring runs the exporter's -check-config on it.
      '';
    };
  };

  config = lib.mkIf gate {
    phillipgreenii = {
      system.launchdServices.userAgents.beads-exporter = {
        label = "com.phillipg.beads-exporter";
        # The environment is explicit and complete: the exporter re-asserts the
        # same values on every bd child (defence in depth), and nothing is
        # inherited from launchd's bare default environment.
        script = ''
          export HOME=${lib.escapeShellArg homeDir}
          export PATH=${lib.escapeShellArg childPath}
          export BD_JSON_ENVELOPE=1
          export BEADS_DOLT_AUTO_START=0
          export BD_BACKUP_ENABLED=0
          mkdir -p ${lib.escapeShellArg stateDir}
          exec ${cfg.package}/bin/beads-exporter -config ${cfg.internal.configFile}
        '';
        runAtLoad = true;
        keepAlive = true;
        manageLogs = {
          enable = true;
          files = [
            stdoutLog
            stderrLog
          ];
        };
        serviceConfig = {
          ProcessType = "Background";
          StandardOutPath = stdoutLog;
          StandardErrorPath = stderrLog;
        };
      };

      observability = {
        logSources.beads-exporter = {
          format = "jsonl";
        };

        metricsTargets.beads-exporter = {
          inherit (cfg) port;
          scrapeInterval = "60s";
        };

        dashboardProviders.beads-exporter = {
          folder = "Claude Agents";
          dashboards = [ ../../../packages/beads-exporter/grafana/beads.json ];
        };

        alertRuleFiles = [ ../../../packages/beads-exporter/grafana/alerting/alerts.yaml ];
      };
    };
  };
}
