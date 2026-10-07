{
  config,
  lib,
  ...
}:
let
  # phillipgreenii.observability.logSources is declared at darwin/system scope
  # in phillipgreenii-nix-support-apps
  # (darwin/modules/observability/registration.nix), so this lives in darwin,
  # not in the home-manager module -- setting it from HM targets an undeclared
  # option and fails eval (same reasoning as pg-connector-pr-github's,
  # -thread-slack's and -issue-jira's registrations).
  obs = config.phillipgreenii.observability;
in
{
  # Bead pg2-5dyz2; contract OBS-1..OBS-3 (Phillip's pg-connector
  # observability ruling, 2026-10-02): the beads issue backend owns its event
  # log and registers it HERE, in the backend's own nix module -- NOT through
  # pg-connector's config, which stays unbound to OTel. Sibling of
  # darwin/modules/pg-connector-issue-jira.
  #
  # Gated on `obs.enable` alone: registration is a no-op on a machine without
  # the observability stack, and harmless when the backend is installed but
  # idle.
  config = lib.mkIf (obs.enable or false) {
    # The backend appends JSONL lines to
    # ${XDG_STATE_HOME}/pg-connector-issue-beads/events.jsonl (eventlog.Path;
    # format and fields: packages/pg-connector/cmd/pg-connector-issue-beads/
    # internal/eventlog): a start row, heartbeat rows while a call runs long,
    # and one final row per call carrying the bd command count and the slowest
    # bd argv and duration.
    #
    # `path` is deliberately NOT set. The logSources submodule's default glob
    # is ${env:XDG_STATE_HOME}/<attr name>/*.jsonl, which for this attribute
    # name is ${XDG_STATE_HOME}/pg-connector-issue-beads/*.jsonl: it matches
    # events.jsonl and not the rotated events.jsonl.1 or the events.jsonl.lock
    # rotation lock. The attribute name is also the default serviceName, so
    # Loki carries service_name="pg-connector-issue-beads". The Go side pins
    # both facts (eventlog's TestPath_DefaultMatchesRegisteredLogSourceGlob),
    # and the flake check test-pg-connector-issue-beads-darwin-module pins this
    # side.
    #
    # `format` stays the default "jsonl": the events carry the ADR 0038
    # fields (time as the timestamp, lowercase level as the severity).
    #
    # errorAlert is switched off: this registration exists to make killed and
    # slow calls ATTRIBUTABLE (query the log), not to add paging. The generic
    # "log error rate high" rule would fire on the first host-wide stall, the
    # exact event the log is meant to explain; a bespoke rule MAY be added
    # later once the log has shown which signal is worth alerting on.
    phillipgreenii.observability.logSources.pg-connector-issue-beads = {
      errorAlert.enable = false;
    };
  };
}
