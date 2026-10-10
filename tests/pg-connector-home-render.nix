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
  render =
    cfg:
    let
      evaluated = lib.evalModules {
        specialArgs = { inherit pkgs lib; };
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
    in
    evaluated.config.xdg.configFile."pg-pr/config.yaml".source;

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
  instancesExample = {
    connector = {
      issue = [
        "pg-connector-issue-jira"
        (instanceBeads "pg2")
        (instanceBeads "zr")
      ];
      scm = "pg-connector-scm-git";
    };
    attention.sources = [
      (instanceBeads "pg2")
      (instanceBeads "zr")
    ];
    search.sources = [
      (instanceBeads "pg2")
      (instanceBeads "zr")
    ];
    activity.sources = [
      (instanceBeads "pg2")
      (instanceBeads "zr")
    ];
    backends = {
      "pg-connector-issue-beads-pg2".activity_actors = [ "Example Person" ];
      "pg-connector-issue-beads-zr".activity_actors = [ "Example Person" ];
    };
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

  # A malformed instance (missing command, or an empty one) must FAIL
  # evaluation, never render a half-entry the registry would reject later.
  renders = cfg: (builtins.tryEval (builtins.deepSeq (render cfg).drvPath true)).success;
in
{
  # Instances in every registration, alongside plain strings.
  instances = render instancesExample;
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
    && !(renders { connector.issue = [ { command = [ "pg-connector-issue-beads" ]; } ]; });
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
