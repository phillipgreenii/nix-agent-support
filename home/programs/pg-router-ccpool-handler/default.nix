{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pg-router-ccpool-handler;

  # tomlFormat / poolConfigFile (pg2-1p4yp): renders THIS handler's own
  # dedicated ccpool pool config.toml -- mirrors home/programs/ccpool's own
  # `settings` -> `tomlFormat.generate` pattern exactly, but scoped to
  # `cfg.pool.settings` (one named pool's config) instead of the shared
  # default (XDG) pool's config.toml. See `pool` option group below for why
  # this handler needs its OWN pool at all: ccpool's default pool cap
  # (`max_sessions = 6`, packages/ccpool/internal/config/config.go) is shared
  # by every ccpool consumer, and this handler alone routinely runs 15-30
  # concurrent dispatches against it -- cap eviction then force-closes
  # actively-working sessions with no regard for in-progress work
  # (packages/ccpool/internal/session/reap.go's Pass 2). Giving this handler
  # its own registered pool (docs/adr/0014-ccpool-reap-all-pool-registry.md)
  # lets its cap be raised WITHOUT touching the shared default pool's cap for
  # every other ccpool consumer (interactive use, other handlers).
  tomlFormat = pkgs.formats.toml { };
  poolConfigFile = tomlFormat.generate "pg-router-ccpool-handler-pool-config.toml" cfg.pool.settings;

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

  # defaultAllowedTools (this bead, pg2-qsred): mirrors
  # internal/config.baseAllowedTools's own literal value exactly.
  # internal/config.Default() computes its AllowedTools field ONCE, at
  # PRTool == "" -- it does NOT dynamically recompute when a later JSON
  # overlay sets a nonempty PRTool -- so this nix default deliberately does
  # the same (no dynamic Bash(<prTool>:*) append here either); see
  # launchConfig.allowedTools's own doc comment below.
  defaultAllowedTools = "Read,Edit,Write,Glob,Grep,Bash(git status:*),Bash(git diff:*),Bash(git log:*),Bash(git add:*),Bash(git commit:*),Bash(git checkout:*),Bash(git switch:*),Bash(git branch:*),Bash(git worktree:*),Bash(git rev-parse:*),Bash(git fetch:*),Bash(bd:*),Bash(go build:*),Bash(go test:*),Bash(go vet:*),Bash(gofmt:*),Bash(go mod:*),Bash(nix flake check:*),Bash(nix fmt:*),Bash(prek:*),Bash(pre-commit:*)";

  # launchConfigFile (this bead, pg2-qsred: home-manager module has no
  # launch-config surface, so real dispatch fails on empty WorktreeDir).
  # Renders `launchConfig` into the on-disk JSON shape
  # cmd/pg-router-ccpool-handler/roleconfig.go's `loadConfig` decodes
  # DIRECTLY into `internal/config.Config` (`json.Unmarshal(data, &c)` where
  # `c := config.Default()`) -- unlike `roleFile` above, there is NO wrapper
  # struct here. `internal/config.Config` carries no `json:"..."` tags of its
  # own, so `encoding/json`'s case-insensitive fallback matches these
  # lowerCamelCase keys onto Config's own PascalCase fields -- confirmed
  # against `loadConfig` and its own test fixtures
  # (`cmd/pg-router-ccpool-handler/roleconfig_test.go`'s
  # `{"permissionMode":"yolo"}`/`{"permissionMode":"plan"}`) and against this
  # bead's own live probe. `confirmIngest`/`budgetTime` render as NANOSECOND
  # integers, not `"25m"`-style duration strings: `Config`'s `time.Duration`
  # fields have no second parse pass the way `roleFile.CCPool.Budget.Time`
  # gets in `loadRole` -- a duration STRING here fails decode ("cannot
  # unmarshal string into Go struct field Config.MaxWait of type
  # time.Duration"), confirmed empirically for this bead. `maxWait`/
  # `pollInterval`/`reminderMsg`/`wrapUpMsg` are deliberately not exposed
  # here (no deployment need identified yet); omitting them from the
  # rendered JSON leaves `config.Default()`'s own values in effect for those
  # fields, since `loadConfig` overlays this JSON onto `Default()`, not the
  # reverse.
  launchConfigFile = pkgs.writeText "pg-router-ccpool-handler-launch-config.json" (
    builtins.toJSON {
      inherit (cfg.launchConfig)
        repoRoot
        worktreeDir
        beadsPrefix
        permissionMode
        allowedTools
        autonomous
        effort
        model
        prTool
        sessionPrefix
        selfLogin
        ;
      confirmIngest = cfg.launchConfig.confirmIngestSeconds * 1000000000;
      budgetTokens = cfg.launchConfig.budget.tokens;
      budgetCost = cfg.launchConfig.budget.cost;
      budgetTime = cfg.launchConfig.budget.timeSeconds * 1000000000;
      reminderPct = cfg.launchConfig.budget.reminderPct;
      cancelPct = cfg.launchConfig.budget.cancelPct;
      hardPct = cfg.launchConfig.budget.hardPct;
    }
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

    # launchConfig / launchConfigFile (this bead, pg2-qsred): the
    # --config/PG_ROUTER_CCPOOL_HANDLER_CONFIG launch/prompt/isolation config
    # `internal/config.Config` decodes -- decoupled from
    # register/periodicDrain/daemon/roles above the same way `roles` is: a
    # deployment wanting only this rendered file (to hand to a consuming
    # flake's own PG_ROUTER_CCPOOL_HANDLER_CONFIG export) needs no other
    # submodule enabled, only `enable` itself (see `launchConfigFile`'s own
    # doc comment for why it is gated on `enable`, unlike `handlerCommandDir`
    # above).
    launchConfig = {
      repoRoot = lib.mkOption {
        type = lib.types.str;
        description = ''
          RepoRoot in the launch config -- the repo a dispatched session's
          WORKSPACE_ROOT derives from (worktree/none isolation) or runs
          directly against. No default -- deployment-specific (mirrors
          `home/programs/pg-router`'s own `periodicDrain.repoRoot`/
          `daemon.repoRoot`, and this module's own `register` options'
          `socket`/`id` convention above).
        '';
      };
      worktreeDir = lib.mkOption {
        type = lib.types.str;
        description = ''
          WorktreeDir in the launch config -- the parent directory fresh
          per-item git worktrees are created under (worktree isolation, the
          default). `internal/config.Default()` hardcodes this to `""` with
          no fallback: every real dispatch using worktree isolation fails
          with `mkdir worktree dir: mkdir : no such file or directory` until
          a deployment supplies this (the bug this bead, pg2-qsred, fixes).
          No default -- deployment-specific.
        '';
      };
      beadsPrefix = lib.mkOption {
        type = lib.types.str;
        default = "zr";
        description = ''
          BeadsPrefix in the launch config -- the expected bd issue-store
          prefix, checked by this module's own precheck. Matches
          `internal/config.Default()`'s own value.
        '';
      };
      permissionMode = lib.mkOption {
        type = lib.types.enum [
          ""
          "default"
          "acceptEdits"
          "plan"
          "auto"
          "dontAsk"
          "bypassPermissions"
        ];
        default = "dontAsk";
        description = ''
          PermissionMode in the launch config -- forwarded verbatim to
          `ccpool new --permission-mode`, and the exact enum
          `internal/config.Config.Validate()` accepts
          (`validPermissionModes`). Matches `internal/config.Default()`'s own
          value.
        '';
      };
      allowedTools = lib.mkOption {
        type = lib.types.str;
        default = defaultAllowedTools;
        description = ''
          AllowedTools in the launch config -- forwarded verbatim to
          `ccpool new --allowed-tools`. Defaults to
          `internal/config.baseAllowedTools`'s own literal value (mirrored
          above as this module's own `defaultAllowedTools`), matching
          `internal/config.Default()`'s own value exactly: Default() computes
          this ONCE, with PRTool == "", so setting `prTool` below WITHOUT
          also overriding this field will NOT automatically grant
          `Bash(<prTool>:*)` -- the real Go `Default()` this mirrors has the
          identical gap. A deployment that wants that grant must set both.
        '';
      };
      autonomous = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          Autonomous in the launch config -- forwarded verbatim to
          `ccpool new`. Matches `internal/config.Default()`'s own value.
        '';
      };
      effort = lib.mkOption {
        type = lib.types.str;
        default = "max";
        description = ''
          Effort in the launch config -- forwarded verbatim to `ccpool new`.
          Matches `internal/config.Default()`'s own value.
        '';
      };
      model = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = ''
          Model in the launch config -- forwarded verbatim to `ccpool new`.
          `""` (the default, matching `internal/config.Default()`) omits the
          flag.
        '';
      };
      prTool = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = ''
          PRTool in the launch config -- the external PR-management tool
          this module's ACL/preflight shell out to. `""` (the default,
          matching `internal/config.Default()`) adds no extra grant to
          `allowedTools`'s own default (see that option's own doc comment).
        '';
      };
      sessionPrefix = lib.mkOption {
        type = lib.types.str;
        default = "pg-router-";
        description = ''
          SessionPrefix in the launch config -- the ccpool `--name` label
          prefix. Matches `internal/config.Default()`'s own value.
        '';
      };
      selfLogin = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = ''
          SelfLogin in the launch config -- the GitHub login the worker
          safety preamble asserts authorship against. `""` is
          `internal/config.Default()`'s own (implicit, zero-value) default --
          empty until a deployment sets it explicitly.
        '';
      };
      confirmIngestSeconds = lib.mkOption {
        type = lib.types.ints.unsigned;
        default = 90;
        description = ''
          ConfirmIngest in the launch config, in SECONDS -- rendered into the
          JSON's `confirmIngest` key as nanoseconds
          (`confirmIngestSeconds * 1e9`), because `internal/config.Config`
          carries no json tags and no custom `UnmarshalJSON`: its
          `time.Duration` fields decode via `encoding/json`'s plain int64
          handling, which requires a JSON NUMBER of nanoseconds, NOT a
          `"25m"`-style duration string (unlike this module's OWN
          `roles.*.ccpool.budget.time` above, which IS a string -- that
          decode path is a second, manual `time.ParseDuration` pass in
          `loadRole` that `loadConfig` has no equivalent of; confirmed
          empirically for this bead, pg2-qsred). Default 90 matches
          `internal/config.Default()`'s own `90 * time.Second`.
        '';
      };
      budget = {
        tokens = lib.mkOption {
          type = lib.types.int;
          default = 0;
          description = ''
            BudgetTokens in the launch config. `<= 0` means unlimited.
            Matches `internal/config.Default()`'s own value (unlimited until
            ccpool N3).
          '';
        };
        cost = lib.mkOption {
          type = lib.types.int;
          default = 0;
          description = ''
            BudgetCost in the launch config, in CENTS. `<= 0` means
            unlimited. Matches `internal/config.Default()`'s own value.
          '';
        };
        timeSeconds = lib.mkOption {
          type = lib.types.ints.unsigned;
          default = 1500;
          description = ''
            BudgetTime in the launch config, in SECONDS -- rendered as
            nanoseconds the same way `confirmIngestSeconds` above is (see
            that option's doc comment for why). Default 1500 (25 minutes)
            matches `internal/config.Default()`'s own `25 * time.Minute`.
          '';
        };
        reminderPct = lib.mkOption {
          type = lib.types.float;
          default = 0.725;
          description = "ReminderPct in the launch config. Matches `internal/config.Default()`'s own value.";
        };
        cancelPct = lib.mkOption {
          type = lib.types.float;
          default = 0.90;
          description = "CancelPct in the launch config. Matches `internal/config.Default()`'s own value.";
        };
        hardPct = lib.mkOption {
          type = lib.types.float;
          default = 1.00;
          description = "HardPct in the launch config. Matches `internal/config.Default()`'s own value.";
        };
      };
    };

    launchConfigFile = lib.mkOption {
      type = lib.types.nullOr lib.types.package;
      readOnly = true;
      # Deliberately NO `default` here -- same "readOnly + unconditional
      # config assignment" reasoning as `handlerCommandDir` above (a
      # `default` here would count as a second `evalOptionValue` definition
      # alongside the always-provided `config` value below).
      description = ''
        Read-only output: the rendered `launchConfig` JSON file, suitable for
        `--config`/`PG_ROUTER_CCPOOL_HANDLER_CONFIG`
        (`cmd/pg-router-ccpool-handler/roleconfig.go`'s `loadConfig`, which
        decodes it DIRECTLY into `internal/config.Config` -- see that
        struct's own field docs and `launchConfig` above). `null` when
        `enable` is false: `repoRoot`/`worktreeDir` above have no default, so
        unconditionally forcing this file's content regardless of `enable`
        would throw "used but not defined" for any consumer that merely
        imports this module without enabling it -- gating on `enable` instead
        keeps that import safe, matching the capability-model's "feature
        aggregate MUST be inert" invariant.
      '';
    };

    # pool (pg2-1p4yp): an OPT-IN dedicated ccpool pool for this handler's own
    # dispatches, distinct from ccpool's shared default (XDG) pool -- see
    # `poolConfigFile`'s doc comment above for the cap-eviction bug this
    # exists to fix. Disabled by default (`pool.enable = false`): an existing
    # deployment that has not opted in keeps dispatching through the shared
    # default pool, byte-for-byte unchanged (ADR 0014's own "zero behavior
    # change for existing single-pool installs" goal, restated for this
    # narrower opt-in).
    #
    # Wiring contract for a deployment that opts in: this module renders
    # `pool.dir`'s config.toml (`pool.settings`) and (via `home.activation`
    # below) bootstraps the pool directory's creation/registration through a
    # REAL `ccpool` invocation -- but it does NOT, and cannot, inject
    # `CCPOOL_POOL` into pg-router CORE's own process environment: this
    # handler is spawned as pg-router core's subprocess
    # (`internal/wireclient.OSRunner.Run`, `exec.CommandContext` with no
    # `cmd.Env` override, so it inherits pg-router core's env verbatim), and
    # this handler's own ccpool CLI calls
    # (`internal/ccpool/cli.go`'s `execCmd`) inherit THIS process's env the
    # same way -- so `CCPOOL_POOL` has to be set at the top of that chain, in
    # pg-router core's OWN daemon/periodicDrain environment
    # (`home/programs/pg-router`'s `daemon.handlerCcpoolPool` /
    # `periodicDrain.handlerCcpoolPool`, mirrored on darwin), not here. A
    # deployment enabling `pool.enable` MUST also set that pg-router-side
    # option to this module's own `pool.dir` value (same interpolate-the-
    # value-across-modules pattern `handlerCommandDir`/`launchConfigFile`
    # above already use).
    pool = {
      enable = lib.mkEnableOption ''
        registering and governing a dedicated ccpool pool for this handler's
        own dispatches, instead of sharing ccpool's default (XDG) pool with
        every other ccpool consumer. See this option group's own
        module-level doc comment for the full wiring contract (this module
        alone cannot set `CCPOOL_POOL` for pg-router core's own dispatch
        subprocess chain -- `home/programs/pg-router`'s own
        `handlerCcpoolPool` option must ALSO be set, to this module's
        `pool.dir`).
      '';
      dir = lib.mkOption {
        type = lib.types.str;
        default = "${config.home.homeDirectory}/.local/state/pg-router-ccpool";
        description = ''
          The dedicated pool's canonical directory (ccpool pool-dir mode --
          `CCPOOL_POOL`/`--pool`, `packages/ccpool/internal/config/pool.go`).
          This value is what a deployment feeds into
          `home/programs/pg-router`'s own `daemon.handlerCcpoolPool` /
          `periodicDrain.handlerCcpoolPool` option -- this module has no way
          to set that option itself (see the module-level doc comment
          above).
        '';
      };
      settings = lib.mkOption {
        inherit (tomlFormat) type;
        default = {
          pool.max_sessions = 40;
        };
        description = ''
          Contents of the dedicated pool's own `config.toml` (merged over
          ccpool's own per-pool defaults -- `packages/ccpool/internal/config`
          `defaults()`: `idle_ttl = 30m`, `auto_reap = true`; a field omitted
          here keeps that default). Mirrors
          `phillipgreenii.programs.ccpool.settings`'s own shape, scoped to
          this one named pool instead of the shared default pool. The
          default (`max_sessions = 40`) is a generous-but-bounded cap in line
          with this handler's actual observed concurrency (15-30 concurrent
          dispatches against ccpool's own shared-pool default of 6) --
          `packages/ccpool/internal/session/reap.go`'s cap-eviction pass
          still runs (this is a HIGHER cap, not reap disabled), it just no
          longer fires at a small fraction of real concurrency.
        '';
        example = {
          pool.max_sessions = 50;
          pool.idle_ttl = "45m";
        };
      };
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
      # launchConfigFile (this bead, pg2-qsred): `null` when disabled -- see
      # that option's own doc comment for why (repoRoot/worktreeDir have no
      # default, so this branch must stay unforced while disabled).
      phillipgreenii.programs.pg-router-ccpool-handler.launchConfigFile =
        if cfg.enable then launchConfigFile else null;
    }
    (lib.mkIf cfg.enable {
      home.packages = [ cfg.package ];

      # pool activation (pg2-1p4yp): bootstrap the dedicated pool dir through
      # a REAL `ccpool` invocation BEFORE installing our own config.toml over
      # it, so pool creation goes through ccpool's own
      # `ensurePoolDir`/`registry.Ensure` path and this pool enrolls in the
      # `ccpool reap-all` registry exactly like any other named pool
      # (docs/adr/0014-ccpool-reap-all-pool-registry.md's "Register on
      # creation only"). Pre-placing config.toml via a bare `home.file`/
      # `mkdir` WITHOUT this bootstrap step would make ccpool see an
      # already-existing dir on its first real use and skip registration
      # entirely (ADR 0014's own documented "pre-existing pools are not
      # enrolled" negative) -- silently trading "cap eviction kills active
      # sessions" for "this pool is never auto-reaped at all" (idle sessions
      # would then leak forever), which is not this fix's intent. `ccpool
      # --pool <dir> list` is read-only over the pool's OWN data (lists
      # sessions; a fresh pool has none) but still exercises the exact
      # create-or-validate codepath `ccpool new` does
      # (`packages/ccpool/internal/config/pool.go`'s `ResolvePool`), so it is
      # safe to (re-)run on every activation: on a fresh dir it creates +
      # registers; on an existing, already-registered dir it just validates.
      # Mirrors this repo's own `ccpoolTrust` activation precedent
      # (`home/programs/ccpool/default.nix`) -- best-effort (`|| true`): a
      # bootstrap hiccup must not break activation, and the handler's own
      # dispatch-time `ccpool new` calls would otherwise hit the same
      # create-or-validate path anyway on first real use.
      home.activation = lib.mkIf cfg.pool.enable {
        pgRouterCcpoolHandlerPool = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
          $DRY_RUN_CMD ${pkgs.ccpool}/bin/ccpool --pool ${lib.escapeShellArg cfg.pool.dir} list >/dev/null 2>&1 || true
          $DRY_RUN_CMD mkdir -p ${lib.escapeShellArg cfg.pool.dir}
          $DRY_RUN_CMD cp -f ${poolConfigFile} ${lib.escapeShellArg cfg.pool.dir}/config.toml
          $DRY_RUN_CMD chmod 0600 ${lib.escapeShellArg cfg.pool.dir}/config.toml
        '';
      };

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
