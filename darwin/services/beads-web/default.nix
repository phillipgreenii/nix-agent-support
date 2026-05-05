{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.services.beads-web;
  beadsWebPkg = pkgs.beads-web;
  beadsPkg = pkgs.llm-agentsPkgs.beads;

  startScript = pkgs.writeShellScriptBin "beads-web" ''
    set -e
    mkdir -p "${cfg.dataDir}"
    export PORT="${toString cfg.port}"
    export HOME="/Users/$(whoami)"
    export PATH="${beadsPkg}/bin:$PATH"
    exec "${beadsWebPkg}/bin/beads-web"
  '';
in
{
  imports = [
    (lib.mkAliasOptionModule
      [ "phillipgreenii" "system" "services" "beads-web" ]
      [ "services" "beads-web" ]
    )
  ];

  options.services.beads-web = {
    enable = lib.mkEnableOption "beads-web Kanban UI for beads task tracking";

    port = lib.mkOption {
      type = lib.types.port;
      default = 3008;
      description = "Port for the beads-web HTTP server";
    };

    dataDir = lib.mkOption {
      type = lib.types.str;
      default =
        let
          user = config.phillipgreenii.system.primaryUser;
          home = config.users.users.${user}.home;
        in
        "${home}/.local/share/beads-web";
      description = "Directory for beads-web data (SQLite DB) and logs";
    };
  };

  config = lib.mkIf cfg.enable {
    launchd.user.agents.beads-web = {
      serviceConfig = {
        ProgramArguments = [ "${startScript}/bin/beads-web" ];
        KeepAlive = true;
        RunAtLoad = true;
        StandardOutPath = "${cfg.dataDir}/beads-web.log";
        StandardErrorPath = "${cfg.dataDir}/beads-web-error.log";
      };
    };
  };
}
