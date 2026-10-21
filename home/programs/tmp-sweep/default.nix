{
  config,
  lib,
  pkgs,
  options,
  ...
}:

# tmp-sweep (bead pg2-bo3n7, packet C of the build_tmp design pg2-2qirl, D10): the
# DAILY schedule that cleans the build-tmp and claude-code-tmp roots. It is a thin
# caller of the one implementation, the pg-disk-reclaimer registry items those two
# modules contribute (packet B); it adds no second script. The agent is an HM-scoped
# launchd user agent registered through
# `phillipgreenii.programs.launchdServices.userAgents` (the pg-keep-awake pattern:
# the option is declared by nix-personal's home module, so it is read from `options`,
# never from `config`, and guarded by optionalAttrs). The SYSTEM-scope option that
# ccpool-reap uses is not reachable from an HM module.
#
# The command line is built from the roots that are ENABLED, always with explicit
# ids: an id-less run would also select unrelated registry items (worktrees, ...).
#   both:        reclaim --aggressiveness 1 build-tmp claude-code-tmp --apply
#   one root:    only that root's id appears
#   neither:     no agent is rendered
# Level 1 only; level 3 stays operator-invoked (the reclaim-deep host action).
#
# Enabling a root for a host is packet D; this module does nothing until a host does.

let
  cfg = config.phillipgreenii.programs.tmp-sweep;
  buildCfg = config.phillipgreenii.programs.build-tmp;
  claudeCfg = config.phillipgreenii.programs.claude-code-tmp;
  isDarwin = pkgs.stdenv.hostPlatform.isDarwin;

  # Read from the `options` argument, never from `config`/`pkgs` (using those to
  # choose attribute NAMES is infinite recursion; see home/programs/pa-monitor).
  hasLaunchdRegistry = options.phillipgreenii.programs ? launchdServices;
  hasReclaimer = options.phillipgreenii.programs ? pg-disk-reclaimer;

  # The registry item ids of the enabled roots, in a fixed order. Each root is
  # swept only when its module is enabled; its path has no default, so it is only
  # read behind the enable flag (the module's own assertions guard its value).
  ids = lib.optional buildCfg.enable "build-tmp" ++ lib.optional claudeCfg.enable "claude-code-tmp";

  # The launchd stdout/stderr files sit under the XDG state home, in their own
  # directory (launchd creates a missing parent), like ccpool-reap.
  logDir = "${config.xdg.stateHome}/tmp-sweep";
in
{
  options.phillipgreenii.programs.tmp-sweep = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = buildCfg.enable || claudeCfg.enable;
      defaultText = lib.literalExpression "config.phillipgreenii.programs.build-tmp.enable || config.phillipgreenii.programs.claude-code-tmp.enable";
      description = ''
        Run the daily launchd sweep of the build-tmp and claude-code-tmp roots
        (aggressiveness 1, through pg-disk-reclaimer). Defaults to true when either
        root module is enabled; the agent sweeps exactly the enabled roots and is
        not rendered when neither is. Requires
        `phillipgreenii.programs.pg-disk-reclaimer.enable` (its registry holds the
        cleanup) and, on darwin, phillipgreenii-nix-personal's HM
        `launchdServices` registry.
      '';
    };

    package = lib.mkOption {
      type = lib.types.package;
      default =
        if hasReclaimer then
          config.phillipgreenii.programs.pg-disk-reclaimer.package
        else
          pkgs.pg-disk-reclaimer;
      defaultText = lib.literalExpression "config.phillipgreenii.programs.pg-disk-reclaimer.package";
      description = ''
        The pg-disk-reclaimer package whose ABSOLUTE store path the agent runs
        (launchd has no per-user profile on its PATH). Defaults to the package of
        the pg-disk-reclaimer module, so the sweep runs the same CLI that wrote
        the registry.
      '';
    };
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      assertions = [
        {
          # The cleanup lives in the registry the reclaimer module writes; without
          # it the daily job would run against a missing registry (design D14).
          assertion = hasReclaimer && config.phillipgreenii.programs.pg-disk-reclaimer.enable;
          message = "phillipgreenii.programs.tmp-sweep.enable requires phillipgreenii.programs.pg-disk-reclaimer.enable = true (and its module imported): the registry that the sweep of the build-tmp and claude-code-tmp roots runs from is only written when it is enabled.";
        }
        {
          assertion = !isDarwin || hasLaunchdRegistry || ids == [ ];
          message = "phillipgreenii.programs.tmp-sweep.enable on darwin needs phillipgreenii.programs.launchdServices, which phillipgreenii-nix-personal's home module declares (personal ADR 0055); import it alongside this module.";
        }
      ];
    })

    # LaunchAgent registration via the HM-scoped launchdServices registry, gated on
    # `enable`, the platform and at least one enabled root at ENTRY level (mkIf), and
    # on the option's declaration at attribute level (optionalAttrs), exactly as
    # home/programs/pg-keep-awake does. A non-darwin host gets no entry and no failure.
    (lib.optionalAttrs hasLaunchdRegistry {
      phillipgreenii.programs.launchdServices.userAgents.tmp-sweep =
        lib.mkIf (cfg.enable && isDarwin && ids != [ ])
          {
            label = "com.phillipg.tmp-sweep";
            # The command line is in the wrapper script, not in ProgramArguments.
            # The CLI reads the registry from
            # ${XDG_CONFIG_HOME:-$HOME/.config}/pg-disk-reclaimer/registry.json and
            # runs level 1 without a prompt (the confirmation gate is level 4).
            script = ''
              exec ${cfg.package}/bin/pg-disk-reclaimer reclaim --aggressiveness 1 ${lib.concatStringsSep " " ids} --apply
            '';
            # A daily calendar job that exits between runs. runAtLoad covers a
            # machine that was powered off at 03:30 (launchd runs a missed calendar
            # job on wake from sleep, not from power-off); the sweep is idempotent
            # and a missing root (unmounted volume) is skipped silently.
            runAtLoad = true;
            keepAlive = false;
            # Not a service that stays running: the activation health check would
            # misread the gap between runs as a failure (as for ccpool-reap).
            healthCheck = false;
            serviceConfig = {
              StartCalendarInterval = [
                {
                  Hour = 3;
                  Minute = 30;
                }
              ];
              StandardOutPath = "${logDir}/sweep.out.log";
              StandardErrorPath = "${logDir}/sweep.err.log";
              EnvironmentVariables = {
                # launchd's default environment has neither: the CLI finds the
                # registry through XDG_CONFIG_HOME (falling back to HOME).
                HOME = config.home.homeDirectory;
                XDG_CONFIG_HOME = config.xdg.configHome;
              };
            };
          };
    })
  ];
}
