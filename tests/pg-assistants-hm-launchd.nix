# Eval-level test of home/programs/pg-assistants, in the style of
# tests/pg-keep-awake-hm-launchd.nix and tests/pg-claude-code-recorder-hm-launchd.nix:
# the module is evaluated against a STUB of personal's HM launchd registry option
# (tests/launchd-registry-stub.nix), stubbed home-manager options (assertions,
# warnings, home.homeDirectory, home.file, home.packages, home.activation with a
# stubbed lib.hm.dag, home.sessionVariables, xdg.configFile, xdg.stateHome and
# programs.tldr) and stubbed platforms. The pg-claude-control module and the
# claude-code marketplaces override option are stub options declared here, each
# switchable off to cover the "not declared" path. Consumed by
# checks.<system>.test-pg-assistants-hm-launchd, which also builds the rendered
# config (so the real `pg-assistants config check` runs on it), reads the
# rendered TOML back, rejects bad renderings with the same command, runs the
# trust activation against a throwaway .claude.json, and runs the rendered
# launchd script under `env -i` through a fake login shell to a fake
# pg-assistants.
#
# All values are EXAMPLE values.
{ lib, pkgs }:
let
  # The flake's check pkgs has no llm-agents overlay: the module's default
  # claudePath reads pkgs.llm-agentsPkgs.claude-code, so a stand-in is supplied.
  fakeClaude = pkgs.writeShellScriptBin "claude" "exit 0";
  # a control package that is NOT pkgs.pg-claude-control, to tell the module's
  # option from its fallback
  fakeControl = pkgs.writeShellScriptBin "pg-claude-control" "exit 0";

  withPlatform = isDarwin: {
    pkgs' = pkgs // {
      llm-agentsPkgs = {
        claude-code = fakeClaude;
      };
      stdenv = pkgs.stdenv // {
        hostPlatform = pkgs.stdenv.hostPlatform // {
          inherit isDarwin;
        };
      };
    };
  };
  darwin = withPlatform true;
  linux = withPlatform false;

  # home-manager's extended lib: only lib.hm.dag.entryAfter is used.
  hmLib = lib // {
    hm = (lib.hm or { }) // {
      dag = (lib.hm.dag or { }) // {
        entryAfter = deps: text: { inherit deps text; };
      };
    };
  };

  stubs =
    {
      tldrEnable,
      sessionVariables,
      sessionPath,
      zshSessionVariables,
    }:
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
        };
        home = {
          homeDirectory = lib.mkOption {
            type = lib.types.str;
            default = "/example/home";
          };
          sessionVariables = lib.mkOption {
            type = lib.types.attrsOf lib.types.str;
            default = sessionVariables;
          };
          sessionPath = lib.mkOption {
            type = lib.types.listOf lib.types.str;
            default = sessionPath;
          };
          file = lib.mkOption {
            type = lib.types.attrsOf lib.types.anything;
            default = { };
          };
          packages = lib.mkOption {
            type = lib.types.listOf lib.types.package;
            default = [ ];
          };
          activation = lib.mkOption {
            type = lib.types.attrsOf lib.types.anything;
            default = { };
          };
        };
        programs.zsh.sessionVariables = lib.mkOption {
          type = lib.types.attrsOf lib.types.str;
          default = zshSessionVariables;
        };
        programs.tldr = {
          enable = lib.mkOption {
            type = lib.types.bool;
            default = tldrEnable;
          };
          customPages = lib.mkOption {
            type = lib.types.attrsOf lib.types.anything;
            default = { };
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
    };

  # The pg-claude-control module's two options this module reads.
  controlStub =
    control:
    { lib, ... }:
    {
      options.phillipgreenii.programs.pg-claude-control = {
        enable = lib.mkOption {
          type = lib.types.bool;
          default = false;
        };
        package = lib.mkOption {
          type = lib.types.package;
          default = pkgs.pg-claude-control;
        };
      };
      config.phillipgreenii.programs.pg-claude-control = control;
    };

  # The claude-marketplaces module's override option.
  marketplaceStub =
    overrides:
    { lib, ... }:
    {
      options.phillipgreenii.programs.claude-code.marketplaces.overrides = lib.mkOption {
        type = lib.types.attrsOf lib.types.bool;
        default = { };
      };
      config.phillipgreenii.programs.claude-code.marketplaces.overrides = overrides;
    };

  evaluateWith =
    {
      pkgs',
      declareLaunchd ? true,
      tldrEnable ? true,
      sessionVariables ? { },
      sessionPath ? [ ],
      zshSessionVariables ? { },
      # null: the module is not declared; otherwise its stub values
      control ? {
        enable = true;
      },
      overrides ? { },
    }:
    cfg:
    lib.evalModules {
      specialArgs = {
        pkgs = pkgs';
        lib = hmLib;
        osConfig = null;
      };
      modules = [
        ../home/programs/pg-assistants/default.nix
        (stubs {
          inherit
            tldrEnable
            sessionVariables
            sessionPath
            zshSessionVariables
            ;
        })
        {
          phillipgreenii.programs.pg-assistants = {
            enable = true;
          }
          // cfg;
        }
      ]
      ++ lib.optional declareLaunchd ./launchd-registry-stub.nix
      ++ lib.optional (control != null) (controlStub control)
      ++ lib.optional (overrides != null) (marketplaceStub overrides);
    };

  case = platform: cfg: evaluateWith platform cfg;

  agentName = "pg-assistants-daemon";
  opts = c: c.config.phillipgreenii.programs.pg-assistants;
  entries = c: c.config.phillipgreenii.programs.launchdServices.userAgents;
  entryOf = c: (entries c).${agentName};
  failedAssertions = c: lib.filter (a: !a.assertion) c.config.assertions;
  messages = c: map (a: a.message) (failedAssertions c);
  settingsOf = c: (opts c).internal.settings;
  hashOf = c: (entryOf c).serviceConfig.EnvironmentVariables.PG_ASSISTANTS_CONFIG_HASH;
  sessionVarsHashOf =
    c: (entryOf c).serviceConfig.EnvironmentVariables.PG_ASSISTANTS_SESSION_VARS_HASH;
  # lib.hasInfix matches by regex, which refuses strings that carry store-path context
  hasInfixCtx =
    infix: s:
    lib.hasInfix (builtins.unsafeDiscardStringContext infix) (builtins.unsafeDiscardStringContext s);
  oneMessageWith =
    c: infix: builtins.length (messages c) == 1 && hasInfixCtx infix (builtins.head (messages c));

  # Two assistants: A sets every optional key, B none (no tick, no window, no
  # env, the default name and effort).
  exampleA = {
    name = "Example A";
    model = "claude-sonnet-5-5";
    effort = "high";
    cwd = "/example/home/assistants/a";
    prompt = "Resume your duties per AGENTS.md.";
    tick = "30s";
    env.EXAMPLE = "value";
    allowedReads = [
      "/example/home/assistants/a/**"
      "/example/work/CLAUDE.md"
    ];
    context = {
      limit = 200000;
      window = 1000000;
      nudge = 0.8;
      interrupt = 0.9;
      kill = 1.2;
    };
  };
  exampleB = {
    model = "claude-opus-5-5";
    cwd = "/example/home/assistants/b";
    prompt = "Resume.";
    context.limit = 150000;
  };
  two = {
    example-a = exampleA;
    example-b = exampleB;
  };

  on = case darwin { assistants = two; };
  onNoPprof = case darwin {
    assistants = two;
    pprof.enable = false;
  };
  noAssistants = case darwin { };
  # only one value differs: assistant A's prompt
  otherPrompt = case darwin {
    assistants = two // {
      example-a = exampleA // {
        prompt = "Something else.";
      };
    };
  };
  # one assistant more
  threeAssistants = case darwin {
    assistants = two // {
      example-c = exampleB // {
        cwd = "/example/home/assistants/c";
      };
    };
  };
  disabled = case darwin {
    enable = false;
    assistants = two;
  };
  tldrOff = case (darwin // { tldrEnable = false; }) { assistants = two; };

  # the operator's session variables: a change restarts the daemon
  sessionVarsA = case (darwin // { sessionVariables.EXAMPLE_VAR = "a"; }) { assistants = two; };
  sessionVarsB = case (darwin // { sessionVariables.EXAMPLE_VAR = "b"; }) { assistants = two; };
  sessionPathA = case (darwin // { sessionPath = [ "/example/bin-a" ]; }) { assistants = two; };
  sessionPathB = case (darwin // { sessionPath = [ "/example/bin-b" ]; }) { assistants = two; };
  zshVarsA = case (darwin // { zshSessionVariables.EXAMPLE_VAR = "a"; }) { assistants = two; };
  zshVarsB = case (darwin // { zshSessionVariables.EXAMPLE_VAR = "b"; }) { assistants = two; };
  # the guards a session blanks, replaced
  otherGuards = case darwin {
    assistants = two;
    sessionBlankEnv = [ "EXAMPLE_GUARD" ];
  };

  # where folder trust is written
  claudeDirAbsolute =
    case (darwin // { sessionVariables.CLAUDE_CONFIG_DIR = "/example/claude-work"; })
      {
        assistants = two;
      };
  claudeDirHome = case (darwin // { sessionVariables.CLAUDE_CONFIG_DIR = "$HOME/.claude-work"; }) {
    assistants = two;
  };
  claudeDirHomeBraced =
    case (darwin // { sessionVariables.CLAUDE_CONFIG_DIR = "\${HOME}/.claude-b"; })
      {
        assistants = two;
      };

  # the assertions
  controlOff = case (darwin // { control.enable = false; }) { assistants = two; };
  controlUndeclared = case (darwin // { control = null; }) { assistants = two; };
  overrideOff = case (darwin // { overrides."pg-claude-control" = false; }) { assistants = two; };
  overrideKeyOff = case (
    darwin
    // {
      overrides."pg-claude-control@phillipgreenii-nix-agent-support-marketplace-local" = false;
    }
  ) { assistants = two; };
  # the resolver reads the qualified key first: it wins over the bare name
  overrideKeyOnBareOff = case (
    darwin
    // {
      overrides = {
        "pg-claude-control@phillipgreenii-nix-agent-support-marketplace-local" = true;
        "pg-claude-control" = false;
      };
    }
  ) { assistants = two; };
  overrideKeyOffBareOn = case (
    darwin
    // {
      overrides = {
        "pg-claude-control@phillipgreenii-nix-agent-support-marketplace-local" = false;
        "pg-claude-control" = true;
      };
    }
  ) { assistants = two; };
  customControl = case (
    darwin
    // {
      control = {
        enable = true;
        package = fakeControl;
      };
    }
  ) { assistants = two; };
  relativeClaudeJson = case darwin {
    assistants = two;
    claudeJson = "relative/.claude.json";
  };
  overrideOn = case (darwin // { overrides."pg-claude-control" = true; }) { assistants = two; };
  overridesUndeclared = case (darwin // { overrides = null; }) { assistants = two; };
  relativeCwd = case darwin {
    assistants = two // {
      example-b = exampleB // {
        cwd = "assistants/b";
      };
    };
  };
  relativeShell = case darwin {
    assistants = two;
    loginShell = "zsh";
  };

  # renderings `config check` MUST reject (built from internal.configUnchecked)
  badEffort = case darwin {
    assistants = two // {
      example-a = exampleA // {
        effort = "turbo";
      };
    };
  };
  overWindow = case darwin {
    assistants = two // {
      example-b = exampleB // {
        model = "claude-haiku-4-5";
        context.limit = 200000;
      };
    };
  };
  duplicateCwd = case darwin {
    assistants = two // {
      example-b = exampleB // {
        inherit (exampleA) cwd;
      };
    };
  };

  # platforms and the registry
  linuxRegistry = case linux { assistants = two; };
  linuxNoRegistry = case (linux // { declareLaunchd = false; }) { assistants = two; };
  darwinNoRegistry = case (darwin // { declareLaunchd = false; }) { assistants = two; };

  # the env -i run: a fake login shell and a fake pg-assistants
  fakeLog = "FAKE_LOG_DIR";
  fakeShell = pkgs.writeScript "example-login-shell" ''
    #!${pkgs.bash}/bin/bash
    # Records its argv, then behaves as a login shell would for `-l -c ...`.
    printf '%s\n' "$0" "$@" > "''$${fakeLog}/shell.argv"
    [ "$1" = -l ] || { echo "fake shell: want -l first, got $1" >&2; exit 90; }
    shift
    exec ${pkgs.bash}/bin/bash "$@"
  '';
  # Answers the module's build-time `config check` and records everything else
  # (absolute paths only: it runs under `env -i`, with no PATH).
  fakePgAssistants = pkgs.writeShellScriptBin "pg-assistants" ''
    if [ "$1" = config ]; then exit 0; fi
    printf '%s\n' "$@" > "''$${fakeLog}/daemon.argv"
    ${pkgs.coreutils}/bin/env > "''$${fakeLog}/daemon.env"
  '';
  fakeRun = case darwin {
    assistants = two;
    package = fakePgAssistants;
    loginShell = "${fakeShell}";
  };

  e = entryOf on;
  inherit (e) script;
  env = e.serviceConfig.EnvironmentVariables;
  pkg = pkgs.pg-assistants;
  binary = "${pkg}/bin/pg-assistants";
  stateHome = "/example/home/.local/state";
  stateDir = "${stateHome}/pg-assistants";
  s = settingsOf on;
  a = builtins.elemAt s.assistant 0;
  b = builtins.elemAt s.assistant 1;
  trustEntry = c: c.config.home.activation.pgAssistantsTrust;
  withQuota = case darwin {
    assistants = two // {
      example-b = exampleB // {
        quota = {
          dollars = 5;
          window = "24h";
        };
      };
    };
  };
  quotaOf = c: (builtins.elemAt (settingsOf c).assistant 1).quota or null;

  cases = {
    # ---- the entry exists only when enabled, on darwin ---------------------
    entryIsTheOnlyOne = builtins.attrNames (entries on) == [ agentName ];
    disabledRegistersNothing = entries disabled == { } && failedAssertions disabled == [ ];
    disabledRendersNothing =
      disabled.config.xdg.configFile == { }
      && disabled.config.home.packages == [ ]
      && disabled.config.home.activation == { }
      && disabled.config.programs.tldr.customPages == { };

    # ---- label, keep-alive and the shutdown budget --------------------------
    label = e.label == "com.phillipg.pg-assistants-daemon";
    keepAlive = e.keepAlive == true;
    runAtLoad = e.runAtLoad == true;
    enabled = e.enable;
    abandonProcessGroup = e.serviceConfig.AbandonProcessGroup == true;
    # the 8 s shutdown deadline plus a 7 s margin
    exitTimeOut = e.serviceConfig.ExitTimeOut == 15;
    serviceConfigKeys =
      builtins.attrNames e.serviceConfig == [
        "AbandonProcessGroup"
        "EnvironmentVariables"
        "ExitTimeOut"
        "StandardErrorPath"
        "StandardOutPath"
      ];
    noExecPath = e.execPath == null;
    manageLogsDefault = e.manageLogs.enable == true;
    noAssertionFailsOnDarwin = failedAssertions on == [ ];
    noWarningOnDarwin = on.config.warnings == [ ];

    # ---- the launchd logs live beside, not inside, the 0700 state dir -------
    logsOutsideStateDir =
      e.serviceConfig.StandardOutPath == "${stateHome}/pg-assistants-launchd/launchd-stdout.log"
      && e.serviceConfig.StandardErrorPath == "${stateHome}/pg-assistants-launchd/launchd-stderr.log"
      && !(lib.hasPrefix "${stateDir}/" e.serviceConfig.StandardErrorPath)
      && !(lib.hasPrefix "${stateDir}/" e.serviceConfig.StandardOutPath);

    # ---- the environment: HOME, the config hash and the pprof socket --------
    envKeys =
      builtins.attrNames env == [
        "HOME"
        "PG_ASSISTANTS_CONFIG_HASH"
        "PG_ASSISTANTS_PPROF_ADDR"
        "PG_ASSISTANTS_SESSION_VARS_HASH"
      ];
    homeIsSet = env.HOME == "/example/home";
    pprofSocketInStateDir = env.PG_ASSISTANTS_PPROF_ADDR == "${stateDir}/pprof.sock";
    pprofOffHasNoAddr =
      builtins.attrNames (entryOf onNoPprof).serviceConfig.EnvironmentVariables == [
        "HOME"
        "PG_ASSISTANTS_CONFIG_HASH"
        "PG_ASSISTANTS_SESSION_VARS_HASH"
      ];
    hashIsTheModules = env.PG_ASSISTANTS_CONFIG_HASH == (opts on).internal.configHash;
    hashLooksLikeSha256 = builtins.match "[0-9a-f]{64}" env.PG_ASSISTANTS_CONFIG_HASH != null;
    sameInputsSameHash = hashOf (case darwin { assistants = two; }) == hashOf on;
    promptChangeChangesHash = hashOf otherPrompt != hashOf on;
    addedAssistantChangesHash = hashOf threeAssistants != hashOf on;
    # a change of the operator's session variables restarts the daemon too
    sessionVarsHashLooksLikeSha256 =
      builtins.match "[0-9a-f]{64}" env.PG_ASSISTANTS_SESSION_VARS_HASH != null;
    sessionVarChangeChangesItsHash =
      sessionVarsHashOf sessionVarsA != sessionVarsHashOf sessionVarsB
      && sessionVarsHashOf sessionVarsA != sessionVarsHashOf on
      &&
        sessionVarsHashOf (
          case (darwin // { sessionVariables.EXAMPLE_VAR = "a"; }) {
            assistants = two;
          }
        ) == sessionVarsHashOf sessionVarsA;
    # ... and only that hash: the rendered config is the same
    sessionVarChangeKeepsConfigHash = hashOf sessionVarsA == hashOf sessionVarsB;

    # ---- what a session starts with blanked: the guards and the daemon's own --
    blankEnvRendered =
      s.blank_env == [
        "__HM_SESS_VARS_SOURCED"
        "__HM_ZSH_SESS_VARS_SOURCED"
        "__NIX_DARWIN_SET_ENVIRONMENT_DONE"
        "PG_ASSISTANTS_CONFIG_HASH"
        "PG_ASSISTANTS_PPROF_ADDR"
        "PG_ASSISTANTS_SESSION_VARS_HASH"
        "XPC_SERVICE_NAME"
      ];
    # every variable the agent sets for the daemon alone is blanked for sessions
    daemonOnlyEnvIsBlanked = lib.all (k: builtins.elem k s.blank_env) (
      builtins.attrNames (removeAttrs env [ "HOME" ])
    );
    # replacing the guards keeps the daemon-only names
    otherGuardsKeepDaemonOnly =
      (settingsOf otherGuards).blank_env == [
        "EXAMPLE_GUARD"
        "PG_ASSISTANTS_CONFIG_HASH"
        "PG_ASSISTANTS_PPROF_ADDR"
        "PG_ASSISTANTS_SESSION_VARS_HASH"
        "XPC_SERVICE_NAME"
      ];

    # ---- the command: the daemon through the operator's login shell --------
    command =
      builtins.unsafeDiscardStringContext script
      == builtins.unsafeDiscardStringContext "exec /bin/zsh -l -c 'exec \"$@\"' /bin/zsh ${binary} daemon --config ${(opts on).internal.configFile} --shutdown-deadline 8s\n";

    # ---- the rendered config -------------------------------------------------
    configRenderedAtXdgPath =
      builtins.attrNames on.config.xdg.configFile == [ "pg-assistants/config.toml" ]
      &&
        toString on.config.xdg.configFile."pg-assistants/config.toml".source
        == toString (opts on).internal.configFile;
    topLevelKeys =
      builtins.attrNames s == [
        "assistant"
        "blank_env"
        "blocks"
        "grace"
        "paths"
        "restart"
        "tick"
      ];
    globalDefaults =
      s.tick == "1m"
      &&
        s.grace == {
          interrupt = "5m";
          kill = "2m";
        }
      &&
        s.blocks == {
          nudge = 0.95;
          interrupt = 0.98;
          kill = 1.0;
          unknown_reset_hold = "1h";
        }
      &&
        s.restart == {
          min_uptime = "1m";
          backoff_initial = "1m";
          backoff_max = "30m";
          start_timeout = "5m";
        };
    pathsFilled =
      s.paths == {
        tmux = "${pkgs.tmux}/bin/tmux";
        claude = "${fakeClaude}/bin/claude";
        pg_claude_control = "${pkgs.pg-claude-control}/bin/pg-claude-control";
        shell = "/bin/zsh";
        state_dir = stateDir;
        claude_json = "/example/home/.claude.json";
      };
    # an array of tables, ordered by id
    assistantsAreAList = builtins.length s.assistant == 2 && a.id == "example-a" && b.id == "example-b";
    # ---- an assistant's quota ------------------------------------------------
    quotaOmittedByDefault = !(b ? quota) && !(a ? quota);
    quotaRendered =
      quotaOf withQuota == {
        dollars = 5;
        window = "24h";
        nudge = 0.8;
        interrupt = 0.95;
        kill = 1.0;
      };
    assistantAEveryKey =
      a == {
        id = "example-a";
        name = "Example A";
        enabled = true;
        model = "claude-sonnet-5-5";
        effort = "high";
        cwd = "/example/home/assistants/a";
        prompt = "Resume your duties per AGENTS.md.";
        tick = "30s";
        env.EXAMPLE = "value";
        allowed_tools = [
          "Read(//example/home/assistants/a/**)"
          "Read(//example/work/CLAUDE.md)"
        ];
        context = {
          limit = 200000;
          window = 1000000;
          nudge = 0.8;
          interrupt = 0.9;
          kill = 1.2;
        };
      };
    # null options are omitted; the module's defaults fill the rest
    assistantBOmitsNulls =
      b == {
        id = "example-b";
        name = "example-b";
        enabled = true;
        model = "claude-opus-5-5";
        effort = "medium";
        cwd = "/example/home/assistants/b";
        prompt = "Resume.";
        context = {
          limit = 150000;
          nudge = 0.8;
          interrupt = 0.95;
          kill = 1.0;
        };
      };
    noAssistantsNoKey = !((settingsOf noAssistants) ? assistant);

    # ---- where folder trust is written ---------------------------------------
    claudeJsonDefault = (opts on).claudeJson == "/example/home/.claude.json";
    claudeJsonFromConfigDir =
      (opts claudeDirAbsolute).claudeJson == "/example/claude-work/.claude.json"
      && (settingsOf claudeDirAbsolute).paths.claude_json == "/example/claude-work/.claude.json";
    # a $HOME in the session variable is expanded at evaluation, never rendered
    claudeJsonHomeExpanded =
      (opts claudeDirHome).claudeJson == "/example/home/.claude-work/.claude.json"
      && (opts claudeDirHomeBraced).claudeJson == "/example/home/.claude-b/.claude.json";

    # ---- the per-user profile and the tldr page ------------------------------
    packageInProfile = map toString on.config.home.packages == [ (toString pkg) ];
    noHomeFile = on.config.home.file == { };
    tldrPageRegistered =
      on.config.programs.tldr.customPages == {
        pg-assistants = {
          platform = "common";
          source = "${pkg}/share/tldr/pages.common/pg-assistants.md";
        };
      };
    tldrNotRegisteredWhenTldrIsOff = tldrOff.config.programs.tldr.customPages == { };

    # ---- folder trust at activation ----------------------------------------
    trustAfterWriteBoundary = (trustEntry on).deps == [ "writeBoundary" ];
    trustEveryCwd = hasInfixCtx "$DRY_RUN_CMD ${binary} trust --claude-json /example/home/.claude.json /example/home/assistants/a /example/home/assistants/b || true" (trustEntry on)
    .text;
    trustUsesTheConfigDir = hasInfixCtx "--claude-json /example/claude-work/.claude.json " (trustEntry claudeDirAbsolute)
    .text;
    noAssistantsNoTrust = noAssistants.config.home.activation == { };

    # ---- the assertions -------------------------------------------------------
    controlOffAsserts = oneMessageWith controlOff "phillipgreenii.programs.pg-claude-control.enable";
    controlUndeclaredPasses = failedAssertions controlUndeclared == [ ];
    # without the control module the store path still comes from pkgs
    controlUndeclaredPath =
      (settingsOf controlUndeclared).paths.pg_claude_control
      == "${pkgs.pg-claude-control}/bin/pg-claude-control";
    overrideOffAsserts = oneMessageWith overrideOff "marketplaces.overrides";
    overrideKeyOffAsserts = oneMessageWith overrideKeyOff "marketplaces.overrides";
    overrideKeyOnBareOffPasses = failedAssertions overrideKeyOnBareOff == [ ];
    overrideKeyOffBareOnAsserts = oneMessageWith overrideKeyOffBareOn "marketplaces.overrides";
    controlPackageFromTheOption =
      (settingsOf customControl).paths.pg_claude_control == "${fakeControl}/bin/pg-claude-control";
    relativeClaudeJsonAsserts = oneMessageWith relativeClaudeJson "claudeJson";
    sessionPathChangesHash = sessionVarsHashOf sessionPathA != sessionVarsHashOf sessionPathB;
    zshVarsChangeHash = sessionVarsHashOf zshVarsA != sessionVarsHashOf zshVarsB;
    overrideOnPasses = failedAssertions overrideOn == [ ];
    overridesUndeclaredPasses = failedAssertions overridesUndeclared == [ ];
    relativeCwdAsserts =
      oneMessageWith relativeCwd "example-b" && oneMessageWith relativeCwd "assistants/b";
    relativeShellAsserts = oneMessageWith relativeShell "loginShell";
    darwinNoRegistryAsserts = oneMessageWith darwinNoRegistry "phillipgreenii.programs.launchdServices";

    # ---- a host that is not darwin: no entry, one warning ---------------------
    linuxNoEntry = entries linuxRegistry == { };
    linuxNoFailure = failedAssertions linuxRegistry == [ ] && failedAssertions linuxNoRegistry == [ ];
    linuxOneWarning =
      builtins.length linuxRegistry.config.warnings == 1
      && lib.hasInfix "no supervisor" (builtins.head linuxRegistry.config.warnings)
      && linuxNoRegistry.config.warnings == linuxRegistry.config.warnings;
    linuxNoRegistryNoOption =
      !(lib.hasAttrByPath [ "phillipgreenii" "programs" "launchdServices" ] linuxNoRegistry.config);
    linuxStillRendersConfig =
      builtins.attrNames linuxRegistry.config.xdg.configFile == [ "pg-assistants/config.toml" ];
  };

  # The rendered entry as a runnable script: its environment as exports (HOME
  # excluded: the run sets its own), then the entry's own text.
  wrapperOf =
    c:
    let
      entry = entryOf c;
      exports = lib.mapAttrsToList (k: v: "export ${k}=${lib.escapeShellArg v}") (
        removeAttrs entry.serviceConfig.EnvironmentVariables [ "HOME" ]
      );
    in
    pkgs.writeText "pg-assistants-daemon-entry.sh" ''
      ${lib.concatStringsSep "\n" exports}
      ${entry.script}
    '';
in
{
  inherit cases;
  failures = builtins.attrNames (lib.filterAttrs (_: ok: !ok) cases);
  # What the shell side checks.
  live = {
    package = pkg;
    tldrSource = on.config.programs.tldr.customPages.pg-assistants.source;
    configFile = (opts on).internal.configFile;
    trustText = (trustEntry on).text;
    # rendered but NOT checked: what `config check` must reject
    uncheckedBadEffort = (opts badEffort).internal.configUnchecked;
    uncheckedOverWindow = (opts overWindow).internal.configUnchecked;
    uncheckedDuplicateCwd = (opts duplicateCwd).internal.configUnchecked;
    # the module's own build-time check: the checked file of a bad rendering
    # FAILS to build (testBuildFailure captures the failure as an output)
    badConfigBuild = pkgs.testers.testBuildFailure (opts badEffort).internal.configFile;
    # the env -i run
    fakeWrapper = wrapperOf fakeRun;
    fakeShell = "${fakeShell}";
    fakeBinary = "${fakePgAssistants}/bin/pg-assistants";
    fakeConfigFile = (opts fakeRun).internal.configFile;
    fakeHash = hashOf fakeRun;
    fakeSessionVarsHash = sessionVarsHashOf fakeRun;
    inherit stateDir;
  };
}
