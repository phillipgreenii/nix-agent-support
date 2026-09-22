{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.pg-router-probe;
in
{
  options.phillipgreenii.programs.pg-router-probe = {
    enable = lib.mkEnableOption ''
      pg-router-probe (a deterministic health probe over pg-router's own
      operational health: Grafana firing-alert check, queue/backlog drift
      check, daemon/handler binary hash sanity check -- files/updates an
      `escalated`-labeled bd issue via pg-connector on a real finding, no
      LLM involved).

      This module only puts the binary on PATH -- it exposes no
      config.yaml-style settings of its own, since every `run` invocation
      is fully parameterized by its own CLI flags (--grafana-url,
      --queue-depth/--backlog, --binary-path, --snapshot-path, ...).
      Wiring THIS role's own scheduling (the pg-router [[query]]/[[role]]
      TOML, the concrete flag values, and the
      PG_CONNECTOR_ISSUE_BEADS_DIR env wrap) is a separate,
      phillipg-nix-ziprecruiter-repo sibling packet's job, not this
      module's.

      Runtime-depends on `pg-connector` being on PATH
      (`cmd/pg-router-probe/connector.go`).
    '';
    package = lib.mkPackageOption pkgs "pg-router-probe" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];
  };
}
