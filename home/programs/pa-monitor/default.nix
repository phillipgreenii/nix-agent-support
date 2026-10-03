{
  config,
  lib,
  pkgs,
  options,
  # Set by the nix-darwin / NixOS home-manager integration; null for a
  # standalone home-manager activation (no system scope to read from).
  osConfig ? null,
  ...
}:

let
  cfg = config.phillipgreenii.programs.pa-monitor;
  tomlFormat = pkgs.formats.toml { };

  # The observability surface is declared at darwin/system scope (in
  # phillipgreenii-nix-support-apps). Read it null-safely from osConfig: absent
  # osConfig (standalone HM), absent `phillipgreenii.observability`, or an
  # observability stack without `mkEmitterEnv` all fall back to "no OTel".
  obs = if osConfig == null then { } else osConfig.phillipgreenii.observability or { };

  # OTel emitter env for the daemon, resolved from the system observability
  # stack. Only the endpoint is carried into config.toml below.
  emitterEnv =
    if obs ? mkEmitterEnv then
      obs.mkEmitterEnv {
        serviceName = "pa-monitor";
        protocol = "grpc";
      }
    else
      { };

  # OTel settings for the shared config file, derived from the same emitterEnv
  # the daemon used to receive via its plist. The ENDPOINT key is present iff
  # obs.enable, so it is the correct gate. We deliberately carry only the
  # endpoint: there is no `protocol` config field (the Go exporters are
  # gRPC-only), OTEL_SERVICE_NAME is set in Go, and no resourceAttrs are passed
  # so OTEL_RESOURCE_ATTRIBUTES is absent.
  otelSettings = lib.optionalAttrs (emitterEnv ? OTEL_EXPORTER_OTLP_ENDPOINT) {
    otel.endpoint = emitterEnv.OTEL_EXPORTER_OTLP_ENDPOINT;
  };

  # Whether the composing flake imports personal's home module, which declares
  # the HM launchdServices registry (cross-platform, ADR 0047). Read from the
  # module `options` arg, NOT from `pkgs`/`config`: both are config-derived, so
  # using them to pick which attribute NAMES this module defines is infinite
  # recursion. A definition of an undeclared option errors even under `mkIf
  # false`, and a linux host may not import personal, so the definition below is
  # only emitted when the option exists; a darwin host that lacks it gets a
  # clear assertion instead of a silent no-op.
  hasLaunchdRegistry = options.phillipgreenii.programs ? launchdServices;
  isDarwin = pkgs.stdenv.hostPlatform.isDarwin;
