{
  config,
  lib,
  pkgs,
  ...
}:
let
  hmUsers = config.home-manager.users or { };
  reapEnabledByAnyUser = lib.any (u: u.phillipgreenii.programs.ccpool.reap.enable or false) (
    lib.attrValues hmUsers
  );

  # Pick the interval from the first user that set one (default 300).
  interval =
    let
      vals = lib.filter (v: v != null) (
        map (u: u.phillipgreenii.programs.ccpool.reap.intervalSeconds or null) (lib.attrValues hmUsers)
      );
    in
    if vals == [ ] then 300 else lib.head vals;

  pkg = pkgs.ccpool;

  # XDG_STATE_HOME default for the reap agent's launchd logs. The plist runs
  # under the primary user; mirror pa-monitor's resolution via system.primaryUser.
  primaryUser = config.system.primaryUser or null;
  stateHome = if primaryUser != null then "/Users/${primaryUser}/.local/state" else "/tmp/ccpool";

  obs = config.phillipgreenii.observability;

  # OTel emitter env for ccpool-reap (design D10): resolved here (darwin
  # scope, where the observability surface — declared in
  # phillipgreenii-nix-support-apps — lives) and merged into the
  # LaunchAgent's EnvironmentVariables, mirroring pa-monitor's/pg-router's/
  # pg-desk-serve's own obs.mkEmitterEnv call pattern exactly. ccpool-reap is
  # the ONE ccpool entry point that is its own independently-scheduled
  # process, not a subprocess of something already wired — it deliberately
  # does not (and structurally cannot) reach the dispatch-invocation path:
  # when pg-router-ccpool-handler execs ccpool as pg-router's own subprocess,
  # the endpoint/protocol vars are inherited for free from pg-router's
  # already-wired LaunchAgent environment (D10). Plain human-interactive
  # shell invocations get OTel wiring only if the human's own shell exports
  # these vars, which is out of scope (D10). Guarded defensively
  # (`obs ? mkEmitterEnv`) the same way every other consumer of this helper
  # is: this repo does not declare phillipgreenii-nix-support-apps as a
  # flake input, so the option only exists once a consuming machine flake
  # imports both.
  emitterEnv =
    if obs ? mkEmitterEnv then
      obs.mkEmitterEnv {
        serviceName = "ccpool";
        protocol = "grpc";
      }
    else
      { };
in
{
  config = lib.mkMerge [
    # phillipgreenii.observability.logSources is declared at darwin/system scope
    # in phillipgreenii-nix-support-apps (darwin/modules/observability/
    # registration.nix), so this lives in darwin, not the home-manager module —
    # setting it from HM targets an undeclared option and fails eval (same
    # reasoning as pg-router's registration). A cross-flake stub in that flake's
    # flake.nix lets it type-check standalone in agent-support CI (pg2-45ab.3).
    #
    # ccpool writes its structured diagnostic log to
    # ${XDG_STATE_HOME}/ccpool/diagnostics.jsonl (time/level/msg JSONL; see
    # internal/diaglog). The default glob ${env:XDG_STATE_HOME}/ccpool/*.jsonl
    # would ALSO match events.jsonl (the DOMAIN event log, no `level` field), so
    # `path` is pinned to the exact diagnostics file to keep that domain log out
    # of the diagnostics->severity pipeline. Guarded on obs.enable so it is a
    # no-op on machines without the stack.
    (lib.mkIf (obs.enable or false) {
      phillipgreenii.observability.logSources.ccpool.path =
        "\${env:XDG_STATE_HOME}/ccpool/diagnostics.jsonl";
    })

    (lib.mkIf reapEnabledByAnyUser {
      phillipgreenii.system.launchdServices.userAgents.ccpool-reap = {
        label = "com.phillipg.ccpool-reap";
        script = ''
          exec ${pkg}/bin/ccpool reap-all
        '';
        runAtLoad = true;
        # `ccpool reap-all` is a periodic short task (StartInterval), not a long-running
        # daemon — it does its work and exits. keepAlive defaults to true in the
        # helper, which would make launchd RESTART it on every exit (a ~10s respawn
        # loop). Disable keepAlive so StartInterval is the only re-trigger (runs at
        # load, then every `interval` seconds), and exempt it from the health check
        # (which expects state=running, which a one-shot never reaches).
        keepAlive = false;
        healthCheck = false;
        serviceConfig = {
          StartInterval = interval; # the periodic re-trigger
          # Surface runtime failures: the agent is keepAlive-off and health-check
          # exempt, so without logs a crashing reap run would be silent. launchd
          # creates the parent dir (~/.local/state/ccpool) if it is missing.
          # Mirrors pa-monitor's stateHome logging pattern.
          #
          # NOTE: these reap stdout/stderr paths are DELIBERATELY their own files
          # (reap.err.log / reap.out.log), NOT the tailed diagnostics.jsonl — the
          # JSONL log is ccpool's structured diagnostics, not launchd's raw
          # process output (pg2-yvnp AC: reap StandardOutPath/StandardErrorPath
          # stay on their own non-tailed paths).
          StandardErrorPath = "${stateHome}/ccpool/reap.err.log";
          StandardOutPath = "${stateHome}/ccpool/reap.out.log";
          # OTel OTLP log-export env (D10) — empty when obs.enable is false
          # or the helper is absent (emitterEnv's own guard above), so this
          # is a no-op merge on a machine without the observability stack.
          EnvironmentVariables = emitterEnv;
        };
      };
    })
  ];
}
