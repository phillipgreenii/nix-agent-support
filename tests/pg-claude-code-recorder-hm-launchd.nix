# Eval-level test of home/programs/pg-claude-code-recorder (docket pg2-v77tsx,
# phase 7a), in the style of tests/pg-keep-awake-hm-launchd.nix: the module is
# evaluated against a STUB of personal's HM launchd registry option
# (tests/launchd-registry-stub.nix), stubbed home-manager options (assertions,
# warnings, home.homeDirectory, home.file, xdg.configFile) and stubbed
# platforms. Consumed by checks.<system>.test-pg-claude-code-recorder-hm-launchd,
# which also builds the rendered configs (the module's build-time check runs the
# real binary on them), reads the rendered TOML back, and runs the rendered
# launchd command under `env -i` against throwaway directories.
#
# The control module (phase 8) is not declared in this flake: the controlStateDir
# coupling is exercised with a STUB option set declared here.
#
# All values are EXAMPLE values.
{ lib, pkgs }:
let
  withPlatform = isDarwin: {
    pkgs' = pkgs // {
      stdenv = pkgs.stdenv // {
        hostPlatform = pkgs.stdenv.hostPlatform // {
          inherit isDarwin;
        };
      };
    };
  };
  darwin = withPlatform true;
  linux = withPlatform false;

  osWithObs = {
    phillipgreenii.observability = {
      enable = true;
      mkEmitterEnv = _: { };
    };
  };

  evaluateWith =
    {
      pkgs',
      declareLaunchd ? true,
      # null: the control module is not declared; otherwise its stub values
      control ? null,
    }:
    cfg: osConfig:
    lib.evalModules {
      specialArgs = {
        pkgs = pkgs';
        inherit lib osConfig;
      };
      modules = [
        ../home/programs/pg-claude-code-recorder/default.nix
        (
          { lib, ... }:
          {
            options = {
              xdg = {
                configFile = lib.mkOption {
                  type = lib.types.attrsOf lib.types.anything;
                  default = { };
                };
                stateHome = lib.mkOption {
                  type = lib.types.str;
                  default = "/example/home/.local/state";
                };
                dataHome = lib.mkOption {
                  type = lib.types.str;
                  default = "/example/home/.local/share";
                };
              };
              home = {
                homeDirectory = lib.mkOption {
                  type = lib.types.str;
                  default = "/example/home";
                };
                sessionVariables = lib.mkOption {
                  type = lib.types.attrsOf lib.types.str;
                  default = { };
                };
                file = lib.mkOption {
                  type = lib.types.attrsOf lib.types.anything;
                  default = { };
                };
                packages = lib.mkOption {
                  type = lib.types.listOf lib.types.package;
                  default = [ ];
                };
              };
              assertions = lib.mkOption {
                type = lib.types.listOf lib.types.anything;
                default = [ ];
              };
              warnings = lib.mkOption {
                type = lib.types.listOf lib.types.str;
                default = [ ];
              };
            };
          }
        )
        {
          phillipgreenii.programs.pg-claude-code-recorder = {
            enable = true;
          }
          // cfg;
        }
      ]
      ++ lib.optional declareLaunchd ./launchd-registry-stub.nix
      ++ lib.optional (control != null) (
        { lib, ... }:
        {
          options.phillipgreenii.programs.pg-claude-control = {
            enable = lib.mkOption {
              type = lib.types.bool;
              default = false;
            };
            stateDir = lib.mkOption {
              type = lib.types.str;
              default = "/example/home/.local/state/pg-claude-control";
            };
          };
          config.phillipgreenii.programs.pg-claude-control = control;
        }
      );
    };

  case =
    platform: cfg: osConfig:
    evaluateWith platform cfg osConfig;

  opts = c: c.config.phillipgreenii.programs.pg-claude-code-recorder;
  entries = c: c.config.phillipgreenii.programs.launchdServices.userAgents;
  entryOf = name: c: (entries c).${name};
  failedAssertions = c: lib.filter (a: !a.assertion) c.config.assertions;
  messages = c: map (a: a.message) (failedAssertions c);
  agent = "pg-claude-code-recorder";
  # lib.hasInfix matches by regex, which refuses strings that carry store-path context
  hasInfixCtx =
    infix: s:
    lib.hasInfix (builtins.unsafeDiscardStringContext infix) (builtins.unsafeDiscardStringContext s);

  on = case darwin { daemon.enable = true; } osWithObs;
  onNoPprof = case darwin {
    daemon.enable = true;
    pprof.enable = false;
  } null;
  # the package and command only: no agent
  packageOnly = case darwin { } osWithObs;
  disabled = case darwin { enable = false; } osWithObs;
  second = case darwin {
    daemon.enable = true;
    instance = "second";
    claudeDir = "/example/second-claude";
    stateDir = "/example/home/.local/share/pg-claude-code-recorder-second";
  } osWithObs;
  linuxDaemon = case linux { daemon.enable = true; } osWithObs;
  linuxNoRegistry = case (linux // { declareLaunchd = false; }) { daemon.enable = true; } osWithObs;
  darwinNoRegistry = case (darwin // { declareLaunchd = false; }) { daemon.enable = true; } osWithObs;
  # a value that differs: the config (and so the job) changes
  onOtherSettle = case darwin {
    daemon.enable = true;
    settle.after = "12h";
  } osWithObs;
  onEventSettle = case darwin {
    daemon.enable = true;
    settle = {
      mode = "event";
      tick = "10m";
      reconcile = "2h";
      min_wake = "10s";
    };
  } osWithObs;
  onEndsSweep = case darwin {
    daemon.enable = true;
    ends.sweep_every = "30m";
  } osWithObs;
  onFifoSweep = case darwin {
    daemon.enable = true;
    fifo.sweep_every = "30m";
  } osWithObs;
  onRollingDoctor = case darwin {
    daemon.enable = true;
    doctor = {
      bytes_per_slice = 0;
      slice_every = "30s";
      allow_slow_cycle = true;
    };
    wal.truncate_above = 0;
  } osWithObs;
  onSettings = case darwin {
    daemon.enable = true;
    settings.doctor.interval = "6h";
  } osWithObs;

  onQueryTimeout = case darwin {
    daemon.enable = true;
    query.timeout = "10s";
  } osWithObs;

  onMaxConn = case darwin {
    daemon.enable = true;
    query.maxConnections = 0;
  } osWithObs;

  # the control module coupling
  controlCoupled =
    case
      (
        darwin
        // {
          control = {
            enable = true;
            stateDir = "/example/control";
          };
        }
      )
      {
        daemon.enable = true;
      }
      osWithObs;
  controlMismatch =
    case
      (
        darwin
        // {
          control = {
            enable = true;
            stateDir = "/example/control";
          };
        }
      )
      {
        daemon.enable = true;
        controlStateDir = "/example/elsewhere";
      }
      osWithObs;
  controlOff =
    case
      (
        darwin
        // {
          control = {
            enable = false;
            stateDir = "/example/control";
          };
        }
      )
      {
        daemon.enable = true;
      }
      osWithObs;
  controlOffMismatch =
    case
      (
        darwin
        // {
          control = {
            enable = false;
            stateDir = "/example/control";
          };
        }
      )
      {
        daemon.enable = true;
        controlStateDir = "/example/elsewhere";
      }
      osWithObs;

  e = entryOf agent on;
  binary = "${pkgs.pg-claude-code-recorder}/bin/pg-claude-code-recorder";
  stateHome = "/example/home/.local/state";
  rendered = c: (opts c).internal.configUnchecked;
  # the rendered config as data, read back with the toml parser
  data = c: (opts c).internal.settings;
  d = data on;

  cases = {
    # ---- nothing without enable / daemon.enable ------------------------------
    disabledRegistersNothing = entries disabled == { } && disabled.config.xdg.configFile == { };
    disabledInstallsNothing = disabled.config.home.packages == [ ];
    noDaemonNoAgent = entries packageOnly == { };
    packageOnlyInstallsPackage =
      map toString packageOnly.config.home.packages == [ (toString pkgs.pg-claude-code-recorder) ];
    entryIsTheOnlyOne = builtins.attrNames (entries on) == [ agent ];
    noFailedAssertionOnDarwin = failedAssertions on == [ ] && on.config.warnings == [ ];

    # ---- label and command --------------------------------------------------
    label = e.label == "com.phillipg.pg-claude-code-recorder";
    command = hasInfixCtx "${binary} serve --config ${(opts on).configFile} --pprof &\n" e.script;
    commandWithoutPprof = hasInfixCtx "${binary} serve --config ${(opts onNoPprof).configFile} &\n" (entryOf agent onNoPprof)
    .script;
    # exit 8 (schema_rebuild_required) is turned into exit 0 by the script, so KeepAlive restarts only after an unsuccessful exit
    keepAliveOnlyAfterFailure = e.keepAlive == { SuccessfulExit = false; };
    scriptTurnsRefusalIntoCleanExit = hasInfixCtx "if [ \"$rc\" -eq 8 ]; then\n  exit 0\nfi\nexit \"$rc\"" e.script;
    runAtLoad = e.runAtLoad == true;
    noExecPath = e.execPath == null;
    manageLogsDefault = e.manageLogs.enable == true;
    serviceConfigKeys =
      builtins.attrNames e.serviceConfig == [
        "EnvironmentVariables"
        "StandardErrorPath"
        "StandardOutPath"
      ];
    logsOutsideStateDir =
      e.serviceConfig.StandardOutPath
      == "${stateHome}/pg-claude-code-recorder-launchd/launchd-stdout.log";
    envHasHomeAndStateHome =
      e.serviceConfig.EnvironmentVariables.HOME == "/example/home"
      && e.serviceConfig.EnvironmentVariables.XDG_STATE_HOME == stateHome;
    noLogsMaxBytesOption = !(opts on ? logs);
    changedConfigChangesTheJob = (entryOf agent onOtherSettle).script != e.script;

    # ---- the instance suffix ------------------------------------------------
    instanceSuffixesLabel =
      builtins.attrNames (entries second) == [ "pg-claude-code-recorder-second" ]
      &&
        (entryOf "pg-claude-code-recorder-second" second).label
        == "com.phillipg.pg-claude-code-recorder.second";
    instanceHasOwnPprofSocket =
      (entryOf "pg-claude-code-recorder-second" second)
      .serviceConfig.EnvironmentVariables.PG_CLAUDE_CODE_RECORDER_PPROF_ADDR
      == "${stateHome}/pg-claude-code-recorder-second/pprof.sock";
    # the socket is where client.DefaultSocket looks ($XDG_STATE_HOME/pg-claude-code-recorder),
    # never under the data-home stateDir (pg2-wb689g)
    socketNotUnderStateDir = !(lib.hasPrefix "${d.stateDir}/" d.socket);
    instanceHasOwnSocket =
      (data second).socket == "${stateHome}/pg-claude-code-recorder-second/recorder.sock"
      && !(lib.hasPrefix "${(data second).stateDir}/" (data second).socket);
    # the darwin observability module's daemonLog path (flake.nix darwin-module check)
    # is this agent's stderr log with the state home spelled as the log-source variable
    stderrLogMatchesDarwinLogSource =
      lib.replaceStrings [ stateHome ] [ "\${env:XDG_STATE_HOME}" ] e.serviceConfig.StandardErrorPath
      == "\${env:XDG_STATE_HOME}/pg-claude-code-recorder-launchd/launchd-stderr.log";
    instanceStderrLogMatchesDarwinLogSource =
      lib.replaceStrings [ stateHome ] [ "\${env:XDG_STATE_HOME}" ]
        (entryOf "pg-claude-code-recorder-second" second).serviceConfig.StandardErrorPath
      == "\${env:XDG_STATE_HOME}/pg-claude-code-recorder-launchd-second/launchd-stderr.log";
    defaultInstanceHasNoPprofEnv =
      !(e.serviceConfig.EnvironmentVariables ? PG_CLAUDE_CODE_RECORDER_PPROF_ADDR);
    instanceConfigPath =
      builtins.attrNames second.config.xdg.configFile == [
        "pg-claude-code-recorder-second/config.toml"
      ];

    # ---- the options table's defaults, as rendered ---------------------------
    configAtXdgPath =
      builtins.attrNames on.config.xdg.configFile == [ "pg-claude-code-recorder/config.toml" ]
      &&
        toString on.config.xdg.configFile."pg-claude-code-recorder/config.toml".source
        == toString (opts on).configFile;
    removeDisabledByDefault = d.remove.enable == false && (opts on).remove.enable == false;
    settleAndRemoveDefaults = d.settle.after == "24h" && d.remove.after == "30d";
    settleLoopDefaults =
      d.settle.mode == "scan"
      && d.settle.tick == "5m"
      && d.settle.reconcile == "1h"
      && d.settle.min_wake == "5s";
    endsSweepDefault = d.ends.sweep_every == "10m";
    endsSweepRendered = (data onEndsSweep).ends.sweep_every == "30m";
    fifoSweepDefault = d.fifo.sweep_every == "10m";
    fifoSweepRendered = (data onFifoSweep).fifo.sweep_every == "30m";
    doctorDefaults =
      d.doctor.bytes_per_slice == 268435456
      && d.doctor.slice_every == "1m"
      && d.doctor.allow_slow_cycle == false;
    doctorKeysRendered =
      let
        r = data onRollingDoctor;
      in
      r.doctor.bytes_per_slice == 0 && r.doctor.slice_every == "30s" && r.doctor.allow_slow_cycle == true;
    walDefaults = d.wal.truncate_above == 536870912;
    walKeyRendered = (data onRollingDoctor).wal.truncate_above == 0;
    settleLoopKeysRendered =
      let
        r = data onEventSettle;
      in
      r.settle.mode == "event"
      && r.settle.tick == "10m"
      && r.settle.reconcile == "2h"
      && r.settle.min_wake == "10s";
    otherDefaults =
      d.minFreeBytes == 21474836480
      && d.sweep.interval == "15m"
      && d.feed.retention == "14d"
      && d.wait.max == "60s"
      && d.wait.maxWaiters == 256
      && d.held.recheck == "1h"
      && d.held.alertAfter == "3d"
      && d.remove.maxPerRun == 50
      && d.remove.maxPerHour == 200
      && d.blockList == [ ];
    pathDefaults =
      d.claudeDir == "/example/home/.claude"
      && d.stateDir == "/example/home/.local/share/pg-claude-code-recorder"
      && d.socket == "${stateHome}/pg-claude-code-recorder/recorder.sock"
      && d.controlStateDir == "${stateHome}/pg-claude-control";
    claudeDirFollowsSessionVariable =
      (data (
        (case darwin { daemon.enable = true; } osWithObs).extendModules {
          modules = [ { home.sessionVariables.CLAUDE_CONFIG_DIR = "/example/alt-claude"; } ];
        }
      )).claudeDir == "/example/alt-claude";
    pprofDefaultsOn = (opts on).pprof.enable == true;
    observabilityFollowsSystem =
      (opts on).observability.enable == true && (opts onNoPprof).observability.enable == false;
    valueChangeChangesRenderedConfig = toString (rendered onOtherSettle) != toString (rendered on);
    maxConnectionsDefaultRendered =
      (opts on).query.maxConnections == 8 && (data on).query.max_connections == 8;
    queryTimeoutDefaultRendered = (opts on).query.timeout == "25s" && (data on).query.timeout == "25s";
    queryTimeoutOverrideRendered = (data onQueryTimeout).query.timeout == "10s";
    maxConnectionsOverrideRendered =
      (data onSettings).query.max_connections == 8 && (data onMaxConn).query.max_connections == 0;
    settingsMerge =
      (data onSettings).doctor.interval == "6h" && (data onSettings).remove.after == "30d";
    configFileIsAStorePath = lib.hasPrefix builtins.storeDir (toString (opts on).configFile);

    # ---- controlStateDir coupling -------------------------------------------
    controlDirFollowsControlModule =
      (opts controlCoupled).controlStateDir == "/example/control"
      && failedAssertions controlCoupled == [ ];
    controlDirIsRendered = (data controlCoupled).controlStateDir == "/example/control";
    controlMismatchAsserts =
      builtins.length (messages controlMismatch) == 1
      && lib.hasInfix "pg-claude-control.stateDir" (builtins.head (messages controlMismatch));
    controlDisabledDoesNotCouple =
      (opts controlOff).controlStateDir == "${stateHome}/pg-claude-control"
      && failedAssertions controlOff == [ ]
      && failedAssertions controlOffMismatch == [ ];
    noControlModuleNoAssertion = failedAssertions on == [ ];

    # ---- Linux: unsupervised -------------------------------------------------
    linuxNoEntry = entries linuxDaemon == { };
    linuxWarnsUnsupervised =
      builtins.length linuxDaemon.config.warnings == 1
      && lib.hasInfix "unsupervised" (builtins.head linuxDaemon.config.warnings)
      && lib.hasInfix "inotify" (builtins.head linuxDaemon.config.warnings);
    linuxNoFailure = failedAssertions linuxDaemon == [ ] && failedAssertions linuxNoRegistry == [ ];
    linuxNoRegistryNoOption =
      !(lib.hasAttrByPath [ "phillipgreenii" "programs" "launchdServices" ] linuxNoRegistry.config);
    linuxStillInstalls = linuxDaemon.config.home.packages != [ ];

    # ---- darwin without personal's module: a clear assertion ----------------
    darwinNoRegistryAsserts =
      builtins.length (messages darwinNoRegistry) == 1
      && lib.hasInfix "phillipgreenii.programs.launchdServices" (
        builtins.head (messages darwinNoRegistry)
      );
  };

  # What the live run reads: the rendered config with example dirs the shell
  # rewrites to a throwaway root, and the entry's own command text.
  liveCase = case darwin {
    daemon.enable = true;
    claudeDir = "/example/live/claude";
    stateDir = "/example/live/state";
    # in a directory the daemon must create itself, like the state home default
    socketPath = "/example/live/xdg-state/pg-claude-code-recorder/recorder.sock";
    controlStateDir = "/example/live/control";
  } null;
  liveEntry = entryOf agent liveCase;
in
{
  inherit cases;
  failures = builtins.attrNames (lib.filterAttrs (_: ok: !ok) cases);
  live = {
    package = pkgs.pg-claude-code-recorder;
    inherit ((opts liveCase)) configFile;
    inherit (liveEntry) script;
    # rendered and checked (building them runs the real binary on them)
    configFileDefault = (opts on).configFile;
    # rendered but NOT checked: what the build-time check must reject
    uncheckedBadDuration = rendered (
      case darwin {
        daemon.enable = true;
        settings.sweep.interval = "xyz";
      } osWithObs
    );
  };
}
