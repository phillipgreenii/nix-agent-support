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

  # Every pre-alert capability and key the module already renders.
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
      perBackend."pg-connector-issue-beads" = {
        threshold = "72h";
        exclude = "no-attention";
      };
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
in
{
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
  # The design 5.1 Grafana sample, on its own.
  alertGrafana = render alertExample;
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
      perBackend = legacy.attention.perBackend // alertExample.attention.perBackend;
    };
    inherit (alertExample) alertBackends;
  };
}
