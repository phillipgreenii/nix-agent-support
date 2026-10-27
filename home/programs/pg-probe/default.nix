{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.pg-probe;
in
{
  options.phillipgreenii.programs.pg-probe = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = config.phillipgreenii.programs.claude-code.enable;
      defaultText = lib.literalExpression "config.phillipgreenii.programs.claude-code.enable";
      example = true;
      description = ''
        Install pg-probe: a read-only probe for unattended sessions. `pg-probe pprof` GETs one
        allowlisted `/debug/pprof` path over a daemon's pprof unix socket (any other path and any
        method but GET are refused), and `pg-probe wait-log` returns the first line of a log file
        that matches a regex (replacing `timeout N tail -f FILE | grep -m1 RE`), refusing any file
        outside a fixed list of log roots. It is NOT a generic curl or unix-socket client. Defaults on
        exactly when Claude is enabled, since it exists to serve agent sessions.

        Installing it does not grant it: a role's `--allowed-tools` list (the consuming machine
        flake) decides which sessions may run `Bash(pg-probe:*)`.
      '';
    };
    package = lib.mkPackageOption pkgs "pg-probe" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    programs.tldr.customPages = lib.mkIf config.programs.tldr.enable {
      pg-probe = {
        platform = "common";
        source = "${cfg.package}/share/tldr/pages.common/pg-probe.md";
      };
    };
  };
}
