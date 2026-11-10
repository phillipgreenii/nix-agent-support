{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.services.pg-checkout-pool-exporter;
  obs = config.phillipgreenii.observability;

  # XDG_STATE_HOME default for macOS user agents, matching every other
  # generic services.* launchd module in this repo (beads-exporter, pg-router).
  primaryUser = config.system.primaryUser or null;
  homeDir = if primaryUser != null then "/Users/${primaryUser}" else "/tmp/pg-checkout-pool-exporter";
  stateHome = "${homeDir}/.local/state";
  stateDir = "${stateHome}/pg-checkout-pool-exporter";

  # The binary logs plain lines on stderr (and prints nothing on stdout), so
  # both launchd streams are kept as plain log files, rotated by manageLogs.
  stdoutLog = "${stateDir}/launchd-stdout.log";
  stderrLog = "${stateDir}/launchd-stderr.log";

  gate = cfg.enable && (obs.enable or false);
in
{
  options.phillipgreenii.services.pg-checkout-pool-exporter = {
    enable = lib.mkEnableOption "the pg-checkout-pool Prometheus exporter (pg-checkout-pool serve) as a launchd user agent";

    package = lib.mkPackageOption pkgs "pg-checkout-pool" { };

    port = lib.mkOption {
      type = lib.types.port;
      default = 9148;
      description = ''
        Loopback listen port of the metrics endpoint. The default is the
        port reserved for this exporter in the shared port ledger; this repo
        cannot read that ledger, so the value is a literal here.
      '';
    };

    pprof.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Expose Go's runtime profiler (`net/http/pprof`) for the exporter on a
        mode-0600 unix socket at `$XDG_STATE_HOME/pg-checkout-pool-exporter/pprof.sock`
        (the module writes the resolved absolute path into the launchd agent's
        `PG_CHECKOUT_POOL_PPROF_ADDR`). On by default (the Go daemon guide requires it): when `false` the
        variable is absent and the exporter opens no profiling listener. The
        endpoint is a unix socket only, so no
        port is allocated, so the port ledger is not involved. Capture recipe:
        `docs/go-daemon-profiling-runbook.md` in `phillipg-nix-repo-base`.
      '';
    };

    poolRoot = lib.mkOption {
      type = lib.types.str;
      default = "";
      example = "/path/to/pool-state";
      description = ''
        Absolute pool state directory the exporter reads (`pg-checkout-pool
        serve --root`). Empty lets the command pick its default
        (`PG_CHECKOUT_POOL_ROOT`, else under `XDG_STATE_HOME`; the agent sets
        `XDG_STATE_HOME` to `~/.local/state`, the same default every other
        process of the user sees).

        This MUST be the pool the leases are taken from: when any
        pg-router-ccpool-handler role sets `ccpool.isolation.poolRoot`, set
        this to the same path, or the exporter reports an empty pool while the
        real one is busy (the leaked-slot, quarantine and waiter rules then
        stay quiet).
      '';
    };
  };

  config = lib.mkIf gate {
    assertions = [
      {
        assertion = cfg.poolRoot == "" || lib.hasPrefix "/" cfg.poolRoot;
        message = "phillipgreenii.services.pg-checkout-pool-exporter.poolRoot must be empty or an absolute path (got \"${cfg.poolRoot}\").";
      }
    ];

    phillipgreenii = {
      system.launchdServices.userAgents.pg-checkout-pool-exporter = {
        label = "com.phillipg.pg-checkout-pool-exporter";
        # The environment is explicit: nothing is inherited from launchd's bare
        # default environment. The listener is loopback only (the command
        # refuses any other address).
        script = ''
          export HOME=${lib.escapeShellArg homeDir}
          export XDG_STATE_HOME=${lib.escapeShellArg stateHome}
          mkdir -p ${lib.escapeShellArg stateDir}
          exec ${cfg.package}/bin/pg-checkout-pool serve \
            --addr 127.0.0.1:${toString cfg.port}${
              lib.optionalString (cfg.poolRoot != "") " \\\n    --root ${lib.escapeShellArg cfg.poolRoot}"
            }
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
          # serve bounds the http Shutdown at 5 s (cmd/pg-checkout-pool/serve.go)
          # and launchd's default ExitTimeOut is also 5 s, so launchd could
          # SIGKILL mid-drain. Keep this above that bound, under the 60 s cap.
          ExitTimeOut = 15;
          StandardOutPath = stdoutLog;
          StandardErrorPath = stderrLog;
        }
        # Profiling endpoint (github.com/phillipgreenii/x/pprofserver): a 0600
        # unix socket under the exporter's own state dir (created by the script
        # above). launchd does not expand $XDG_STATE_HOME in
        # EnvironmentVariables, so the resolved stateDir is written out.
        // lib.optionalAttrs cfg.pprof.enable {
          EnvironmentVariables.PG_CHECKOUT_POOL_PPROF_ADDR = "${stateDir}/pprof.sock";
        };
      };

      # The binary logs plain text (not the jsonl contract), so the launchd
      # stdout and stderr files are a raw source; the registry requires an entry
      # for every launchd service with log collection on.
      observability.logSources.pg-checkout-pool-exporter = {
        path = "${stateDir}/launchd-*.log";
        format = "raw";
      };

      # Working signal (nix-support-apps ADR 0041, option a): the
      # pg-checkout-pool-exporter-down rule in the pool's alert file watches
      # up{job="pg-checkout-pool"}; the job name is the attribute name below.
      # That file, the dashboard and the probe registration of its uids are
      # wired by the machine (the rule files are not added here, so a machine
      # that registers them keeps one copy).
      observability.metricsTargets.pg-checkout-pool = {
        inherit (cfg) port;
        scrapeInterval = "60s";
      };
    };
  };
}
