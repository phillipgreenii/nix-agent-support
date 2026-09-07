{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pg-connector;
in
{
  options.phillipgreenii.programs.pg-connector = {
    enable = lib.mkEnableOption "pg-connector CLI (unified pluggable connector umbrella CLI)";
    package = lib.mkPackageOption pkgs "pg-connector" { };
  };

  config = lib.mkIf cfg.enable {
    # cfg.package (the Tier-1 umbrella binary) dispatches to its Tier-2
    # capability backends by execing a bare binary name found on $PATH —
    # packages/pg-connector/pkg/scriptout/exec.go's runInvoke resolves the
    # binary named in the connector.<type> registry
    # (packages/pg-connector/cmd/pg-connector/registry.go) with no compiled-in
    # linkage between the binaries. All four backends therefore MUST ship
    # alongside cfg.package whenever this module is enabled, or dispatch to
    # that capability fails at runtime with "executable file not found in
    # $PATH". None of the four has an independent CLI identity of its own
    # (each package's own meta.description says so — they speak only the
    # scriptout wire protocol) or a tldr page, so — unlike cfg.package — they
    # are not exposed as separate mkPackageOption overrides here; they are
    # read straight from pkgs, matching flake.nix's own package-attr names.
    home.packages = [
      cfg.package
      pkgs.pg-connector-pr-github
      pkgs.pg-connector-ci-github-actions
      pkgs.pg-connector-issue-beads
      pkgs.pg-connector-scm-git
    ];

    # No programs.tldr.customPages entry: unlike pg-pr's own module,
    # packages/pg-connector/default.nix's postInstall generates only a man
    # page + completions — it does not copy/generate a tldr page the way
    # packages/pg-pr/default.nix does from its own committed pg-pr.md — so
    # there is nothing at ${cfg.package}/share/tldr/pages.common/pg-connector.md
    # to reference yet.

    # Phase 0: install the umbrella CLI + its Tier-2 backends only, mirroring
    # pg-pr's own home/programs/pg-pr/default.nix Phase 0. Config-file
    # generation (the connector: registry itself), daemon units, and plugin
    # marketplace registration are out of scope here.
  };
}
