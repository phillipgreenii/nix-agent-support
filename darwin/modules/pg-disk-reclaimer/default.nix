{
  config,
  lib,
  ...
}:
let
  obs = config.phillipgreenii.observability;

  # The scheduled job lives in home-manager scope (home/programs/pg-disk-reclaimer);
  # the log source is a system-scope option, so it follows the HM flag across
  # `home-manager.users.<u>` (the pg-repo-runner pattern).
  hmUsers = config.home-manager.users or { };
  autoUsers = lib.filter (
    u:
    (u.phillipgreenii.programs.pg-disk-reclaimer.enable or false)
    && (u.phillipgreenii.programs.pg-disk-reclaimer.auto.enable or false)
  ) (lib.attrValues hmUsers);
in
{
  # The run log is JSON lines on stdout (auto.jsonl under
  # $XDG_STATE_HOME/pg-disk-reclaimer), so the default glob of the log source
  # matches it and not the stderr file. The alert is an ERROR line (free space
  # below the lowest threshold), which the source's generic error alert picks up.
  config = lib.mkIf ((obs.enable or false) && autoUsers != [ ]) {
    phillipgreenii.observability.logSources.pg-disk-reclaimer = {
      format = "jsonl";
    };
  };
}
