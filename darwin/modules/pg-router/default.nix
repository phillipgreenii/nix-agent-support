{
  config,
  lib,
  pkgs,
  ...
}:
let
  # phillipgreenii.observability.logSources is declared at darwin/system scope in
  # phillipgreenii-nix-support-apps (darwin/modules/observability/registration.nix),
  # so this lives in darwin, not in the home-manager module — setting it from HM
  # targets an undeclared option and fails eval (same reasoning as pa-monitor's
  # dashboardProviders registration).
  #
  # pg-router writes its JSONL event log to the standard path
  # ${XDG_STATE_HOME}/pg-router/events.jsonl.
  #
  # Relabeled to `pg-router-events` (observability design decision D3): the
  # NEW OTLP log push this module now also wires (emitterEnv below) claims
  # the plain `pg-router` service_name for pg-router's own operational
  # WARN/ERROR slog lines, so this pre-existing registration — which only
  # ever carried the dispatch-outcome ledger (events.jsonl), a narrower
  # signal — moves to its own label rather than colliding with it.
  #
  # `path` MUST be set explicitly here. The logSources submodule's default
  # glob is derived from the ATTRIBUTE NAME
  # (${env:XDG_STATE_HOME}/${name}/*.jsonl, phillipgreenii-nix-support-apps's
  # darwin/modules/observability/registration.nix), so leaving it unset after
  # the D3 rename would watch ${XDG_STATE_HOME}/pg-router-events/*.jsonl — a
  # directory that never exists, since events.jsonl itself stayed at
  # ${XDG_STATE_HOME}/pg-router/events.jsonl. That mismatch is exactly what
  # left the pg-router-events Loki stream empty from D3 onward (bead
  # pg2-hybyu) — the filelog receiver watched a glob that could never match.
  obs = config.phillipgreenii.observability;

  # OTel OTLP log-export env for the daemon (design section 5.4), mirroring
  # pa-monitor's/ollama's own darwin module precedent for
  # `obs.mkEmitterEnv` exactly: resolved here (darwin scope, where the
  # observability surface is declared) and merged into the LaunchAgent's
  # EnvironmentVariables below. `obs ? mkEmitterEnv` guards against a
  # narrower eval fixture (this repo's own flake.nix checks) that stubs
  # `logSources` but declares no such helper.
  emitterEnv =
    if obs ? mkEmitterEnv then
      obs.mkEmitterEnv {
        serviceName = "pg-router";
        protocol = "grpc";
      }
    else
      { };

  # Read pg-router.daemon.enable across all HM users; the LaunchAgent gets
  # registered once at system scope when any user opted in — mirrors
  # pa-monitor's own hmUsers pattern. Exactly one daemon-enabled user's
  # config drives the single LaunchAgent instance (scoped to ENABLED users
  # only: repoRoot/configText have no default, so reading them off a
  # daemon-DISABLED user would throw "used but not defined").
  hmUsers = config.home-manager.users or { };
  daemonUsers = lib.filter (u: u.phillipgreenii.programs.pg-router.daemon.enable or false) (
    lib.attrValues hmUsers
  );
  daemonEnabledByAnyUser = daemonUsers != [ ];
  daemonCfg =
    if daemonEnabledByAnyUser then
      (lib.head daemonUsers).phillipgreenii.programs.pg-router.daemon
    else
      null;

  pkg = pkgs.pg-router;

  # The rendered daemon config must load under the very binary the LaunchAgent
  # execs, or the build fails (see the home module's checked-config.nix).
  checkedConfigFile =
    (import ../../../home/programs/pg-router/checked-config.nix {
      inherit pkgs;
      package = pkg;
    }).mkChecked;

  # XDG_STATE_HOME default for macOS user agents. The launchdServices helper
  # plist runs under the primary user, so resolve via system.primaryUser.
  primaryUser = config.system.primaryUser or null;
  stateHome = if primaryUser != null then "/Users/${primaryUser}/.local/state" else "/tmp/pg-router";

  # The one shutdown deadline, in seconds. It is passed to the daemon
  # (`--shutdown-deadline`) and launchd's ExitTimeOut is derived from it, so the
  # daemon's own watchdog always fires before launchd's SIGKILL.
  shutdownDeadlineSeconds = 53;
  exitTimeOutMarginSeconds = 7;
  exitTimeOutSeconds = shutdownDeadlineSeconds + exitTimeOutMarginSeconds;
