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
  # option and fails eval (same reasoning as pg-connector-issue-beads's and
  # -thread-slack's registrations).
  obs = config.phillipgreenii.observability;
in
{
  # Bead pg2-t7me1.4; contract OBS-1..OBS-3 (Phillip's pg-connector
  # observability ruling, 2026-10-02): the pg-task-focus calendar and attention
  # backend owns its event log and registers it HERE, in the backend's own nix
  # module -- NOT through pg-connector's config, which stays unbound to OTel.
  # Sibling of darwin/modules/pg-connector-issue-beads. (The daemon's own
  # observability registrations live in darwin/modules/pg-task-focus.)
  #
  # Gated on `obs.enable` alone: registration is a no-op on a machine without
  # the observability stack, and harmless when the backend is installed but
  # idle.
  config = lib.mkIf (obs.enable or false) {
    # The backend appends JSONL lines to
    # ${XDG_STATE_HOME}/pg-connector-calendar-task-focus/events.jsonl
    # (eventlog.Path; format and fields: packages/pg-connector/cmd/
    # pg-connector-calendar-task-focus/internal/eventlog): a start row, heartbeat
    # rows while a call runs long, and one final row per call carrying the
    # number of daemon requests, the last HTTP status and, on a failure, the
    # stage where it broke (connect, status or decode).
    #
    # `path` is deliberately NOT set. The logSources submodule's default glob
    # is ${env:XDG_STATE_HOME}/<attr name>/*.jsonl, which for this attribute
    # name is ${XDG_STATE_HOME}/pg-connector-calendar-task-focus/*.jsonl: it
    # matches events.jsonl and not the rotated events.jsonl.1 or the
    # events.jsonl.lock rotation lock. The attribute name is also the default
    # serviceName, so Loki carries
    # service_name="pg-connector-calendar-task-focus". The Go side pins both
    # facts (eventlog's TestPath_DefaultMatchesRegisteredLogSourceGlob), and the
    # flake check test-pg-connector-calendar-task-focus-darwin-module pins this
    # side.
    #
    # `format` stays the default "jsonl": the events carry the ADR 0038
    # fields (time as the timestamp, lowercase level as the severity).
    #
    # errorAlert is switched off: this registration exists to make killed and
    # failed calls ATTRIBUTABLE (query the log), not to add paging. A daemon
    # that is down already fires the daemon's own "Daemon down" alert
    # (darwin/modules/pg-task-focus), and a second page for the same cause from
    # every polling consumer would only be noise.
    phillipgreenii.observability.logSources.pg-connector-calendar-task-focus = {
      errorAlert.enable = false;
    };
  };
}
