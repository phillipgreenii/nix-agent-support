# Eval-level rendering test for home/programs/pg-connector (bead pg2-9tql6).
#
# Evaluates the module with lib.evalModules (stubbing only home.packages and
# xdg.configFile, like checks.test-pa-monitor-config-gating) and returns, per
# scenario, the generated shared-config derivation that the module installs at
# xdg.configFile."pg-pr/config.yaml".source. Consumed by
# checks.test-pg-connector-home-rendering, which diffs each against a golden
# file under tests/fixtures/pg-connector-home/.
#
# All values below are EXAMPLE values for fixtures only. The real machine
# registration (base_url, real queries) lives in the consuming machine flake.
{ lib, pkgs }:
let
  # evaluateWith: the whole evaluated module set, for a scenario's cfg and an
  # osConfig (null models a standalone home-manager activation). `pkgs'` lets a
  # scenario present another platform (the launchd cases of
  # tests/pg-connector-github-hm-launchd.nix), and `declareLaunchd = false`
  # models a host that does not import personal's home module.
  evaluateWith =
    {
      pkgs' ? pkgs,
      declareLaunchd ? true,
    }:
    cfg: osConfig:
    lib.evalModules {
      # home-manager always passes osConfig (null for a standalone activation),
      # so the stub does too; the module cannot rely on its `? null` default
      # under a bare lib.evalModules.
      specialArgs = {
        pkgs = pkgs';
        inherit lib osConfig;
      };
      modules = [
        ../home/programs/pg-connector/default.nix
        (
          { lib, ... }:
          {
            options = {
              home.packages = lib.mkOption {
                type = lib.types.listOf lib.types.anything;
                default = [ ];
              };
              xdg.configFile = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
              # Declared by home-manager itself: the module reads the home
              # directory for the daemon's absolute state path and appends to
              # warnings when the registry rewrite is withheld.
              home.homeDirectory = lib.mkOption {
                type = lib.types.str;
                default = "/example/home";
              };
              warnings = lib.mkOption {
                type = lib.types.listOf lib.types.str;
                default = [ ];
              };
              # Also declared by home-manager: the module asserts that
              # supervisor = "launchd" is possible on this host.
              assertions = lib.mkOption {
                type = lib.types.listOf lib.types.anything;
                default = [ ];
              };
            };
          }
        )
        {
          phillipgreenii.programs.pg-connector = cfg // {
            enable = true;
          };
        }
      ]
      # personal's HM launchdServices option, stubbed (this flake has no input
      # on personal; see tests/launchd-registry-stub.nix).
      ++ lib.optional declareLaunchd ./launchd-registry-stub.nix;
    };
  evaluate = evaluateWith { };

  render = cfg: (evaluate cfg null).config.xdg.configFile."pg-pr/config.yaml".source;

  # Every pre-alert capability and key the module still renders. The deadline
  # keys (`attention.perBackend.<name>.threshold`/`exclude`) are not among
  # them: they were retired with the PR, Jira and beads backends' own
  # `list_attention` code, so this scenario no longer sets them.
  legacy = {
    connector = {
      pr = [ "pg-connector-pr-github" ];
      issue = [
        "pg-connector-issue-beads"
        "pg-connector-issue-jira"
      ];
      ci = [ "pg-connector-ci-github-actions" ];
      scm = "pg-connector-scm-git";
      thread = [ "pg-connector-thread-slack" ];
      calendar = [ "pg-connector-calendar-osx-bridge" ];
      agentsession = [ "pg-connector-agentsession-pa-monitor" ];
    };
    attention = {
      sources = [
        "pg-connector-issue-beads"
        "pg-connector-pr-github"
      ];
    };
    search.sources = [ "pg-connector-issue-beads" ];
    backends."pg-connector-pr-github".example_key = "example-value";
    state.consumer_prune_after = "720h";
    configSchemaVersion = 2;
  };

  # Design 5.1's Grafana example, rendered through the alert options.
  alertExample = {
    connector.alert = [ "pg-connector-alert-grafana" ];
    attention.sources = [ "pg-connector-alert-grafana" ];
    attention.perBackend."pg-connector-alert-grafana".attentionQuery = "attention";
    alertBackends."pg-connector-alert-grafana" = {
      base_url = "https://grafana.example.localhost";
      queries = {
        everything = "{}";
        attention = [ ''{severity=~"critical|warning"}'' ];
        quiet-hours = ''{severity="critical"}'';
      };
    };
  };

  # mail (docket pg2-qc5uc): the registration alone, with example-only values.
  mailExample = {
    connector.mail = [ "pg-connector-mail-osx-bridge" ];
    attention.sources = [ "pg-connector-mail-osx-bridge" ];
    search.sources = [ "pg-connector-mail-osx-bridge" ];
  };

  # activity (docket pg2-vfmp7.1): the registration alone, example-only values.
  activityExample = {
    connector.pr = [ "pg-connector-pr-github" ];
    activity.sources = [ "pg-connector-pr-github" ];
  };

  # Instances (bead pg2-91y12, INV-REG-4): one beads binary registered twice
  # under suffixed names with a per-instance argv, in every registration
  # (connector.issue next to a plain string, attention/search/activity
  # sources), plus a plain-string scm. EXAMPLE paths only.
  instanceBeads = suffix: {
    name = "pg-connector-issue-beads-${suffix}";
    command = [
      "pg-connector-issue-beads"
      "--beads-dir"
      "/example/${suffix}"
    ];
  };
  # The same instance after a rename (bead pg2-ik9ew): previousNames renders as
  # previous_names; an empty list renders no key at all.
  instanceBeadsRenamed =
    suffix: instanceBeads suffix // { previousNames = [ "pg-connector-issue-beads" ]; };
  instancesExample = {
    connector = {
      issue = [
        "pg-connector-issue-jira"
        (instanceBeadsRenamed "pg2")
        (instanceBeadsRenamed "zr")
      ];
      scm = "pg-connector-scm-git";
    };
    attention.sources = [
      (instanceBeadsRenamed "pg2")
      (instanceBeadsRenamed "zr")
    ];
    search.sources = [
      (instanceBeadsRenamed "pg2")
      (instanceBeadsRenamed "zr")
    ];
    activity.sources = [
      (instanceBeadsRenamed "pg2")
      (instanceBeadsRenamed "zr")
    ];
    backends = {
      "pg-connector-issue-beads-pg2".activity_actors = [ "Example Person" ];
      "pg-connector-issue-beads-zr".activity_actors = [ "Example Person" ];
    };
  };

  # Beads attention (bead pg2-wyeq4): both tracker instances registered under
  # attention.sources and search.sources, each with its OWN attentionLabels
  # (rendered as backends.<instance>.attention_labels), plus the alerts
  # backend's attentionQuery alongside to prove the two keys coexist. EXAMPLE
  # values only.
  beadsAttentionExample = {
    attention = {
      sources = [
        (instanceBeads "pg2")
        (instanceBeads "zr")
      ];
      perBackend = {
        "pg-connector-issue-beads-pg2".attentionLabels = [
          "attention"
          "human-focus"
        ];
        "pg-connector-issue-beads-zr".attentionLabels = [ "attention" ];
      };
    };
    search.sources = [
      (instanceBeads "pg2")
      (instanceBeads "zr")
    ];
  };

  # The bd budget (docket pg2-2qyyh.5): two beads instances and a bare-string
  # beads registration given ONE group, next to a Jira string and an scm string
  # that must stay untouched. EXAMPLE values only.
  bdBudgetAll = {
    maxConcurrent = 6;
    maxPerMinute = 60;
    waitBound = 3;
    writeReserve = 1;
    group = "example-shared";
  };
  bdBudgetExample = {
    connector = {
      issue = [
        "pg-connector-issue-jira"
        "pg-connector-issue-beads"
        (instanceBeads "pg2")
        (instanceBeads "zr")
      ];
      scm = "pg-connector-scm-git";
    };
    attention.sources = [
      (instanceBeads "pg2")
      (instanceBeads "zr")
    ];
    bdBudget = bdBudgetAll;
  };
  # Only waitBound set, as a duration string: one flag, nothing else.
  bdBudgetPartialExample = {
    connector.issue = [
      (instanceBeads "pg2")
      "pg-connector-issue-jira"
    ];
    bdBudget.waitBound = "4s";
  };

  # scm as a single {name, command} instance (connector.scm is single-valued).
  scmInstanceExample = {
    connector.scm = {
      name = "pg-connector-scm-git-example";
      command = [
        "pg-connector-scm-git"
        "--example"
      ];
    };
  };

  # ---- pg-connector-github (docket pg2-z5fax, Phase 2) ----------------------
  # One registration of the two old GitHub instance names in EVERY registry
  # list, plus an unrelated plain-string name next to them. EXAMPLE values only.
  githubRegistry = {
    connector = {
      pr = [ "pg-connector-pr-github" ];
      ci = [ "pg-connector-ci-github-actions" ];
    };
    search.sources = [ "pg-connector-pr-github" ];
    activity.sources = [ "pg-connector-pr-github" ];
    attention.sources = [
      "pg-connector-issue-beads"
      "pg-connector-pr-github"
    ];
    backends."pg-connector-pr-github" = {
      queries = {
        mine = "is:pr is:open author:@me";
        team = [
          "is:pr is:open review-requested:@me"
          "is:pr is:open assignee:@me"
        ];
      };
      rate_reserve_points = 500;
    };
    backends."pg-connector-ci-github-actions".example_key = "example-value";
  };
  githubOn = {
    enable = true;
    host = "github.example.invalid";
    supervisor = "launchd";
  };
  # A system scope whose observability stack answers mkEmitterEnv.
  osWithObs = {
    phillipgreenii.observability.mkEmitterEnv = _: {
      OTEL_EXPORTER_OTLP_ENDPOINT = "http://otel.example.invalid:4317";
    };
  };

  ghCase = github: osConfig: evaluate (githubRegistry // { inherit github; }) osConfig;
  ghEnabled = ghCase githubOn osWithObs;
  ghEnabledNoOs = ghCase githubOn null;
  ghEnabledEmptyOs = ghCase githubOn { };
  # Supervisor none and no allowUnsupervised: binary and config, no rewrite.
  ghUnsupervised = ghCase (
    githubOn
    // {
      supervisor = "none";
    }
  ) osWithObs;
  ghUnsupervisedAllowed = ghCase (
    githubOn
    // {
      supervisor = "none";
      allowUnsupervised = true;
    }
  ) osWithObs;
  ghDisabled = ghCase { enable = false; } osWithObs;
  # A watched query (docket pg2-6mo45.3, packet P2a-2): the object form
  # { expr, watched = true } on one query and the unchanged string and list
  # forms on the others. The backends block is free-form, so the option that
  # carries the flag is the query value itself; the daemon config and the
  # umbrella block render it from that one declaration.
  githubWatchedRegistry = githubRegistry // {
    backends = githubRegistry.backends // {
      "pg-connector-pr-github" = githubRegistry.backends."pg-connector-pr-github" // {
        queries = {
          mine = {
            expr = "is:pr is:open author:@me";
            watched = true;
          };
          team = [
            "is:pr is:open review-requested:@me"
            "is:pr is:open assignee:@me"
          ];
          sweep = "is:pr is:open label:example";
        };
      };
    };
  };
  ghWatched = evaluate (githubWatchedRegistry // { github = githubOn; }) osWithObs;
  # No github.* attribute at all: today's configuration.
  ghAbsent = evaluate githubRegistry null;

  rc = e: e.config.phillipgreenii.programs.pg-connector.renderedConfig;
  ghOpts = e: e.config.phillipgreenii.programs.pg-connector.github;
  pkgNames = e: map (p: p.name or (toString p)) e.config.home.packages;
  installsDaemon = e: builtins.elem (ghOpts e).package.name (pkgNames e);
  entry = name: kind: e: {
    inherit name;
    command = [
      "pg-connector-github"
      "--config"
      (toString (ghOpts e).configFile)
      kind
    ];
  };
  prEntry = entry "pg-connector-pr-github" "pr";
  ciEntry = entry "pg-connector-ci-github-actions" "ci";
  # The argv of every occurrence of the two old names in a rendered registry.
  occurrences =
    r:
    builtins.filter
      (
        x:
        builtins.isAttrs x
        && (x.name == "pg-connector-pr-github" || x.name == "pg-connector-ci-github-actions")
      )
      (
        r.connector.pr or [ ]
        ++ r.connector.ci or [ ]
        ++ r.search.sources or [ ]
        ++ r.activity.sources or [ ]
        ++ r.attention.sources or [ ]
      );
  oldRegistry = {
    connector = {
      pr = [ "pg-connector-pr-github" ];
      ci = [ "pg-connector-ci-github-actions" ];
    };
    search.sources = [ "pg-connector-pr-github" ];
    activity.sources = [ "pg-connector-pr-github" ];
    attention.sources = [
      "pg-connector-issue-beads"
      "pg-connector-pr-github"
    ];
  };
  registryOf = r: {
    inherit (r)
      connector
      search
      activity
      attention
      ;
  };
  noStamp =
    e:
    !(builtins.any (n: (rc e).backends.${n} ? config_hash || (rc e).backends.${n} ? config_path) [
      "pg-connector-pr-github"
      "pg-connector-ci-github-actions"
    ]);
  stamped =
    e:
    builtins.all
      (
        n:
        (rc e).backends.${n}.config_hash == (ghOpts e).configHash
        && (rc e).backends.${n}.config_path == toString (ghOpts e).configFile
      )
      [
        "pg-connector-pr-github"
        "pg-connector-ci-github-actions"
      ];

  githubCases = {
    # github.enable = false (and no github.* at all): the registry, the
    # installed packages and the blocks are exactly today's.
    disabledRegistryUnchanged = registryOf (rc ghDisabled) == oldRegistry;
    absentRegistryUnchanged = registryOf (rc ghAbsent) == oldRegistry;
    disabledPackagesIdentical =
      map (p: p.drvPath) ghDisabled.config.home.packages
      == map (p: p.drvPath) ghAbsent.config.home.packages
      && !(installsDaemon ghDisabled);
    disabledRenderedIdentical = rc ghDisabled == rc ghAbsent;
    disabledNoStampNoWarning = noStamp ghDisabled && ghDisabled.config.warnings == [ ];

    # Enabled under launchd: every occurrence of both old names in every list
    # carries the argv form with ONE --config path, the names are unchanged.
    enabledPr = (rc ghEnabled).connector.pr == [ (prEntry ghEnabled) ];
    enabledCi = (rc ghEnabled).connector.ci == [ (ciEntry ghEnabled) ];
    enabledSearch = (rc ghEnabled).search.sources == [ (prEntry ghEnabled) ];
    enabledActivity = (rc ghEnabled).activity.sources == [ (prEntry ghEnabled) ];
    enabledAttention =
      (rc ghEnabled).attention.sources == [
        "pg-connector-issue-beads"
        (prEntry ghEnabled)
      ];
    enabledOccurrenceCount = builtins.length (occurrences (rc ghEnabled)) == 5;
    enabledOneConfigPath =
      builtins.length (lib.unique (map (x: builtins.elemAt x.command 2) (occurrences (rc ghEnabled))))
      == 1;
    enabledPathIsStorePath = lib.hasPrefix builtins.storeDir (toString (ghOpts ghEnabled).configFile);
    enabledStamped = stamped ghEnabled;
    enabledInstallsDaemon = installsDaemon ghEnabled;
    enabledNoWarning = ghEnabled.config.warnings == [ ];
    # the old backends stay installed (rollback)
    enabledKeepsOldPackages =
      builtins.elem pkgs.pg-connector-pr-github.name (pkgNames ghEnabled)
      && builtins.elem pkgs.pg-connector-ci-github-actions.name (pkgNames ghEnabled);
    # the queries and the other block keys survive the stamp
    enabledBlockKeepsKeys =
      (rc ghEnabled).backends."pg-connector-pr-github".queries.mine == "is:pr is:open author:@me"
      && (rc ghEnabled).backends."pg-connector-ci-github-actions".example_key == "example-value";

    # a watched query renders as the object form in the umbrella block and the
    # unwatched ones keep their plain string and list forms
    watchedBlockKeepsForms =
      (rc ghWatched).backends."pg-connector-pr-github".queries.mine.watched == true
      && builtins.isString (rc ghWatched).backends."pg-connector-pr-github".queries.sweep
      && builtins.isList (rc ghWatched).backends."pg-connector-pr-github".queries.team;

    # supervisor = none, no allowUnsupervised: binary and config installed,
    # old backends kept exactly, no stamp, and the evaluation warning.
    unsupervisedInstalls = installsDaemon ghUnsupervised;
    unsupervisedKeepsRegistry = registryOf (rc ghUnsupervised) == oldRegistry;
    unsupervisedNoStamp = noStamp ghUnsupervised;
    unsupervisedWarns =
      builtins.length ghUnsupervised.config.warnings == 1
      && lib.hasInfix "allowUnsupervised" (builtins.head ghUnsupervised.config.warnings);
    unsupervisedHasConfigPath = lib.hasPrefix builtins.storeDir (
      toString (ghOpts ghUnsupervised).configFile
    );
    # ... and allowUnsupervised rewrites it anyway.
    allowedRewrites =
      (rc ghUnsupervisedAllowed).connector.pr == [ (prEntry ghUnsupervisedAllowed) ]
      && (rc ghUnsupervisedAllowed).connector.ci == [ (ciEntry ghUnsupervisedAllowed) ]
      && builtins.length (occurrences (rc ghUnsupervisedAllowed)) == 5
      && stamped ghUnsupervisedAllowed;
    allowedNoWarning = ghUnsupervisedAllowed.config.warnings == [ ];

    # An absent or observability-less system scope renders (no endpoint) and
    # does not fail evaluation.
    nullOsConfigRenders = (rc ghEnabledNoOs).connector.pr == [ (prEntry ghEnabledNoOs) ];
    emptyOsConfigRenders = (rc ghEnabledEmptyOs).connector.pr == [ (prEntry ghEnabledEmptyOs) ];
    # The stamp (hash and path) follows the content: an OTel endpoint is part of
    # the daemon config, so it changes both.
    configFollowsContent =
      (ghOpts ghEnabled).configHash != (ghOpts ghEnabledNoOs).configHash
      && (ghOpts ghEnabled).configFile != (ghOpts ghEnabledNoOs).configFile;
    # The unsupervised registry rewrite does not depend on the supervisor: the
    # same settings give the same config path.
    configIndependentOfSupervisor =
      (ghOpts ghEnabled).configFile == (ghOpts ghUnsupervisedAllowed).configFile;
  };

  # ---- pg-connector-issue-jira (docket pg2-2qyyh.2, Phase 2) ----------------
  # The Jira instance name registered in EVERY registry list: a plain string in
  # three of them and a full instance (with a former name) in search.sources,
  # next to unrelated names. EXAMPLE values only.
  jiraName = "pg-connector-issue-jira";
  jiraRegistry = {
    connector.issue = [
      "pg-connector-issue-beads"
      jiraName
    ];
    attention.sources = [
      "pg-connector-issue-beads"
      jiraName
    ];
    search.sources = [
      {
        name = jiraName;
        command = [ "example-old-binary" ];
        previousNames = [ "example-former-name" ];
      }
    ];
    activity.sources = [ jiraName ];
    backends.${jiraName} = {
      queries = {
        mine = {
          expr = "assignee = currentUser()";
          watched = true;
        };
        team = [
          "project = EXAMPLE AND status = Open"
          "project = EXAMPLE AND assignee is EMPTY"
        ];
        sweep = "labels = example";
      };
      example_key = "example-value";
    };
  };
  jiraOn = {
    enable = true;
    supervisor = "launchd";
    pjiraPath = "/example/bin/pjira";
  };
  jiraRewriteOn = jiraOn // {
    registry.rewrite = true;
  };
  jiraCase = jira: osConfig: evaluate (jiraRegistry // { inherit jira; }) osConfig;
  jiraAbsent = evaluate jiraRegistry null;
  jiraDisabled = jiraCase { enable = false; } osWithObs;
  jiraEnabled = jiraCase jiraOn osWithObs;
  jiraRewritten = jiraCase jiraRewriteOn osWithObs;
  jiraEnabledNoOs = jiraCase jiraOn null;
  jiraOtherSetting = jiraCase (jiraOn // { settings.request_rate_per_second = 4; }) osWithObs;
  jiraSettingsOverride = jiraCase (
    jiraOn
    // {
      settings = {
        workers = 3;
        state_dir = "/example/other";
        instance = "example-other";
        pjira_path = "/example/other/pjira";
        queries = { };
      };
    }
  ) osWithObs;
  jiraWithEnv = jiraCase (jiraOn // { pjiraEnvironment.EXAMPLE_VAR = "example-value"; }) osWithObs;
  jiraNoSupervisor = jiraCase (
    jiraRewriteOn
    // {
      supervisor = "none";
    }
  ) osWithObs;
  jiraNoSupervisorAllowed = jiraCase (
    jiraRewriteOn
    // {
      supervisor = "none";
      allowUnsupervised = true;
    }
  ) osWithObs;
  # a rewrite gated off keeps its daemon file and warns nothing
  jiraNoneNoRewrite = jiraCase (jiraOn // { supervisor = "none"; }) osWithObs;
  # another instance name: only the entries of THAT name are rewritten
  jiraOtherInstanceName = "pg-connector-issue-jira-example";
  jiraOtherInstance = evaluate (
    jiraRegistry
    // {
      connector.issue = [
        jiraName
        jiraOtherInstanceName
      ];
      jira = jiraRewriteOn // {
        instance = jiraOtherInstanceName;
      };
    }
  ) osWithObs;
  # both daemons on at once: each rewrites only its own names
  jiraWithGithub = evaluate (
    githubRegistry
    // {
      connector = githubRegistry.connector // {
        issue = [ jiraName ];
      };
      backends = githubRegistry.backends // jiraRegistry.backends;
      github = githubOn;
      jira = jiraRewriteOn;
    }
  ) osWithObs;

  jOpts = e: e.config.phillipgreenii.programs.pg-connector.jira;
  jiraEntry = e: {
    name = jiraName;
    command = [
      "pg-connector-issue-jira"
      "--config"
      (toString (jOpts e).configFile)
      "--instance"
      jiraName
    ];
  };
  jiraOccurrences =
    r:
    builtins.filter (x: builtins.isAttrs x && x.name == jiraName) (
      r.connector.issue or [ ]
      ++ r.search.sources or [ ]
      ++ r.activity.sources or [ ]
      ++ r.attention.sources or [ ]
    );
  # The registry lists as a host without the daemon renders them, the
  # search.sources instance in its rendered (snake_case) form.
  oldJiraRegistry = {
    connector.issue = [
      "pg-connector-issue-beads"
      jiraName
    ];
    attention.sources = [
      "pg-connector-issue-beads"
      jiraName
    ];
    search.sources = [
      {
        name = jiraName;
        command = [ "example-old-binary" ];
        previous_names = [ "example-former-name" ];
      }
    ];
    activity.sources = [ jiraName ];
  };
  jiraRegistryOf = r: {
    inherit (r)
      connector
      search
      activity
      attention
      ;
  };
  jiraCases = {
    # jira.enable = false (and no jira.* at all): the registry, the installed
    # packages, the blocks and the whole rendered document are exactly today's.
    disabledRegistryUnchanged = jiraRegistryOf (rc jiraDisabled) == oldJiraRegistry;
    absentRegistryUnchanged = jiraRegistryOf (rc jiraAbsent) == oldJiraRegistry;
    disabledRenderedIdentical = rc jiraDisabled == rc jiraAbsent;
    disabledPackagesIdentical =
      map (p: p.drvPath) jiraDisabled.config.home.packages
      == map (p: p.drvPath) jiraAbsent.config.home.packages;
    disabledNoWarning = jiraDisabled.config.warnings == [ ];
    disabledFailsNoAssertion = !builtins.any (a: !a.assertion) jiraDisabled.config.assertions;

    # The options and their defaults.
    defaultsAreOff =
      let
        o = (evaluate jiraRegistry null).config.phillipgreenii.programs.pg-connector.jira;
      in
      o.enable == false
      && o.registry.rewrite == false
      && o.allowUnsupervised == false
      && o.pprof.enable == true
      && o.instance == jiraName
      && o.pjiraPath == null
      && o.pjiraEnvironment == { }
      && o.settings == { }
      && o.package.name == pkgs.pg-connector-issue-jira.name;
    supervisorDefaultFollowsPlatform =
      (jOpts jiraAbsent).supervisor == (if pkgs.stdenv.hostPlatform.isDarwin then "launchd" else "none");

    # Enabled, registry.rewrite = false (the default): the daemon file exists,
    # the registry, the blocks and the whole rendered document are unchanged.
    gateOffRenderedIdentical = rc jiraEnabled == rc jiraAbsent;
    gateOffRegistryUnchanged = jiraRegistryOf (rc jiraEnabled) == oldJiraRegistry;
    gateOffNoStamp =
      !((rc jiraEnabled).backends.${jiraName} ? config_hash)
      && !((rc jiraEnabled).backends.${jiraName} ? config_path);
    gateOffNoWarning = jiraEnabled.config.warnings == [ ];
    gateOffConfigIsAStorePath = lib.hasPrefix builtins.storeDir (
      toString (jOpts jiraEnabled).configFile
    );
    gateOffKeepsStockPackageOnce =
      lib.count (p: p.name == pkgs.pg-connector-issue-jira.name) jiraEnabled.config.home.packages == 1;

    # registry.rewrite = true: every list naming the backend carries the argv
    # form with ONE --config path; a plain string is converted; an instance
    # keeps its former names; the umbrella block is stamped, and keeps its keys.
    rewriteIssue =
      (rc jiraRewritten).connector.issue == [
        "pg-connector-issue-beads"
        (jiraEntry jiraRewritten)
      ];
    rewriteAttention =
      (rc jiraRewritten).attention.sources == [
        "pg-connector-issue-beads"
        (jiraEntry jiraRewritten)
      ];
    rewriteSearchKeepsFormerNames =
      (rc jiraRewritten).search.sources == [
        (jiraEntry jiraRewritten // { previous_names = [ "example-former-name" ]; })
      ];
    rewriteActivity = (rc jiraRewritten).activity.sources == [ (jiraEntry jiraRewritten) ];
    rewriteOccurrenceCount = builtins.length (jiraOccurrences (rc jiraRewritten)) == 4;
    rewriteOneConfigPath =
      builtins.length (
        lib.unique (map (x: builtins.elemAt x.command 2) (jiraOccurrences (rc jiraRewritten)))
      ) == 1;
    rewriteArgvShape =
      (builtins.elemAt (jiraEntry jiraRewritten).command 0) == "pg-connector-issue-jira"
      && (builtins.elemAt (jiraEntry jiraRewritten).command 1) == "--config"
      && (builtins.elemAt (jiraEntry jiraRewritten).command 3) == "--instance"
      && (builtins.elemAt (jiraEntry jiraRewritten).command 4) == jiraName
      && builtins.length (jiraEntry jiraRewritten).command == 5;
    rewriteStamped =
      (rc jiraRewritten).backends.${jiraName}.config_hash == (jOpts jiraRewritten).configHash
      && (rc jiraRewritten).backends.${jiraName}.config_path == toString (jOpts jiraRewritten).configFile;
    rewriteBlockKeepsKeys =
      (rc jiraRewritten).backends.${jiraName}.example_key == "example-value"
      && (rc jiraRewritten).backends.${jiraName}.queries.mine.watched == true
      && builtins.isString (rc jiraRewritten).backends.${jiraName}.queries.sweep
      && builtins.isList (rc jiraRewritten).backends.${jiraName}.queries.team;
    rewriteOtherEntriesUntouched = builtins.elem "pg-connector-issue-beads" (rc jiraRewritten)
    .connector.issue;
    rewriteNoWarning = jiraRewritten.config.warnings == [ ];
    # the daemon file does not depend on the gate, so flipping it changes only
    # the registry (and the stamp), never the config or the supervisor entry
    gateDoesNotChangeConfig =
      (jOpts jiraRewritten).configFile == (jOpts jiraEnabled).configFile
      && (jOpts jiraRewritten).configHash == (jOpts jiraEnabled).configHash;

    # Only the entries named `jira.instance` are rewritten.
    otherInstanceRewritesOnlyItsName =
      (rc jiraOtherInstance).connector.issue == [
        jiraName
        {
          name = jiraOtherInstanceName;
          command = [
            "pg-connector-issue-jira"
            "--config"
            (toString (jOpts jiraOtherInstance).configFile)
            "--instance"
            jiraOtherInstanceName
          ];
        }
      ]
      && (rc jiraOtherInstance).activity.sources == [ jiraName ];

    # supervisor = none without allowUnsupervised withholds the rewrite (and
    # says so); allowUnsupervised permits it.
    unsupervisedKeepsRegistry = jiraRegistryOf (rc jiraNoSupervisor) == oldJiraRegistry;
    unsupervisedNoStamp = !((rc jiraNoSupervisor).backends.${jiraName} ? config_hash);
    unsupervisedWarns =
      builtins.length jiraNoSupervisor.config.warnings == 1
      && lib.hasInfix "allowUnsupervised" (builtins.head jiraNoSupervisor.config.warnings);
    unsupervisedAllowedRewrites =
      builtins.length (jiraOccurrences (rc jiraNoSupervisorAllowed)) == 4
      && jiraNoSupervisorAllowed.config.warnings == [ ];
    noneWithoutGateIsQuiet =
      jiraNoneNoRewrite.config.warnings == [ ]
      && jiraRegistryOf (rc jiraNoneNoRewrite) == oldJiraRegistry;

    # The stamp (hash and path) follows the content: an OTel endpoint, a
    # setting and a pjira environment each change both.
    configFollowsOtel =
      (jOpts jiraEnabled).configHash != (jOpts jiraEnabledNoOs).configHash
      && (jOpts jiraEnabled).configFile != (jOpts jiraEnabledNoOs).configFile;
    configFollowsSetting =
      (jOpts jiraEnabled).configHash != (jOpts jiraOtherSetting).configHash
      && (jOpts jiraEnabled).configFile != (jOpts jiraOtherSetting).configFile;
    configFollowsPjiraEnvironment =
      (jOpts jiraEnabled).configHash != (jOpts jiraWithEnv).configHash
      && (jOpts jiraEnabled).configFile != (jOpts jiraWithEnv).configFile;
    configFollowsQueries =
      (jOpts jiraEnabled).configHash != (jOpts (
        evaluate (
          jiraRegistry
          // {
            jira = jiraOn;
            backends.${jiraName}.queries = {
              mine = "assignee = currentUser()";
            };
          }
        ) osWithObs
      )).configHash;
    sameInputsSameConfig =
      (jOpts (jiraCase jiraOn osWithObs)).configFile == (jOpts jiraEnabled).configFile;
    # A null or observability-less system scope renders and does not fail.
    nullOsConfigRenders = lib.hasPrefix builtins.storeDir (toString (jOpts jiraEnabledNoOs).configFile);

    # Both daemons at once: each rewrites only its own names, the GitHub
    # rewrite is exactly what it is without Jira.
    bothRewriteOwnNames =
      builtins.length (occurrences (rc jiraWithGithub)) == 5
      && builtins.length (jiraOccurrences (rc jiraWithGithub)) == 1
      && (rc jiraWithGithub).connector.issue == [ (jiraEntry jiraWithGithub) ]
      && (rc jiraWithGithub).connector.pr == [ (prEntry jiraWithGithub) ];
    bothStampOwnBlocks =
      (rc jiraWithGithub).backends.${jiraName}.config_hash == (jOpts jiraWithGithub).configHash
      &&
        (rc jiraWithGithub).backends."pg-connector-pr-github".config_hash == (ghOpts jiraWithGithub)
        .configHash
      && (jOpts jiraWithGithub).configHash != (ghOpts jiraWithGithub).configHash;
    # the Jira daemon's binary is installed once the daemon is on, alongside
    # the GitHub daemon's
    bothInstallBinaries = installsDaemon jiraWithGithub;
  };

  # lib.backendBinDirs (bead pg2-p8xtx): the helper a pg-wrap spec author calls
  # to get the backend PATH entries. It MUST equal the backend set the module
  # installs, so a package added to home.packages but missing from the helper
  # fails here (the `bypassing` scenario below is that mutation, kept as a
  # permanent negative control so the check cannot go vacuous).
  binDirOf = p: "${lib.getBin p}/bin";
  helperOf = e: e.config.phillipgreenii.programs.pg-connector.lib.backendBinDirs;
  umbrellaOf = e: e.config.phillipgreenii.programs.pg-connector.package;
  # Every installed package except the umbrella itself, as PATH entries.
  installedBackendDirs =
    e: map binDirOf (builtins.filter (p: toString p != toString (umbrellaOf e)) e.config.home.packages);
  helperMatchesInstalled =
    e: lib.sort lib.lessThan (helperOf e { }) == lib.sort lib.lessThan (installedBackendDirs e);
  plain = evaluate { } null;
  # A package installed WITHOUT going through the module's backend list.
  bypassing = plain.extendModules { modules = [ { home.packages = [ pkgs.hello ]; } ]; };
  # Neither the helper nor the module may name a ZR tool (they stay in
  # nix-ziprecruiter); every entry is a pg-connector package.
  zrFree = lib.all (d: lib.hasInfix "-pg-connector" d) (helperOf plain { includeSelf = true; });
  backendBinDirsCases = {
    matchesInstalledByDefault = helperMatchesInstalled plain;
    matchesInstalledWithGithubDaemon = helperMatchesInstalled ghEnabled;
    daemonListedOnlyWhenEnabled =
      builtins.elem (binDirOf (ghOpts ghEnabled).package) (helperOf ghEnabled { })
      && !(builtins.elem (binDirOf pkgs.pg-connector-github) (helperOf plain { }));
    includeSelfPrependsUmbrella =
      helperOf plain { includeSelf = true; } == [ (binDirOf (umbrellaOf plain)) ] ++ helperOf plain { }
      && !(builtins.elem (binDirOf (umbrellaOf plain)) (helperOf plain { }));
    noDuplicates = lib.length (helperOf plain { }) == lib.length (lib.unique (helperOf plain { }));
    namesNoZrTool = zrFree;
    # The mutation: a backend added to home.packages but absent from the helper
    # MUST be detected, so the comparison above is not vacuously true.
    bypassingIsDetected = !(helperMatchesInstalled bypassing);
  };
  backendBinDirsFailures = builtins.attrNames (lib.filterAttrs (_: ok: !ok) backendBinDirsCases);

  # The pinned backend set (bead pg2-vn8kd). The comparison above cannot see a
  # backend REMOVED from `backendPackages`: the helper and home.packages both
  # derive from that one list, so they shrink together and still agree, and
  # the backend then silently vanishes from every pg-wrap PATH. This list is
  # the independent second source. Adding or removing a pg-connector-* backend
  # in `backendPackages` (home/programs/pg-connector/default.nix) MUST be
  # mirrored here, in the SAME change; the failure message names the backend
  # and the file to edit. `pg-connector-github` is not listed: it is the
  # optional daemon, pinned separately below.
  expectedBackendNames = [
    "pg-connector-pr-github"
    "pg-connector-ci-github-actions"
    "pg-connector-issue-beads"
    "pg-connector-issue-jira"
    "pg-connector-scm-git"
    "pg-connector-activity-git"
    "pg-connector-thread-slack"
    "pg-connector-calendar-osx-bridge"
    "pg-connector-calendar-task-focus"
    "pg-connector-agentsession-pa-monitor"
    "pg-connector-alert-grafana"
    "pg-connector-mail-osx-bridge"
  ];
  # The PATH entries the pinned names stand for, with a hint per entry.
  backendSetProblems =
    e: daemon:
    let
      expected =
        map (n: {
          name = n;
          dir = binDirOf pkgs.${n};
        }) expectedBackendNames
        ++ daemon;
      actual = helperOf e { };
      missing = builtins.filter (x: !(builtins.elem x.dir actual)) expected;
      unexpected = builtins.filter (d: !(builtins.elem d (map (x: x.dir) expected))) actual;
    in
    map (
      x:
      "backend ${x.name} is pinned in expectedBackendNames (tests/pg-connector-home-render.nix) but missing from backendPackages (home/programs/pg-connector/default.nix): re-add it there, or remove it from the pinned list if it was removed on purpose"
    ) missing
    ++ map (
      d:
      "${d} is in backendPackages (home/programs/pg-connector/default.nix) but not pinned in expectedBackendNames (tests/pg-connector-home-render.nix): add its name to the pinned list, or remove it from backendPackages"
    ) unexpected;
  # Default scope (daemon off) and the daemon-enabled scope, which adds the
  # configured github daemon package.
  backendSetFailures =
    backendSetProblems plain [ ]
    ++ backendSetProblems ghEnabled [
      {
        name = "pg-connector-github (github.enable)";
        dir = binDirOf (ghOpts ghEnabled).package;
      }
    ];

  # A malformed instance (missing command, or an empty one) must FAIL
  # evaluation, never render a half-entry the registry would reject later.
  renders = cfg: (builtins.tryEval (builtins.deepSeq (render cfg).drvPath true)).success;
