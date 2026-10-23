{
  config,
  lib,
  pkgs,
  # Set by the nix-darwin / NixOS home-manager integration; null for a
  # standalone home-manager activation (no system scope to read from).
  osConfig ? null,
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

  # pg-wrap wiring for ccpool roles (design ruling 30; bead pg2-12gr4.8.12).
  #
  # pg-wrap is a STANDALONE wrapper, mutually unaware of pg-router and ccpool.
  # ccpool passes env to claude through tmux's `-e` flags, so wrapping the
  # adapter or `ccpool new` would never reach claude INSIDE tmux. This module
  # therefore wraps a ccpool role through ccpool's own opaque `claude.bin`:
  #
  #   * `claude.bin = pg-wrap-exec` in the config.toml of every pool it manages
  #     that hosts a role with a `spec`;
  #   * a rendered pg-wrap spec per such role (`exec` = claude by default);
  #   * `PG_WRAP_SPEC=<spec>` in that role's `sessionEnv`;
  #   * evaluation FAILS in both directions: a spec role on a pool whose
  #     rendered `claude.bin` is not `pg-wrap-exec` (claude would run
  #     unwrapped), and a role WITHOUT a spec on a `pg-wrap-exec` pool (every
  #     launch there would decline).
  #
  # Pool resolution mirrors the handler's own dispatch: a ccpool role with
  # `ccpool.pool.enable` is on its own pool; otherwise, with the handler-wide
  # `pool.enable`, it is on the handler-wide pool; otherwise it is on ccpool's
  # default pool, which this module does not manage (so its `claude.bin` is
  # never `pg-wrap-exec` and a spec role there fails the first check). A
  # ccpool role on that unmanaged default pool fails evaluation whether or not
  # it has a spec (`unmanagedPoolAssertions`, bead pg2-3qx9e).
  pgWrapExecBin = "${pkgs.pg-wrap}/bin/pg-wrap-exec";

  # The ten variable names every rendered spec's `keep` carries: the participant
  # markers, the item context and ccpool's four injected markers. The
  # child-session markers ccpool blanks (`CLAUDE_CODE_CHILD_SESSION`,
  # `CLAUDECODE`, ...) are deliberately NOT here: they MUST NOT survive.
  wrapSpecOwnedKeep = [
    "BEADS_ACTOR"
    "BEADS_DIR"
    "WORKSPACE_ROOT"
    "PG_ROUTER_ROLE"
    "PG_ROUTER_ITEM_ID"
    "PG_ROUTER_ITEM_TYPE"
    "CCPOOL_EXTERNAL_ID"
    "PA_MONITOR_NO_NUDGE"
    "CCPOOL_POOL"
    "CCPOOL_AUTONOMOUS"
  ];

  # claudeBaseline (design ruling 20, INTERIM until flox environments exist for
  # the claude roles; bead pg2-12gr4.8.31): the pg-wrap spec `wrap` entries and
  # `keep` names covering everything claude needs to RUN in a pool session, for
  # `roles.<name>.spec = { inherit (cfg.claudeBaseline) wrap keep; strictEnv = ..; }`.
  # `wrap` is ONE entry (an argv array: env, then PATH=<pathDirs>, then one
  # NAME=value per OTel variable), so a consumer may append its own entries after
  # it. Its PATH holds claude, every bare command the enabled plugins' hooks
  # invoke (ccpool, claude-extended-tool-approver, session-mode, claude-work-start,
  # claude-work-end; the nix check test-claude-baseline-hook-commands derives and
  # asserts that list from the hooks.json files) and the prompts' tools (gh, bd,
  # jq, git, nix, pg-connector, pn), then the system directories (/usr/bin:/bin:
  # /usr/sbin:/sbin). Scope (bead pg2-hou7i): only what the GENERIC allowed tools
  # and hooks name belongs here. A role's own toolchain (go, gofmt, gofumpt,
  # prek, pre-commit, pg-hooks, and any site-specific tool) is declared by that
  # role through `claudeBaseline.extend { extraPathPkgs = [ ... ]; }`; the nix
  # check test-claude-baseline-role-tool-commands fails when a role's allowed
  # tools name a bare command its spec PATH lacks. `bd` defaults to the llm-agents
  # package; a deployment whose pool sessions need its own wrapped bd (claim
  # guard, dolt on PATH) passes `extend { bd = ...; }`.
  #
  # Stated gap: this repo does not package `pn` (a flake package of
  # phillipgreenii/nix-repo-base, absent from the overlays this flake applies), so
  # `pn` joins the PATH only when the consumer's `pkgs` carries `pkgs.pn`; when it
  # is null (as in this repo's own checks) `pn` is left out.
  #
  # OTel: read null-safely from the system observability stack (as pa-monitor
  # does), restricted to the OTEL_EXPORTER_OTLP_* names, so OTEL_SERVICE_NAME is
  # dropped. No osConfig, or no mkEmitterEnv, sets no OTel variable.
  claudeObs = if osConfig == null then { } else osConfig.phillipgreenii.observability or { };
  claudeEmitterEnv =
    if claudeObs ? mkEmitterEnv then
      claudeObs.mkEmitterEnv {
        serviceName = "pg-router-ccpool-handler-claude";
        protocol = "grpc";
      }
    else
      { };
  claudeOtelEnv = lib.filterAttrs (n: _v: lib.hasPrefix "OTEL_EXPORTER_OTLP_" n) claudeEmitterEnv;
  # nixpkgs carries a `pn` alias that THROWS ("removed as upstream was archived"),
  # so a plain `pkgs ? pn` is not enough: tryEval turns that throw into "absent".
  claudeBaselinePn = builtins.tryEval (pkgs.pn or null);
  # System directories appended AFTER the store directories (bead pg2-hou7i): a
  # strict session otherwise has no ls/cat/grep/sed/sh/security/ssh/curl by bare
  # name. Last, so a store tool (git, nix, bd, ...) always wins over a system
  # shim of the same name. They are not in `pathDirs` (which the hook-command
  # scan resolves inside the nix sandbox, where these directories do not exist).
  claudeBaselineSystemDirs = [
    "/usr/bin"
    "/bin"
    "/usr/sbin"
    "/sbin"
  ];
  # mkClaudeBaseline: the baseline for a given `bd` and extra PATH packages.
  # `claudeBaseline` is `mkClaudeBaseline { }`; `claudeBaseline.extend` is this
  # function, so a consumer swaps the bd or adds its role's own tools:
  #   claudeBaseline.extend { bd = <workspace-wrapped bd>; extraPathPkgs = [ pkgs.go ]; }
  mkClaudeBaseline =
    {
      bd ? pkgs.llm-agentsPkgs.beads,
      extraPathPkgs ? [ ],
    }:
    let
      pathPkgs = [
        pkgs.llm-agentsPkgs.claude-code
        pkgs.ccpool
        pkgs.claude-extended-tool-approver
        pkgs.session-mode
        pkgs.claude-activity
        pkgs.pg-connector
        pkgs.gh
        pkgs.jq
        # git and nix: the handler-wide allowed tools name both (`Bash(git ...)`,
        # `Bash(nix flake check:*)`, `Bash(nix fmt:*)`), so every claude role needs
        # them by bare name (evidence: `nix: command not found` exit 127 in the
        # drain-pg2 transcripts, pg2-12gr4.3).
        pkgs.git
        pkgs.nix
        bd
      ]
      ++ lib.optional (claudeBaselinePn.success && claudeBaselinePn.value != null) claudeBaselinePn.value
      ++ extraPathPkgs;
      pathDirs = map (p: "${lib.getBin p}/bin") pathPkgs;
    in
    {
      inherit pathDirs;
      systemDirs = claudeBaselineSystemDirs;
      extend = mkClaudeBaseline;
      wrap = [
        (
          [
            "${pkgs.coreutils}/bin/env"
            "PATH=${lib.concatStringsSep ":" (pathDirs ++ claudeBaselineSystemDirs)}"
            # A bd of ANY provenance must not spawn its own dolt server or write
            # backups from a pool session (the machine-wide rule is delivered by the
            # workspace bd wrapper, which a strict env cannot assume).
            "BEADS_DOLT_AUTO_START=0"
            "BD_BACKUP_ENABLED=0"
          ]
          ++ lib.mapAttrsToList (n: v: "${n}=${toString v}") claudeOtelEnv
        )
      ];
      keep = [
        "TMUX"
        "TMUX_PANE"
      ]
      ++ wrapSpecOwnedKeep;
    };
  claudeBaseline = mkClaudeBaseline { };

  # renderWrapSpec: `{ wrap, keep, strictEnv, exec } -> <spec file derivation>`,
  # the pg-wrap spec as TOML (`schema_version = 1` plus the four keys). `keep`
  # is the caller's own extra names; the ten module-owned names are always
  # added and the result is de-duplicated, so a caller (a `claudeBaseline`
  # helper, a command role) may list some of them again harmlessly. Exposed as
  # `lib.renderWrapSpec` so command-role specs reuse it.
  renderWrapSpec =
    {
      wrap ? [ ],
      keep ? [ ],
      strictEnv ? false,
      exec,
    }:
    tomlFormat.generate "pg-wrap-spec.toml" {
      schema_version = 1;
      inherit wrap exec;
      strict_env = strictEnv;
      keep = lib.unique (wrapSpecOwnedKeep ++ keep);
    };

  specRoles = lib.filterAttrs (_name: roleCfg: roleCfg.spec != null) cfg.roles;
  # A role's rendered spec. Choice (the design is silent): every `sessionEnv`
  # key other than PG_WRAP_SPEC (which pg-wrap-exec strips itself) joins the
  # spec's `keep`, so a variable an operator injects into the session survives
  # `strict_env` and reaches claude.
  specFiles = lib.mapAttrs (
    _name: roleCfg:
    renderWrapSpec {
      inherit (roleCfg.spec) wrap strictEnv exec;
      keep = roleCfg.spec.keep ++ lib.attrNames (removeAttrs roleCfg.sessionEnv [ "PG_WRAP_SPEC" ]);
    }
  ) specRoles;

  # commandSpecFiles (bead pg2-8puhw): a COMMAND role's pg-wrap spec, declared as
  # `roles.<name>.command.spec`. The spec has no `exec` (pg-wrap prepends it to the
  # payload, and a command role's payload is its own argv) and no pool: it carries
  # the role's static env wrappers (`wrap`), and the rendered role file's argv is
  # `[ pg-wrap run --spec <spec> -- <argv...> ]` (design section 4, "Per-item
  # values for a wrapper"). `handlerCommandDir` runs `pg-wrap check` over each one.
  commandSpecRoles = lib.filterAttrs (
    _name: roleCfg: roleCfg.type == "command" && roleCfg.command != null && roleCfg.command.spec != null
  ) cfg.roles;
  commandSpecFiles = lib.mapAttrs (
    _name: roleCfg:
    renderWrapSpec {
      inherit (roleCfg.command.spec) wrap keep strictEnv;
      exec = [ ];
    }
  ) commandSpecRoles;
  # The argv a command role's rendered file carries: the role's own, behind
  # `pg-wrap run --spec <spec> --` when it declares a `command.spec`.
  commandArgvFor =
    name: roleCfg:
    lib.optionals (commandSpecFiles ? ${name}) [
      "${pkgs.pg-wrap}/bin/pg-wrap"
      "run"
      "--spec"
      "${commandSpecFiles.${name}}"
      "--"
    ]
    ++ roleCfg.command.argv;

  ccpoolRoles = lib.filterAttrs (_name: roleCfg: roleCfg.type == "ccpool") cfg.roles;
  # The ccpool roles that share the handler-wide pool (only meaningful while
  # `pool.enable`; otherwise they run on ccpool's unmanaged default pool).
  handlerPoolRoles = lib.optionalAttrs cfg.pool.enable (
    lib.filterAttrs (_name: roleCfg: !roleCfg.ccpool.pool.enable) ccpoolRoles
  );
  # A managed pool's FINAL settings: the module's own `claude.bin` (exactly when
  # some role on the pool has a spec) merged UNDER the user's `settings`, so a
  # user-set `settings.claude.bin` overrides it and the two-way check then fires.
  withWrapBin =
    hasSpecRole: settings:
    lib.recursiveUpdate (lib.optionalAttrs hasSpecRole { claude.bin = pgWrapExecBin; }) settings;
  handlerPoolSettings = withWrapBin (lib.any (r: r.spec != null) (
    lib.attrValues handlerPoolRoles
  )) cfg.pool.settings;
  rolePoolSettings = roleCfg: withWrapBin (roleCfg.spec != null) roleCfg.ccpool.pool.settings;
  # Where a ccpool role runs, and the `claude.bin` that pool's config.toml carries.
  poolOf =
    roleCfg:
    if roleCfg.ccpool.pool.enable then
      {
        dir = roleCfg.ccpool.pool.dir;
        managed = true;
        claudeBin = (rolePoolSettings roleCfg).claude.bin or null;
      }
    else if cfg.pool.enable then
      {
        dir = cfg.pool.dir;
        managed = true;
        claudeBin = handlerPoolSettings.claude.bin or null;
      }
    else
      {
        dir = "<ccpool default pool, not managed by this module>";
        managed = false;
        claudeBin = null;
      };
  wrapPoolAssertions = lib.concatLists (
    lib.mapAttrsToList (
      name: roleCfg:
      let
        pool = poolOf roleCfg;
        onWrapExecPool = pool.managed && pool.claudeBin == pgWrapExecBin;
      in
      if roleCfg.spec != null then
        [
          {
            assertion = pool.claudeBin == pgWrapExecBin;
            message = ''
              phillipgreenii.programs.pg-router-ccpool-handler.roles.${name}: the role
              has a `spec`, but its pool (${pool.dir}) does not run
              `claude.bin = ${pgWrapExecBin}` (rendered claude.bin:
              ${if pool.claudeBin == null then "unset" else toString pool.claudeBin}), so claude would
              run unwrapped. Put the role on a pool this module manages
              (`roles.${name}.ccpool.pool.enable` or the handler-wide `pool.enable`)
              and do not override that pool's `settings.claude.bin`.
            '';
          }
        ]
      else
        [
          {
            assertion = !onWrapExecPool;
            message = ''
              phillipgreenii.programs.pg-router-ccpool-handler.roles.${name}: the role
              has no `spec`, but its pool (${pool.dir}) runs
              `claude.bin = ${pgWrapExecBin}`, which refuses every launch without
              PG_WRAP_SPEC. Give the role a `spec`, or move it to a pool without
              a spec role.
            '';
          }
        ]
    ) ccpoolRoles
  );
  # A ccpool role MUST resolve to a pool this module manages: its own
  # `ccpool.pool` or the enabled handler-wide `pool` (bead pg2-3qx9e). A role
  # on ccpool's unmanaged default pool is rejected whether or not it has a
  # spec, so a role cannot silently share the default pool's cap and reap
  # cadence with every other ccpool consumer.
  unmanagedPoolAssertions = lib.mapAttrsToList (name: roleCfg: {
    assertion = (poolOf roleCfg).managed;
    message = ''
      phillipgreenii.programs.pg-router-ccpool-handler.roles.${name}: the role
      resolves to neither its own pool (`roles.${name}.ccpool.pool.enable`) nor
      an enabled handler-wide pool (`pool.enable`), so it would run on
      ${(poolOf roleCfg).dir}. Enable one of them.
    '';
  }) ccpoolRoles;
  wrapSpecAssertions = lib.mapAttrsToList (name: roleCfg: {
    assertion = roleCfg.type == "ccpool";
    message = ''
      phillipgreenii.programs.pg-router-ccpool-handler.roles.${name}: `spec` is valid
      only on a ccpool role; a command role is wrapped through its argv.
    '';
  }) specRoles;
  commandSpecAssertions =
    lib.mapAttrsToList
      (name: roleCfg: {
        assertion = roleCfg.type == "command";
        message = ''
          phillipgreenii.programs.pg-router-ccpool-handler.roles.${name}: `command.spec` is
          valid only on a command role; a ccpool role's pg-wrap spec is `spec`.
        '';
      })
      (lib.filterAttrs (_n: roleCfg: roleCfg.command != null && roleCfg.command.spec != null) cfg.roles);
  wrapSessionEnvAssertions = lib.mapAttrsToList (name: roleCfg: {
    assertion = !(roleCfg.sessionEnv ? PG_WRAP_SPEC);
    message = ''
      phillipgreenii.programs.pg-router-ccpool-handler.roles.${name}: `sessionEnv`
      sets PG_WRAP_SPEC, but the role has a `spec`, so this module owns that value.
      Remove it from `sessionEnv`.
    '';
  }) specRoles;

  poolConfigFile = tomlFormat.generate "pg-router-ccpool-handler-pool-config.toml" handlerPoolSettings;

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
    tomlFormat.generate "pg-router-ccpool-handler-pool-config-${name}.toml" (rolePoolSettings roleCfg);
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

  # mkCandidateScan (bead pg2-tfckf): the read-only half of the sweep above --
  # classifies every direct child of the worktree dir as a candidate (clean, no
  # lock file: the ones the dry run selects) or held (not a linked worktree,
  # git operation in progress, or uncommitted changes), using the SAME checks in
  # the SAME order as mkWorktreeSweepScript. Leaves `wtdir`, the `cands` array,
  # the `held` count and the `held_paths` array set for the caller to size.
  #
  # `held` counts every skipped child (not a linked worktree, git operation in
  # progress, uncommitted changes). `held_paths` (bead pg2-0tj7h, the size
  # contract's "held, not removable") lists only the children that are
  # removable but for a safety guard -- locked or dirty worktrees. A child that
  # is not a linked worktree at all is counted in `held` but is NOT in
  # `held_paths`: the guard is not what stops its removal.
  mkCandidateScan = ''
    wtdir=${lib.escapeShellArg cfg.launchConfig.worktreeDir}
    cands=()
    held_paths=()
    held=0
    while IFS= read -r wt; do
      gitdir=$(git -C "$wt" rev-parse --git-dir 2>/dev/null) || {
        held=$((held + 1))
        continue
      }
      case "$gitdir" in
        /*) : ;;
        *) gitdir="$wt/$gitdir" ;;
      esac
      if [ -f "$gitdir/index.lock" ] || [ -f "$gitdir/HEAD.lock" ]; then
        held=$((held + 1))
        held_paths+=("$wt")
        continue
      fi
      if [ -n "$(git -C "$wt" status --porcelain 2>/dev/null)" ]; then
        held=$((held + 1))
        held_paths+=("$wt")
        continue
      fi
      cands+=("$wt")
    done < <(find "$wtdir" -mindepth 1 -maxdepth 1 -type d 2>/dev/null)
  '';

  worktreeReclaimRegistryEntry = {
    id = "pg-router-ccpool-handler-worktrees";
    description = "pg-router-ccpool-handler's per-bead git worktrees -- belt-and-suspenders net for pg2-4roho's primary dispatch-completion cleanup (internal/executor/ccpool.go's cleanupWorktree)";
    path = cfg.launchConfig.worktreeDir;
    # displayCommand and sizeCommand (bead pg2-tfckf) size ONLY the candidate
    # worktrees -- the clean ones the dry run would select -- rather than `du`
    # over the whole pool: worktrees held back by uncommitted changes or an
    # in-flight git operation are not reclaimable, and they are usually the big
    # ones, so a whole-tree `du` both overstated the reclaim and blew the
    # default display ceiling. The held ones are reported as a count only
    # (sizing them would bring back the slow `du`). Both run via a fresh
    # `bash -c` (pgdr_display_output / pgdr_size_kb), so mkCandidateScan's
    # variables need no subshell.
    #
    # Leading `:` (pg2-2wu8d): see mkWorktreeSweepScript's own doc comment
    # above -- pgdr_validate_commands_exist's leading-token check trips on a
    # first real statement that is a bare assignment rather than a command
    # name. `:` is a real no-op builtin satisfying that check first; it does
    # not change the string's own exit status, which is still the final
    # `echo`'s (displayCommand) or `awk`'s / `echo`'s (sizeCommand).
    displayCommand = ''
      :
      ${mkCandidateScan}
      total=$(find "$wtdir" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l | tr -d ' ')
      kb=0
      if [ "''${#cands[@]}" -gt 0 ]; then
        kb=$(du -sk "''${cands[@]}" 2>/dev/null | awk '{s += $1} END {print s + 0}')
      fi
      human=$(awk -v k="$kb" 'BEGIN {
        if (k >= 1048576) printf "%.1fG", k / 1048576
        else if (k >= 1024) printf "%.1fM", k / 1024
        else printf "%dK", k
      }')
      echo "$total worktree(s): ''${#cands[@]} reclaimable ($human), $held held (uncommitted changes or git operation in progress)"
    '';
    # `git status` per worktree plus `du` over the candidates: slow on a large
    # monorepo, so both the `list` display and the `reclaim` sizing get a
    # roomier per-item ceiling than the CLI defaults.
    displayTimeoutSeconds = 180;
    # Reclaimable size, in KiB, of the candidate worktrees (the CLI's
    # sizeCommand contract: the first field of the first output line).
    sizeCommand = ''
      :
      ${mkCandidateScan}
      if [ "''${#cands[@]}" -gt 0 ]; then
        du -sk "''${cands[@]}" 2>/dev/null | awk '{s += $1} END {print s + 0}'
      else
        echo 0
      fi
    '';
    # Size contract (bead pg2-0tj7h): sizeCommand is exact (`du` over exactly
    # the candidates the sweep removes), so sizeKind stays at its default. The
    # dirty and locked worktrees the sweep skips are the removable-but-guarded
    # data: heldSizeCommand sizes them (KiB, first line) so a "0K" reclaim next
    # to 4.6G held is visible rather than silent.
    heldSizeCommand = ''
      :
      ${mkCandidateScan}
      if [ "''${#held_paths[@]}" -gt 0 ]; then
        du -sk "''${held_paths[@]}" 2>/dev/null | awk '{s += $1} END {print s + 0}'
      else
        echo 0
      fi
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

  # mkPoolMetricsScript (bead pg2-mr0sl, reworked by pg2-om899.6): the
  # `poolMetrics` timer's ExecStart -- one `--pool <name>=<dir>` per role with
  # `ccpool.pool.enable` (derived from `ccpoolRolesWithOwnPool`, defined
  # further down once `cfg.roles` is in scope). `pool-capacity` now makes
  # ccpool push the ccpool_pool_capacity OTLP gauge itself (pool = basename
  # of the resolved dir); the OTEL_* env comes from the caller (the darwin
  # LaunchAgent's obs.mkEmitterEnv). The former pool-capacity.prom writer
  # (mktemp with no trap, leaking a temp file on every failed run) is gone,
  # so no shell redirection is needed -- but the script is kept as a
  # derivation so the systemd/LaunchAgent wiring and its eval checks are
  # unchanged.
  #
  # The handler-wide pool (`pool.enable`, dir `pool.dir`) is reported too,
  # under the display label "handler" (bead pg2-h5vno). It is the pool every
  # role WITHOUT its own `ccpool.pool` dispatches into (e.g. the
  # escalation-triager roles), so without it
  # `ccpool_pool_capacity{pool="pg-router-ccpool"}` (pool = basename of
  # `pool.dir`) was never emitted and that pool's capacity was invisible.
  mkPoolMetricsScript =
    let
      rolePoolArgs = lib.concatMapStrings (
        name: " --pool ${name}=${lib.escapeShellArg cfg.roles.${name}.ccpool.pool.dir}"
      ) (lib.attrNames ccpoolRolesWithOwnPool);
      handlerPoolArg = lib.optionalString cfg.pool.enable " --pool handler=${lib.escapeShellArg cfg.pool.dir}";
      poolArgs = rolePoolArgs + handlerPoolArg;
    in
    pkgs.writeShellScript "pg-router-ccpool-handler-pool-metrics" ''
      set -eu
      exec ${cfg.package}/bin/pg-router-ccpool-handler pool-capacity${poolArgs}
    '';

  # toolSetGrants (bead pg2-cwn44): the grants of one NAMED tool set from
  # `cfg.toolSets`. An undeclared name is an eval-time error rather than a
  # silently empty grant, so a typo in a role's `toolSets` can never leave the
  # role without the tools it believes it has (nor, the other way round, hide a
  # set that was removed).
  toolSetGrants =
    name:
    cfg.toolSets.${name}
      or (throw "phillipgreenii.programs.pg-router-ccpool-handler: unknown tool set \"${name}\" (declared: ${lib.concatStringsSep ", " (lib.attrNames cfg.toolSets)})");

  # roleExtraAllowedTools (bead pg2-cwn44): what a role's rendered role file
  # carries as `extraAllowedTools` -- the role's own list plus its declared tool
  # sets, in that order, first occurrence wins.
  roleExtraAllowedTools =
    ccpoolCfg:
    lib.unique (ccpoolCfg.extraAllowedTools ++ lib.concatMap toolSetGrants ccpoolCfg.toolSets);

  # checkRenderedScript (bead pg2-12gr4.8.32): `check-rendered [--spec <file>]...
  # [--role <file>]...`, the build-time check of what this module renders. Each
  # `--spec` runs pg-wrap's own `pg-wrap check --spec <file>`; each `--role` runs
  # the handler's own `validate --skip-pool-dirs --role-config <file>` (the nix
  # sandbox has no pool directories). The two tools run side by side and neither
  # calls the other. Every failing file is printed with its messages; the exit
  # is 1 if any check failed, else 0. A `validate` run fails when it exits
  # non-zero, or its reply is not parsable or has a non-empty `problems`;
  # warnings are printed and never fail.
  checkRenderedScript = pkgs.writeShellScript "check-rendered" ''
    PATH=${lib.makeBinPath [ pkgs.coreutils ]}
    jq=${pkgs.jq}/bin/jq
    pgwrap=${pkgs.pg-wrap}/bin/pg-wrap
    handler=${cfg.package}/bin/pg-router-ccpool-handler
    failed=0
    while [ $# -gt 0 ]; do
      kind="$1"
      case "$kind" in
        --spec | --role) ;;
        *)
          echo "check-rendered: unknown argument: $kind" >&2
          exit 2
          ;;
      esac
      if [ $# -lt 2 ]; then
        echo "check-rendered: $kind needs a file" >&2
        exit 2
      fi
      file="$2"
      shift 2
      if [ "$kind" = --spec ]; then
        if ! out="$("$pgwrap" check --spec "$file" 2>&1)"; then
          echo "check-rendered: FAIL spec $file:" >&2
          echo "$out" >&2
          failed=1
        fi
      else
        rc=0
        errf="$(mktemp)"
        reply="$(printf '%s' '{"schemaVersion":"1","id":"check-rendered"}' |
          "$handler" validate --skip-pool-dirs --role-config "$file" 2>"$errf")" || rc=$?
        msgs=""
        if [ "$rc" -ne 0 ]; then
          msgs="validate exited $rc: $reply $(cat "$errf")"
        elif ! msgs="$(printf '%s' "$reply" | "$jq" -r '
            if (.problems | type) == "array" then .problems[] | "problem: " + .
            else error("reply has no problems array") end' 2>&1)"; then
          msgs="unparsable validate reply: $reply $msgs"
        fi
        rm -f "$errf"
        if [ -n "$msgs" ]; then
          echo "check-rendered: FAIL role $file:" >&2
          echo "$msgs" >&2
          failed=1
        fi
        # Warnings are printed and never fail.
        warns="$(printf '%s' "$reply" | "$jq" -r '(.warnings // [])[] | "warning: " + .' 2>/dev/null || true)"
        if [ -n "$warns" ]; then
          echo "check-rendered: role $file:" >&2
          echo "$warns" >&2
        fi
      fi
    done
    exit "$failed"
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
          # survivesShutdown (bead pg2-dtigc, ADR 0085): a ccpool-backed role's
          # handler supervises its session independently of the daemon, so the
          # daemon MUST NOT cancel its dispatch when it shuts down (a restart
          # leaves the session and its dispatch running; the next daemon
          # re-offers the event and absorbs the live session). Derived from
          # `type`, not a user option: a command-type role stays cancellable
          # (short, and killing a stuck one is the point). pg-router reads
          # only this one key of the file; the Go `roleFile` ignores it.
          survivesShutdown = true;
          # sessionEnv (design ruling 30): the role's own entries plus, for a
          # role with a spec, the module-owned PG_WRAP_SPEC (right operand
          # wins; a role-set PG_WRAP_SPEC also fails an assertion).
          sessionEnv =
            roleCfg.sessionEnv
            // lib.optionalAttrs (roleCfg.spec != null) { PG_WRAP_SPEC = "${specFiles.${name}}"; };
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
            budgetStopEscalateAfter = roleCfg.ccpool.budgetStopEscalateAfter;
            isolation = {
              Type = roleCfg.ccpool.isolation.type;
              Path = roleCfg.ccpool.isolation.path;
            }
            // lib.optionalAttrs roleCfg.ccpool.isolation.prefetch.enable {
              # prefetch (bead pg2-hh32y): omitted unless enabled, so a role that
              # never opts in decodes `Prefetch == nil` (unchanged behavior).
              # Field names are Go's own (roles.PrefetchConfig has no json tags).
              Prefetch = {
                Remote = roleCfg.ccpool.isolation.prefetch.remote;
                Refspec = roleCfg.ccpool.isolation.prefetch.refspec;
                Rev = roleCfg.ccpool.isolation.prefetch.rev;
                DiffBase = roleCfg.ccpool.isolation.prefetch.diffBase;
                Timeout = roleCfg.ccpool.isolation.prefetch.timeout;
              };
            };
          }
          // lib.optionalAttrs (roleCfg.ccpool.worktreeQuietWindow != null) {
            # worktreeQuietWindow (bead pg2-uyahp, INV-CCH-23): omitted when null, so
            # a role that never sets it decodes to "no override" (the handler-wide
            # 2m default applies, unchanged behavior).
            inherit (roleCfg.ccpool) worktreeQuietWindow;
          }
          // lib.optionalAttrs (roleCfg.ccpool.effort != null) {
            # effort (bead pg2-svkob, INV-CCH-31): omitted when null, so a role that
            # never sets it decodes to "no override" (the launch-config effort
            # applies, unchanged behavior).
            inherit (roleCfg.ccpool) effort;
          }
          // lib.optionalAttrs (roleCfg.ccpool.precheck != null) {
            # precheck (bead pg2-nk6th.3): omitted when null, so a role that never
            # opts in decodes to "" (the historical behavior). The role file is
            # rendered from an EXPLICIT field list above plus these optionalAttrs,
            # not the whole option set: an option that is not added here is
            # silently dropped.
            inherit (roleCfg.ccpool) precheck;
          }
          // lib.optionalAttrs (roleExtraAllowedTools roleCfg.ccpool != [ ]) {
            # extraAllowedTools (bead pg2-nk6th.5): omitted when empty, so a role
            # that never sets it decodes to "no extra grants" (the handler-wide
            # allowedTools applies unchanged). Carries the role's own
            # `extraAllowedTools` followed by the grants of every named tool set
            # the role declares in `toolSets` (bead pg2-cwn44), de-duplicated;
            # the handler merges them onto the handler-wide list for THIS role
            # only, so a role that declares no set never gains its grants.
            extraAllowedTools = roleExtraAllowedTools roleCfg.ccpool;
          }
          // lib.optionalAttrs (roleCfg.ccpool.beadsDir != "") {
            # beadsDir (bead pg2-2grpj): omitted when unset, like poolDir.
            inherit (roleCfg.ccpool) beadsDir;
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
            argv = commandArgvFor name roleCfg;
          };
          # strictBaseEnv (design ruling 30): command roles only; a ccpool
          # role's minimal env comes from its spec's `strict_env`.
          inherit (roleCfg) strictBaseEnv;
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
  #
  # Build-time check (bead pg2-12gr4.8.32): the directory is built by a
  # derivation that first runs `checkRenderedScript` over every role file and
  # every rendered spec (`specFiles` and `commandSpecFiles`), so a spec that fails `pg-wrap check` or a
  # role file that fails the handler's `validate` fails the build of
  # `handlerCommandDir`, and with it everything that consumes it. The layout is
  # unchanged: one `<role>.json` symlink per role and nothing else.
  handlerCommandDir = pkgs.runCommand "pg-router-ccpool-handler-roles" { } (
    ''
      ${checkRenderedScript} ${
        lib.concatStringsSep " " (
          (lib.mapAttrsToList (_name: file: "--spec ${file}") specFiles)
          ++ (lib.mapAttrsToList (_name: file: "--spec ${file}") commandSpecFiles)
          ++ (lib.mapAttrsToList (name: roleCfg: "--role ${roleFileFor name roleCfg}") cfg.roles)
        )
      }
      mkdir -p "$out"
    ''
    + lib.concatStringsSep "\n" (
      lib.mapAttrsToList (
        name: roleCfg: "ln -s ${roleFileFor name roleCfg} $out/${lib.escapeShellArg "${name}.json"}"
      ) cfg.roles
    )
  );

  # defaultAllowedTools (this bead, pg2-qsred): mirrors
  # internal/config.baseAllowedTools's own literal value exactly.
  # internal/config.Default() sets its AllowedTools field to that constant.
  #
  # pg-connector issue */ccpool * (bead pg2-s9zh5): widened alongside
  # packages/pg-router-ccpool-handler/internal/config.go's own
  # baseAllowedTools (see that constant's doc comment for the full
  # rationale) -- every dispatched role shares this ONE process-wide
  # allowlist (a per-role extraAllowedTools has existed since pg2-nk6th.5, but
  # was not used for this fix), so a triager-shaped role's own dispatch prompt
  # instructing it to use `pg-connector issue show/comment/update/close` and
  # `ccpool list/state/reply/tail` had no path to those verbs under
  # PermissionMode=dontAsk (auto-deny, no prompt possible).
  #
  # Code-executing toolchain grants (go build|test|vet|mod, gofmt) are NOT here
  # (bead pg2-cwn44), and neither are prek and pre-commit (bead pg2-bg9qq: they
  # run the repo-defined hook code): they live in the named `toolSets.go` and
  # `toolSets.hooks` sets, which a role opts into with
  # `roles.<name>.ccpool.toolSets` (see that option).
  defaultAllowedTools = "Read,Edit,Write,Glob,Grep,Bash(git status:*),Bash(git diff:*),Bash(git log:*),Bash(git add:*),Bash(git commit:*),Bash(git checkout:*),Bash(git switch:*),Bash(git branch:*),Bash(git worktree:*),Bash(git rev-parse:*),Bash(git fetch:*),Bash(bd:*),Bash(nix flake check:*),Bash(nix fmt:*),Bash(pg-connector issue show:*),Bash(pg-connector issue comment:*),Bash(pg-connector issue update:*),Bash(pg-connector issue close:*),Bash(ccpool list:*),Bash(ccpool state:*),Bash(ccpool reply:*),Bash(ccpool tail:*)";

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
      # originProbe (bead pg2-4gi2c, INV-CCH-10): the per-origin availability
      # probe/decline gate. Durations render as nanosecond integers for the same
      # reason confirmIngest does. `remote` is omitted when empty so the Go
      # side's own default ("origin") applies.
      originProbe = {
        origins = map (
          o:
          {
            inherit (o) key repoRoot;
          }
          // lib.optionalAttrs (o.remote != "") { inherit (o) remote; }
        ) cfg.launchConfig.originProbe.origins;
        inherit (cfg.launchConfig.originProbe) failureThreshold stateDir;
        ttl = cfg.launchConfig.originProbe.ttlSeconds * 1000000000;
        timeout = cfg.launchConfig.originProbe.timeoutSeconds * 1000000000;
      };
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
                  "close-or-triage"
                  "close-or-split-triage"
                  "close-or-release"
                ];
                description = ''
                  Bead-done semantics (`roleFile.CCPool.Completion` / `roles.Completion`).
                  `close-or-release` (bead pg2-nk6th.3, INV-CCH-28) is for a role whose
                  session claims its bead and MAY give it back unfinished (a drain
                  worker): done on close, or once the session has ENDED (not merely
                  idle) with the bead open or deferred and unassigned; a session that
                  ended without claiming earns a two-strike unclaimed end.
                '';
              };
              precheck = lib.mkOption {
                type = lib.types.nullOr (
                  lib.types.enum [
                    "review"
                    "ready"
                    "triage"
                  ]
                );
                default = null;
                description = ''
                  Opts this role in to a zero-model-cost dispatch precheck
                  (`roleFile.CCPool.Precheck` / `roles.Precheck`, bead pg2-nk6th.3,
                  INV-CCH-22): `review` (bead closed, PR merged, pending review
                  already covers the head), `ready` (for a role that claims its
                  own bead: the bead must still be open, unclaimed, groomed, not
                  for a human, not deferred and not blocked; a bead the role's own
                  actor holds `in_progress` is never declined) or `triage` (for a
                  `close-or-triage` role, which works a bead it does not claim: the
                  bead must still match the role's own discovery query, so it is
                  declined when it is not open, is deferred or blocked, carries
                  `human`, or no longer carries `escalated`; bead pg2-p9lcg).
                  The handler that reads the rendered role file MUST be deployed
                  first, since it rejects a precheck value it does not know.
                  Default `null`
                  leaves the historical behavior: only the role NAMED `review` is
                  prechecked. Omitted from the rendered role file when `null`.
                '';
              };
              beadsDir = lib.mkOption {
                type = lib.types.str;
                default = "";
                description = ''
                  Workspace directory of the bd tracker this role's items live
                  in, when that is NOT the handler's own repoRoot tracker
                  (`roleFile.CCPool.BeadsDir`, bead pg2-2grpj) -- e.g. the pg2
                  tracker for the escalation triager. Every handler-side bd
                  call for the role (completion polling, on_failure, unclaim,
                  comment) resolves against it. Empty (default) keeps repoRoot.
                '';
              };
              extraAllowedTools = lib.mkOption {
                type = lib.types.listOf lib.types.str;
                default = [ ];
                example = [
                  "Skill"
                  "Bash(git push:*)"
                  "Bash(gh pr create:*)"
                ];
                description = ''
                  Per-role tool grants (`roleFile.CCPool.ExtraAllowedTools`, bead
                  pg2-nk6th.5), merged onto the handler-wide
                  `launchConfig.allowedTools` (+ `launchConfig.extraAllowedTools`)
                  for THIS role's `ccpool new --allowed-tools` only: the
                  handler-wide grants come first, then these in order, with
                  duplicates dropped. Each entry is one grant in the same syntax
                  `allowedTools` uses. Default `[ ]` adds nothing, leaving the
                  handler-wide list unchanged for the role.

                  Put a grant only one role needs here, never in the handler-wide
                  list, which would widen every role. In particular `git push`
                  MUST NOT go into the handler-wide list. Prefix grants are
                  coarse: `Bash(git -C:*)` permits `git -C <path> push`, and
                  `Bash(gh pr:*)` also permits `gh pr merge`/`edit`, so prefer
                  narrow grants such as `Bash(gh pr create:*)`; a "no push / no
                  merge" rule for a prefix grant stays prompt-enforced only.
                '';
              };
              toolSets = lib.mkOption {
                type = lib.types.listOf lib.types.str;
                default = [ ];
                example = [ "go" ];
                description = ''
                  Named tool sets this role declares (bead pg2-cwn44), each a key
                  of the module-level `toolSets` option. The grants of every
                  declared set are appended to this role's rendered
                  `extraAllowedTools`, so they reach THIS role's
                  `ccpool new --allowed-tools` only. Default `[ ]` declares none:
                  the role then holds the handler-wide list alone, which carries
                  no code-executing toolchain grant (no `go test`, no `gofmt`).
                  Declare a set only on a role that works trusted trees with that
                  toolchain; a role that handles untrusted PR content MUST NOT
                  (agent-support ADR 0035). An undeclared name is an eval error.
                '';
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
              budgetStopEscalateAfter = lib.mkOption {
                type = lib.types.ints.unsigned;
                default = 3;
                description = ''
                  Per-bead budget-stop threshold (`roleFile.CCPool.BudgetStopEscalateAfter`,
                  TOML key `budget_stop_escalate_after`, bead pg2-6akgz): each watchdog
                  hard stop records a per-session `budget-stop:<session>` label on the
                  bead before unclaiming it, and comments `budget stop <n> of <threshold>
                  (session <id>)`. When the count of distinct stop sessions reaches
                  this threshold the handler labels the bead `escalated` (feeding the
                  deployment's escalated-work triager; a triage role's own stop goes to
                  `human` instead, bead pg2-cty80). Budgets are NEVER changed by it. `0`
                  disables the record entirely (kill switch).
                '';
              };
              worktreeQuietWindow = lib.mkOption {
                type = lib.types.nullOr lib.types.str;
                default = null;
                example = "30s";
                description = ''
                  Per-role override of the worktree quiet window
                  (`roles.CCPoolConfig.WorktreeQuietWindow`, bead pg2-uyahp, INV-CCH-23): how
                  long the finished session and its Agent-tool subagents' transcripts must
                  show no write before the handler removes the worktree and closes the
                  settled session. Until then the idle session still counts against the
                  pool's `max_sessions`. A Go duration string (e.g. `"30s"`). `null` (the
                  default) keeps the handler-wide 2m window, unchanged. Must be at least
                  2 x the handler poll interval (20s by default) and at most the handler
                  wait bound (10m). A shorter window frees the slot sooner but risks
                  mistaking a silent-but-running subagent for a finished one, so set it
                  only on a role with few subagents (review).
                '';
              };
              effort = lib.mkOption {
                type = lib.types.nullOr (
                  lib.types.enum [
                    "low"
                    "medium"
                    "high"
                    "xhigh"
                    "max"
                  ]
                );
                default = null;
                example = "high";
                description = ''
                  Per-role override of the claude `--effort` level
                  (`roles.CCPoolConfig.Effort`, bead pg2-svkob, INV-CCH-31) for this
                  role's launched sessions only. `null` (the default) keeps the
                  launch-config `effort` (`"max"` by default), unchanged. The handler
                  reads role files at start, so a change needs an apply AND a handler
                  restart, and a session already running keeps the effort it started
                  with. Does not touch `model`.
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
                    prefetch = {
                      enable = lib.mkEnableOption ''
                        handler-side worktree pre-fetch (bead pg2-hh32y): after the
                        worktree exists and BEFORE the session launches, the handler
                        fetches the item's commit if it is not local, checks the worktree
                        out detached at it, and writes the numstat and diff to files under
                        `<worktree>/.pg-router/`. The outcome reaches the prompt as
                        `{{.Prefetch.OK}}`, `{{.Prefetch.Rev}}`, `{{.Prefetch.DiffFile}}`
                        and `{{.Prefetch.NumstatFile}}`. Best effort: a failure is logged
                        and surfaces as `OK == false`, so the prompt keeps its own
                        fallback. Valid only with worktree isolation
                      '';
                      remote = lib.mkOption {
                        type = lib.types.str;
                        default = "";
                        description = "Git remote the `refspec` is fetched from (a template over the dispatch context). \"\" means `origin`.";
                      };
                      refspec = lib.mkOption {
                        type = lib.types.str;
                        default = "";
                        example = ''pull/{{index .Item.Metadata "pr_number"}}/head'';
                        description = "Ref to fetch when `rev` is not already a local commit (a template). \"\" disables the fetch: `rev` must then already be local.";
                      };
                      rev = lib.mkOption {
                        type = lib.types.str;
                        default = "";
                        example = ''{{index .Item.Metadata "head_sha"}}'';
                        description = "Commit the worktree is checked out at, detached (a template). Required when `enable`; must render to a 7-64 digit hex object id.";
                      };
                      diffBase = lib.mkOption {
                        type = lib.types.str;
                        default = "";
                        example = "origin/main";
                        description = "Base for the numstat/diff files, taken as `git diff <base>...<rev>` (a template). \"\" writes neither file.";
                      };
                      timeout = lib.mkOption {
                        type = lib.types.str;
                        default = "";
                        example = "5m";
                        description = "Go duration bounding the whole pre-fetch. \"\" means 5m.";
                      };
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
            options.spec = lib.mkOption {
              type = lib.types.nullOr (
                lib.types.submodule {
                  options = {
                    wrap = lib.mkOption {
                      type = lib.types.listOf (lib.types.either lib.types.str (lib.types.listOf lib.types.str));
                      default = [ ];
                      description = ''
                        pg-wrap's `wrap` chain, outermost first (a string entry is
                        shell-parsed, a list is an argv entry; the first word or element of
                        each MUST be an absolute path). The usual entry is one argv entry
                        `[ "''${pkgs.coreutils}/bin/env" "PATH=..." "NAME=value" ]`.
                      '';
                    };
                    keep = lib.mkOption {
                      type = lib.types.listOf lib.types.str;
                      default = [ ];
                      description = ''
                        Extra variable names `strictEnv` keeps, on top of the ten this
                        module always adds.
                      '';
                    };
                    strictEnv = lib.mkOption {
                      type = lib.types.bool;
                      default = false;
                      description = ''
                        The spec's `strict_env`: start the wrap chain from pg-wrap's minimal
                        environment. Independent of the role's `strictBaseEnv`, which the
                        handler applies before pg-wrap starts; a family's rollout flips both.
                      '';
                    };
                  };
                }
              );
              default = null;
              description = ''
                This command role's pg-wrap spec (design ruling 30, bead pg2-8puhw), or
                `null` for none. A role with a spec gets a rendered spec file (no `exec`)
                and its rendered argv becomes `pg-wrap run --spec <spec> -- <argv...>`;
                `handlerCommandDir` runs `pg-wrap check` over the spec, so a broken spec
                fails the build. Valid only on a `type = "command"` role.
              '';
            };
          }
        );
        default = null;
        description = ''
          This role's bare-command argv -- required (non-null) iff `type` ==
          "command" (`roleFile.Command`).
        '';
      };
      strictBaseEnv = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Rendered as the role JSON's `strictBaseEnv` key (`roleFile.StrictBaseEnv`):
          start a COMMAND role's argv under the minimal base environment. Rendered
          only into a `type = "command"` role's JSON; the handler ignores the key on
          a ccpool role, whose minimal environment comes from its `spec`
          (`strictEnv`) instead, so a ccpool role's JSON never carries it.
        '';
      };
      sessionEnv = lib.mkOption {
        type = lib.types.attrsOf lib.types.str;
        default = { };
        description = ''
          Rendered as the role JSON's `sessionEnv` key (`roleFile.SessionEnv`): extra
          environment passed to the role's ccpool session (`ccpool new --env`).
          Rendered only into a `type = "ccpool"` role's JSON. The handler refuses a
          key that collides with a participant marker (`BEADS_ACTOR`, `BEADS_DIR`,
          `WORKSPACE_ROOT`, any `PG_ROUTER_*`), an empty key or one containing `=`.

          A role with a `spec` also carries `PG_WRAP_SPEC=<its spec file>` here, added
          by this module. Setting `sessionEnv.PG_WRAP_SPEC` yourself on a role that has
          a `spec` fails evaluation (the module owns that value).

          Choice (the design is silent): under a spec's `strict_env`, every other
          `sessionEnv` key is added to that spec's `keep`, because an operator who
          injects a variable into the role's session means it to reach claude;
          dropping it silently would defeat the option.
        '';
      };
      spec = lib.mkOption {
        type = lib.types.nullOr (
          lib.types.submodule {
            options = {
              wrap = lib.mkOption {
                type = lib.types.listOf (lib.types.either lib.types.str (lib.types.listOf lib.types.str));
                default = [ ];
                description = ''
                  pg-wrap's `wrap` chain, outermost first. A string entry is
                  shell-parsed by pg-wrap; a list is an argv entry. The first word
                  of every string entry and the first element of every array entry
                  MUST be an absolute path (render from store paths and
                  `lib.escapeShellArg` string-entry arguments).
                '';
              };
              keep = lib.mkOption {
                type = lib.types.listOf lib.types.str;
                default = [ ];
                description = ''
                  Extra variable names the minimal environment keeps, on top of the
                  ten this module always adds (the participant markers, the item
                  context and ccpool's four injected markers).
                '';
              };
              strictEnv = lib.mkOption {
                type = lib.types.bool;
                default = false;
                description = ''
                  The spec's `strict_env`: start the wrap chain from the minimal
                  environment instead of the inherited one. A rollback switch,
                  flipped per family by a later change.
                '';
              };
              exec = lib.mkOption {
                type = lib.types.listOf lib.types.str;
                default = [ "${pkgs.llm-agentsPkgs.claude-code}/bin/claude" ];
                defaultText = lib.literalExpression ''[ "''${pkgs.llm-agentsPkgs.claude-code}/bin/claude" ]'';
                description = ''
                  The spec's `exec`: the payload argv prefix (absolute first
                  element). `pg-wrap-exec` appends claude's own flags to it.
                '';
              };
            };
          }
        );
        default = null;
        description = ''
          This role's pg-wrap spec (design ruling 30), or `null` for none. Valid only
          on a ccpool role (a command role declares `command.spec`, which wraps its
          argv; a `spec` on a command role fails evaluation). A role with a spec gets a rendered spec file, the
          `PG_WRAP_SPEC` entry in its `sessionEnv`, and `claude.bin = pg-wrap-exec`
          in the config.toml of the pool it runs on; evaluation fails when that pool's
          rendered `claude.bin` is anything else, and when a role WITHOUT a spec runs
          on a pool whose rendered `claude.bin` is `pg-wrap-exec` (every launch there
          would decline).
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

    # toolSets (bead pg2-cwn44, operator ruling on pg2-mh0kd, 2026-10-14): named
    # sets of tool grants a role opts into with `roles.<name>.ccpool.toolSets`,
    # rather than the handler-wide `launchConfig.allowedTools` every role shares.
    # Nix-only by design: a set renders into the role's own `extraAllowedTools`
    # (bead pg2-nk6th.5), which the handler already merges per role, so no Go
    # config mechanism backs it.
    toolSets = lib.mkOption {
      type = lib.types.attrsOf (lib.types.listOf lib.types.str);
      default = { };
      description = ''
        Named tool sets, keyed by set name, each a list of grants in the same
        syntax `launchConfig.allowedTools` uses. A role opts into a set by name
        in `roles.<name>.ccpool.toolSets`; the set's grants are then rendered into
        that role's `extraAllowedTools`. The module defines the `go` set (at
        `mkDefault` priority, so a consumer may define further sets without
        losing it, or replace it): `go`, the Go toolchain's code-executing verbs
        (go build/test/vet/mod, gofmt, gofumpt), and `hooks`, the repo hook
        runners (prek, pre-commit; bead pg2-bg9qq), both deliberately NOT in the
        handler-wide list. Granting a command does not put it on PATH: a role
        whose spec is built from `claudeBaseline` adds the set's binaries with
        `claudeBaseline.extend { extraPathPkgs = [ ... ]; }` (the check
        test-claude-baseline-role-tool-commands fails when they are absent); a
        role without a spec gets them from the primary user's home-manager profile.
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

    # lib.renderWrapSpec / specFiles / poolConfigFiles (design ruling 30, bead
    # pg2-12gr4.8.12): read-only outputs, set unconditionally like
    # `handlerCommandDir`, so a consumer or a check can read them without this
    # module's own machinery enabled.
    lib.renderWrapSpec = lib.mkOption {
      type = lib.types.raw;
      readOnly = true;
      description = ''
        Read-only function `{ wrap, keep, strictEnv, exec } -> <spec file
        derivation>`: renders a pg-wrap spec (`schema_version = 1` plus the four
        keys) as TOML. The ten module-owned `keep` names are always added and the
        result is de-duplicated. Command-role specs reuse it.
      '';
    };

    lib.checkRenderedScript = lib.mkOption {
      type = lib.types.package;
      readOnly = true;
      description = ''
        Read-only executable script `check-rendered [--spec <file>]... [--role
        <file>]...`. Each `--spec` runs `pg-wrap check --spec <file>`; each
        `--role` runs the handler's `validate --skip-pool-dirs --role-config
        <file>` (the request on stdin). It prints every failing file with its
        messages, then exits 1 if any check failed and 0 otherwise. A `validate`
        run fails when it exits non-zero or its reply is unparsable or has a
        non-empty `problems`; warnings are printed and never fail.
        `handlerCommandDir` runs it over every role file and every `specFiles`
        entry, so a failing file fails the build.
      '';
    };

    commandSpecFiles = lib.mkOption {
      type = lib.types.attrsOf lib.types.package;
      readOnly = true;
      description = ''
        Read-only output: command role name -> that role's rendered pg-wrap spec
        file (`roles.<name>.command.spec`), for every command role with one.
        `handlerCommandDir` runs `pg-wrap check` over each.
      '';
    };

    specFiles = lib.mkOption {
      type = lib.types.attrsOf lib.types.package;
      readOnly = true;
      description = ''
        Read-only output: role name -> that role's rendered pg-wrap spec file, for
        every role with a `spec`.
      '';
    };

    claudeBaseline = lib.mkOption {
      type = lib.types.raw;
      readOnly = true;
      description = ''
        Read-only INTERIM helper (design ruling 20): `{ wrap; keep; pathDirs;
        systemDirs; extend; }` covering everything claude needs to run in a pool
        session (claude, the enabled plugins' bare hook commands, the prompts'
        tools including git and nix, the system directories, the OTel endpoint
        and protocol without OTEL_SERVICE_NAME). Use as `roles.<name>.spec = {
        inherit (cfg.claudeBaseline) wrap keep; strictEnv = ...; }`. `wrap` is a
        single env PATH=... entry (store directories, then the system directories
        /usr/bin:/bin:/usr/sbin:/sbin; it also pins BEADS_DOLT_AUTO_START=0 and
        BD_BACKUP_ENABLED=0), so a consumer may append its own entries after it;
        `pathDirs` is the list of store bin directories and `systemDirs` the system
        ones. `extend { bd ? <llm-agents beads>; extraPathPkgs ? [ ]; }` returns
        the same shape with another bd and/or the role's own toolchain packages on
        PATH (a role that declares a tool set, e.g. `go`, adds its binaries here).
        `pn` joins the PATH only when the consumer's `pkgs` carries `pkgs.pn`.
        Set unconditionally, like the other read-only outputs.
      '';
    };

    poolConfigFiles = lib.mkOption {
      type = lib.types.attrsOf lib.types.package;
      readOnly = true;
      description = ''
        Read-only output: pool name -> the `config.toml` this module installs into
        that pool, for every pool it manages (the role name for a per-role pool;
        `handler` for the handler-wide pool). Carries `claude.bin` as rendered.
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
          `internal/config.Default()`'s own value exactly. A deployment that
          needs further grants adds them through `extraAllowedTools`.
        '';
      };
      extraAllowedTools = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = ''
          Additive tool grants appended (comma-joined) onto `allowedTools`'s
          own value when rendering the launch-config JSON -- each entry is a
          single grant in the same syntax `allowedTools` uses, e.g.
          `"Bash(pg-connector pr review:*)"`. Exists (pg2-yybrp) so a deployment that
          needs a few extra grants (e.g. for `pg-connector`/`pg-desk`
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
      originProbe = {
        origins = lib.mkOption {
          type = lib.types.listOf (
            lib.types.submodule {
              options = {
                key = lib.mkOption {
                  type = lib.types.strMatching "[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+";
                  example = "git.example.com/org/repo";
                  description = ''
                    The normalized repo key `<host>/<org>/<repo>`. Names the origin
                    in its state file, logs, and the `origin status|ignore`
                    subcommands.
                  '';
                };
                repoRoot = lib.mkOption {
                  type = lib.types.str;
                  description = ''
                    The checkout the probe runs `git ls-remote` in. A dispatch
                    whose repo root equals this path (worktree/none isolation:
                    `launchConfig.repoRoot`; path isolation: the role's fixed
                    path) is declined while the origin is unavailable. A role
                    whose repo root matches no entry is never gated.
                  '';
                };
                remote = lib.mkOption {
                  type = lib.types.str;
                  default = "";
                  description = "Remote name or URL for `git ls-remote`. Empty means `origin`.";
                };
              };
            }
          );
          default = [ ];
          description = ''
            OriginProbe.Origins in the launch config: git origins the handler
            probes before accepting a dispatch (INV-CCH-10). An unavailable
            origin (mount missing, ssh-agent/step cert unusable, network down,
            timeout) makes the handler decline dispatches into THAT repo with
            busy reason `origin-unavailable`; other repos keep dispatching.
            Empty (the default) disables the feature.
          '';
        };
        failureThreshold = lib.mkOption {
          type = lib.types.ints.positive;
          default = 2;
          description = "OriginProbe.FailureThreshold (K): consecutive failed probes before an origin is gated. Matches the Go default.";
        };
        ttlSeconds = lib.mkOption {
          type = lib.types.ints.unsigned;
          default = 60;
          description = ''
            OriginProbe.TTL in SECONDS (rendered as nanoseconds): how long a
            probe result is reused before the next dispatch re-probes.
          '';
        };
        timeoutSeconds = lib.mkOption {
          type = lib.types.ints.positive;
          default = 20;
          description = "OriginProbe.Timeout in SECONDS (rendered as nanoseconds): the hard bound on one `git ls-remote`.";
        };
        stateDir = lib.mkOption {
          type = lib.types.str;
          default = "";
          description = ''
            OriginProbe.StateDir. Empty resolves to
            `$XDG_STATE_HOME/pg-router-ccpool-handler`, else
            `~/.local/state/pg-router-ccpool-handler`; per-origin state lives in
            its `origin-state/` subdirectory.
          '';
        };
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
        `ccpool.pool.enable` set, plus the handler-wide `pool` when
        `pool.enable` is set, making ccpool emit the
        `ccpool_pool_capacity` OTLP gauge (needs OTEL_* env; the darwin
        LaunchAgent supplies it via obs.mkEmitterEnv, the Linux systemd
        unit does not). On darwin,
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
      script = lib.mkOption {
        type = lib.types.package;
        readOnly = true;
        description = ''
          Read-only output: the rendered pool-capacity script
          (`mkPoolMetricsScript`) -- one `--pool <name>=<dir>` per role with
          `ccpool.pool.enable`, plus `--pool handler=<pool.dir>` when
          `pool.enable`, which triggers ccpool's OTLP capacity emission.
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
        # toolSets.go (bead pg2-cwn44): the Go toolchain. Every grant here runs
        # code from the tree it is pointed at (`_test.go`, `//go:generate`,
        # `go.mod` toolchain fetches), so it is held back from the handler-wide
        # list and only a role that works trusted trees declares it. mkDefault
        # so a consumer may replace it, and defining another set never drops it.
        toolSets.go = lib.mkDefault [
          "Bash(go build:*)"
          "Bash(go test:*)"
          "Bash(go vet:*)"
          "Bash(go mod:*)"
          "Bash(gofmt:*)"
          "Bash(gofumpt:*)"
        ];
        # toolSets.hooks (bead pg2-bg9qq): the repo hook runners. They run the hook
        # code the repo's config names, attacker-controlled on an untrusted PR
        # checkout (ADR 0035), so only a role that runs hooks directly on a trusted
        # tree declares it. A `git commit` fires hooks without this grant, and
        # pg-hooks is granted separately per role. mkDefault, like `go`.
        toolSets.hooks = lib.mkDefault [
          "Bash(prek:*)"
          "Bash(pre-commit:*)"
        ];
        # handlerCommandDir: this bead, pg2-pteab.
        inherit handlerCommandDir specFiles commandSpecFiles;
        lib.renderWrapSpec = renderWrapSpec;
        lib.checkRenderedScript = checkRenderedScript;
        inherit claudeBaseline;
        poolConfigFiles =
          lib.optionalAttrs cfg.pool.enable { handler = poolConfigFile; }
          // lib.mapAttrs poolConfigFileFor ccpoolRolesWithOwnPool;
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
          assertion = !cfg.poolMetrics.enable || cfg.pool.enable || (ccpoolRolesWithOwnPool != { });
          message = ''
            phillipgreenii.programs.pg-router-ccpool-handler.poolMetrics.enable
            requires `pool.enable = true` or at least one
            `roles.<name>.ccpool.pool.enable = true` entry -- there is no
            dedicated pool to report capacity for otherwise.
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
      ]
      # pg-wrap two-way pool/spec check, spec-on-command-role and
      # role-set-PG_WRAP_SPEC guards (design ruling 30).
      ++ wrapPoolAssertions
      ++ unmanagedPoolAssertions
      ++ wrapSpecAssertions
      ++ commandSpecAssertions
      ++ wrapSessionEnvAssertions;

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
