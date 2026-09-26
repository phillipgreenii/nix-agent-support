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
  # concurrent dispatches against it -- before ADR 0072, cap eviction
  # force-closed actively-working sessions; since then eviction spares
  # working rows and the handler declines busy when the pool is full, so the
  # dedicated pool's remaining purpose is isolation from other consumers'
  # sessions and their reap cadence, not protection from eviction. Giving
  # this handler its own registered pool
  # (docs/adr/0014-ccpool-reap-all-pool-registry.md)
  # lets its cap be raised WITHOUT touching the shared default pool's cap for
  # every other ccpool consumer (interactive use, other handlers).
  tomlFormat = pkgs.formats.toml { };
  poolConfigFile = tomlFormat.generate "pg-router-ccpool-handler-pool-config.toml" cfg.pool.settings;

  # ccpoolRolesWithOwnPool / poolConfigFileFor / roleBootstrapEntries (bead
  # pg2-mr0sl): the per-ROLE analogue of poolConfigFile/the
  # pgRouterCcpoolHandlerPool activation entry above, scoped to each `roles`
  # entry's own `ccpool.pool` group instead of the whole-process `cfg.pool`.
  # See that option group's own doc comment (on `roleSubmodule`) for the
  # full rationale; the bootstrap mechanics themselves (a real
  # `ccpool --pool <dir> list` before installing config.toml, so ccpool's
  # own create-or-validate/registration path runs -- ADR 0014) are copied
  # verbatim from the whole-process version below, just parameterized by
  # role name/dir/settings instead of reading `cfg.pool.*` directly.
  ccpoolRolesWithOwnPool = lib.filterAttrs (
    _name: roleCfg: roleCfg.type == "ccpool" && roleCfg.ccpool.pool.enable
  ) cfg.roles;
  poolConfigFileFor =
    name: roleCfg:
    tomlFormat.generate "pg-router-ccpool-handler-pool-config-${name}.toml" roleCfg.ccpool.pool.settings;
  # Activation entry names must be safe, static-looking bash function names
  # (home-manager's dag activation script naming) -- an underscore
  # separator (never a hyphen) keeps this valid regardless of what a role
  # name happens to contain.
  roleBootstrapEntries = lib.mapAttrs' (
    name: roleCfg:
    lib.nameValuePair "pgRouterCcpoolHandlerPool_${name}" (
      lib.hm.dag.entryAfter [ "writeBoundary" ] ''
        $DRY_RUN_CMD ${pkgs.ccpool}/bin/ccpool --pool ${lib.escapeShellArg roleCfg.ccpool.pool.dir} list >/dev/null 2>&1 || true
        $DRY_RUN_CMD mkdir -p ${lib.escapeShellArg roleCfg.ccpool.pool.dir}
        $DRY_RUN_CMD cp -f ${poolConfigFileFor name roleCfg} ${lib.escapeShellArg roleCfg.ccpool.pool.dir}/config.toml
        $DRY_RUN_CMD chmod 0600 ${lib.escapeShellArg roleCfg.ccpool.pool.dir}/config.toml
      ''
    )
  ) ccpoolRolesWithOwnPool;

  # mkWorktreeSweepScript / worktreeReclaimRegistryEntry (pg2-4roho decision
  # item 4): a `phillipgreenii.programs.pg-disk-reclaimer.registryEntries`
  # contribution for this handler's own per-bead worktree pool
  # (`launchConfig.worktreeDir`) -- the belt-and-suspenders net the decision
  # calls for REGARDLESS of the in-process dispatch-completion cleanup
  # (internal/executor/ccpool.go's cleanupWorktree, decision item 1) and the
  # needs_input-tied-to-session-close rationale (decision item 2): a crash, a
  # bug in that in-process path, or an edge case it never observes still gets
  # caught by this independent sweep -- exactly the role pg-disk-reclaimer
  # already plays for its other registered areas (see that module's own
  # `registryEntries` option doc comment: "Any module MAY append entries
  # here, gated on its own feature's enable"; gated here on THIS module's
  # `enable`, matching that convention, not on `pg-disk-reclaimer.enable`
  # itself -- contributing to an inert option when the reclaimer isn't
  # separately enabled is harmless).
  #
  # Deletion safety guard (decision item 5), applied identically here and in
  # the Go dispatch-completion path: `git worktree remove` (no `--force`)
  # refuses on its own whenever the worktree is dirty (uncommitted changes) --
  # gitclient's own documented native behavior, mirrored here at the shell
  # level rather than reimplemented, so both removal paths rely on the SAME
  # underlying guarantee. An explicit `git status --porcelain` pre-check adds
  # a clearer skip message (and lets the dry-run variant report a skip
  # without attempting a mutating command at all). A best-effort check for
  # `index.lock`/`HEAD.lock` under the worktree's own git-dir additionally
  # skips a worktree with a git operation visibly in progress (covers
  # commit/rebase/merge racing this sweep) -- this is an explicitly best-
  # effort proxy for "an in-flight push in progress": a bare `git push` that
  # touches neither the index nor HEAD locally is NOT caught by it, but this
  # sweep is the courser of two nets (the STRONG, ccpool-session-aware
  # guarantee is decision items 1/2, which run in-process before this ever
  # fires), and a worktree that keeps failing this guard across sweeps
  # surfaces on its own next time (still listed, never force-removed) rather
  # than being silently force-deleted.
  #
  # Wrapped in a subshell so its own locals (wtdir/n/skipped/wt/gitdir) never
  # leak into pg-disk-reclaimer's own `pgdr_reclaim` function scope, since
  # dryRunCommand/removeCommand run via that function's own `eval` (not a
  # fresh `bash -c`, unlike displayCommand below).
  #
  # Leading `:` (pg2-2wu8d): pg-disk-reclaimer's pgdr_validate_commands_exist
  # only checks the leading whitespace-delimited token of a command
  # string's first line, and a bare `(` opening a subshell isn't a command
  # name, so `validate` reported it as a nonexistent command "(". `:` is a
  # real no-op builtin, satisfying that check as a harmless first
  # statement before the actual subshell -- `eval` still runs it then the
  # subshell as two ordinary sequential statements, so this doesn't change
  # dryRunCommand/removeCommand's own exit status (that's the subshell's,
  # since it's the LAST command run).
  mkWorktreeSweepScript = apply: ''
    :
    (
      repo=${lib.escapeShellArg cfg.launchConfig.repoRoot}
      wtdir=${lib.escapeShellArg cfg.launchConfig.worktreeDir}
      n=0
      skipped=0
      while IFS= read -r wt; do
        gitdir=$(git -C "$wt" rev-parse --git-dir 2>/dev/null) || {
          echo "pg-router-ccpool-handler-worktrees: skip (not a linked worktree): $wt"
          skipped=$((skipped + 1))
          continue
        }
        case "$gitdir" in
          /*) : ;;
          *) gitdir="$wt/$gitdir" ;;
        esac
        if [ -f "$gitdir/index.lock" ] || [ -f "$gitdir/HEAD.lock" ]; then
          echo "pg-router-ccpool-handler-worktrees: skip (git operation in progress): $wt"
          skipped=$((skipped + 1))
          continue
        fi
        if [ -n "$(git -C "$wt" status --porcelain 2>/dev/null)" ]; then
          echo "pg-router-ccpool-handler-worktrees: skip (uncommitted changes): $wt"
          skipped=$((skipped + 1))
          continue
        fi
        # branch derives from wt's own basename: internal/worktree's Ensure
        # (packages/pg-router-ccpool-handler/internal/worktree/worktree.go)
        # names the worktree dir <worktreeDir>/<beadID> and its anchor branch
        # pg-router/<beadID> deterministically from the SAME beadID, so the
        # dir's basename recovers it here without needing beads/ccpool state.
        beadid=$(basename "$wt")
        branch="pg-router/$beadid"
        ${
          if apply then
            ''
              if git -C "$wt" worktree remove "$wt" 2>&1; then
                echo "pg-router-ccpool-handler-worktrees: removed: $wt"
                n=$((n + 1))
                # Branch delete (bead pg2-ci75j): x/gitclient's RemoveWorktree
                # (mirrored here at the shell level) only ever ran `git
                # worktree remove` and never touched the branch, orphaning it
                # in $repo forever -- the ~100+-stray-branch state pg2-ci75j
                # found in the ZR monorepo. Anchored at $repo, never at $wt:
                # $wt no longer exists once removed above, and a branch is a
                # repo-level ref anyway (the same anchor CreateWorktree itself
                # used to create it). These are pg-router's own throwaway
                # per-dispatch anchor branches, reset at HEAD every dispatch
                # -- never a user branch with independent value -- so once
                # the worktree is confirmed removed the branch has served its
                # purpose; most are never merged into anything, so `-D`
                # (force) is used rather than `-d`, which would refuse
                # constantly for exactly that reason. Fails soft like the
                # removal above: left for the next sweep, never escalated.
                if git -C "$repo" branch -D "$branch" 2>&1; then
                  echo "pg-router-ccpool-handler-worktrees: branch deleted: $branch"
                else
                  echo "pg-router-ccpool-handler-worktrees: skip (branch delete failed -- left for next sweep): $branch"
                fi
              else
                echo "pg-router-ccpool-handler-worktrees: skip (remove failed -- left for next sweep): $wt"
                skipped=$((skipped + 1))
              fi
            ''
          else
            ''
              echo "pg-router-ccpool-handler-worktrees: would remove: $wt"
              echo "pg-router-ccpool-handler-worktrees: would delete branch: $branch"
              n=$((n + 1))
            ''
        }
      done < <(find "$wtdir" -mindepth 1 -maxdepth 1 -type d 2>/dev/null)
      echo "pg-router-ccpool-handler-worktrees: $n ${
        if apply then "removed" else "removable"
      }, $skipped skipped"
    )
  '';

  worktreeReclaimRegistryEntry = {
    id = "pg-router-ccpool-handler-worktrees";
    description = "pg-router-ccpool-handler's per-bead git worktrees -- belt-and-suspenders net for pg2-4roho's primary dispatch-completion cleanup (internal/executor/ccpool.go's cleanupWorktree)";
    path = cfg.launchConfig.worktreeDir;
    # Leading `:` (pg2-2wu8d): see mkWorktreeSweepScript's own doc comment
    # above -- pgdr_validate_commands_exist's leading-token check trips on
    # this string's first real statement being a bare `n=$(find ...)`
    # assignment rather than a command name (reads as a nonexistent
    # command "n=$(find"). `:` is a real no-op builtin satisfying that
    # check first; this runs via a fresh `bash -c` (pgdr_display_output),
    # not `eval`, but the reasoning is the same -- a leading no-op
    # statement doesn't change the string's own exit status, which is
    # still the final `echo`'s.
    displayCommand = ''
      :
      n=$(find ${lib.escapeShellArg cfg.launchConfig.worktreeDir} -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l | tr -d ' ')
      sz=$(du -sh ${lib.escapeShellArg cfg.launchConfig.worktreeDir} 2>/dev/null | cut -f1)
      if [ -z "$sz" ]; then sz="0"; fi
      echo "$n worktree(s), $sz"
    '';
    variants = [
      {
        aggressiveness = 1;
        variantDescription = "remove any per-bead worktree that is fully clean (no uncommitted changes, no lock file indicating an in-flight git operation) -- skips anything dirty or mid-operation, leaving it for the next sweep (decision item 5's own guard)";
        dryRunCommand = mkWorktreeSweepScript false;
        removeCommand = mkWorktreeSweepScript true;
      }
    ];
  };

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

  # mkPoolMetricsScript (bead pg2-mr0sl): the `poolMetrics` timer's
  # ExecStart -- one `--pool <name>=<dir>` per role with `ccpool.pool.enable`
  # (derived from `ccpoolRolesWithOwnPool`, defined further down once
  # `cfg.roles` is in scope -- see that binding's own doc comment), then an
  # atomic write of `pool-capacity`'s stdout to `outputPath` (a same-
  # directory temp file, then `mv -f`, so a scraper never reads a
  # half-written file). Unlike `mkRegisterExec` above (a single binary
  # invocation, no redirection needed), this needs a real shell for the
  # temp-file/redirect/rename dance, hence `pkgs.writeShellScript` rather
  # than a bare `lib.concatStringsSep` argv string.
  mkPoolMetricsScript =
    let
      poolArgs = lib.concatMapStrings (
        name: " --pool ${name}=${lib.escapeShellArg cfg.roles.${name}.ccpool.pool.dir}"
      ) (lib.attrNames ccpoolRolesWithOwnPool);
    in
    pkgs.writeShellScript "pg-router-ccpool-handler-pool-metrics" ''
      set -eu
      outputPath=${lib.escapeShellArg cfg.poolMetrics.outputPath}
      mkdir -p "$(dirname "$outputPath")"
      tmp="$(mktemp "$outputPath.XXXXXX")"
      ${cfg.package}/bin/pg-router-ccpool-handler pool-capacity${poolArgs} > "$tmp"
      mv -f "$tmp" "$outputPath"
    '';

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
          }
          // lib.optionalAttrs roleCfg.ccpool.pool.enable {
            # poolDir (bead pg2-mr0sl): only rendered when this role opted
            # into its own dedicated pool -- omitted (not merely "") when
            # disabled, so `roleconfig.go`'s `loadRole` sees an absent JSON
            # key and decodes the zero-value "" exactly like every field
            # this schema already omits when unset, keeping a
            # non-opted-in role's dispatch pointed at whatever CCPOOL_POOL
            # this process already inherits (unchanged behavior).
            poolDir = roleCfg.ccpool.pool.dir;
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
  #
  # pg-connector issue */ccpool * (bead pg2-s9zh5): widened alongside
  # packages/pg-router-ccpool-handler/internal/config.go's own
  # baseAllowedTools (see that constant's doc comment for the full
  # rationale) -- every dispatched role shares this ONE process-wide
  # allowlist (no per-role override mechanism exists in `roles`/
  # `roleFileFor` below), so a triager-shaped role's own dispatch prompt
  # instructing it to use `pg-connector issue show/comment/update/close` and
  # `ccpool list/state/reply/tail` had no path to those verbs under
  # PermissionMode=dontAsk (auto-deny, no prompt possible).
  defaultAllowedTools = "Read,Edit,Write,Glob,Grep,Bash(git status:*),Bash(git diff:*),Bash(git log:*),Bash(git add:*),Bash(git commit:*),Bash(git checkout:*),Bash(git switch:*),Bash(git branch:*),Bash(git worktree:*),Bash(git rev-parse:*),Bash(git fetch:*),Bash(bd:*),Bash(go build:*),Bash(go test:*),Bash(go vet:*),Bash(gofmt:*),Bash(go mod:*),Bash(nix flake check:*),Bash(nix fmt:*),Bash(prek:*),Bash(pre-commit:*),Bash(pg-connector issue show:*),Bash(pg-connector issue comment:*),Bash(pg-connector issue update:*),Bash(pg-connector issue close:*),Bash(ccpool list:*),Bash(ccpool state:*),Bash(ccpool reply:*),Bash(ccpool tail:*)";

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
        autonomous
        effort
        model
        prTool
        sessionPrefix
        selfLogin
        ;
      # allowedTools (this bead, pg2-yybrp): extraAllowedTools is additive --
      # appended (comma-joined) onto allowedTools's own value rather than
      # replacing it -- so a deployment can grant a handful of extra tools
      # without restating allowedTools's entire default list (see
      # extraAllowedTools's own doc comment for why that restatement is a
      # drift hazard).
      allowedTools =
        if cfg.launchConfig.extraAllowedTools == [ ] then
          cfg.launchConfig.allowedTools
        else
          lib.concatStringsSep "," ([ cfg.launchConfig.allowedTools ] ++ cfg.launchConfig.extraAllowedTools);
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
              # pool (bead pg2-mr0sl): a per-ROLE dedicated ccpool pool,
              # mirroring the whole-process `pool` option group below
              # (pg2-1p4yp) at a finer grain -- that mechanism gives the
              # WHOLE handler process one shared pool; this gives each ROLE
              # its own, so review/feedback/worker never draw from the same
              # slot count (this bead's whole point). Disabled by default
              # (`pool.enable = false`): an existing deployment that has not
              # opted in keeps dispatching this role through whatever
              # CCPOOL_POOL this process already inherits (today, the
              # whole-process `pool` group above, or ccpool's own shared
              # default XDG pool), byte-for-byte unchanged.
              #
              # Wiring: when enabled, `roleFileFor` renders this role's own
              # `poolDir` (this option's `dir`) into its JSON
              # (`roleFile.CCPool.PoolDir` /
              # `roles.CCPoolConfig.PoolDir`), and `dispatch.go`'s
              # `buildDeps` overrides CCPOOL_POOL to it for every ccpool
              # subprocess call THIS role's own dispatch makes
              # (`internal/ccpool.NewCLIRunnerForPool`) -- entirely inside
              # this ONE handler process, unlike the whole-process `pool`
              # group, which can only be threaded in via
              # `home/programs/pg-router`'s own
              # `daemon.handlerCcpoolPool`/`periodicDrain.handlerCcpoolPool`
              # (pg-router core's own process environment). This module
              # bootstraps `dir`'s config.toml the same way the
              # whole-process `pool` group's own `home.activation` does (see
              # `poolConfigFileFor`/`roleBootstrapEntries` below) -- through
              # a real `ccpool --pool <dir> list` call, so a fresh dir
              # enrolls in ccpool's own `reap-all` registry
              # (docs/adr/0014-ccpool-reap-all-pool-registry.md) instead of
              # silently never being reaped.
              pool = lib.mkOption {
                type = lib.types.submodule {
                  options = {
                    enable = lib.mkEnableOption ''
                      registering and governing a dedicated ccpool pool for
                      THIS role's own dispatches, instead of sharing
                      whatever CCPOOL_POOL this handler process already
                      inherits with every other role. See this option
                      group's own module-level doc comment (on the sibling
                      `ccpool` submodule) for the full wiring contract.
                    '';
                    dir = lib.mkOption {
                      type = lib.types.str;
                      default = "";
                      description = ''
                        This role's own dedicated pool's canonical directory
                        (ccpool pool-dir mode -- `CCPOOL_POOL`/`--pool`,
                        `packages/ccpool/internal/config/pool.go`). Empty
                        (the default) is only valid while `enable` is
                        `false`; a deployment that sets `enable = true`
                        MUST also set this -- a role name alone is not
                        unique enough to derive a safe path from (e.g.
                        across multiple repos/deployments sharing one
                        `$HOME`), so no non-empty default is guessed.
                      '';
                    };
                    settings = lib.mkOption {
                      inherit (tomlFormat) type;
                      default = {
                        pool.max_sessions = 40;
                      };
                      description = ''
                        Contents of this role's own dedicated pool's
                        `config.toml` (merged over ccpool's own per-pool
                        defaults -- `packages/ccpool/internal/config`
                        `defaults()`: `idle_ttl = 30m`, `auto_reap = true`).
                        Mirrors the whole-process `pool.settings` option's
                        own shape and defaults, scoped to this one role's
                        own named pool. Set `pool.max_sessions` to this
                        role's own configured cap (e.g. review=1,
                        feedback=1, worker=3 -- bead pg2-mr0sl's own
                        acceptance criteria).
                      '';
                      example = {
                        pool.max_sessions = 1;
                      };
                    };
                  };
                };
                default = { };
                description = ''
                  This role's own dedicated ccpool pool (bead pg2-mr0sl) --
                  see this option group's own doc comment above.
                '';
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
      extraAllowedTools = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = ''
          Additive tool grants appended (comma-joined) onto `allowedTools`'s
          own value when rendering the launch-config JSON -- each entry is a
          single grant in the same syntax `allowedTools` uses, e.g.
          `"Bash(pg-pr review:*)"`. Exists (pg2-yybrp) so a deployment that
          needs a few extra grants (e.g. for `pg-pr`/`pg-connector`/`pg-desk`
          CLIs a dispatched role's prompt or skill requires) can add just
          those, rather than restating `allowedTools`'s entire default list
          -- a drift hazard, since a restated copy silently diverges from
          this module's own default as it evolves. Default `[ ]` appends
          nothing, leaving `allowedTools` unchanged.
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
          dispatches against ccpool's own shared-pool default of 6).
          Before ADR 0072, cap eviction force-closed actively-working
          sessions; since then eviction spares working rows and the
          handler declines busy when the pool is full, so the dedicated
          pool's remaining purpose is isolation from other consumers'
          sessions and their reap cadence, not protection from eviction.
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

    # poolMetrics (bead pg2-mr0sl): a systemd --user timer periodically
    # running the `pool-capacity` subcommand for every role with
    # `ccpool.pool.enable` set, so each role's own dedicated pool's
    # occupancy is independently scrapable (this bead's "per-pool/per-role
    # capacity metric" acceptance criterion) -- see `mkPoolMetricsScript`'s
    # own doc comment below for the exec mechanics.
    poolMetrics = {
      enable = lib.mkEnableOption ''
        a systemd --user timer that periodically runs
        `pg-router-ccpool-handler pool-capacity` for every role with
        `ccpool.pool.enable` set, writing Prometheus exposition-format text
        to `outputPath`. On darwin,
        `darwin/modules/pg-router-ccpool-handler/default.nix` mirrors this
        into a LaunchAgent with a `StartInterval` (this HM systemd unit
        alone is a darwin no-op, matching `periodicDrain`/`daemon`'s own
        darwin-mirroring precedent).
      '';
      # intervalSeconds (unlike periodicDrain.interval's freeform systemd
      # time-span string above) is a plain integer number of SECONDS --
      # deliberately, so this ONE value drives both platforms with no
      # string-parsing/conversion needed: systemd's own time-span grammar
      # treats a bare number with no unit suffix as seconds (so
      # `toString intervalSeconds` is a legal OnUnitActiveSec/OnBootSec
      # value below), and darwin's LaunchAgent StartInterval
      # (darwin/modules/pg-router-ccpool-handler's own mirror) is ALREADY
      # a plain integer-seconds field -- matching
      # darwin/modules/pg-ccaudit's own `sweep.intervalSeconds` precedent.
      # This is why poolMetrics gets a REAL darwin mirror where
      # periodicDrain/daemon's own freeform-string intervals do not.
      intervalSeconds = lib.mkOption {
        type = lib.types.ints.unsigned;
        default = 60;
        description = "Seconds between pool-capacity passes (OnUnitActiveSec/OnBootSec on Linux, StartInterval on darwin).";
      };
      outputPath = lib.mkOption {
        type = lib.types.str;
        default = "${config.home.homeDirectory}/.local/state/pg-router-ccpool-handler/pool-capacity.prom";
        description = ''
          Where the rendered Prometheus exposition-format text is written.
          Written atomically (a same-directory temp file, then renamed) so
          a textfile-collector-style scraper never reads a half-written
          file. Ends in `.prom` by convention (node_exporter's textfile
          collector convention) -- this module does not itself run
          node_exporter or wire any scrape config; that is
          deployment-specific.
        '';
      };
      script = lib.mkOption {
        type = lib.types.package;
        readOnly = true;
        description = ''
          Read-only output: the rendered pool-capacity script
          (`mkPoolMetricsScript`) -- one `--pool <name>=<dir>` per role with
          `ccpool.pool.enable`, then an atomic write to `outputPath`.
          Exposed (mirroring `handlerCommandDir`/`launchConfigFile`'s own
          cross-module re-export pattern) so
          `darwin/modules/pg-router-ccpool-handler` can wire its own
          LaunchAgent to the SAME script this HM module's own systemd timer
          runs, rather than re-deriving the `--pool` argv independently.
        '';
      };
    };
  };

  config = lib.mkMerge [
    {
      # handlerCommandDir / launchConfigFile / poolMetrics.script are all
      # pure functions of `roles`/`cfg.enable`/`poolMetrics.*`/`package`
      # alone, set UNCONDITIONALLY (not gated on cfg.enable, unlike
      # everything below) -- a consumer that only wants one of these
      # rendered outputs (e.g. to hand to `home/programs/pg-router`'s own
      # `handlerCommandDir` option, or darwin's own LaunchAgent mirror)
      # should not need this module's own register/systemd machinery
      # enabled too. Nested under one `phillipgreenii.programs.pg-router-
      # ccpool-handler` binding (rather than three separately dotted
      # top-level assignments) per statix's repeated-keys lint.
      phillipgreenii.programs.pg-router-ccpool-handler = {
        # handlerCommandDir: this bead, pg2-pteab.
        inherit handlerCommandDir;
        # launchConfigFile: this bead, pg2-qsred. `null` when disabled --
        # see that option's own doc comment for why (repoRoot/worktreeDir
        # have no default, so this branch must stay unforced while
        # disabled).
        launchConfigFile = if cfg.enable then launchConfigFile else null;
        # poolMetrics.script: bead pg2-mr0sl.
        poolMetrics.script = mkPoolMetricsScript;
      };
    }
    (lib.mkIf cfg.enable {
      home.packages = [ cfg.package ];

      # pg2-4roho decision item 4: register this handler's own worktree pool
      # with pg-disk-reclaimer as a belt-and-suspenders safety net -- see
      # worktreeReclaimRegistryEntry's own doc comment above for the full
      # rationale and its relationship to decision items 1/2/5.
      phillipgreenii.programs.pg-disk-reclaimer.registryEntries = [ worktreeReclaimRegistryEntry ];

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
      # roleBootstrapEntries (bead pg2-mr0sl) is merged in UNCONDITIONALLY
      # here (not itself behind another lib.mkIf): it already resolves to
      # `{ }` whenever no role has `ccpool.pool.enable` set
      # (ccpoolRolesWithOwnPool filters down to nothing), so merging it
      # changes nothing for a deployment that only uses the whole-process
      # `cfg.pool` group (or neither) -- exactly like `handlerCommandDir`'s
      # own "empty roles -> harmless empty output" posture elsewhere in this
      # module.
      home.activation =
        (lib.optionalAttrs cfg.pool.enable {
          pgRouterCcpoolHandlerPool = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
            $DRY_RUN_CMD ${pkgs.ccpool}/bin/ccpool --pool ${lib.escapeShellArg cfg.pool.dir} list >/dev/null 2>&1 || true
            $DRY_RUN_CMD mkdir -p ${lib.escapeShellArg cfg.pool.dir}
            $DRY_RUN_CMD cp -f ${poolConfigFile} ${lib.escapeShellArg cfg.pool.dir}/config.toml
            $DRY_RUN_CMD chmod 0600 ${lib.escapeShellArg cfg.pool.dir}/config.toml
          '';
        })
        // roleBootstrapEntries;

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
        {
          # poolMetrics has nothing to report when no role opted into its
          # own dedicated pool (bead pg2-mr0sl) -- catch that misconfiguration
          # at eval time rather than silently shipping a timer whose every
          # run exits usage-error (runPoolCapacity's own "at least one --pool
          # name=dir is required" guard).
          assertion = !cfg.poolMetrics.enable || (ccpoolRolesWithOwnPool != { });
          message = ''
            phillipgreenii.programs.pg-router-ccpool-handler.poolMetrics.enable
            requires at least one `roles.<name>.ccpool.pool.enable = true` entry
            -- there is no per-role dedicated pool to report capacity for
            otherwise.
          '';
        }
        {
          # A role that opts into its own dedicated pool but leaves `dir`
          # at its empty default would bootstrap (home.activation) and
          # dispatch against `CCPOOL_POOL=""`, which ccpool's own
          # ResolvePool treats as "unset" and falls back to the shared
          # default XDG pool -- silently defeating the whole point of
          # opting in. Caught at eval time rather than at first dispatch.
          assertion =
            ccpoolRolesWithOwnPool == { }
            || lib.all (roleCfg: roleCfg.ccpool.pool.dir != "") (lib.attrValues ccpoolRolesWithOwnPool);
          message = ''
            phillipgreenii.programs.pg-router-ccpool-handler.roles.<name>.ccpool.pool.enable
            = true requires that role's own `ccpool.pool.dir` to be set (non-empty)
            -- an empty dir would resolve to ccpool's own shared default pool,
            defeating the point of a per-role dedicated pool.
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

        # poolMetrics (bead pg2-mr0sl): mirrors the drain service/timer pair
        # above exactly, just running mkPoolMetricsScript's pool-capacity
        # pass instead of a register heartbeat.
        services.pg-router-ccpool-handler-pool-metrics = lib.mkIf cfg.poolMetrics.enable {
          Unit.Description = "pg-router-ccpool-handler: report per-role ccpool pool capacity";
          Service = {
            Type = "oneshot";
            ExecStart = "${mkPoolMetricsScript}";
          };
        };

        timers.pg-router-ccpool-handler-pool-metrics = lib.mkIf cfg.poolMetrics.enable {
          Unit.Description = "Run pg-router-ccpool-handler pool-capacity periodically";
          Install.WantedBy = [ "timers.target" ];
          Timer = {
            OnUnitActiveSec = toString cfg.poolMetrics.intervalSeconds;
            OnBootSec = toString cfg.poolMetrics.intervalSeconds;
            Persistent = true;
          };
        };
      };
    })
  ];
}
