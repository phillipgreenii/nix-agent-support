{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.services.beads-dolt-server;

  startScript = pkgs.writeShellScriptBin "beads-dolt-server" ''
    set -e
    DOLT_DATA_DIR="''${XDG_DATA_HOME:-$HOME/.local/share}/beads-dolt"
    mkdir -p "$DOLT_DATA_DIR"
    cd "$DOLT_DATA_DIR"
    exec "${pkgs.unstable.dolt}/bin/dolt" sql-server -H 127.0.0.1 -P ${toString cfg.port}
  '';
in
{
  imports = [
    (lib.mkAliasOptionModule
      [ "phillipgreenii" "system" "services" "beads-dolt-server" ]
      [ "services" "beads-dolt-server" ]
    )
  ];

  options.services.beads-dolt-server = {
    enable = lib.mkEnableOption "shared Dolt SQL server for beads projects";

    port = lib.mkOption {
      type = lib.types.port;
      default = 3307;
      description = "Port for the Dolt SQL server";
    };
  };

  config = lib.mkIf cfg.enable {
    launchd.user.agents.beads-dolt-server = {
      serviceConfig = {
        ProgramArguments = [ "${startScript}/bin/beads-dolt-server" ];
        KeepAlive = true;
        RunAtLoad = true;
        StandardOutPath = "/tmp/beads-dolt-server.log";
        StandardErrorPath = "/tmp/beads-dolt-server-error.log";
      };
    };
  };
}
