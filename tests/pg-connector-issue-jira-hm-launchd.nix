# Eval-level test of the pg-connector-issue-jira supervisor entry (docket
# pg2-2qyyh.2, Phase 2) in home/programs/pg-connector, the sibling of
# tests/pg-connector-github-hm-launchd.nix: the module is evaluated against a
# STUB of personal's HM launchd registry option (tests/launchd-registry-stub.nix)
# and against stubbed platforms. Consumed by
# checks.test-pg-connector-issue-jira-hm-launchd, which also runs the rendered
# entry's command for real, against a fake binary, under `env -i`.
#
# Scenario builders come from tests/pg-connector-home-render.nix (`harness`).
# All values are EXAMPLE values.
{ lib, pkgs }:
let
  inherit (import ./pg-connector-home-render.nix { inherit lib pkgs; }) harness;
  inherit (harness)
    evaluateWith
    githubRegistry
    githubOn
    osWithObs
    jiraRegistry
    jiraOn
    jiraRewriteOn
    jOpts
    ;

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

  case =
    platform: jira: osConfig:
    evaluateWith platform (jiraRegistry // { inherit jira; }) osConfig;

  agentName = "pg-connector-issue-jira-daemon";
  entries = c: c.config.phillipgreenii.programs.launchdServices.userAgents;
  entryOf = c: (entries c).${agentName};
  failedAssertions = c: lib.filter (a: !a.assertion) c.config.assertions;
  messages = c: map (a: a.message) (failedAssertions c);
  stateDir = "/example/home/.local/state/pg-connector-issue-jira-daemon";

  # A fake daemon binary: records the path it was exec'd as and its argv, using
  # only shell builtins (the live run has an empty environment).
  fakeBinary = pkgs.writeShellScriptBin "pg-connector-issue-jira" ''
    {
      printf '%s\n' "$0"
      printf '%s\n' "$@"
    } > "$FAKE_JIRA_OUT"
  '';

  # darwin, launchd (the default there), a system scope with an OTel endpoint
  launchdOn = case darwin jiraOn osWithObs;
  launchdRewriteOn = case darwin jiraRewriteOn osWithObs;
  launchdPprofOff = case darwin (jiraOn // { pprof.enable = false; }) osWithObs;
  launchdOnNoOs = case darwin jiraOn null;
  launchdOnOtherBinary = case darwin (jiraOn // { package = pkgs.hello; }) osWithObs;
  launchdOnOtherSetting = case darwin (jiraOn // { settings.request_rate_per_second = 4; }) osWithObs;
  launchdOnDefaultSupervisor = case darwin {
    enable = true;
    inherit (jiraOn) pjiraPath;
  } osWithObs;
  launchdFake = case darwin (jiraOn // { package = fakeBinary; }) null;
  supervisorNone = case darwin (jiraRewriteOn // { supervisor = "none"; }) osWithObs;
  supervisorNoneAllowed = case darwin (
    jiraRewriteOn
    // {
      supervisor = "none";
      allowUnsupervised = true;
    }
  ) osWithObs;
  jiraDisabled = case darwin { enable = false; } osWithObs;
  # launchd on a host that is not darwin: the registry may or may not be declared
  linuxLaunchd = case linux jiraOn osWithObs;
  linuxLaunchdNoRegistry = case (linux // { declareLaunchd = false; }) jiraOn osWithObs;
  linuxDefault = case (linux // { declareLaunchd = false; }) {
    enable = true;
    inherit (jiraOn) pjiraPath;
  } osWithObs;
  darwinNoRegistry = case (darwin // { declareLaunchd = false; }) jiraOn osWithObs;
  # no pjira path, and a relative one: evaluation must fail an assertion
  noPjira = case darwin { enable = true; } osWithObs;
  relativePjira = case darwin (jiraOn // { pjiraPath = "pjira"; }) osWithObs;
  # both daemons
  both = evaluateWith darwin (
    githubRegistry
    // {
      github = githubOn;
      jira = jiraOn;
    }
  ) osWithObs;

  e = entryOf launchdOn;
  inherit (e) script;
  configPath = toString (jOpts launchdOn).configFile;
  binary = "${pkgs.pg-connector-issue-jira}/bin/pg-connector-issue-jira";

  # lib.hasInfix builds a regex from the needle, which Nix refuses for a string
  # that refers to a store path; the needle's context is irrelevant to the match.
  carries = needle: lib.hasInfix (builtins.unsafeDiscardStringContext needle);

  cases = {
    # ---- the entry: label, restart policy, command, logs -------------------
    entryIsTheOnlyOne = builtins.attrNames (entries launchdOn) == [ agentName ];
    label = e.label == "com.phillipg.pg-connector-issue-jira-daemon";
    keepAlive = e.keepAlive == true;
    runAtLoad = e.runAtLoad == true;
    enabled = e.enable;
    # the command is exactly the daemon on the rendered config, nothing else
    command = script == "exec ${binary} --config ${configPath} serve\n";
    commandCarriesConfigAndServe = carries "--config ${configPath} serve" script;
    configIsAStorePath = lib.hasPrefix builtins.storeDir configPath;
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
    pprofEnabledByDefault = (jOpts launchdOn).pprof.enable == true;
    pprofEnabledServiceConfigIsExact =
      e.serviceConfig == {
        StandardOutPath = "${stateDir}/launchd-stdout.log";
        StandardErrorPath = "${stateDir}/launchd-stderr.log";
        EnvironmentVariables.PG_CONNECTOR_ISSUE_JIRA_PPROF_ADDR = "${stateDir}/pprof.sock";
      };
    pprofDisabledOmitsVariable =
      (jOpts launchdPprofOff).pprof.enable == false
      && !(entryOf launchdPprofOff).serviceConfig ? EnvironmentVariables
      &&
        (entryOf launchdPprofOff).serviceConfig == {
          StandardOutPath = "${stateDir}/launchd-stdout.log";
          StandardErrorPath = "${stateDir}/launchd-stderr.log";
        };
    pprofDisabledStillRegisters = (entryOf launchdPprofOff).label == e.label;
    pprofToggleKeepsConfigAndCommand =
      (jOpts launchdPprofOff).configFile == (jOpts launchdOn).configFile
      && (entryOf launchdPprofOff).script == script;
    defaultSupervisorRegisters = (entryOf launchdOnDefaultSupervisor).label == e.label;
    noAssertionFailsOnDarwin = failedAssertions launchdOn == [ ];
    noWarningOnDarwin = launchdOn.config.warnings == [ ];

    # ---- an apply that changes the binary or the config restarts the agent ----
    configChangeChangesCommand =
      (jOpts launchdOn).configFile != (jOpts launchdOnNoOs).configFile
      && (jOpts launchdOn).configHash != (jOpts launchdOnNoOs).configHash
      && (entryOf launchdOnNoOs).script != script
      && carries "--config ${toString (jOpts launchdOnNoOs).configFile} serve" (entryOf launchdOnNoOs)
      .script;
    settingChangeChangesCommand =
      (jOpts launchdOn).configFile != (jOpts launchdOnOtherSetting).configFile
      && (jOpts launchdOn).configHash != (jOpts launchdOnOtherSetting).configHash
      && (entryOf launchdOnOtherSetting).script != script;
    binaryChangeChangesCommand =
      (entryOf launchdOnOtherBinary).script != script
      && carries "${pkgs.hello}/bin/pg-connector-issue-jira" (entryOf launchdOnOtherBinary).script;
    sameInputsSameCommand = (entryOf (case darwin jiraOn osWithObs)).script == script;

    # ---- the entry does not depend on the deploy gate ------------------------
    gateKeepsEntry =
      (entryOf launchdRewriteOn).script == script
      && (entryOf launchdRewriteOn).label == e.label
      && (entryOf launchdRewriteOn).serviceConfig == e.serviceConfig;
    # the registry follows the gate: unchanged when off, rewritten when on
    gateOffRegistryUnchanged =
      launchdOn.config.phillipgreenii.programs.pg-connector.renderedConfig.connector.issue == [
        "pg-connector-issue-beads"
        "pg-connector-issue-jira"
      ];
    gateOnRegistryRewritten =
      (builtins.elemAt launchdRewriteOn.config.phillipgreenii.programs.pg-connector.renderedConfig.connector.issue 1)
      .command == [
        "pg-connector-issue-jira"
        "--config"
        (toString (jOpts launchdRewriteOn).configFile)
        "--instance"
        "pg-connector-issue-jira"
      ];

    # ---- supervisor = none registers no service ----------------------------
    noneRegistersNothing = entries supervisorNone == { };
    noneStillWarns = builtins.length supervisorNone.config.warnings == 1;
    noneNoFailedAssertion = failedAssertions supervisorNone == [ ];
    noneAllowedRegistersNothing =
      entries supervisorNoneAllowed == { } && failedAssertions supervisorNoneAllowed == [ ];
    disabledRegistersNothing = entries jiraDisabled == { } && failedAssertions jiraDisabled == [ ];

    # ---- launchd on a host that is not darwin fails evaluation -------------
    linuxLaunchdAsserts =
      builtins.length (messages linuxLaunchd) == 1
      && lib.hasInfix ''supervisor = "launchd" is only supported on darwin'' (
        builtins.head (messages linuxLaunchd)
      )
      && lib.hasInfix "pg2-opk5g" (builtins.head (messages linuxLaunchd));
    linuxLaunchdNoEntry = entries linuxLaunchd == { };
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
    linuxDefaultSupervisorIsNone =
      (jOpts linuxDefault).supervisor == "none"
      && failedAssertions linuxDefault == [ ]
      && !(lib.hasAttrByPath [ "phillipgreenii" "programs" "launchdServices" ] linuxDefault.config);
    darwinNoRegistryAsserts =
      builtins.length (messages darwinNoRegistry) == 1
      && lib.hasInfix "phillipgreenii.programs.launchdServices" (
        builtins.head (messages darwinNoRegistry)
      );

    # ---- an absolute pjira path is mandatory (launchd supplies no PATH) -----
    missingPjiraAsserts =
      builtins.length (messages noPjira) == 1
      && lib.hasInfix "jira.pjiraPath" (builtins.head (messages noPjira))
      && lib.hasInfix "ABSOLUTE" (builtins.head (messages noPjira));
    relativePjiraAsserts =
      builtins.length (messages relativePjira) == 1
      && lib.hasInfix "jira.pjiraPath" (builtins.head (messages relativePjira));
    absolutePjiraPasses = failedAssertions launchdOn == [ ];

    # ---- both daemons: two independent entries ------------------------------
    bothEntries =
      builtins.attrNames (entries both) == [
        "pg-connector-github-daemon"
        agentName
      ]
      && (entries both).pg-connector-github-daemon.label == "com.phillipg.pg-connector-github-daemon"
      && (entries both).${agentName}.label == e.label
      && failedAssertions both == [ ];
  };
in
{
  inherit cases;
  failures = builtins.attrNames (lib.filterAttrs (_: ok: !ok) cases);
  # What the live run starts: the entry's command text (against the fake
  # binary, no system scope) and the pieces the shell side compares it with.
  live = {
    inherit ((entryOf launchdFake)) script;
    package = fakeBinary;
    inherit ((jOpts launchdFake)) configFile;
  };
}