in
{
  options.phillipgreenii.programs.pa-monitor = {
    enable = lib.mkEnableOption "pa-monitor (per-user Claude agents daemon + TUI)";
    package = lib.mkPackageOption pkgs "pa-monitor" { };

    daemon.enable = lib.mkEnableOption ''
      pa-monitor-daemon LaunchAgent — runs the daemon continuously at
      login. Disabled by default; opt in per host.

      On darwin the LaunchAgent is registered right here, in this HM
      module, via `phillipgreenii.programs.launchdServices.userAgents`
      (personal ADR 0055), co-located with the per-user config.toml it
      reads. That registry is provided by phillipgreenii-nix-personal's
      home module, which the composing flake MUST also import. On linux
      the systemd --user unit lives in nixos/modules/pa-monitor, which
      reads this flag across `config.home-manager.users.<u>`.
    '';

    settings = lib.mkOption {
      inherit (tomlFormat) type;
      default = { };
      example = {
        otel.endpoint = "http://127.0.0.1:4317";
      };
      description = ''
        Written to `~/.config/pa-monitor/config.toml`. Keys must match
        pa-monitor's TOML schema (e.g. `otel.endpoint`,
        `otel.resource_attributes`, `plan_tier`, `[[decorator]]`). When empty,
        no file is written and pa-monitor uses its built-in defaults. The file
        is rendered whenever the program (`enable`) **or** the daemon
        (`daemon.enable`) is enabled, so a daemon-only host still gets its
        config.
      '';
    };
  };

  # Only the Grafana dashboard/alert registration lives in the parallel darwin
  # module (darwin/modules/pa-monitor), because those options are declared at
  # darwin/system scope, not HM scope. The LaunchAgent is registered below.
  config = lib.mkMerge [
    (lib.mkIf (config.phillipgreenii.programs.claude-code.enable && cfg.enable) {
      home.packages = [ cfg.package ];
    })
    # config.toml is the daemon's single source of OTel settings. Render it
    # whenever settings were supplied AND the full program OR the daemon is
    # enabled — decoupled from `claude.enable && enable` so a daemon-only host
    # (the LaunchAgent gate below is daemon.enable-only)
    # still gets its config file. Intentionally NOT coupled to home.packages:
    # the LaunchAgent runs the daemon from the Nix store path, so the binary
    # need not be on the user's PATH.
    (lib.mkIf
      (
        cfg.settings != { }
        && ((config.phillipgreenii.programs.claude-code.enable && cfg.enable) || cfg.daemon.enable)
      )
      {
        xdg.configFile."pa-monitor/config.toml".source =
          tomlFormat.generate "pa-monitor-config.toml" cfg.settings;
      }
    )
    # Feed the system observability OTel endpoint into this user's settings
    # (formerly pushed from the darwin module via home-manager.sharedModules).
    # Reads only this user's own daemon.enable and the read-only osConfig, so
    # there is no read-then-write recursion over home-manager.users.
    (lib.mkIf (cfg.daemon.enable && otelSettings != { }) {
      phillipgreenii.programs.pa-monitor.settings = otelSettings;
    })
    # LaunchAgent registration via the HM-scoped launchdServices registry
    # (personal ADR 0055, amending ADR 0049). The wrapper still lands at
    # /nix/var/nix/profiles/system/sw/libexec/pg-launchd/pa-monitor-daemon (stable
    # path, off the user PATH, GC-rooted via the system profile by the darwin
    # bridge), and the helper embeds PG_LAUNCHD_WRAPPER = wrapper.outPath so a
    # pa-monitor change restarts the agent. HM's own activation health check
    # (home.activation.launchdServicesHealthCheck) polls it after
    # setupLaunchAgents and aborts the rebuild on failure.
    #
    # Presence gate: see hasLaunchdRegistry above (optionalAttrs on the option's
    # declaration, never on config). daemon.enable and the platform gate the
    # ENTRY (config-level, so mkIf is fine). launchd.agents only emits on darwin;
    # on linux the daemon is a systemd --user unit from nixos/modules/pa-monitor.
    {
      assertions = [
        {
          assertion = !(cfg.daemon.enable && isDarwin) || hasLaunchdRegistry;
          message = "phillipgreenii.programs.pa-monitor.daemon.enable on darwin needs phillipgreenii.programs.launchdServices, which phillipgreenii-nix-personal's home module declares (personal ADR 0055); import it alongside this module.";
        }
      ];
    }
    (lib.optionalAttrs hasLaunchdRegistry {
      phillipgreenii.programs.launchdServices.userAgents.pa-monitor-daemon =
        lib.mkIf (cfg.daemon.enable && isDarwin)
          {
            label = "com.phillipg.pa-monitor-daemon";
            script = ''
              exec ${cfg.package}/bin/pa-monitor daemon "$@"
            '';
            runAtLoad = true;
            keepAlive = true;
            serviceConfig = {
              StandardErrorPath = "${config.xdg.stateHome}/pa-monitor/launchd-stderr.log";
              StandardOutPath = "${config.xdg.stateHome}/pa-monitor/launchd-stdout.log";
              # OTel is sourced from ~/.config/pa-monitor/config.toml (single source
              # of truth, written above from the system observability stack) — not
              # injected as plist env.
            };
            # logCollection.enable = false (pg2-fdtvv): ADR 0011 ("Persist transitions to a JSONL
            # file, tail with the OTel collector" was explicitly REJECTED) means this daemon's real
            # log stream (internal/otel/emitter.go's LogEvent calls — block.usage.limit_hit,
            # nudge.sent, etc.) is pushed directly over OTLP via otlploggrpc, never written to a
            # local file. There is therefore no file for a filelog-based `logSources` entry to tail;
            # the StandardOutPath/StandardErrorPath above only ever carry occasional
            # failed-to-start diagnostics (daemon.go's fmt.Fprintf(os.Stderr, ...) calls), not this
            # daemon's real signal — same "launchd's raw process output, not the app's structured
            # log" distinction ccpool-reap's own StandardOutPath/StandardErrorPath comment draws.
            logCollection.enable = false;
          };
    })
  ];
}
