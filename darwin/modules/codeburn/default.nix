{
  config,
  lib,
  ...
}:
let
  # Read each HM user's codeburn config; select users who enabled the web surface. Mirrors the
  # ollama darwin module's filter+head idiom (we need the enabled user's package + port, not just
  # a boolean). The single user agent runs under the primary user.
  hmUsers = config.home-manager.users or { };
  enabledCfgs = lib.filter (c: (c.enable or false) && (c.web.enable or false)) (
    map (u: u.phillipgreenii.programs.codeburn or { }) (lib.attrValues hmUsers)
  );
  webEnabled = enabledCfgs != [ ];
  cbCfg = if webEnabled then lib.head enabledCfgs else { };

  primaryUser = config.system.primaryUser or null;
in
{
  # Runs the codeburn web dashboard (`codeburn web`) as a launchd user agent so its localhost
  # server is always up — a consuming machine can then reverse-proxy/link it (the port is
  # cbCfg.web.port). Registered through the canonical launchdServices helper (ADR 0049): the
  # option is DECLARED in phillipgreenii-nix-personal; here we only SET it, so this relies on the
  # consuming machine also importing that flake's darwinModules. Never write launchd.user.agents
  # directly.
  config = lib.mkIf (webEnabled && primaryUser != null) {
    phillipgreenii.system.launchdServices.userAgents.codeburn = {
      label = "phillipgreenii.codeburn";
      script = ''
        exec ${cbCfg.package}/bin/codeburn web --port ${toString cbCfg.web.port} --no-open "$@"
      '';
      keepAlive = true;
      runAtLoad = true;
      # Trial safety: a wobbly web server (malformed user settings, a slow cold-cache pricing
      # fetch, etc.) MUST NOT be able to fail `darwin-rebuild switch`. Read-only config is
      # verified not to crash codeburn, so this is belt-and-suspenders — flip it on once the
      # trial proves stable if you want activation to gate on the server being up.
      healthCheck = false;
      # Working signal: `phillipgreenii-nix-support-apps` ADR 0041, category (d) recorded
      # exemption. No metricsTargets, functional probe, or logSources errorAlert is registered
      # for codeburn web — for now that's acceptable because the trial is a single local web
      # dashboard with no other consumers, so a silent failure has low consequence. Revisit with
      # category (a) (metrics) or (c) (log-based alert on the plain-text logs below) on the same
      # trigger as the healthCheck comment above: once the trial graduates to permanent.
      serviceConfig = {
        StandardOutPath = "/Users/${primaryUser}/Library/Logs/codeburn-web.out.log";
        StandardErrorPath = "/Users/${primaryUser}/Library/Logs/codeburn-web.err.log";
      };
    };
  };
}