in
{
  # The scenario builders, for tests/pg-connector-github-hm-launchd.nix.
  harness = {
    inherit
      evaluateWith
      githubRegistry
      githubOn
      osWithObs
      ghOpts
      jiraRegistry
      jiraOn
      jiraRewriteOn
      jOpts
      ;
  };
  # github.enable = false renders the legacy golden byte for byte.
  legacyGithubDisabled = render (legacy // { github.enable = false; });
  # The pg-connector-github cases: name -> bool, and the failing names.
  inherit githubCases;
  githubFailures = builtins.attrNames (lib.filterAttrs (_: ok: !ok) githubCases);
  # The pg-connector-issue-jira cases (docket pg2-2qyyh.2).
  inherit jiraCases;
  jiraFailures = builtins.attrNames (lib.filterAttrs (_: ok: !ok) jiraCases);
  # Rendered Jira daemon configs (store paths) for the jq checks of the check.
  jira = {
    enabled = (jOpts jiraEnabled).configFile;
    enabledHash = (jOpts jiraEnabled).configHash;
    enabledNoOs = (jOpts jiraEnabledNoOs).configFile;
    withEnv = (jOpts jiraWithEnv).configFile;
    settingsOverride = (jOpts jiraSettingsOverride).configFile;
    otherSetting = (jOpts jiraOtherSetting).configFile;
    enabledRegistry = render (jiraRegistry // { jira = jiraRewriteOn; });
    pjira = jiraOn.pjiraPath;
  };
  # github.enable = false renders the legacy golden byte for byte, jira.enable
  # = false too.
  legacyJiraDisabled = render (legacy // { jira.enable = false; });
  inherit backendBinDirsFailures backendSetFailures;
  # Rendered daemon configs (store paths) and the values the shell side of the
  # check compares them against.
  github = {
    enabled = (ghOpts ghEnabled).configFile;
    enabledHash = (ghOpts ghEnabled).configHash;
    enabledNoOs = (ghOpts ghEnabledNoOs).configFile;
    watched = (ghOpts ghWatched).configFile;
    enabledRegistry = render (githubRegistry // { github = githubOn; });
    package = pkgs.pg-connector-github;
    ghPath = lib.getExe pkgs.gh;
  };
  # The registry the module renders for an enabled daemon (docket pg2-z5fax.5,
  # Phase 3c), as the umbrella loads it: only the five registry lists, with the
  # daemon config's store path replaced by one fixed example path, so the
  # result is comparable to tests/fixtures/pg-connector-home/
  # github-instances.yaml (the SAME bytes as the Go parse fixture
  # packages/pg-connector/cmd/pg-connector/testdata/registry-github-instances.yaml).
  # The `backends` blocks (config stamp), `state` and `configSchemaVersion` are
  # dropped: the stamp follows the config's content and is pinned by
  # `githubCases` and the jq checks of the check.
  githubInstancesNormalized =
    pkgs.runCommand "pg-connector-github-instances-normalized"
      {
        nativeBuildInputs = [ pkgs.yq-go ];
        configPath = toString (ghOpts ghEnabledNoOs).configFile;
        fixedPath = "/example/state/pg-connector-github/config.json";
      }
      ''
        yq '
          {"connector": .connector, "search": .search, "activity": .activity, "attention": .attention}
          | (.. | select(tag == "!!str") | select(. == strenv(configPath))) = strenv(fixedPath)
        ' ${render (githubRegistry // { github = githubOn; })} > $out
      '';
  # Instances in every registration, alongside plain strings.
  instances = render instancesExample;
  # Beads attention_labels render per instance next to the registrations.
  beadsAttention = render beadsAttentionExample;
  # An unset attentionLabels renders no key at all, never a null.
  beadsAttentionUnset = render {
    attention = {
      inherit (beadsAttentionExample.attention) sources;
      perBackend."pg-connector-issue-beads-pg2" = { };
    };
  };
  # bdBudget values render as flags on every beads registration; two instances
  # with one group render the same --budget-group.
  bdBudget = render bdBudgetExample;
  bdBudgetPartial = render bdBudgetPartialExample;
  # Every bdBudget value explicitly null renders the pre-change `instances`
  # golden byte for byte (an unconfigured deployment does not move).
  bdBudgetUnset = render (
    instancesExample
    // {
      bdBudget = {
        maxConcurrent = null;
        maxPerMinute = null;
        waitBound = null;
        writeReserve = null;
        group = null;
      };
    }
  );
  # A single-valued scm accepts one {name, command} too.
  scmInstance = render scmInstanceExample;
  # Entries missing command or with an empty command are rejected at eval.
  malformedInstancesRejected =
    !(renders { connector.issue = [ { name = "pg-connector-issue-beads-pg2"; } ]; })
    && !(renders {
      connector.issue = [
        {
          name = "pg-connector-issue-beads-pg2";
          command = [ ];
        }
      ];
    })
    && !(renders { connector.issue = [ { command = [ "pg-connector-issue-beads" ]; } ]; })
    # previousNames must be a list of strings
    && !(renders {
      connector.issue = [
        (instanceBeads "pg2" // { previousNames = "pg-connector-issue-beads"; })
      ];
    });
  # Pre-alert configuration: MUST stay byte-for-byte unchanged.
  legacy = render legacy;
  # An explicit empty connector.alert is omitted exactly like thread/calendar.
  legacyEmptyAlert = render (
    legacy
    // {
      connector = legacy.connector // {
        alert = [ ];
      };
    }
  );
  # mail registration on its own.
  mail = render mailExample;
  # An explicit empty connector.mail is omitted exactly like thread/calendar.
  legacyEmptyMail = render (
    legacy
    // {
      connector = legacy.connector // {
        mail = [ ];
      };
    }
  );
  # activity registration on its own.
  activity = render activityExample;
  # An explicit empty activity.sources is omitted exactly like attention/search.
  legacyEmptyActivity = render (
    legacy
    // {
      activity.sources = [ ];
    }
  );
  # The design 5.1 Grafana sample, on its own.
  alertGrafana = render alertExample;
  # The retired deadline options are rejected, not silently ignored: a host
  # that still sets one fails evaluation instead of rendering a dead key.
  retiredDeadlineOptionsRejected =
    builtins.all
      (
        key:
        !(builtins.tryEval (
          builtins.deepSeq
            (render {
              attention.perBackend."pg-connector-issue-beads".${key} = "72h";
            }).drvPath
            true
        )).success
      )
      [
        "threshold"
        "exclude"
      ];
  # Alert options alongside the legacy registrations.
  legacyPlusAlert = render {
    inherit (legacy)
      search
      state
      configSchemaVersion
      backends
      ;
    connector = legacy.connector // {
      inherit (alertExample.connector) alert;
    };
    attention = {
      sources = legacy.attention.sources ++ alertExample.attention.sources;
      inherit (alertExample.attention) perBackend;
    };
    inherit (alertExample) alertBackends;
  };
}
