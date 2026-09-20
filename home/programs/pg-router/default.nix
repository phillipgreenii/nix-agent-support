{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pg-router;

  # Shared PG_ROUTER_* Environment-list builder for both systemd units below
  # (periodicDrain's oneshot run-until-idle pass, daemon's long-running
  # `run` core): each spawns a pg-router process needing the same
  # REPO_ROOT/BEADS_PREFIX/CONFIG trio, plus daemon-only INV-LIFE-2
  # gate-path overrides. Kept as one `let` so the two units cannot drift on
  # how they assemble PG_ROUTER_CONFIG's writeText call or the optional-var
  # handling (Task 1.7).
  mkEnvironment =
    {
      repoRoot,
      beadsPrefix,
      configText,
      configFileName,
      operatorPausedPath ? null,
      cicdDownPath ? null,
      handlerCommand ? null,
      handlerCommandDir ? null,
      handlerConfig ? null,
      handlerCcpoolPool ? null,
      metricsAddr ? null,
    }:
    [
      "PG_ROUTER_REPO_ROOT=${repoRoot}"
    ]
    ++ lib.optional (beadsPrefix != null) "PG_ROUTER_BEADS_PREFIX=${beadsPrefix}"
    ++ [
      "PG_ROUTER_CONFIG=${pkgs.writeText configFileName configText}"
    ]
    ++ lib.optional (operatorPausedPath != null) "PG_ROUTER_OPERATOR_PAUSED=${operatorPausedPath}"
    ++ lib.optional (cicdDownPath != null) "PG_ROUTER_CICD_DOWN=${cicdDownPath}"
    # handlerCommand/handlerCommandDir (this bead, pg2-pteab): threads the
    # now-landed Go-level PG_ROUTER_HANDLER_COMMAND[_DIR] support
    # (internal/config/config.go, bead pg2-ymb3v) into this systemd unit's
    # own Environment, following the same optional-var pattern as
    # operatorPausedPath/cicdDownPath above.
    ++ lib.optional (handlerCommand != null) "PG_ROUTER_HANDLER_COMMAND=${handlerCommand}"
    ++ lib.optional (handlerCommandDir != null) "PG_ROUTER_HANDLER_COMMAND_DIR=${handlerCommandDir}"
    # handlerConfig: threads PG_ROUTER_CCPOOL_HANDLER_CONFIG, the handler
    # participant's OWN launch/isolation config (RepoRoot/WorktreeDir/
    # BeadsPrefix, --config/PG_ROUTER_CCPOOL_HANDLER_CONFIG in
    # pg-router-ccpool-handler's own internal/config), into this systemd
    # unit's Environment — same optional-var pattern as handlerCommand[Dir]
    # above. Without this, the handler process falls back to its own
    # Default() (WorktreeDir: ""), and every dispatch fails at worktree
    # creation (`mkdir : no such file or directory`) before any real work
    # starts.
    ++ lib.optional (handlerConfig != null) "PG_ROUTER_CCPOOL_HANDLER_CONFIG=${handlerConfig}"
    # handlerCcpoolPool (pg2-1p4yp): sets CCPOOL_POOL directly in pg-router
    # core's OWN process environment -- NOT a PG_ROUTER_* var, but ccpool's
    # own pool-selection env var (`packages/ccpool/internal/config/pool.go`),
    # read verbatim by every `ccpool` CLI invocation downstream of this core
    # process. It has to be set HERE (not in `pg-router-ccpool-handler`'s own
    # module) because that handler is spawned as THIS core's subprocess
    # (`internal/wireclient.OSRunner.Run`, no `cmd.Env` override -> inherits
    # this process's env), and the handler's own `ccpool` calls
    # (`internal/ccpool/cli.go`) inherit the handler's env the same way --
    # so the var has to originate at the top of that inheritance chain.
    # `null` (the default) leaves every dispatch on ccpool's shared default
    # (XDG) pool, unchanged from before this option existed. Typically set to
    # `phillipgreenii.programs.pg-router-ccpool-handler.pool.dir`'s own value
    # (see that module's `pool` option group for the paired opt-in and its
    # own bootstrap/registration step).
    ++ lib.optional (handlerCcpoolPool != null) "CCPOOL_POOL=${handlerCcpoolPool}"
    # metricsAddr (pg2-ui2i3): threads PG_ROUTER_METRICS_ADDR, which
    # internal/config/config.go only reads for the `run` core (this
    # systemd unit's daemon service, not periodicDrain's `run-until-idle`),
    # into the OTel Prometheus /metrics direct-scrape endpoint — same
    # optional-var pattern as the other daemon-only fields above.
    ++ lib.optional (metricsAddr != null) "PG_ROUTER_METRICS_ADDR=${metricsAddr}";
in
{
  options.phillipgreenii.programs.pg-router = {
    enable = lib.mkEnableOption ''
      pg-router (PR-feedback orchestrator: a `pg-router run-until-idle` pass discovers
      ready beads and dispatches feedback-processor / worker sessions via ccpool).
      See `periodicDrain` (timer-driven run-until-idle) and `daemon` (long-running
      `run`) below for
      turnkey systemd deployment — the two are mutually exclusive.
      Runtime-depends on `ccpool` and `bd` being on PATH.
    '';
    package = lib.mkPackageOption pkgs "pg-router" { };

    periodicDrain = {
      enable = lib.mkEnableOption ''
        a systemd --user timer that runs `pg-router run-until-idle` periodically,
        rather than a one-off manual invocation or the `daemon` submodule's
        long-running core (see `daemon.enable`). Mutually exclusive with
        `daemon.enable`: both are independent pg-router cores and cannot share
        the same LogDir/socket/WAL. Each fire is one drain-and-exit pass:
        discover ready work, dispatch roles, wait for completion, exit.
      '';
      interval = lib.mkOption {
        type = lib.types.str;
        default = "5m";
        description = "Systemd time-span between drain passes (OnUnitActiveSec/OnBootSec).";
      };
      repoRoot = lib.mkOption {
        type = lib.types.str;
        description = ''
          PG_ROUTER_REPO_ROOT: a real git repository pg-router anchors to (it runs
          `git worktree add` there for any role using the default "worktree"
          isolation strategy, and always reads bd's connection config from
          `<repoRoot>/.beads`). No default — deployment-specific.
        '';
      };
      beadsPrefix = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          PG_ROUTER_BEADS_PREFIX override. `null` leaves pg-router's own default
          (`zr`) or any externally-set env var in effect; set explicitly for a
          deployment using a different bd issue prefix.
        '';
      };
      configText = lib.mkOption {
        type = lib.types.lines;
        description = ''
          The pg-router `config.toml` content (`[[query]]`/`[[role]]` etc.),
          rendered into the Nix store and pointed at via `PG_ROUTER_CONFIG` —
          fully declarative, so no machine-local `.pg-router/config.toml`
          bootstrap step is needed.
        '';
      };
      handlerCommand = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          PG_ROUTER_HANDLER_COMMAND override: the argv prefix invoked, over
          the wire, for every enabled role's registered handler participant
          (e.g. `pg-router-ccpool-handler`). `null` leaves it unset — an
          unconfigured deployment gets a clear per-dispatch error instead of
          a hardcoded participant name (GOAL-MIN-1's Floor).
        '';
      };
      handlerCommandDir = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          PG_ROUTER_HANDLER_COMMAND_DIR override: a directory of per-role
          JSON files (`<role.Name>.json`) letting differently-configured
          roles sharing one `handlerCommand` binary (e.g.
          feedback/worker/review) dispatch through their own participant
          config — typically
          `phillipgreenii.programs.pg-router-ccpool-handler.handlerCommandDir`
          (interpolated to a string, e.g. `"${cfg.handlerCommandDir}"`, since
          this option is a plain string like `handlerCommand` above, not a
          package). `null` leaves it unset — every enabled role then resolves
          to the plain `handlerCommand` argv, unchanged from before this
          option existed.
        '';
      };
      handlerConfig = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          PG_ROUTER_CCPOOL_HANDLER_CONFIG override: a path to the handler
          participant's own launch/isolation config (RepoRoot/WorktreeDir/
          BeadsPrefix/etc — `pg-router-ccpool-handler`'s own
          `--config`/`PG_ROUTER_CCPOOL_HANDLER_CONFIG`), typically
          `phillipgreenii.programs.pg-router-ccpool-handler.launchConfigFile`
          (interpolated to a string, like `handlerCommandDir` above). `null`
          leaves it unset — the handler process falls back to its own
          Default() (an empty WorktreeDir), so every dispatch needing
          isolation fails at worktree creation.
        '';
      };
      handlerCcpoolPool = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          CCPOOL_POOL override for this core's own process environment (NOT
          a PG_ROUTER_* var — ccpool's own pool-selection env var), so every
          ccpool dispatch this core's handler subprocess chain launches uses
          a dedicated pool instead of ccpool's shared default pool. `null`
          (the default) leaves it unset, unchanged from before this option
          existed. Typically set to
          `phillipgreenii.programs.pg-router-ccpool-handler.pool.dir`'s own
          value — see that module's `pool` option group (`pg2-1p4yp`) for
          the paired opt-in and its own bootstrap/registration step, and
          that option group's doc comment for why this var can only be set
          HERE, not in that module.
        '';
      };
    };

    daemon = {
      enable = lib.mkEnableOption ''
        a systemd --user service that runs `pg-router run` — the long-running
        core, producing + dispatching on a fixed poll interval until
        SIGINT/SIGTERM — rather than the timer-driven one-shot
        `periodicDrain`. Mutually exclusive with `periodicDrain.enable`: both
        are independent pg-router cores over the same LogDir/socket/WAL
        (events.jsonl, the discovery record, the push-ingest socket), and
        running both would race on that shared state. On darwin,
        `darwin/modules/pg-router/default.nix` mirrors this into a LaunchAgent
        (this HM systemd unit alone is a darwin no-op).
      '';
      repoRoot = lib.mkOption {
        type = lib.types.str;
        description = ''
          PG_ROUTER_REPO_ROOT for the daemon core — see `periodicDrain.repoRoot`
          for the full contract. No default — deployment-specific.
        '';
      };
      beadsPrefix = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "PG_ROUTER_BEADS_PREFIX override — see `periodicDrain.beadsPrefix`.";
      };
      configText = lib.mkOption {
        type = lib.types.lines;
        description = "The pg-router `config.toml` content — see `periodicDrain.configText`.";
      };
      handlerCommand = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "PG_ROUTER_HANDLER_COMMAND override for the daemon core — see `periodicDrain.handlerCommand`.";
      };
      handlerCommandDir = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "PG_ROUTER_HANDLER_COMMAND_DIR override for the daemon core — see `periodicDrain.handlerCommandDir`.";
      };
      handlerConfig = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "PG_ROUTER_CCPOOL_HANDLER_CONFIG override for the daemon core — see `periodicDrain.handlerConfig`.";
      };
      handlerCcpoolPool = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "CCPOOL_POOL override for the daemon core — see `periodicDrain.handlerCcpoolPool`.";
      };
      metricsAddr = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          PG_ROUTER_METRICS_ADDR: listen address (`host:port`) for the daemon
          core's OTel Prometheus `/metrics` direct-scrape HTTP endpoint
          (`internal/config/config.go`, `internal/metrics`). `null` leaves it
          unset, disabling the endpoint (`Config.Load()`'s own default).
          Daemon-only — `internal/config/config.go` only reads
          `PG_ROUTER_METRICS_ADDR` for the `run` core; `periodicDrain`'s
          `run-until-idle` has no equivalent option since a drain-and-exit
          pass has nothing to keep scraping.
        '';
      };
      gates = {
        operatorPausedPath = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = ''
            PG_ROUTER_OPERATOR_PAUSED override: the `operator-paused` gate file path
            (`INV-LIFE-2`). `null` leaves `Config.Load()`'s own default
            (`<PG_ROUTER_LOG_DIR>/gates/operator-paused`) in effect — note that
            default is now live even when unset here (Task 1.2b): a stray
            file already at that path gates a daemon that previously could
            not be gated, and gate files are never swept.
          '';
        };
        cicdDownPath = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = ''
            PG_ROUTER_CICD_DOWN override: the `cicd-down` gate file path
            (`INV-LIFE-2`). `null` leaves `Config.Load()`'s own default
            (`<PG_ROUTER_LOG_DIR>/gates/cicd-down`) in effect — same
            gates-default-on hazard as `operatorPausedPath` above.
          '';
        };
      };
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    assertions = [
      {
        assertion = !(cfg.periodicDrain.enable && cfg.daemon.enable);
        message = ''
          phillipgreenii.programs.pg-router: periodicDrain.enable and
          daemon.enable cannot both be true — they are two independent
          pg-router cores (`run-until-idle` vs `run`) over the same
          LogDir/socket/WAL (events.jsonl, the discovery record, the
          push-ingest socket), and running both would race on that shared
          state. Enable exactly one.
        '';
      }
      {
        # tc-24qs: pg-router's own enable doc says "Runtime-depends on `ccpool`
        # ... being on PATH", but that dependency was never enforced, so a
        # consumer enabling only `programs.pg-router` evaluates cleanly and then
        # fails at RUNTIME on every single dispatch — not loudly, either: the
        # tmux session and `claude` process start and stay alive
        # (`state=starting live=true`), so it presents as a silent 10-minute
        # hang per attempt ("did not reach ready before timeout"), not an
        # error. Root cause: `programs.ccpool`'s module is the only thing that
        # renders `claude.plugin_dir` into ccpool's config.toml; without it
        # ccpool falls back to its own empty-string default, the
        # ccpool-plugin's SessionStart hook (`ccpool hook start`) never
        # registers, and no session can ever reach `ready` (see
        # packages/ccpool's session.ErrNoPluginDir, which now fails the
        # underlying `ccpool new`/`reply` call immediately instead of hanging
        # — this assertion catches the same misconfiguration earlier, at
        # eval/build time, before any dispatch is even attempted).
        assertion = config.phillipgreenii.programs.ccpool.enable;
        message = ''
          phillipgreenii.programs.pg-router.enable requires
          phillipgreenii.programs.ccpool.enable = true (or the "claude-fleet"
          capability, which enables both together) — pg-router dispatches every
          role via a launched `ccpool` session, and only the ccpool module
          renders claude.plugin_dir into ccpool's config.toml. Without it,
          the ccpool-plugin's SessionStart hook never registers and every
          dispatch would silently hang for the full Wait timeout before
          failing with "did not reach ready before timeout" (tc-24qs).
        '';
      }
    ];

    systemd = {
      user = {
        services = {
          pg-router-drain = lib.mkIf cfg.periodicDrain.enable {
            Unit.Description = "pg-router run-until-idle: one discover -> dispatch -> wait pass";
            Service = {
              Type = "oneshot";
              ExecStart = "${cfg.package}/bin/pg-router run-until-idle";
              Environment = mkEnvironment {
                repoRoot = cfg.periodicDrain.repoRoot;
                beadsPrefix = cfg.periodicDrain.beadsPrefix;
                configText = cfg.periodicDrain.configText;
                configFileName = "pg-router-drain-config.toml";
                handlerCommand = cfg.periodicDrain.handlerCommand;
                handlerCommandDir = cfg.periodicDrain.handlerCommandDir;
                handlerConfig = cfg.periodicDrain.handlerConfig;
                handlerCcpoolPool = cfg.periodicDrain.handlerCcpoolPool;
              };
            };
          };

          pg-router-daemon = lib.mkIf cfg.daemon.enable {
            Unit.Description = "pg-router run: long-running core (discover/dispatch on a fixed poll interval)";
            Install.WantedBy = [ "default.target" ];
            Service = {
              ExecStart = "${cfg.package}/bin/pg-router run";
              Restart = "on-failure";
              Environment = mkEnvironment {
                repoRoot = cfg.daemon.repoRoot;
                beadsPrefix = cfg.daemon.beadsPrefix;
                configText = cfg.daemon.configText;
                configFileName = "pg-router-daemon-config.toml";
                operatorPausedPath = cfg.daemon.gates.operatorPausedPath;
                cicdDownPath = cfg.daemon.gates.cicdDownPath;
                handlerCommand = cfg.daemon.handlerCommand;
                handlerCommandDir = cfg.daemon.handlerCommandDir;
                handlerConfig = cfg.daemon.handlerConfig;
                handlerCcpoolPool = cfg.daemon.handlerCcpoolPool;
                metricsAddr = cfg.daemon.metricsAddr;
              };
            };
          };
        };

        timers.pg-router-drain = lib.mkIf cfg.periodicDrain.enable {
          Unit.Description = "Run pg-router run-until-idle periodically";
          Install.WantedBy = [ "timers.target" ];
          Timer = {
            OnUnitActiveSec = cfg.periodicDrain.interval;
            OnBootSec = cfg.periodicDrain.interval;
            Persistent = true;
          };
        };
      };
    };
  };
}