in
assert lib.assertMsg (exitTimeOutSeconds <= 60)
  "pg-router: shutdown deadline (${toString shutdownDeadlineSeconds}s) plus margin (${toString exitTimeOutMarginSeconds}s) exceeds launchd's 60s ExitTimeOut clamp";
{
  config = lib.mkMerge [
    (lib.mkIf (obs.enable or false) {
      phillipgreenii.observability.logSources.pg-router-events = {
        serviceName = "pg-router-events";
        path = "\${env:XDG_STATE_HOME}/pg-router/events.jsonl";
      };

      # pg2-02n5o: liveness/backlog/queue-depth/failure-rate alert rules for
      # the pg_router_* metric catalog (see the file's own header for the
      # incident context, folder-convergence mechanism, and why this is
      # gated on `obs.enable` alone rather than `daemonEnabledByAnyUser` --
      # mirrors darwin/modules/pa-monitor/default.nix's own
      # alertRuleFiles precedent).
      phillipgreenii.observability.alertRuleFiles = [
        ../../../packages/pg-router/grafana/alerting/alerts.yaml
      ];
    })

    # LaunchAgent registration via the canonical helper (ADR 0049, amended by
    # 0051), mirroring darwin/modules/pa-monitor/default.nix (precedents:
    # pa-monitor, codeburn, ccpool, pg-ccaudit): the HM systemd.user.services
    # daemon unit alone is a darwin no-op (darwin has no systemd), so the
    # daemon submodule needs its own LaunchAgent to actually run there
    # (Task 1.7).
    (lib.mkIf daemonEnabledByAnyUser {
      phillipgreenii.system.launchdServices.userAgents.pg-router-daemon = {
        label = "com.phillipg.pg-router-daemon";
        script = ''
          export PG_ROUTER_REPO_ROOT=${lib.escapeShellArg daemonCfg.repoRoot}
          ${lib.optionalString (
            daemonCfg.beadsPrefix != null
          ) "export PG_ROUTER_BEADS_PREFIX=${lib.escapeShellArg daemonCfg.beadsPrefix}"}
          export PG_ROUTER_CONFIG=${checkedConfigFile "pg-router-daemon-config.toml" daemonCfg.configText}
          ${lib.optionalString (
            daemonCfg.handlerConfig != null
          ) "export PG_ROUTER_CCPOOL_HANDLER_CONFIG=${lib.escapeShellArg daemonCfg.handlerConfig}"}
          ${lib.optionalString (
            daemonCfg.handlerCcpoolPool != null
          ) "export CCPOOL_POOL=${lib.escapeShellArg daemonCfg.handlerCcpoolPool}"}
          ${lib.optionalString (
            daemonCfg.metricsAddr != null
          ) "export PG_ROUTER_METRICS_ADDR=${lib.escapeShellArg daemonCfg.metricsAddr}"}
          exec ${pkg}/bin/pg-router run --shutdown-deadline ${toString shutdownDeadlineSeconds}s
        '';
        runAtLoad = true;
        keepAlive = true;
        serviceConfig = {
          StandardErrorPath = "${stateHome}/pg-router/launchd-stderr.log";
          StandardOutPath = "${stateHome}/pg-router/launchd-stdout.log";
          # OTLP log-export env (design section 5.4, D6) — empty when
          # obs.enable is false or the helper is absent (emitterEnv's own
          # guard above), so this is a no-op merge on a machine without the
          # observability stack.
          #
          # Profiling endpoint (github.com/phillipgreenii/x/pprofserver): a 0600
          # unix socket under the daemon's own state dir, so no port is
          # allocated or exposed. launchd does not expand $XDG_STATE_HOME in
          # EnvironmentVariables, so the resolved stateHome is written out. The
          # daemon creates the socket's directory and ignores a bad value.
          # Absent when daemon.pprof.enable is false. Capture recipe:
          # docs/go-daemon-profiling-runbook.md in nix-repo-base.
          #
          # GOFLAGS=-p=3 bounds the Go build parallelism of the agent sessions
          # this daemon starts under tmux. The Go env file written by the
          # home-manager go-env module already carries it; this is insurance
          # for a session whose HOME or Go env file is not the usual one. An
          # environment GOFLAGS replaces the file value, so this stays the
          # same value as the file's.
          #
          # GOTOOLCHAIN=local is the same insurance for the go-env module's
          # toolchain setting: a session's `go` older than a module's go
          # directive fails instead of downloading another toolchain, whose
          # compiler would keep its own copy of the shared GOCACHE. A
          # non-empty environment value beats the file, so a tool that sets
          # GOTOOLCHAIN in its own environment still overrides this.
          EnvironmentVariables =
            emitterEnv
            // {
              GOFLAGS = "-p=3";
              GOTOOLCHAIN = "local";
            }
            // lib.optionalAttrs daemonCfg.pprof.enable {
              PG_ROUTER_PPROF_ADDR = "${stateHome}/pg-router/pprof.sock";
            };
          # ExitTimeOut is DERIVED from the shutdown deadline this module also
          # passes to the daemon (`--shutdown-deadline`, see shutdownDeadlineSeconds
          # above): deadline plus a 7 s margin. The daemon splits that one
          # deadline between its shutdown phases (drain of in-flight
          # dispatches, preShutdown hooks, in-flight tail, flush; see
          # cmd/pg-router/shutdown_budget.go) and hard-exits with a logged
          # shutdown_overrun when the deadline passes, so launchd's SIGKILL
          # (ExitTimeOut after SIGTERM) can never be the first to fire and the
          # two numbers cannot drift: change the deadline here, never
          # ExitTimeOut alone. launchd silently clamps ExitTimeOut to 60 (61,
          # 90 and 120 all print `exit timeout = 60` in `launchctl print`;
          # measured 2026-10-03 on macOS 26 / Darwin 25.6.0), so the module
          # asserts deadline plus margin stays within 60.
          ExitTimeOut = exitTimeOutSeconds;
          # AbandonProcessGroup: launchd.plist(5) says that when
          # a job dies launchd kills any remaining processes with the same
          # process group ID as the job unless this key is true. Observed
          # live (2026-10-04): the daemon is its own process-group leader
          # (ppid 1, pgid == pid) and every handler child shares that pgid,
          # so without this key launchd group-kills whatever is still
          # running at daemon exit -- after the graceful drain
          # (the drain phase) and inside ExitTimeOut -- which defeats
          # the drain design (pg2-euh4f, pg2-s3fzr, pg2-vu2zc).
          #
          # Chosen over the alternative (per-handler Setpgid/Setsid in
          # internal/wireclient.OSRunner) because it is plist-only: it does
          # not change how `pg-router run-until-idle`/CLI invocations spawn
          # handlers (a per-handler group would stop an interactive Ctrl-C
          # reaching them and puts them in a background group for any TTY
          # use), and it needs no Go change. Trade-off, accepted: a hung
          # group member is no longer reaped by launchd (it is reaped
          # only by its own exit). ExitTimeOut is derived from the
          # shutdown deadline, see above.
          #
          # pg2-dtigc (ADR 0085): this key is now LOAD-BEARING for the
          # restart requirement ("restarts MUST NOT kill existing
          # sessions"), not just a drain bugfix. The daemon no longer
          # cancels a session-backed role's dispatch at shutdown (the role's
          # survives-shutdown setting: the `survives_shutdown` key of its
          # [[role]] table, rendered by
          # home/programs/pg-router-ccpool-handler), so such a role's adapter
          # and its sessions are EXPECTED to outlive the daemon and must not
          # be group-killed, whichever adapter binary the role names. They
          # are no longer untracked orphans: the new daemon replays the
          # un-accepted offer, re-offers it, and the redelivered dispatch
          # absorbs the live session. A role without the setting (a command
          # role) is unchanged (drain, then cancel).
          AbandonProcessGroup = true;
        };
      };
    })
  ];
}
