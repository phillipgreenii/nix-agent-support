{
  config,
  lib,
  ...
}:
let
  # phillipgreenii.observability.logSources is declared at darwin/system scope in
  # phillipgreenii-nix-support-apps (darwin/modules/observability/registration.nix),
  # so the registration lives here, not in the home-manager module (setting it
  # from HM targets an undeclared option and fails eval). The module follows the
  # HM enable flag across home-manager.users, as darwin/modules/pg-keep-awake does.
  obs = config.phillipgreenii.observability;
  hmUsers = config.home-manager.users or { };
  guardUsers = lib.filter (u: u.phillipgreenii.programs.pg-origin-guard.enable or false) (
    lib.attrValues hmUsers
  );
  gate = (obs.enable or false) && guardUsers != [ ];
in
{
  # The tool writes one JSONL event file per UTC day,
  # ${XDG_STATE_HOME}/pg-origin-guard/events-YYYY-MM-DD.jsonl, and prunes old ones.
  #
  # `path` MUST be explicit: the logSources default glob is derived from the
  # ATTRIBUTE NAME (${env:XDG_STATE_HOME}/${name}/*.jsonl), which would also match
  # here, but the daily-file glob is the contract, so it is spelled out (the
  # pg-router darwin module's same lesson, where a renamed attribute silently
  # watched a directory that never existed). The filelog receiver globs on every
  # poll, so a new day's file is picked up without a restart and a pruned file
  # simply disappears. `serviceName` is the stream the Grafana alert selects on.
  config = lib.mkIf gate {
    phillipgreenii.observability.logSources.pg-origin-guard = {
      serviceName = "pg-origin-guard";
      path = "\${env:XDG_STATE_HOME}/pg-origin-guard/events-*.jsonl";
    };

    # The auth-unavailable alert reads that log stream. Its folder "Work" converges
    # by title (never pin a folderUid). Not registered with pg-router-probe.
    phillipgreenii.observability.alertRuleFiles = [
      ../../../packages/pg-origin-guard/grafana/alerting/alerts.yaml
    ];
  };
}
