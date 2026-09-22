{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.ccpool-probe;
in
{
  options.phillipgreenii.programs.ccpool-probe = {
    enable = lib.mkEnableOption ''
      ccpool-probe (a deterministic health probe over ccpool's own
      operational health, from pg-router's point of view: stuck
      needs_input sessions and errored/working zombie-session-count
      drift -- files/updates an `escalated`-labeled bd issue via
      pg-connector on a real finding, no LLM involved).

      This module only puts the binary on PATH -- it exposes no
      config.yaml-style settings of its own, since every `run` invocation
      is fully parameterized by its own CLI flags (--ccpool-timeout,
      --pg-connector-timeout, --snapshot-path, ...). Wiring THIS role's
      own scheduling (the pg-router [[query]]/[[role]] TOML and the
      PG_CONNECTOR_ISSUE_BEADS_DIR env wrap) is a separate,
      phillipg-nix-ziprecruiter-repo sibling packet's job, not this
      module's.

      Runtime-depends on both `ccpool` and `pg-connector` being on PATH
      (`cmd/ccpool-probe/ccpoolexec.go`, `cmd/ccpool-probe/connector.go`).
    '';
    package = lib.mkPackageOption pkgs "ccpool-probe" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];
  };
}
