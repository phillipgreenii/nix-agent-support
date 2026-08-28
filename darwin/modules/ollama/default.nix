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

  # State dir for the functional-probe agent below, mirroring pa-monitor's/
  # ccpool's primaryUser-derived stateHome (the plist's StandardOut/ErrPath
  # need a nix-resolved absolute path; a bare $XDG_STATE_HOME is only
  # expanded inside the shell script itself, not by launchd).
  stateHome = if primaryUser != null then "/Users/${primaryUser}/.local/state" else "/tmp/ollama";

  # Cadence for the functional probe (userAgents.ollama-probe below). Kept
  # here so the errorAlert tuning on the logSources registration can cite
  # the same number instead of it drifting out of sync in a comment.
  probeIntervalSeconds = 120;

  obs = config.phillipgreenii.observability;
in
{
  config = lib.mkMerge [
    (lib.mkIf (ollamaEnabled && primaryUser != null) {
      # LaunchAgent registration via the canonical helper (ADR 0049, amended by
      # 0051). The wrapper lands at
      # /nix/var/nix/profiles/system/sw/libexec/pg-launchd/ollama (stable path,
      # off the user PATH, GC-rooted via the system profile — so this daemon
      # wrapper no longer shadows the real `ollama` CLI); the helper embeds
      # PG_LAUNCHD_WRAPPER = wrapper.outPath
      # so plist-hash compares trigger nix-darwin to bootout+bootstrap whenever
      # the ollama package (and thus the wrapper) changes.
      #
      # Behavior is preserved 1:1 from the prior `launchd.agents.ollama` HM block:
      #   - ProgramArguments = [ <stable wrapper> ] ++ loadModels  (extraArgs)
      #   - KeepAlive = true, RunAtLoad = true
      #   - EnvironmentVariables = { OLLAMA_HOST = host:port; } // extraEnv
      #   - ProcessType + StandardOut/ErrPath under ~/Library/Logs
      #
      # Working signal: `phillipgreenii-nix-support-apps` ADR 0041 category
      # (b) — a functional probe. This agent only proves the process is
      # alive; the actual check against ollama's own API lives on the
      # sibling `ollama-probe` agent below.
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

      # Working signal implementation: `phillipgreenii-nix-support-apps`
      # ADR 0041 category (b) — "an active check that exercises the
      # service's real function (not just 'is the process there')". Every
      # `probeIntervalSeconds` this does a real `GET /api/tags` against
      # OLLAMA_HOST (ollama's own model-listing endpoint), recording the
      # outcome as ADR 0038 JSONL.
      #
      # Delivery deliberately reuses the (c) JSONL + generated-`errorAlert`
      # pipeline (`phillipgreenii-nix-support-apps`
      # darwin/modules/observability/logsource-health.nix) rather than
      # standing up a dedicated Prometheus exporter the way mysql-probe.nix
      # does for category (b)'s other instance: a whole custom exporter
      # binary is disproportionate for a single stateless HTTP check, and
      # the JSONL route already satisfies what ADR 0041 actually requires —
      # "an active check ... and alerts on probe failure" — without
      # inventing new delivery infrastructure. The `errorAlert` tuning below
      # is NOT that option group's generic default (10 errors/10m, sized for
      # much higher-frequency app logs); it is matched to this agent's own
      # probeIntervalSeconds so a real outage is still caught, not silently
      # under the default's noise floor.
      #
      # keepAlive = false / healthCheck = false: a periodic StartInterval
      # check, not a persistent process — same "no steady `state = running`
      # between ticks" reasoning as this repo's own ccpool-reap agent
      # (darwin/modules/ccpool/default.nix).
      phillipgreenii.system.launchdServices.userAgents.ollama-probe = {
        label = "phillipgreenii.ollama-probe";
        keepAlive = false;
        runAtLoad = true;
        healthCheck = false;
        script = ''
          set -eu
          : "''${XDG_STATE_HOME:=$HOME/.local/state}"
          state_dir="$XDG_STATE_HOME/ollama-probe"
          /bin/mkdir -p "$state_dir"
          now="$(/bin/date -u +%Y-%m-%dT%H:%M:%SZ)"
          url="http://${ollamaCfg.host}:${toString ollamaCfg.port}/api/tags"
          if ${pkgs.curl}/bin/curl --fail --silent --show-error --max-time 5 --output /dev/null "$url"; then
            ${pkgs.jq}/bin/jq -cn --arg time "$now" --arg msg "GET /api/tags ok ($url)" \
              '{time:$time, level:"info", msg:$msg, service:"ollama-probe"}' >> "$state_dir/probe.jsonl"
          else
            ${pkgs.jq}/bin/jq -cn --arg time "$now" --arg msg "GET /api/tags failed ($url)" \
              '{time:$time, level:"error", msg:$msg, service:"ollama-probe"}' >> "$state_dir/probe.jsonl"
          fi
        '';
        serviceConfig = {
          StartInterval = probeIntervalSeconds;
          StandardOutPath = "${stateHome}/ollama-probe/launchd.out.log";
          StandardErrorPath = "${stateHome}/ollama-probe/launchd.err.log";
        };
      };
    })

    # phillipgreenii.observability.logSources is declared at darwin/system
    # scope in phillipgreenii-nix-support-apps
    # (darwin/modules/observability/registration.nix), so this lives in
    # darwin, not the home-manager module — same reasoning as ccpool's/
    # pr-pool's own logSources registration. Guarded on obs.enable so it is
    # a no-op on machines without the stack.
    (lib.mkIf (ollamaEnabled && primaryUser != null && (obs.enable or false)) {
      phillipgreenii.observability.logSources.ollama-probe = {
        # See the ollama-probe agent's own comment above: tuned to
        # probeIntervalSeconds (120s), not the option group's generic
        # default. At most ~5 checks land in a 10-minute window at this
        # cadence, so the default 10-errors/10m threshold would never fire.
        # 3 failures in 10m tolerates one isolated blip (e.g. a cold-start
        # race with the main ollama agent) while still catching a sustained
        # outage well inside the incident window ADR 0041 exists to close.
        errorAlert.threshold = 3;
        errorAlert.window = "10m";
      };
    })
  ];
}
