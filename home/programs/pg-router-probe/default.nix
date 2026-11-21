{
  config,
  lib,
  pkgs,
  options,
  ...
}:

let
  cfg = config.phillipgreenii.programs.pg-router-probe;
  ls = cfg.landerStale;
  isDarwin = pkgs.stdenv.hostPlatform.isDarwin;

  # Read from the `options` argument, never from `config`/`pkgs` (using those to
  # choose attribute NAMES is infinite recursion; see home/programs/pg-gwm).
  hasLaunchdRegistry = options.phillipgreenii.programs ? launchdServices;
in
{
  options.phillipgreenii.programs.pg-router-probe = {
    enable = lib.mkEnableOption ''
      pg-router-probe (a deterministic health probe over pg-router's own
      operational health: Grafana firing-alert check and daemon/handler
      binary hash sanity check (the queue/backlog reading is recorded in the
      snapshot only; it escalates nothing) -- files/updates an
      `escalated`-labeled bd issue via pg-connector on a real finding, no
      LLM involved).

      This module only puts the binary on PATH -- it exposes no
      config.yaml-style settings of its own, since every `run` invocation
      is fully parameterized by its own CLI flags (--grafana-url,
      --queue-depth/--backlog, --binary-path, --snapshot-path, ...).
      Wiring THIS role's own scheduling (the pg-router [[query]]/[[role]]
      TOML and the concrete flag values, including --connector-backend naming
      the registered pg-connector instance that serves the target tracker) is
      a separate,
      phillipg-nix-ziprecruiter-repo sibling packet's job, not this
      module's.

      Runtime-depends on `pg-connector` being on PATH
      (`cmd/pg-router-probe/connector.go`).
    '';
    package = lib.mkPackageOption pkgs "pg-router-probe" { };

    landerStale = {
      enable = lib.mkEnableOption ''
        a scheduled `pg-router-probe lander-stale` run (launchd user agent
        `com.phillipg.pg-router-lander-stale`, darwin only; it needs
        `phillipgreenii.programs.launchdServices`, which
        phillipgreenii-nix-personal's home module declares, and is skipped
        without it). It runs `pg-git-lander status --json --stale-after
        <threshold>` in each clone of `repos` and files or refreshes ONE
        `escalated` bead per stopped land operation older than the threshold,
        carrying its resume commands; a stopped operation whose owner died is
        otherwise never noticed. The bead tracker is chosen by
        `connectorBackend`'';
      repos = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        example = [ "/src/example-repo" ];
        description = "Absolute paths of the canonical clones to check.";
      };
      threshold = lib.mkOption {
        type = lib.types.str;
        default = "2h";
        description = "A stopped operation older than this (a Go duration) is stale (`--stale-after`).";
      };
      intervalSeconds = lib.mkOption {
        type = lib.types.ints.positive;
        default = 1800;
        description = "How often launchd runs the check (StartInterval).";
      };
      connectorBackend = lib.mkOption {
        type = lib.types.str;
        default = "pg-connector-issue-beads";
        description = "The registered pg-connector instance serving the tracker the alert beads go to (`--connector-backend`).";
      };
      dedupQuery = lib.mkOption {
        type = lib.types.str;
        default = "escalated-all";
        description = "The named pg-connector query listing every non-closed escalated bead (`--dedup-query`).";
      };
      landerPackage = lib.mkPackageOption pkgs "pg-git-lander" { };
    };
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      home.packages = [ cfg.package ];
      assertions = [
        {
          assertion = !ls.enable || ls.repos != [ ];
          message = "phillipgreenii.programs.pg-router-probe.landerStale.enable requires at least one entry in landerStale.repos";
        }
      ];
    })

    # Gated on the option's DECLARATION (optionalAttrs), never isDarwin alone: a
    # definition of an undeclared option errors even under mkIf false.
    (lib.optionalAttrs hasLaunchdRegistry {
      phillipgreenii.programs.launchdServices.userAgents.pg-router-lander-stale =
        lib.mkIf (cfg.enable && ls.enable && isDarwin)
          {
            label = "com.phillipg.pg-router-lander-stale";
            script = ''
              exec ${
                lib.escapeShellArgs (
                  [
                    "${cfg.package}/bin/pg-router-probe"
                    "lander-stale"
                    "--pg-git-lander-path"
                    "${ls.landerPackage}/bin/pg-git-lander"
                    "--stale-after"
                    ls.threshold
                    "--connector-backend"
                    ls.connectorBackend
                    "--dedup-query"
                    ls.dedupQuery
                  ]
                  ++ lib.concatMap (r: [
                    "--repo"
                    r
                  ]) ls.repos
                )
              }
            '';
            runAtLoad = false;
            keepAlive = false;
            healthCheck = false;
            serviceConfig = {
              StartInterval = ls.intervalSeconds;
              Nice = 10;
              ProcessType = "Background";
              EnvironmentVariables = {
                HOME = config.home.homeDirectory;
                XDG_CONFIG_HOME = config.xdg.configHome;
                XDG_STATE_HOME = config.xdg.stateHome;
                PATH = lib.concatStringsSep ":" [
                  "${config.home.profileDirectory}/bin"
                  "/etc/profiles/per-user/${config.home.username}/bin"
                  "/run/current-system/sw/bin"
                  "/nix/var/nix/profiles/default/bin"
                  "/usr/bin"
                  "/bin"
                ];
              };
            };
          };
    })
  ];
}
