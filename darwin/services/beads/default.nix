{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.services.beads;
  beadsPkg = pkgs.llm-agentsPkgs.beads;
  beadsWebCfg = config.services.beads-web;
  doltServerCfg = config.services.beads-dolt-server;

  beadsDoltConfigure = pkgs.writeShellScript "beads-dolt-configure" (
    builtins.readFile ./beads-dolt-configure.sh
  );

  projectType = lib.types.submodule {
    options = {
      name = lib.mkOption {
        type = lib.types.str;
        description = "Human-readable project name";
      };
      database = lib.mkOption {
        type = lib.types.str;
        description = "Dolt database name (should start with beads_)";
      };
      projectPath = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Filesystem path to the project (null for test-only databases)";
      };
      prefix = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Issue ID prefix for bd init (only used if .beads/ doesn't exist)";
      };
    };
  };

  doltPort = toString doltServerCfg.port;
  beadsWebPort = toString beadsWebCfg.port;

  configureCommands = lib.concatMapStringsSep "\n" (
    project:
    if project.projectPath != null then
      ''
        echo "Configuring project: ${project.name}"
        BEADS_DOLT_PORT=${doltPort} ${beadsDoltConfigure} \
          ${lib.escapeShellArg project.database} \
          ${lib.escapeShellArg project.projectPath} \
          ${lib.escapeShellArg (if project.prefix != null then project.prefix else "")}
      ''
    else
      ''
        echo "Skipping project without path: ${project.name}"
      ''
  ) cfg.projects;

  registerCommands = lib.concatMapStringsSep "\n" (
    project:
    if project.projectPath != null then
      ''
        echo "Registering ${project.name} in beads-web..."
        ${pkgs.curl}/bin/curl -sf -X POST "http://localhost:${beadsWebPort}/api/projects" \
          -H "Content-Type: application/json" \
          -d ${
            lib.escapeShellArg (
              builtins.toJSON {
                inherit (project) name;
                path = "dolt://${project.database}";
              }
            )
          } \
          >/dev/null 2>&1 || echo "  (skipped — beads-web not ready or project already registered)"
      ''
    else
      ""
  ) cfg.projects;

  activationScript = pkgs.writeShellScript "beads-configure" ''
    set -euo pipefail
    export PATH="${beadsPkg}/bin:${pkgs.unstable.dolt}/bin:${pkgs.mysql80}/bin:$PATH"

    DOLT_PORT=${doltPort}

    echo "Waiting for Dolt server on port $DOLT_PORT..."
    for ((i=1; i<=30; i++)); do
      ${pkgs.mysql80}/bin/mysql -h 127.0.0.1 -P "$DOLT_PORT" -u root -e "SELECT 1" &>/dev/null && break
      sleep 1
    done
    ${pkgs.mysql80}/bin/mysql -h 127.0.0.1 -P "$DOLT_PORT" -u root -e "SELECT 1" &>/dev/null \
      || { echo "FATAL: Dolt server not reachable on port $DOLT_PORT after 30s"; exit 1; }

    echo "Dolt server is up. Configuring projects..."

    ${configureCommands}

    echo ""
    echo "Registering projects in beads-web..."

    ${registerCommands}

    echo ""
    echo "beads-configure complete."
  '';
in
{
  imports = [
    (lib.mkAliasOptionModule [ "phillipgreenii" "system" "services" "beads" ] [ "services" "beads" ])
  ];

  options.services.beads = {
    projects = lib.mkOption {
      type = lib.types.listOf projectType;
      default = [ ];
      description = "Beads projects to configure for the shared Dolt server";
    };
  };

  config = lib.mkIf (cfg.projects != [ ]) {
    system.activationScripts.postActivation.text = lib.mkAfter ''
      echo "Running beads-configure..."
      BEADS_USER="''${SUDO_USER:-$(/usr/bin/stat -f '%Su' /dev/console)}"
      sudo -H -u "$BEADS_USER" ${activationScript} || echo "WARNING: beads-configure failed"
    '';
  };
}
