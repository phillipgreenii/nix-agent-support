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

  # roleFileFor renders one role's `roles` entry into the on-disk JSON shape
  # cmd/pg-router-ccpool-handler/roleconfig.go's `loadRole` decodes
  # (`--role-config <dir>/<role.Name>.json`, this bead pg2-pteab, wiring the
  # now-landed Go-level PG_ROUTER_HANDLER_COMMAND_DIR support -- pg2-ymb3v).
  # Field names below are NOT freely chosen: `name`/`type`/the `ccpool`
  # sub-fields/the `command` sub-field are exactly `roleFile`'s own
  # `encoding/json` tags, and `isolation`'s OWN sub-fields (`Type`/`Path`) are
  # capitalized because `roles.IsolationConfig` (packages/pg-router-ccpool-
  # handler/internal/roles/roles.go) carries no json tags at all -- Go's
  # encoding/json then falls back to the literal exported field name. Only
  # the block matching `roleCfg.type` is emitted (mirroring `roleFile.CCPool`/
  # `.Command`'s own `omitempty` pointers), so a "command" role's JSON never
  # carries a stray `ccpool` key and vice versa.
  roleFileFor =
    name: roleCfg:
    pkgs.writeText "${name}.json" (
      builtins.toJSON (
        {
          inherit name;
          inherit (roleCfg) type;
        }
        // lib.optionalAttrs (roleCfg.type == "ccpool") {
          ccpool = {
            inherit (roleCfg.ccpool)
              actor
              skillMD
              completion
              onFailure
              onDispatchFail
              authorshipGuard
              promptBody
              ;
            budget = {
              inherit (roleCfg.ccpool.budget) tokens cost time;
            };
            isolation = {
              Type = roleCfg.ccpool.isolation.type;
              Path = roleCfg.ccpool.isolation.path;
            };
          };
        }
        // lib.optionalAttrs (roleCfg.type == "command") {
          command = {
            inherit (roleCfg.command) argv;
          };
        }
      )
    );

  # handlerCommandDir joins every `roles` entry's rendered JSON file into one
  # directory (`pkgs.linkFarm`), the shape `PG_ROUTER_HANDLER_COMMAND_DIR`
  # (`home/programs/pg-router`'s own `daemon`/`periodicDrain.handlerCommandDir`
  # options) expects: one `<role.Name>.json` per enabled role, resolvable by
  # `filepath.Join(cfg.HandlerCommandDir, role.Name+".json")`
  # (cmd/pg-router/run.go's `handlerCommandFor`). An empty `roles` attrset
  # still resolves cleanly to an empty directory -- never a missing/`null`
  # output -- so a deployment that leaves `roles` unset gets a harmless,
  # empty `handlerCommandDir` rather than an eval failure.
  handlerCommandDir = pkgs.linkFarm "pg-router-ccpool-handler-roles" (
    lib.mapAttrsToList (name: roleCfg: {
      name = "${name}.json";
      path = roleFileFor name roleCfg;
    }) cfg.roles
  );

  roleSubmodule = lib.types.submodule {
    options = {
      type = lib.mkOption {
        type = lib.types.enum [
          "ccpool"
          "command"
        ];
        description = ''
          This role's kind (`roleFile.Type`) -- "ccpool" dispatches a
          ccpool/claude session (`ccpool` below is required), "command" runs
          a bare command (`command` below is required).
        '';
      };
      ccpool = lib.mkOption {
        type = lib.types.nullOr (
          lib.types.submodule {
            options = {
              actor = lib.mkOption {
                type = lib.types.str;
                description = "ccpool `--actor` (`roleFile.CCPool.Actor`).";
              };
              skillMD = lib.mkOption {
                type = lib.types.str;
                default = "";
                description = "ccpool `--skill` markdown path (`roleFile.CCPool.SkillMD`).";
              };
              completion = lib.mkOption {
                type = lib.types.enum [
                  "close-only"
                  "close-or-handback"
                ];
                description = "Bead-done semantics (`roleFile.CCPool.Completion` / `roles.Completion`).";
              };
              onFailure = lib.mkOption {
                type = lib.types.enum [
                  "unclaim"
                  "add-human"
                ];
                description = "What to do to the bead on a flagged dispatch (`roleFile.CCPool.OnFailure` / `roles.FailureAction`).";
              };
              onDispatchFail = lib.mkOption {
                type = lib.types.enum [
                  "unclaim"
                  "leave"
                ];
                description = "What to do when the nudge could not be sent (`roleFile.CCPool.OnDispatchFail` / `roles.DispatchFailAction`).";
              };
              authorshipGuard = lib.mkOption {
                type = lib.types.bool;
                default = false;
                description = "`roleFile.CCPool.AuthorshipGuard`.";
              };
              promptBody = lib.mkOption {
                type = lib.types.str;
                default = "";
                description = "The task prompt template source (`roleFile.CCPool.PromptBody`).";
              };
              budget = lib.mkOption {
                type = lib.types.submodule {
                  options = {
                    tokens = lib.mkOption {
                      type = lib.types.int;
                      default = 0;
                      description = ''
                        Token-count ceiling for this role's ccpool watchdog
                        (`roleFile.CCPool.Budget.Tokens` /
                        `budget.Budget.Tokens`). `<= 0` means unlimited (no
                        token-based watchdog dimension).
                      '';
                    };
                    cost = lib.mkOption {
                      type = lib.types.int;
                      default = 0;
                      description = ''
                        Estimated-cost ceiling in cents for this role's
                        ccpool watchdog (`roleFile.CCPool.Budget.Cost` /
                        `budget.Budget.Cost`). `<= 0` means unlimited (no
                        cost-based watchdog dimension).
                      '';
                    };
                    time = lib.mkOption {
                      type = lib.types.str;
                      default = "";
                      description = ''
                        Wall-clock time ceiling for this role's ccpool
                        watchdog, as a `time.ParseDuration` string (e.g.
                        `"25m"`) (`roleFile.CCPool.Budget.Time` /
                        `budget.Budget.Time`). `""` (the default) or `"0s"`
                        means unlimited (no time-based watchdog dimension) —
                        the old `[role.ccpool.budget]` schema's own way to
                        deliberately request no watchdog for a role (e.g.
                        "feedback").
                      '';
                    };
                  };
                };
                default = { };
                description = ''
                  This role's ccpool watchdog budget
                  (`roleFile.CCPool.Budget` / `roles.CCPoolConfig.Budget`'s
                  Tokens/Cost/Time dimensions — Thresholds/Prices stay
                  pool-wide and are not settable per role here). Every field
                  left at its default renders the unlimited zero value: no
                  watchdog runs for this role.
                '';
              };
              isolation = lib.mkOption {
                type = lib.types.submodule {
                  options = {
                    type = lib.mkOption {
                      type = lib.types.enum [
                        ""
                        "worktree"
                        "none"
                        "path"
                        "workforest"
                      ];
                      default = "";
                      description = ''
                        How this ccpool role's WORKSPACE_ROOT is prepared
                        (`roles.IsolationConfig.Type`); `""` means "worktree"
                        (the long-standing default).
                      '';
                    };
                    path = lib.mkOption {
                      type = lib.types.str;
                      default = "";
                      description = "Fixed directory to create-or-reuse; only meaningful when `type` == \"path\" (`roles.IsolationConfig.Path`).";
                    };
                  };
                };
                default = { };
                description = "`roleFile.CCPool.Isolation`.";
              };
            };
          }
        );
        default = null;
        description = ''
          This role's ccpool launch/behavior config -- required (non-null)
          iff `type` == "ccpool" (`roleFile.CCPool`).
        '';
      };
      command = lib.mkOption {
        type = lib.types.nullOr (
          lib.types.submodule {
            options.argv = lib.mkOption {
              type = lib.types.listOf lib.types.str;
              description = "The argv this command role dispatches (`roleFile.Command.Argv`).";
            };
          }
        );
        default = null;
        description = ''
          This role's bare-command argv -- required (non-null) iff `type` ==
          "command" (`roleFile.Command`).
        '';
      };
    };
  };

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

    # roles / handlerCommandDir (this bead, pg2-pteab): decoupled from
    # register/periodicDrain/daemon above -- those are the heartbeat/health
    # mechanism (see the module-level doc comment); this is real per-role
    # dispatch config, rendered declaratively instead of hand-authored JSON
    # files. Populated independently of periodicDrain/daemon.enable, so a
    # deployment that only wants the rendered directory (e.g. to hand to
    # `home/programs/pg-router`'s own `handlerCommandDir` option) without
    # this module's own register LaunchAgent/systemd unit still gets it.
    roles = lib.mkOption {
      type = lib.types.attrsOf roleSubmodule;
      default = { };
      description = ''
        Per-role dispatch config, keyed by role name -- shaped like
        `cmd/pg-router-ccpool-handler/roleconfig.go`'s `roleFile` (the
        `--role-config`/`PG_ROUTER_CCPOOL_HANDLER_ROLE` JSON shape). Each
        entry renders to its own `pkgs.writeText "<name>.json"`, joined into
        one directory exposed as `handlerCommandDir` below. Populate 2+
        differently-configured roles (e.g. `feedback`/`worker`/`review`, each
        with its own ccpool actor/prompt/completion policy) to let
        `home/programs/pg-router`'s `daemon`/`periodicDrain.handlerCommandDir`
        differentiate dispatch per role instead of every role sharing the
        identical handler command (`PG_ROUTER_HANDLER_COMMAND_DIR`; closes the
        gap bead `pg2-ymb3v` fixed at the Go level).
      '';
    };

    handlerCommandDir = lib.mkOption {
      type = lib.types.package;
      readOnly = true;
      description = ''
        Read-only output: the directory of per-role JSON files rendered from
        `roles` above (one `<role.Name>.json` per entry), suitable for
        `home/programs/pg-router`'s `daemon`/
        `periodicDrain.handlerCommandDir` option
        (`PG_ROUTER_HANDLER_COMMAND_DIR`). Resolves to an empty directory when
        `roles` is empty.
      '';
    };

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

  config = lib.mkMerge [
    {
      # handlerCommandDir (this bead, pg2-pteab): a pure function of `roles`
      # alone, set UNCONDITIONALLY (not gated on cfg.enable, unlike
      # everything below) -- a consumer that only wants the rendered
      # directory to hand to `home/programs/pg-router`'s own
      # `handlerCommandDir` option should not need this module's own
      # register/systemd machinery enabled too.
      phillipgreenii.programs.pg-router-ccpool-handler.handlerCommandDir = handlerCommandDir;
    }
    (lib.mkIf cfg.enable {
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
    })
  ];
}
