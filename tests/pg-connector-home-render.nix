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
  # evaluate: the whole evaluated module set, for a scenario's cfg and an
  # osConfig (null models a standalone home-manager activation).
  evaluate =
    cfg: osConfig:
    lib.evalModules {
      # home-manager always passes osConfig (null for a standalone activation),
      # so the stub does too; the module cannot rely on its `? null` default
      # under a bare lib.evalModules.
      specialArgs = {
        inherit pkgs lib osConfig;
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
            };
          }
        )
        {
          phillipgreenii.programs.pg-connector = cfg // {
            enable = true;
          };
        }
      ];
    };

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

  # A malformed instance (missing command, or an empty one) must FAIL
  # evaluation, never render a half-entry the registry would reject later.
  renders = cfg: (builtins.tryEval (builtins.deepSeq (render cfg).drvPath true)).success;
in
{
  # github.enable = false renders the legacy golden byte for byte.
  legacyGithubDisabled = render (legacy // { github.enable = false; });
  # The pg-connector-github cases: name -> bool, and the failing names.
  inherit githubCases;
  githubFailures = builtins.attrNames (lib.filterAttrs (_: ok: !ok) githubCases);
  # Rendered daemon configs (store paths) and the values the shell side of the
  # check compares them against.
  github = {
    enabled = (ghOpts ghEnabled).configFile;
    enabledHash = (ghOpts ghEnabled).configHash;
    enabledNoOs = (ghOpts ghEnabledNoOs).configFile;
    enabledRegistry = render (githubRegistry // { github = githubOn; });
    package = pkgs.pg-connector-github;
    ghPath = lib.getExe pkgs.gh;
  };
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
