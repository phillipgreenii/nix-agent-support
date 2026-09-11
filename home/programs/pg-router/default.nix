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
    }:
    [
      "PG_ROUTER_REPO_ROOT=${repoRoot}"
    ]
    ++ lib.optional (beadsPrefix != null) "PG_ROUTER_BEADS_PREFIX=${beadsPrefix}"
    ++ [
      "PG_ROUTER_CONFIG=${pkgs.writeText configFileName configText}"
    ]
    ++ lib.optional (operatorPausedPath != null) "PG_ROUTER_OPERATOR_PAUSED=${operatorPausedPath}"
    ++ lib.optional (cicdDownPath != null) "PG_ROUTER_CICD_DOWN=${cicdDownPath}";
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
