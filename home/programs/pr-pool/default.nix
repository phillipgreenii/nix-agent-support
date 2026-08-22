{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pr-pool;
in
{
  options.phillipgreenii.programs.pr-pool = {
    enable = lib.mkEnableOption ''
      pr-pool (PR-feedback orchestrator: a `pr-pool drain` pass discovers ready
      beads and dispatches feedback-processor / worker sessions via ccpool).
      Runtime-depends on `ccpool` and `bd` being on PATH.
    '';
    package = lib.mkPackageOption pkgs "pr-pool" { };

    periodicDrain = {
      enable = lib.mkEnableOption ''
        a systemd --user timer that runs `pr-pool drain` periodically, rather
        than a one-off manual invocation or pr-pool's long-running daemon mode
        (not reachable via any CLI subcommand as of this option's
        introduction). Each fire is one drain-and-exit pass: discover ready
        work, dispatch roles up to their configured cap, wait for completion,
        exit.
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
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    systemd.user.services.pr-pool-drain = lib.mkIf cfg.periodicDrain.enable {
      Unit.Description = "pr-pool drain: one discover -> dispatch -> wait pass";
      Service = {
        Type = "oneshot";
        ExecStart = "${cfg.package}/bin/pr-pool drain";
        Environment = [
          "PR_POOL_REPO_ROOT=${cfg.periodicDrain.repoRoot}"
        ]
        ++ lib.optional (
          cfg.periodicDrain.beadsPrefix != null
        ) "PR_POOL_BEADS_PREFIX=${cfg.periodicDrain.beadsPrefix}"
        ++ [
          "PR_POOL_CONFIG=${pkgs.writeText "pr-pool-drain-config.toml" cfg.periodicDrain.configText}"
        ];
      };
    };

    systemd.user.timers.pr-pool-drain = lib.mkIf cfg.periodicDrain.enable {
      Unit.Description = "Run pr-pool drain periodically";
      Install.WantedBy = [ "timers.target" ];
      Timer = {
        OnUnitActiveSec = cfg.periodicDrain.interval;
        OnBootSec = cfg.periodicDrain.interval;
        Persistent = true;
      };
    };
  };
}
