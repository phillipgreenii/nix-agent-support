{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pr-pool;

  # Shared PR_POOL_* Environment-list builder for both systemd units below
  # (periodicDrain's oneshot run-until-idle pass, daemon's long-running
  # `run` core): each spawns a pr-pool process needing the same
  # REPO_ROOT/BEADS_PREFIX/CONFIG trio, plus daemon-only INV-LIFE-2
  # gate-path overrides. Kept as one `let` so the two units cannot drift on
  # how they assemble PR_POOL_CONFIG's writeText call or the optional-var
  # handling (Task 1.7).
  mkEnvironment =
    {
      repoRoot,
      beadsPrefix,
      configText,
      configFileName,
      quotaPausedPath ? null,
      cicdDownPath ? null,
    }:
    [
      "PR_POOL_REPO_ROOT=${repoRoot}"
    ]
    ++ lib.optional (beadsPrefix != null) "PR_POOL_BEADS_PREFIX=${beadsPrefix}"
    ++ [
      "PR_POOL_CONFIG=${pkgs.writeText configFileName configText}"
    ]
    ++ lib.optional (quotaPausedPath != null) "PR_POOL_QUOTA_PAUSED=${quotaPausedPath}"
    ++ lib.optional (cicdDownPath != null) "PR_POOL_CICD_DOWN=${cicdDownPath}";
in
{
  options.phillipgreenii.programs.pr-pool = {
    enable = lib.mkEnableOption ''
      pr-pool (PR-feedback orchestrator: a `pr-pool run-until-idle` pass discovers
      ready beads and dispatches feedback-processor / worker sessions via ccpool).
      See `periodicDrain` (timer-driven run-until-idle) and `daemon` (long-running
      `run`) below for
      turnkey systemd deployment — the two are mutually exclusive.
      Runtime-depends on `ccpool` and `bd` being on PATH.
    '';
    package = lib.mkPackageOption pkgs "pr-pool" { };

    periodicDrain = {
      enable = lib.mkEnableOption ''
        a systemd --user timer that runs `pr-pool run-until-idle` periodically,
        rather than a one-off manual invocation or the `daemon` submodule's
        long-running core (see `daemon.enable`). Mutually exclusive with
        `daemon.enable`: both are independent pr-pool cores and cannot share
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
          PR_POOL_REPO_ROOT: a real git repository pr-pool anchors to (it runs
          `git worktree add` there for any role using the default "worktree"
          isolation strategy, and always reads bd's connection config from
          `<repoRoot>/.beads`). No default — deployment-specific.
        '';
      };
      beadsPrefix = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          PR_POOL_BEADS_PREFIX override. `null` leaves pr-pool's own default
          (`zr`) or any externally-set env var in effect; set explicitly for a
          deployment using a different bd issue prefix.
        '';
      };
      configText = lib.mkOption {
        type = lib.types.lines;
        description = ''
          The pr-pool `config.toml` content (`[[query]]`/`[[role]]` etc.),
          rendered into the Nix store and pointed at via `PR_POOL_CONFIG` —
          fully declarative, so no machine-local `.pr-pool/config.toml`
          bootstrap step is needed.
        '';
      };
    };

    daemon = {
      enable = lib.mkEnableOption ''
        a systemd --user service that runs `pr-pool run` — the long-running
        core, producing + dispatching on a fixed poll interval until
        SIGINT/SIGTERM — rather than the timer-driven one-shot
        `periodicDrain`. Mutually exclusive with `periodicDrain.enable`: both
        are independent pr-pool cores over the same LogDir/socket/WAL
        (events.jsonl, the discovery record, the push-ingest socket), and
        running both would race on that shared state. On darwin,
        `darwin/modules/pr-pool/default.nix` mirrors this into a LaunchAgent
        (this HM systemd unit alone is a darwin no-op).
      '';
      repoRoot = lib.mkOption {
        type = lib.types.str;
        description = ''
          PR_POOL_REPO_ROOT for the daemon core — see `periodicDrain.repoRoot`
          for the full contract. No default — deployment-specific.
        '';
      };
      beadsPrefix = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "PR_POOL_BEADS_PREFIX override — see `periodicDrain.beadsPrefix`.";
      };
      configText = lib.mkOption {
        type = lib.types.lines;
        description = "The pr-pool `config.toml` content — see `periodicDrain.configText`.";
      };
      gates = {
        quotaPausedPath = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = ''
            PR_POOL_QUOTA_PAUSED override: the `quota-paused` gate file path
            (`INV-LIFE-2`). `null` leaves `Config.Load()`'s own default
            (`<PR_POOL_LOG_DIR>/gates/quota-paused`) in effect — note that
            default is now live even when unset here (Task 1.2b): a stray
            file already at that path gates a daemon that previously could
            not be gated, and gate files are never swept.
          '';
        };
        cicdDownPath = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = ''
            PR_POOL_CICD_DOWN override: the `cicd-down` gate file path
            (`INV-LIFE-2`). `null` leaves `Config.Load()`'s own default
            (`<PR_POOL_LOG_DIR>/gates/cicd-down`) in effect — same
            gates-default-on hazard as `quotaPausedPath` above.
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
          phillipgreenii.programs.pr-pool: periodicDrain.enable and
          daemon.enable cannot both be true — they are two independent
          pr-pool cores (`run-until-idle` vs `run`) over the same
          LogDir/socket/WAL (events.jsonl, the discovery record, the
          push-ingest socket), and running both would race on that shared
          state. Enable exactly one.
        '';
      }
    ];

    systemd = {
      user = {
        services = {
          pr-pool-drain = lib.mkIf cfg.periodicDrain.enable {
            Unit.Description = "pr-pool run-until-idle: one discover -> dispatch -> wait pass";
            Service = {
              Type = "oneshot";
              ExecStart = "${cfg.package}/bin/pr-pool run-until-idle";
              Environment = mkEnvironment {
                repoRoot = cfg.periodicDrain.repoRoot;
                beadsPrefix = cfg.periodicDrain.beadsPrefix;
                configText = cfg.periodicDrain.configText;
                configFileName = "pr-pool-drain-config.toml";
              };
            };
          };

          pr-pool-daemon = lib.mkIf cfg.daemon.enable {
            Unit.Description = "pr-pool run: long-running core (discover/dispatch on a fixed poll interval)";
            Install.WantedBy = [ "default.target" ];
            Service = {
              ExecStart = "${cfg.package}/bin/pr-pool run";
              Restart = "on-failure";
              Environment = mkEnvironment {
                repoRoot = cfg.daemon.repoRoot;
                beadsPrefix = cfg.daemon.beadsPrefix;
                configText = cfg.daemon.configText;
                configFileName = "pr-pool-daemon-config.toml";
                quotaPausedPath = cfg.daemon.gates.quotaPausedPath;
                cicdDownPath = cfg.daemon.gates.cicdDownPath;
              };
            };
          };
        };

        timers.pr-pool-drain = lib.mkIf cfg.periodicDrain.enable {
          Unit.Description = "Run pr-pool run-until-idle periodically";
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
