{
  config,
  lib,
  pkgs,
  ...
}:
let
  # The ollama LaunchAgent is registered at darwin/system scope because the
  # canonical helper `phillipgreenii.system.launchdServices.userAgents` is
  # declared there (phillipgreenii-nix-personal lib/options/launchd-services.nix),
  # not at HM scope. Per ADR 0049 every auto-started LaunchAgent MUST go through
  # that helper for stable-path indirection, PG_LAUNCHD_WRAPPER restart-on-change,
  # and the activation health check. The user-scope option API and on-PATH
  # package stay in the parallel HM module (home/programs/ollama); this module
  # reads that module's options across config.home-manager.users.<u> (same
  # pattern as pa-monitor / ccpool).
  hmUsers = config.home-manager.users or { };

  # Per-user ollama configs that opted in. The LaunchAgent runs once under the
  # primary user (gui/<primary-uid>/<label>), so when multiple users enable it
  # we take the first enabled user's settings — mirrors ccpool's "first user
  # that set one" selection for per-user values.
  enabledOllamaCfgs = lib.filter (o: o.enable or false) (
    map (u: u.phillipgreenii.programs.ollama or { }) (lib.attrValues hmUsers)
  );

  ollamaEnabled = enabledOllamaCfgs != [ ];
  ollamaCfg = if ollamaEnabled then lib.head enabledOllamaCfgs else { };

  # Rebuild the same wrapper the HM module used to embed, from the selected
  # user's package. Its body execs the real `ollama serve` + first-run model
  # pull (see home/programs/ollama/wrapper.nix).
  wrapper = lib.optionalAttrs ollamaEnabled (
    import ../../../home/programs/ollama/wrapper.nix {
      inherit pkgs lib;
      ollamaPackage = ollamaCfg.package;
    }
  );

  primaryUser = config.system.primaryUser or null;
in
{
  # LaunchAgent registration via the canonical helper (ADR 0049, amended by
  # 0051). The wrapper lands at
  # /nix/var/nix/profiles/system/sw/libexec/pg-launchd/ollama (stable path,
  # off the user PATH, GC-rooted via the system profile — so this daemon
  # wrapper no longer shadows the real `ollama` CLI); the helper embeds
  # PG_LAUNCHD_WRAPPER = wrapper.outPath
  # so plist-hash compares trigger nix-darwin to bootout+bootstrap whenever the
  # ollama package (and thus the wrapper) changes.
  #
  # Behavior is preserved 1:1 from the prior `launchd.agents.ollama` HM block:
  #   - ProgramArguments = [ <stable wrapper> ] ++ loadModels  (extraArgs)
  #   - KeepAlive = true, RunAtLoad = true
  #   - EnvironmentVariables = { OLLAMA_HOST = host:port; } // extraEnv
  #   - ProcessType + StandardOut/ErrPath under ~/Library/Logs
  config = lib.mkIf (ollamaEnabled && primaryUser != null) {
    phillipgreenii.system.launchdServices.userAgents.ollama = {
      label = "phillipgreenii.ollama";
      # The helper's `script` becomes writeShellScriptBin "ollama"; it must exec
      # the real wrapper binary (pgii-ollama-server) and forward loadModels,
      # which the helper appends as extraArgs → ProgramArguments.
      script = ''
        exec ${wrapper}/bin/pgii-ollama-server "$@"
      '';
      extraArgs = ollamaCfg.loadModels;
      keepAlive = true;
      runAtLoad = true;
      serviceConfig = {
        ProcessType = ollamaCfg.processType;
        EnvironmentVariables = {
          OLLAMA_HOST = "${ollamaCfg.host}:${toString ollamaCfg.port}";
        }
        // ollamaCfg.extraEnv;
        StandardOutPath = "/Users/${primaryUser}/Library/Logs/ollama.out.log";
        StandardErrorPath = "/Users/${primaryUser}/Library/Logs/ollama.err.log";
      };
    };
  };
}
