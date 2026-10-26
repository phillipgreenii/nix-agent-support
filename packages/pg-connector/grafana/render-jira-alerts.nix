# Renders the Jira daemon alert rules template for one tracked_set_alert_size (bead pg2-l79uxc).
#
# The template (alerting/pg-connector-issue-jira-daemon-alerts.yaml) carries the token
# __TRACKED_SET_ALERT_SIZE__ in the pg-conn-jira-tracked-over summary, description and threshold.
# The daemon's own `tracked_set_alert_size` setting is the single source of truth: callers pass
# that value (the darwin module reads it from `jira.settings`), and the default below MUST equal
# the daemon's DefaultTrackedSetAlertSize (the flake check test-pg-connector-issue-jira-alert-rules
# compares them).
{ pkgs }:
let
  inherit (pkgs) lib;
  token = "__TRACKED_SET_ALERT_SIZE__";
  template = ./alerting/pg-connector-issue-jira-daemon-alerts.yaml;
in
rec {
  # Mirrors DefaultTrackedSetAlertSize in
  # cmd/pg-connector-issue-jira/internal/config/defaults.go.
  defaultTrackedSetAlertSize = 55;

  inherit token template;

  render =
    size:
    assert lib.assertMsg (
      builtins.isInt size && size > 0
    ) "tracked_set_alert_size must be a positive integer, got: ${builtins.toJSON size}";
    pkgs.writeText "pg-connector-issue-jira-daemon-alerts.yaml" (
      builtins.replaceStrings [ token ] [ (toString size) ] (builtins.readFile template)
    );
}
