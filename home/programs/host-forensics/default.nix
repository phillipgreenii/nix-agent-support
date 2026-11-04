{
  config,
  lib,
  pkgs,
  options,
  ...
}:

# host-forensics: retain the crash evidence that rotates away before anyone looks.
# A launchd user agent (the tmp-sweep pattern: an HM-scoped agent registered through
# `phillipgreenii.programs.launchdServices.userAgents`, whose option personal's home
# module declares and is therefore read from `options`) runs `host-forensics run` at
# load (a login after a boot) and daily at 03:00:
#   - copies any new /Library/Logs/DiagnosticReports/*.panic into
#     $XDG_STATE_HOME/host-forensics/panics (never overwritten, never pruned);
#   - appends one line of free disk, swap, load and wired memory per day to
#     $XDG_STATE_HOME/host-forensics/snapshots.tsv, which outlives the metrics store.
# Enabling it on a host is a host decision; this module does nothing until a host does.

let
  cfg = config.phillipgreenii.programs.host-forensics;
  isDarwin = pkgs.stdenv.hostPlatform.isDarwin;
  hasLaunchdRegistry = options.phillipgreenii.programs ? launchdServices;
  logDir = "${config.xdg.stateHome}/host-forensics-launchd";
  script = pkgs.writeShellScript "host-forensics" (builtins.readFile ./host-forensics.bash);
in
{
  options.phillipgreenii.programs.host-forensics = {
    enable = lib.mkEnableOption ''
      host-forensics: a launchd user agent that copies new kernel panic reports into
      a retained directory under the XDG state home and records a daily snapshot of
      free disk, swap, load and wired memory. Needs phillipgreenii-nix-personal's HM
      `launchdServices` registry on darwin
    '';

    script = lib.mkOption {
      type = lib.types.path;
      internal = true;
      readOnly = true;
      default = script;
      description = "The store path of the host-forensics script the agent runs. Read-only; used by tests.";
    };
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      assertions = [
        {
          assertion = !isDarwin || hasLaunchdRegistry;
          message = "phillipgreenii.programs.host-forensics.enable on darwin needs phillipgreenii.programs.launchdServices, which phillipgreenii-nix-personal's home module declares (personal ADR 0055); import it alongside this module.";
        }
      ];
    })

    (lib.optionalAttrs hasLaunchdRegistry {
      phillipgreenii.programs.launchdServices.userAgents.host-forensics =
        lib.mkIf (cfg.enable && isDarwin)
          {
            label = "com.phillipg.host-forensics";
            script = ''
              exec ${script} run
            '';
            # A daily job that exits between runs; runAtLoad also covers the first login after a boot.
            runAtLoad = true;
            keepAlive = false;
            healthCheck = false;
            serviceConfig = {
              StartCalendarInterval = [
                {
                  Hour = 3;
                  Minute = 0;
                }
              ];
              StandardOutPath = "${logDir}/host-forensics.out.log";
              StandardErrorPath = "${logDir}/host-forensics.err.log";
              EnvironmentVariables = {
                HOME = config.home.homeDirectory;
                XDG_STATE_HOME = config.xdg.stateHome;
              };
            };
          };
    })
  ];
}
