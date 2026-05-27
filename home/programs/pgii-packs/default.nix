# pgii-packs home-manager module.
#
# Exposes per-pack toggles plus a list of cities to install into. The
# activation script (./activation.sh) writes managed [imports.<name>]
# blocks into each city's pack.toml at home-manager activation time.
#
# Why pack.toml (not city.toml): gascity treats [packs.<name>] in city.toml
# as a remote git source. Local file-system imports go through
# [imports.<name>] in the city's top-level pack.toml. Verified empirically
# against gascity 1.1.0.
#
# Spec: docs/superpowers/specs/2026-05-26-pgii-packs-migration-design.md
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pgii;

  # Build the table of pack name → drv for enabled packs.
  # Each entry is { name; drv; } so we can use it for both home.file rooting
  # and the --packs JSON arg passed to activation.sh.
  enabledPacks = lib.optional cfg.packs.test-fixture.enable {
    name = "pgii-pack-test-fixture";
    drv = pkgs.pgii-pack-test-fixture;
  };

  anyPackEnabled = enabledPacks != [ ];

  packStorePathMap = lib.listToAttrs (
    map (p: {
      inherit (p) name;
      value = "${p.drv}";
    }) enabledPacks
  );
in
{
  options.phillipgreenii.programs.pgii = {

    gascity = {
      cities = lib.mkOption {
        type = lib.types.listOf lib.types.path;
        default = [ ];
        example = [ "/Users/phillipg/gc" ];
        description = ''
          Absolute paths to gascity cities (directories containing pack.toml)
          that should receive managed [imports.<name>] blocks for any
          enabled pgii pack below. The activation script writes/updates the
          blocks in <city>/pack.toml on every home-manager rebuild;
          disabling a pack removes its block on the next rebuild.
        '';
      };

      reloadSupervisor = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          After writing pack.toml, run `gc --city <city> supervisor reload`
          for each city whose <city>/.gc/controller.sock exists and where
          `gc` is on PATH. Reload failures warn but do not fail activation.
        '';
      };
    };

    packs = {
      test-fixture.enable = lib.mkEnableOption ''
        pgii-pack-test-fixture (validation pack for the pgii-packs pipeline).
      '';
      # Real packs (pr-support, dolt-hacks, workers, gastown, bead-importer)
      # are added in their respective phase plans.
    };
  };

  config = lib.mkIf anyPackEnabled {

    home.file = lib.mkMerge (
      map (p: {
        ".local/share/pgii-packs/${p.name}".source = p.drv;
      }) enabledPacks
    );

    home.activation.pgii-packs =
      let
        activationScript = pkgs.writeShellApplication {
          name = "pgii-packs-activation";
          runtimeInputs = [
            pkgs.bash
            pkgs.coreutils
            pkgs.jq
            pkgs.gnugrep
            pkgs.gawk
            pkgs.gnused
          ];
          text = builtins.readFile ./activation.sh;
        };
      in
      lib.hm.dag.entryAfter [ "writeBoundary" ] ''
        run ${activationScript}/bin/pgii-packs-activation \
          --cities ${lib.escapeShellArg (builtins.toJSON cfg.gascity.cities)} \
          --packs  ${lib.escapeShellArg (builtins.toJSON packStorePathMap)} \
          ${lib.optionalString cfg.gascity.reloadSupervisor "--reload"}
      '';

    assertions = [
      {
        assertion = !anyPackEnabled || cfg.gascity.cities != [ ];
        message = ''
          Enabling any pgii pack requires at least one city in
          phillipgreenii.programs.pgii.gascity.cities.
        '';
      }
    ];

    # home.activation.pgii-packs wiring is added in Task 14.
  };
}
