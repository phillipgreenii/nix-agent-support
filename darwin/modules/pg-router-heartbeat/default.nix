{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pg-router-heartbeat;

  # Default page: a banner in the console user's session, delivered when a
  # session exists. With no console session (the observed outage was a boot
  # with no login) nothing on this machine can reach the operator in real time;
  # an off-machine channel is an open operator decision and goes in
  # `notifyCommand` without a code change.
  consoleBanner = pkgs.writeShellScript "pg-router-heartbeat-banner" ''
    uid=$(/usr/bin/stat -f %u /dev/console 2>/dev/null || echo 0)
    [ "$uid" -gt 0 ] || exit 0
    exec /bin/launchctl asuser "$uid" /usr/bin/osascript \
      -e 'on run argv' \
      -e 'display notification (item 1 of argv) with title "pg-router-heartbeat"' \
      -e 'end run' "$1"
  '';
in
{
  options.phillipgreenii.programs.pg-router-heartbeat = {
    enable = lib.mkEnableOption "the out-of-stack heartbeat that pages when no Prometheus sample has arrived for 30 awake minutes";
    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.pg-router-heartbeat;
      defaultText = lib.literalExpression "pkgs.pg-router-heartbeat";
      description = "The pg-router-heartbeat package.";
    };
    prometheusUrl = lib.mkOption {
      type = lib.types.str;
      default = "http://127.0.0.1:9090";
      description = "Prometheus base URL probed each tick.";
    };
    notifyCommand = lib.mkOption {
      type = lib.types.str;
      default = "${consoleBanner} {message}";
      defaultText = lib.literalMD "a console-session banner script";
      description = "Command (split on spaces, no shell) run on a page; `{message}` is replaced by the text.";
    };
    stateDir = lib.mkOption {
      type = lib.types.str;
      default = "/var/db/pg-router-heartbeat";
      description = "Directory for the state file and the `alert` marker file.";
    };
  };

  # A system-domain LaunchDaemon starts at boot, before any login, which is the
  # window the user-agent stack cannot cover. StartInterval does not fire while
  # the host sleeps; the binary treats the resulting tick gap as sleep.
  config = lib.mkIf cfg.enable {
    launchd.daemons.pg-router-heartbeat = {
      serviceConfig = {
        Label = "com.phillipg.pg-router-heartbeat";
        ProgramArguments = [
          "${cfg.package}/bin/pg-router-heartbeat"
          "--prometheus-url"
          cfg.prometheusUrl
          "--state-dir"
          cfg.stateDir
          "--notify-command"
          cfg.notifyCommand
        ];
        RunAtLoad = true;
        StartInterval = 60;
        StandardErrorPath = "/var/log/pg-router-heartbeat.log";
        StandardOutPath = "/var/log/pg-router-heartbeat.log";
      };
    };
  };
}
