{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pg-go-gate;
in
{
  options.phillipgreenii.programs.pg-go-gate = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = config.phillipgreenii.programs.claude-code.enable;
      defaultText = lib.literalExpression "config.phillipgreenii.programs.claude-code.enable";
      example = true;
      description = ''
        Install pg-go-gate, the host-wide 3-slot gate that agents run `go test`, `go build` and
        `go install` through (under nice 10), so a burst of agent sessions cannot saturate the
        host. It serves Claude agent sessions, so it defaults on exactly when Claude is enabled.
      '';
    };
    package = lib.mkPackageOption pkgs "pg-go-gate" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    programs.tldr.customPages.pg-go-gate = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.package}/share/tldr/pages.common/pg-go-gate.md";
    };
  };
}
