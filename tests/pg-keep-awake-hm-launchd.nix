# Eval-level test of the pg-keep-awake keeper entry (docket pg2-8bgxg.2, packet
# 17a) in home/programs/pg-keep-awake, in the style of
# checks.<system>.test-pg-connector-github-hm-launchd: the module is evaluated
# against a STUB of personal's HM launchd registry option
# (tests/launchd-registry-stub.nix, the field names and types of personal's
# launchd-service-submodule.nix) and against stubbed platforms. Consumed by
# checks.<system>.test-pg-keep-awake-hm-launchd, which also builds the rendered
# configs (so `pg-keep-awake config check` runs on them) and exposes the
# rendered entry as a runnable wrapper for the live run.
#
# The check derivation carries `passthru.wrapper`: the rendered keeper entry as a
# shell script (the entry's own text plus its EnvironmentVariables minus HOME as
# exports, which is what launchd would set). The sandbox cannot run `caffeinate`
# or `pmset`, so the live run is a separate command, not part of the check:
#
#   w=$(nix build .#checks.<system>.test-pg-keep-awake-hm-launchd.wrapper --no-link --print-out-paths)
#   env -i PATH=/usr/bin:/bin XDG_STATE_HOME=<tmp> XDG_CONFIG_HOME=<tmp> HOME=<tmp> \
#     PG_KEEP_AWAKE_BATTERY_SOURCE=file:<tmp>/battery "$w" &
#
# All values are EXAMPLE values.
{ lib, pkgs }:
let
  # The module registers the entry only for a darwin host (and a Linux builder
  # also runs this check), so the platform is presented explicitly.
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

  # The system observability stack, as the module reads it null-safely.
  osWithObs = {
    phillipgreenii.observability = {
      enable = true;
      mkEmitterEnv = _: {
        OTEL_EXPORTER_OTLP_ENDPOINT = "http://collector.example:4317";
      };
    };
  };
  # the stack present but disabled: mkEmitterEnv yields nothing
  osObsDisabled = {
    phillipgreenii.observability = {
      enable = false;
      mkEmitterEnv = _: { };
    };
  };

  evaluateWith =
    {
      pkgs',
      declareLaunchd ? true,
    }:
    cfg: osConfig:
    lib.evalModules {
      # home-manager always passes osConfig (null for a standalone activation),
      # so the stub does too.
      specialArgs = {
        pkgs = pkgs';
        inherit lib osConfig;
      };
      modules = [
        ../home/programs/pg-keep-awake/default.nix
        (
          { lib, ... }:
          {
            options = {
              # Declared by home-manager itself: the module renders the config
              # through xdg.configFile, reads the home directory and state home
              # for the launchd environment and log paths, and asserts.
              xdg.configFile = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
              xdg.stateHome = lib.mkOption {
                type = lib.types.str;
                default = "/example/home/.local/state";
              };
              home.homeDirectory = lib.mkOption {
                type = lib.types.str;
                default = "/example/home";
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
          phillipgreenii.programs.pg-keep-awake = cfg // {
            enable = cfg.enable or true;
          };
        }
      ]
      # personal's HM launchdServices option, stubbed (this flake has no input
      # on personal; see tests/launchd-registry-stub.nix).
      ++ lib.optional declareLaunchd ./launchd-registry-stub.nix;
    };

  case =
    platform: cfg: osConfig:
    evaluateWith platform cfg osConfig;

  agentName = "pg-keep-awake";
  opts = c: c.config.phillipgreenii.programs.pg-keep-awake;
  entries = c: c.config.phillipgreenii.programs.launchdServices.userAgents;
  entryOf = c: (entries c).${agentName};
  failedAssertions = c: lib.filter (a: !a.assertion) c.config.assertions;
  messages = c: map (a: a.message) (failedAssertions c);
  configFileOf = c: c.config.xdg.configFile."pg-keep-awake/config.toml".source;

  # darwin, with a system scope that supplies an OTel endpoint
  on = case darwin { } osWithObs;
  # darwin without the system scope: a different rendered config (no endpoint)
  onNoOs = case darwin { } null;
  onObsDisabled = case darwin { } osObsDisabled;
  # only one value differs: the keeper tick
  onOtherHeartbeat = case darwin { heartbeatIntervalSeconds = 20; } osWithObs;
  # only one value differs: the battery hold
  onOtherHold = case darwin { battery.holdPct = 40; } osWithObs;
  disabled = case darwin { enable = false; } osWithObs;
  # the two relations the module asserts
  badWatchdog = case darwin {
    heartbeatIntervalSeconds = 10;
    watchdogTimeoutSeconds = 59;
  } osWithObs;
  watchdogAtBoundary = case darwin {
    heartbeatIntervalSeconds = 10;
    watchdogTimeoutSeconds = 60;
  } osWithObs;
  badRelease = case darwin {
    battery.holdPct = 30;
    battery.releasePct = 31;
  } osWithObs;
  releaseAtBoundary = case darwin {
    battery.holdPct = 30;
    battery.releasePct = 30;
  } osWithObs;
  # a host that is not darwin: the registry may or may not be declared
  linuxRegistry = case linux { } osWithObs;
  linuxNoRegistry = case (linux // { declareLaunchd = false; }) { } osWithObs;
  # darwin without personal's home module
  darwinNoRegistry = case (darwin // { declareLaunchd = false; }) { } osWithObs;

  e = entryOf on;
  inherit (e) script;
  env = e.serviceConfig.EnvironmentVariables;
  binary = "${pkgs.pg-keep-awake}/bin/pg-keep-awake";
  stateDir = "/example/home/.local/state";

  cases = {
    # ---- the entry exists only when enabled, on darwin ---------------------
    entryIsTheOnlyOne = builtins.attrNames (entries on) == [ agentName ];
    disabledRegistersNothing = entries disabled == { } && failedAssertions disabled == [ ];
    disabledRendersNothing = disabled.config.xdg.configFile == { };

    # ---- label and command -------------------------------------------------
    label = e.label == "com.phillipg.pg-keep-awake";
    # the whole command is the keeper, no config flag: it reads the XDG path
    command = script == "exec ${binary} keeper\n";
    noConfigFlag = !(lib.hasInfix "--config" (builtins.unsafeDiscardStringContext script));
    keepAlive = e.keepAlive == true;
    runAtLoad = e.runAtLoad == true;
    enabled = e.enable;
    abandonProcessGroup = e.serviceConfig.AbandonProcessGroup == true;
    # no stray keys beyond the documented ones
    serviceConfigKeys =
      builtins.attrNames e.serviceConfig == [
        "AbandonProcessGroup"
        "EnvironmentVariables"
        "StandardErrorPath"
        "StandardOutPath"
      ];
    # launchd's files stay out of the keeper's 0700 state directory
    logsOutsideStateDir =
      e.serviceConfig.StandardOutPath == "${stateDir}/pg-keep-awake-launchd/launchd-stdout.log"
      && e.serviceConfig.StandardErrorPath == "${stateDir}/pg-keep-awake-launchd/launchd-stderr.log";
    # personal's defaults stay untouched
    noExecPath = e.execPath == null;
    manageLogsDefault = e.manageLogs.enable == true;
    logCollectionDefault = e.logCollection.enable == true;
    noAssertionFailsOnDarwin = failedAssertions on == [ ];
    noWarningOnDarwin = on.config.warnings == [ ];

    # ---- the config is rendered where the keeper reads it ------------------
    configRenderedAtXdgPath =
      builtins.attrNames on.config.xdg.configFile == [ "pg-keep-awake/config.toml" ]
      && toString (configFileOf on) == toString (opts on).internal.configFile;
    configIsAStorePath = lib.hasPrefix builtins.storeDir (toString (configFileOf on));

    # ---- a changed config changes the job definition -----------------------
    hashIsInTheJobDefinition =
      env ? PG_KEEP_AWAKE_CONFIG_HASH && env.PG_KEEP_AWAKE_CONFIG_HASH == (opts on).internal.configHash;
    hashLooksLikeSha256 = builtins.match "[0-9a-f]{64}" env.PG_KEEP_AWAKE_CONFIG_HASH != null;
    homeIsSet = env.HOME == "/example/home";
    sameInputsSameJob =
      (entryOf (case darwin { } osWithObs)).serviceConfig == e.serviceConfig
      && (entryOf (case darwin { } osWithObs)).script == script;
    valueChangeChangesHash =
      (entryOf onOtherHeartbeat).serviceConfig.EnvironmentVariables.PG_KEEP_AWAKE_CONFIG_HASH
      != env.PG_KEEP_AWAKE_CONFIG_HASH
      && (entryOf onOtherHeartbeat).serviceConfig != e.serviceConfig;
    batteryChangeChangesHash =
      (entryOf onOtherHold).serviceConfig.EnvironmentVariables.PG_KEEP_AWAKE_CONFIG_HASH
      != env.PG_KEEP_AWAKE_CONFIG_HASH;
    otelChangeChangesHash =
      (entryOf onNoOs).serviceConfig.EnvironmentVariables.PG_KEEP_AWAKE_CONFIG_HASH
      != env.PG_KEEP_AWAKE_CONFIG_HASH;

    # ---- the OTel endpoint comes only from the observability option --------
    otelFromObservability =
      (opts on).internal.configUnchecked != (opts onNoOs).internal.configUnchecked
      && (opts onObsDisabled).internal.configUnchecked == (opts onNoOs).internal.configUnchecked;
    noObsStillRegisters = (entryOf onNoOs).label == e.label && failedAssertions onNoOs == [ ];

    # ---- the assertions -----------------------------------------------------
    watchdogTooShortAsserts =
      builtins.length (messages badWatchdog) == 1
      && lib.hasInfix "watchdogTimeoutSeconds" (builtins.head (messages badWatchdog));
    watchdogAtSixTimesPasses = failedAssertions watchdogAtBoundary == [ ];
    releaseAboveHoldAsserts =
      builtins.length (messages badRelease) == 1
      && lib.hasInfix "battery.releasePct" (builtins.head (messages badRelease));
    releaseEqualHoldPasses = failedAssertions releaseAtBoundary == [ ];

    # ---- a host that is not darwin: no entry, no failure -------------------
    linuxNoEntry = entries linuxRegistry == { };
    linuxNoFailure = failedAssertions linuxRegistry == [ ] && failedAssertions linuxNoRegistry == [ ];
    linuxNoRegistryNoOption =
      !(lib.hasAttrByPath [ "phillipgreenii" "programs" "launchdServices" ] linuxNoRegistry.config);
    linuxStillRendersConfig =
      builtins.attrNames linuxRegistry.config.xdg.configFile == [
        "pg-keep-awake/config.toml"
      ];

    # ---- darwin without personal's module: a clear assertion ---------------
    darwinNoRegistryAsserts =
      builtins.length (messages darwinNoRegistry) == 1
      && lib.hasInfix "phillipgreenii.programs.launchdServices" (
        builtins.head (messages darwinNoRegistry)
      );
  };

  # The rendered entry as a runnable script: its own text plus what launchd
  # would put in the environment (HOME excluded: the live run sets its own).
  wrapperOf =
    c:
    let
      entry = entryOf c;
      exports = lib.mapAttrsToList (k: v: "export ${k}=${lib.escapeShellArg v}") (
        removeAttrs entry.serviceConfig.EnvironmentVariables [ "HOME" ]
      );
    in
    pkgs.writeShellScript "pg-keep-awake-keeper-wrapper" ''
      ${lib.concatStringsSep "\n" exports}
      ${entry.script}
    '';
in
{
  inherit cases;
  failures = builtins.attrNames (lib.filterAttrs (_: ok: !ok) cases);
  # What the shell side compares against.
  live = {
    inherit script;
    package = pkgs.pg-keep-awake;
    wrapper = wrapperOf on;
    hash = env.PG_KEEP_AWAKE_CONFIG_HASH;
    hashOtherHeartbeat =
      (entryOf onOtherHeartbeat).serviceConfig.EnvironmentVariables.PG_KEEP_AWAKE_CONFIG_HASH;
    # rendered configs (the checked ones: building them runs `config check`)
    configFile = (opts on).internal.configFile;
    configFileNoOs = (opts onNoOs).internal.configFile;
    # rendered but NOT validated: what the build-time check must reject
    uncheckedBadWatchdog = (opts badWatchdog).internal.configUnchecked;
    uncheckedBadRelease = (opts badRelease).internal.configUnchecked;
  };
}
