{
  config,
  lib,
  pkgs,
  ...
}:
let
  # Read osx-bridge-api.enable across all HM users; the LaunchAgent gets
  # registered once at system scope when any user opted in — mirrors
  # pa-monitor's/pg-router's own hmUsers pattern (darwin/modules/pa-monitor,
  # darwin/modules/pg-router).
  hmUsers = config.home-manager.users or { };
  daemonEnabledByAnyUser = lib.any (u: u.phillipgreenii.programs.osx-bridge-api.enable or false) (
    lib.attrValues hmUsers
  );

  pkg = pkgs.osx-bridge-api;

  # XDG_STATE_HOME default for macOS user agents. The launchdServices helper
  # plist runs under the primary user, so resolve via system.primaryUser
  # (same convention as pa-monitor's/pg-router's own darwin modules).
  primaryUser = config.system.primaryUser or null;
  stateHome =
    if primaryUser != null then "/Users/${primaryUser}/.local/state" else "/tmp/osx-bridge-api";
  socketPath = "${stateHome}/osx-bridge-api/osx-bridge-api.sock";
in
{
  # LaunchAgent registration via the canonical helper (ADR 0049, amended by
  # 0051), mirroring darwin/modules/pa-monitor/default.nix and
  # darwin/modules/pg-router/default.nix: this is the ONLY deployment path
  # osx-bridge-api has (design: "Deployment: a
  # phillipgreenii.system.launchdServices.userAgents entry (gui/<uid>
  # domain, KeepAlive), never a bespoke launchd wiring" — and "Why a shared
  # daemon, not per-backend direct access": a process launched directly by
  # launchd, with no GUI-terminal parent, is its own TCC "responsible"
  # identity, unlike a process running inside this workspace's interactive
  # terminal).
  #
  # AC #2-4 (a real TCC consent prompt firing on first run against THIS
  # module's own LaunchAgent, matching Calendar.app's data, reproducing
  # from a clean rebuild, and surviving `launchctl kickstart`) are exactly
  # what this registration makes possible to verify live — none of it can
  # be exercised or faked from this Nix module or from an automated test;
  # it requires a human applying this configuration on a real machine.
  config = lib.mkIf daemonEnabledByAnyUser {
    phillipgreenii.system.launchdServices.userAgents.osx-bridge-api-daemon = {
      label = "com.phillipg.osx-bridge-api-daemon";
      script = ''
        export OSX_BRIDGE_API_SOCKET=${lib.escapeShellArg socketPath}
        exec ${pkg}/bin/osx-bridge-api
      '';
      runAtLoad = true;
      keepAlive = true;
      serviceConfig = {
        StandardErrorPath = "${stateHome}/osx-bridge-api/launchd-stderr.log";
        StandardOutPath = "${stateHome}/osx-bridge-api/launchd-stdout.log";
      };
    };
  };
}
