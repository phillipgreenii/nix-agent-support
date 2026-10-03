{
  config,
  lib,
  ...
}:
let
  # phillipgreenii.observability.{logSources,alertRuleFiles} are declared at
  # darwin/system scope in phillipgreenii-nix-support-apps
  # (darwin/modules/observability/{registration,alerting}.nix), so this lives
  # in darwin, not in the home-manager module -- setting them from HM targets
  # undeclared options and fails eval (same reasoning as pg-router's and
  # pg-rescue's registrations).
  obs = config.phillipgreenii.observability;
in
{
  # Bead pg2-ph0o4; contract OBS-1..OBS-3 (Phillip's pg-connector
  # observability ruling, 2026-10-02): the pr-github backend owns its event
  # log and its alert rules, and they are registered HERE, in the
  # backend's own nix module -- NOT through pg-connector's config, which stays
  # unbound to OTel.
  #
  # Gated on `obs.enable` alone: registration is a no-op on a machine without
  # the observability stack, and harmless when the backend is installed but
  # idle (the rules simply see no data and stay OK).
  config = lib.mkIf (obs.enable or false) {
    # The backend appends one JSONL line per call to
    # ${XDG_STATE_HOME}/pg-connector-pr-github/events.jsonl (eventlog.Path;
    # format and fields: packages/pg-connector/cmd/pg-connector-pr-github/
    # internal/eventlog).
    #
    # `path` is deliberately NOT set. The logSources submodule's default glob
    # is ${env:XDG_STATE_HOME}/<attr name>/*.jsonl, which for this attribute
    # name is ${XDG_STATE_HOME}/pg-connector-pr-github/*.jsonl: it matches
    # events.jsonl and not the rotated events.jsonl.1 or the events.jsonl.lock
    # rotation lock. The attribute name is also the default serviceName, so
    # Loki carries service_name="pg-connector-pr-github" -- the selector the
    # alert rules use. The Go side pins both facts (eventlog's
    # TestPath_DefaultMatchesRegisteredLogSourceGlob), and the flake check
    # test-pg-connector-pr-github-darwin-module pins this side.
    #
    # `format` stays the default "jsonl": the events carry the ADR 0038
    # fields (time as the timestamp, lowercase level as the severity).
    #
    # errorAlert is switched off: the generic "log error rate high" rule would
    # fire on the same error-level lines the bespoke rules below already
    # cover (unauthenticated and unavailable are the two error levels), with
    # a count-only threshold that cannot tell auth from quota apart, so each
    # incident would page twice.
    phillipgreenii.observability.logSources.pg-connector-pr-github = {
      errorAlert.enable = false;
    };

    # LogQL rules for the three named conditions: unauthenticated within 15m,
    # sustained unavailable, GraphQL remaining below the rate reserve for
    # 10m. Surfaces in the SwiftBar menu through pg-connector-alert-grafana
    # with no menubar change (it lists every firing Grafana alert).
    phillipgreenii.observability.alertRuleFiles = [
      ../../../packages/pg-connector/grafana/alerting/pr-github-alerts.yaml
    ];
  };
}
