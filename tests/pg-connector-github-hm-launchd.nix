# Eval-level test of the pg-connector-github supervisor entry (docket pg2-z5fax,
# Phase 2) in home/programs/pg-connector, in the style of
# checks.<system>.test-pa-monitor-hm-launchd: the module is evaluated against a
# STUB of personal's HM launchd registry option (tests/launchd-registry-stub.nix,
# the field names and types of personal's launchd-service-submodule.nix) and
# against stubbed platforms. Consumed by checks.test-pg-connector-github-hm-launchd,
# which also runs the rendered entry's command for real.
#
# Scenario builders come from tests/pg-connector-home-render.nix (`harness`), so
# the registry, the options and the system scopes are the ones that test uses.
# All values are EXAMPLE values.
{ lib, pkgs }:
let
  inherit (import ./pg-connector-home-render.nix { inherit lib pkgs; }) harness;
  inherit (harness)
    evaluateWith
    githubRegistry
    githubOn
    osWithObs
    ghOpts
    ;

  # The module registers the entry only for a darwin host (and a Linux builder
  # also runs this check), so the platform is presented explicitly, like
  # test-pa-monitor-hm-launchd does.
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

  case =
    platform: github: osConfig:
    evaluateWith platform (githubRegistry // { inherit github; }) osConfig;

  agentName = "pg-connector-github-daemon";
  entries = c: c.config.phillipgreenii.programs.launchdServices.userAgents;
  entryOf = c: (entries c).${agentName};
  failedAssertions = c: lib.filter (a: !a.assertion) c.config.assertions;
  messages = c: map (a: a.message) (failedAssertions c);
  stateDir = "/example/home/.local/state/pg-connector-github";

  # darwin, launchd (the default there), a system scope with an OTel endpoint
  launchdOn = case darwin githubOn osWithObs;
  # the opt-in pprof endpoint turned off: the variable is absent
  launchdPprofOff = case darwin (githubOn // { pprof.enable = false; }) osWithObs;
  # darwin without the system scope: a different rendered config (no endpoint)
  launchdOnNoOs = case darwin githubOn null;
  # a different binary: only the package differs
  launchdOnOtherBinary = case darwin (githubOn // { package = pkgs.hello; }) osWithObs;
  # a different config setting: only the cap differs
  launchdOnOtherCap = case darwin (githubOn // { graphqlPointsPerHour = 4000; }) osWithObs;
  launchdOnDefaultSupervisor = case darwin {
    enable = true;
    inherit (githubOn) host;
  } osWithObs;
  supervisorNone = case darwin (githubOn // { supervisor = "none"; }) osWithObs;
  supervisorNoneAllowed = case darwin (
    githubOn
    // {
      supervisor = "none";
      allowUnsupervised = true;
    }
  ) osWithObs;
  githubDisabled = case darwin { enable = false; } osWithObs;
  # launchd on a host that is not darwin: the registry may or may not be declared
  linuxLaunchd = case linux githubOn osWithObs;
  linuxLaunchdNoRegistry = case (linux // { declareLaunchd = false; }) githubOn osWithObs;
  # the Linux default (supervisor = "none"), registry not declared
  linuxDefault = case (linux // { declareLaunchd = false; }) {
    enable = true;
    inherit (githubOn) host;
  } osWithObs;
  # darwin without personal's home module
  darwinNoRegistry = case (darwin // { declareLaunchd = false; }) githubOn osWithObs;

  e = entryOf launchdOn;
  inherit (e) script;
  configPath = toString (ghOpts launchdOn).configFile;
  binary = "${pkgs.pg-connector-github}/bin/pg-connector-github";

  # lib.hasInfix builds a regex from the needle, which Nix refuses for a string
  # that refers to a store path; the needle's context is irrelevant to the match.
  carries = needle: lib.hasInfix (builtins.unsafeDiscardStringContext needle);

  cases = {
    # ---- the entry: label, restart policy, command, logs -------------------
    entryIsTheOnlyOne = builtins.attrNames (entries launchdOn) == [ agentName ];
    label = e.label == "com.phillipg.pg-connector-github-daemon";
    keepAlive = e.keepAlive == true;
    runAtLoad = e.runAtLoad == true;
    enabled = e.enable;
    # the command is exactly the daemon on the rendered config, nothing else
    command = script == "exec ${binary} --config ${configPath} serve\n";
    commandCarriesConfigAndServe = carries "--config ${configPath} serve" script;
    configIsAStorePath = lib.hasPrefix builtins.storeDir configPath;
    # personal's defaults stay untouched: HM scope has no execPath, manageLogs
    # stays enabled over the launchd stdout/stderr files, log collection stays on
    noExecPath = e.execPath == null;
    manageLogsDefault =
      e.manageLogs.enable == true
      &&
        e.manageLogs.files == [
          "${stateDir}/launchd-stdout.log"
          "${stateDir}/launchd-stderr.log"
        ]
      && e.manageLogs.thresholdBytes == 20 * 1024 * 1024
      && e.manageLogs.archiveCount == 1;
    logCollectionDefault = e.logCollection.enable == true;
    logPaths =
      e.serviceConfig.StandardOutPath == "${stateDir}/launchd-stdout.log"
      && e.serviceConfig.StandardErrorPath == "${stateDir}/launchd-stderr.log";
    # the opt-in pprof endpoint (default on): the resolved absolute socket path
    # under the daemon's state dir, and nothing else in the serviceConfig
    pprofEnabledByDefault = (ghOpts launchdOn).pprof.enable == true;
    pprofEnabledSetsSocket =
      e.serviceConfig.EnvironmentVariables == {
        PG_CONNECTOR_GITHUB_PPROF_ADDR = "${stateDir}/pprof.sock";
      }
      &&
        e.serviceConfig.EnvironmentVariables.PG_CONNECTOR_GITHUB_PPROF_ADDR
        == "/example/home/.local/state/pg-connector-github/pprof.sock";
    pprofEnabledServiceConfigIsExact =
      e.serviceConfig == {
        StandardOutPath = "${stateDir}/launchd-stdout.log";
        StandardErrorPath = "${stateDir}/launchd-stderr.log";
        EnvironmentVariables.PG_CONNECTOR_GITHUB_PPROF_ADDR = "${stateDir}/pprof.sock";
      };
    pprofDisabledOmitsVariable =
      (ghOpts launchdPprofOff).pprof.enable == false
      && !(entryOf launchdPprofOff).serviceConfig ? EnvironmentVariables
      &&
        (entryOf launchdPprofOff).serviceConfig == {
          StandardOutPath = "${stateDir}/launchd-stdout.log";
          StandardErrorPath = "${stateDir}/launchd-stderr.log";
        };
    pprofDisabledStillRegisters = (entryOf launchdPprofOff).label == e.label;
    # the variable rides in the entry's serviceConfig, not the command, so
    # toggling it never changes the rendered config file
    pprofToggleKeepsConfigAndCommand =
      (ghOpts launchdPprofOff).configFile == (ghOpts launchdOn).configFile
      && (entryOf launchdPprofOff).script == script;
    # the supervisor is the default on darwin
    defaultSupervisorRegisters = (entryOf launchdOnDefaultSupervisor).label == e.label;
    noAssertionFailsOnDarwin = failedAssertions launchdOn == [ ];
    noWarningOnDarwin = launchdOn.config.warnings == [ ];

    # ---- an apply that changes the binary or the config restarts the agent ----
    # (the registration embeds the wrapper's store-path hash, and the wrapper is
    # this script, so a different script is a different wrapper)
    configChangeChangesCommand =
      (ghOpts launchdOn).configFile != (ghOpts launchdOnNoOs).configFile
      && (entryOf launchdOnNoOs).script != script
      && carries "--config ${toString (ghOpts launchdOnNoOs).configFile} serve" (entryOf launchdOnNoOs)
      .script;
    settingChangeChangesCommand =
      (ghOpts launchdOn).configFile != (ghOpts launchdOnOtherCap).configFile
      && (entryOf launchdOnOtherCap).script != script;
    binaryChangeChangesCommand =
      (entryOf launchdOnOtherBinary).script != script
      && carries "${pkgs.hello}/bin/pg-connector-github" (entryOf launchdOnOtherBinary).script;
    sameInputsSameCommand = (entryOf (case darwin githubOn osWithObs)).script == script;

    # ---- supervisor = none registers no service ----------------------------
    noneRegistersNothing = entries supervisorNone == { };
    noneStillWarns = builtins.length supervisorNone.config.warnings == 1;
    noneNoFailedAssertion = failedAssertions supervisorNone == [ ];
    noneAllowedRegistersNothing =
      entries supervisorNoneAllowed == { } && failedAssertions supervisorNoneAllowed == [ ];
    disabledRegistersNothing = entries githubDisabled == { } && failedAssertions githubDisabled == [ ];

    # ---- launchd on a host that is not darwin fails evaluation -------------
    # (an assertion, never a silent no-op; no entry is emitted either)
    linuxLaunchdAsserts =
      builtins.length (messages linuxLaunchd) == 1
      && lib.hasInfix ''supervisor = "launchd" is only supported on darwin'' (
        builtins.head (messages linuxLaunchd)
      )
      && lib.hasInfix "pg2-opk5g" (builtins.head (messages linuxLaunchd));
    linuxLaunchdNoEntry = entries linuxLaunchd == { };
    # without personal's module the option is undeclared; the module still
    # evaluates and says why, the same way
    linuxLaunchdNoRegistryAsserts =
      builtins.length (messages linuxLaunchdNoRegistry) == 1
      && lib.hasInfix ''supervisor = "launchd" is only supported on darwin'' (
        builtins.head (messages linuxLaunchdNoRegistry)
      );
    linuxLaunchdNoRegistryNoOption =
      !(lib.hasAttrByPath [
        "phillipgreenii"
        "programs"
        "launchdServices"
      ] linuxLaunchdNoRegistry.config);
    # the Linux default needs neither the option nor an assertion
    linuxDefaultSupervisorIsNone =
      (ghOpts linuxDefault).supervisor == "none"
      && failedAssertions linuxDefault == [ ]
      && !(lib.hasAttrByPath [ "phillipgreenii" "programs" "launchdServices" ] linuxDefault.config);
    # darwin without personal's module: a clear assertion instead of an
    # "option does not exist" evaluation error
    darwinNoRegistryAsserts =
      builtins.length (messages darwinNoRegistry) == 1
      && lib.hasInfix "phillipgreenii.programs.launchdServices" (
        builtins.head (messages darwinNoRegistry)
      );
  };
in
{
  inherit cases;
  failures = builtins.attrNames (lib.filterAttrs (_: ok: !ok) cases);
  # What the live run starts: the entry's command text and the pieces the shell
  # side compares it against.
  live = {
    inherit script;
    # no system scope, so the daemon has no OTel endpoint to dial
    scriptNoOs = (entryOf launchdOnNoOs).script;
    package = pkgs.pg-connector-github;
    inherit ((ghOpts launchdOnNoOs)) configFile;
  };
}
