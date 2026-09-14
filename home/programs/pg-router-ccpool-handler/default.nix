{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pg-router-ccpool-handler;

  # mkRegisterExec renders the `register` invocation shared by periodicDrain's
  # timer-triggered heartbeat and daemon's boot-time announcement below --
  # this module's ONLY systemd-facing action (Task 5.12; `phillipgreenii-nix-
  # agent-support` ADR 0065's "Operator surface" section: "wiring a
  # systemd(user) service that registers the handler binary with a running
  # pg-router core"). `dispatch`/`query`/`postStartup`/`preShutdown` are NOT
  # invoked by this unit: per cmd/pg-router-ccpool-handler/register.go's own
  # doc comments, every caller of THOSE subcommands is a core-issued spawn
  # (an "executable participant", docs/behavior/interfaces.md's Lifecycle
  # section) -- wiring pg-router's own core to actually spawn them is docket
  # pg2-oju6w's Task 5.4, and remains an accepted, explicitly out-of-scope
  # gap as of this task (ADR 0065's Addendum, "Known, explicitly
  # out-of-scope gap": `Orchestrator.Handler` is never populated with a real
  # `wireclient.Client` in production `cmd/pg-router` code today).
  mkRegisterExec =
    {
      socket,
      token,
      id,
      self,
    }:
    lib.concatStringsSep " " (
      [
        "${cfg.package}/bin/pg-router-ccpool-handler"
        "register"
        "--socket"
        socket
        "--id"
        id
        "--self"
        self
      ]
      ++ lib.optionals (token != null) [
        "--token"
        token
      ]
    );

  registerOptions = {
    socket = lib.mkOption {
      type = lib.types.str;
      description = ''
        Path to pg-router's core unix-domain socket (`--socket`, or
        `PG_ROUTER_SOCKET` if this were left to env-var resolution instead --
        this module always passes it explicitly). This is the SAME socket
        `home/programs/pg-router`'s `periodicDrain`/`daemon` core listens on
        (`internal/core.SocketPath(LogDir)`); it lives under whatever
        `PG_ROUTER_LOG_DIR` that core deployment uses. No default --
        deployment-specific.
      '';
    };
    token = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        Auth token for pg-router's core socket (`--token`). `null` omits
        `--token` entirely. The core mints this token itself at `Listen`
        time (`internal/core.Service.Ref`) and, per DEC-WIRE-2, is meant to
        hand it to a participant via a core-issued callback command with the
        address/token already baked in -- that callback wiring is Task 5.4's
        own not-yet-real production gap (see the module-level doc comment
        above), so today a deployment supplying a real, live token here MUST
        source it out-of-band from wherever the running core's own token
        currently is.
      '';
    };
    id = lib.mkOption {
      type = lib.types.str;
      description = ''
        This participant's own chosen registration id (`--id`) --
        `interfaces.md`'s Lifecycle section: "the participant names its own
        chosen id". No default -- deployment-specific.
      '';
    };
    self = lib.mkOption {
      type = lib.types.enum [
        "healthy"
        "degraded"
        "unavailable"
      ];
      default = "healthy";
      description = "This participant's initial self-reported health (`--self`).";
    };
  };
