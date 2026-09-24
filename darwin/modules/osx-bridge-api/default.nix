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

  # phillipgreenii.observability is declared at darwin/system scope in
  # phillipgreenii-nix-support-apps (darwin/modules/observability/
  # registration.nix); this repo does not declare that flake as an input, so
  # the option only exists once a consuming machine flake imports both --
  # same defensive pattern as darwin/modules/pa-monitor's/pg-router's own
  # `obs` binding.
  obs = config.phillipgreenii.observability;
in
{
  # LaunchAgent registration via the canonical helper (`phillipgreenii-nix-personal`
  # ADR 0049, amended by 0051 and 0054), mirroring darwin/modules/pa-monitor/default.nix
  # and darwin/modules/pg-router/default.nix: this is the ONLY deployment path
  # osx-bridge-api has (design: "Deployment: a
  # phillipgreenii.system.launchdServices.userAgents entry (gui/<uid>
  # domain, KeepAlive), never a bespoke launchd wiring" — and "Why a shared
  # daemon, not per-backend direct access": a process launched directly by
  # launchd, with no GUI-terminal parent, is its own TCC "responsible"
  # identity, unlike a process running inside this workspace's interactive
  # terminal).
  #
  # Uses execPath (`phillipgreenii-nix-personal` ADR 0054), not script: a
  # personal-information TCC consent prompt attributes to ProgramArguments[0]'s
  # FIRST loaded Mach-O image, and a shell-script wrapper's first image is the
  # shell interpreter, not this daemon (confirmed live, pg2-xacj6 — the prompt
  # displayed "bash"). execPath resolves the stable wrapper path as a plain
  # symlink straight to the real binary instead, so the correct binary gets the
  # correct TCC identity. The env var this daemon previously injected via shell
  # `export` moves to serviceConfig.EnvironmentVariables, the native plist
  # mechanism — execPath has no shell to run an `export` in.
  #
  # AC #2-4 (a real TCC consent prompt firing on first run against THIS
  # module's own LaunchAgent, matching Calendar.app's data, reproducing
  # from a clean rebuild, and surviving `launchctl kickstart`) are exactly
  # what this registration makes possible to verify live — none of it can
  # be exercised or faked from this Nix module or from an automated test;
  # it requires a human applying this configuration on a real machine.
  config = lib.mkMerge [
    (lib.mkIf daemonEnabledByAnyUser {
      phillipgreenii.system.launchdServices.userAgents.osx-bridge-api-daemon = {
        label = "com.phillipg.osx-bridge-api-daemon";
        execPath = "${pkg}/bin/osx-bridge-api";
        runAtLoad = true;
        keepAlive = true;
        serviceConfig = {
          StandardErrorPath = "${stateHome}/osx-bridge-api/launchd-stderr.log";
          StandardOutPath = "${stateHome}/osx-bridge-api/launchd-stdout.log";
          EnvironmentVariables = {
            OSX_BRIDGE_API_SOCKET = socketPath;
          };
        };
      };
    })

    # phillipgreenii.observability.logSources is declared at darwin/system scope
    # in phillipgreenii-nix-support-apps, so this lives in darwin, not the
    # home-manager module -- same reasoning as ccpool's/pg-router's own
    # logSources registration. Guarded on obs.enable so it is a no-op on
    # machines without the stack.
    #
    # osx-bridge-api has no OTel/JSONL logging at all (cmd/osx-bridge-api/
    # main.go logs entirely via plain fmt.Println/fmt.Fprintf to stdout/
    # stderr -- "osx-bridge-api: listening on %s", "calendar provider
    # unavailable: %v", etc.), so the launchd-captured StandardOutPath/
    # StandardErrorPath ARE this daemon's real (and only) log stream, unlike
    # pa-monitor-daemon's own launchd captures (which are secondary to a
    # direct OTLP push and got logCollection.enable = false instead). format
    # = "raw": plain text, not the ADR 0038 JSONL contract.
    (lib.mkIf (daemonEnabledByAnyUser && (obs.enable or false)) {
      phillipgreenii.observability.logSources.osx-bridge-api-daemon = {
        path = "${stateHome}/osx-bridge-api/launchd-*.log";
        format = "raw";
      };
    })
  ];
}