in
{
  options.phillipgreenii.programs.pg-router-ccpool-handler = {
    enable = lib.mkEnableOption ''
      pg-router-ccpool-handler (pg-router's ccpool/command participant:
      realizes INTF-HANDLER for the ccpool-backed and command-backed role
      kinds, and INTF-SOURCE for the beads-backed pull query). See
      `periodicDrain` (timer-driven re-registration) and `daemon`
      (boot-time registration) below for turnkey systemd deployment of the
      `register` step against a running pg-router core -- the two are
      mutually exclusive.
      Runtime-depends on `ccpool`, `bd`, and `git` being on PATH
      (`internal/ccpool/cli.go`, `internal/beads/runner.go`,
      `internal/gitenv/gitenv.go`).
    '';
    package = lib.mkPackageOption pkgs "pg-router-ccpool-handler" { };

    periodicDrain = {
      enable = lib.mkEnableOption ''
        a systemd --user timer that periodically re-runs
        `pg-router-ccpool-handler register` against a running pg-router
        core, rather than a single boot-time announcement (see
        `daemon.enable`). Mutually exclusive with `daemon.enable`. Mirrors
        `home/programs/pg-router`'s own `periodicDrain` naming (a repeated,
        timer-triggered one-shot pass) -- here, one re-registration pass
        rather than one discover/dispatch pass, since this binary has no
        long-running `run` equivalent of its own (see the module-level doc
        comment on `mkRegisterExec` above for why).
      '';
      interval = lib.mkOption {
        type = lib.types.str;
        default = "5m";
        description = "Systemd time-span between re-registration passes (OnUnitActiveSec/OnBootSec).";
      };
    }
    // registerOptions;

    daemon = {
      enable = lib.mkEnableOption ''
        a systemd --user service that runs `pg-router-ccpool-handler
        register` once at boot and marks itself active (`RemainAfterExit`)
        rather than the timer-driven repeated form (see
        `periodicDrain.enable`). Mutually exclusive with
        `periodicDrain.enable`. On darwin,
        `darwin/modules/pg-router-ccpool-handler/default.nix` mirrors this
        into a LaunchAgent (this HM systemd unit alone is a darwin no-op),
        matching `darwin/modules/pg-router`'s own `daemon` LaunchAgent
        mirror.
      '';
    }
    // registerOptions;
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    assertions = [
      {
        assertion = !(cfg.periodicDrain.enable && cfg.daemon.enable);
        message = ''
          phillipgreenii.programs.pg-router-ccpool-handler: periodicDrain.enable
          and daemon.enable cannot both be true -- pick one registration
          strategy per deployment.
        '';
      }
      {
        # Mirrors home/programs/pg-router's own tc-24qs assertion (its
        # "Runtime-depends on `ccpool` ... being on PATH" enable-doc line was
        # never enforced there either, for the SAME underlying dependency --
        # see that module's own comment) -- applied HERE directly rather
        # than by relocation, because this module is the one that actually
        # shells out to `ccpool` now, since Task 5.2/5.3 physically moved
        # internal/ccpool into this package. Without
        # phillipgreenii.programs.ccpool.enable, the ccpool-plugin's
        # SessionStart hook never registers and a ccpool-kind dispatch would
        # silently hang for its full Wait timeout (tc-24qs).
        assertion = config.phillipgreenii.programs.ccpool.enable;
        message = ''
          phillipgreenii.programs.pg-router-ccpool-handler.enable requires
          phillipgreenii.programs.ccpool.enable = true -- this module's
          ccpool role kind shells out to the `ccpool` binary on PATH
          (internal/ccpool/cli.go), and only the ccpool module renders
          claude.plugin_dir into ccpool's own config.toml (tc-24qs).
        '';
      }
    ];

    systemd.user = {
      services = {
        pg-router-ccpool-handler-drain = lib.mkIf cfg.periodicDrain.enable {
          Unit.Description = "pg-router-ccpool-handler: one register pass against a running pg-router core";
          Service = {
            Type = "oneshot";
            ExecStart = mkRegisterExec {
              socket = cfg.periodicDrain.socket;
              token = cfg.periodicDrain.token;
              id = cfg.periodicDrain.id;
              self = cfg.periodicDrain.self;
            };
          };
        };

        pg-router-ccpool-handler-daemon = lib.mkIf cfg.daemon.enable {
          Unit.Description = "pg-router-ccpool-handler: boot-time registration against a running pg-router core";
          Install.WantedBy = [ "default.target" ];
          Service = {
            Type = "oneshot";
            RemainAfterExit = true;
            ExecStart = mkRegisterExec {
              socket = cfg.daemon.socket;
              token = cfg.daemon.token;
              id = cfg.daemon.id;
              self = cfg.daemon.self;
            };
          };
        };
      };

      timers.pg-router-ccpool-handler-drain = lib.mkIf cfg.periodicDrain.enable {
        Unit.Description = "Run pg-router-ccpool-handler register periodically";
        Install.WantedBy = [ "timers.target" ];
        Timer = {
          OnUnitActiveSec = cfg.periodicDrain.interval;
          OnBootSec = cfg.periodicDrain.interval;
          Persistent = true;
        };
      };
    };
  };
}
