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
  enabledPacks =
    lib.optional cfg.packs.test-fixture.enable {
      name = "pgii-pack-test-fixture";
      drv = pkgs.pgii-pack-test-fixture;
    }
    ++ lib.optional cfg.packs.pr-support.enable {
      name = "pgii-pr-support";
      drv = pkgs.pgii-pack-pr-support;
    }
    ++ lib.optional cfg.packs.dolt-hacks.enable {
      name = "pgii-dolt-hacks";
      drv = pkgs.pgii-pack-dolt-hacks;
    }
    ++ lib.optional cfg.packs.workers.enable {
      name = "pgii-workers";
      drv = pkgs.pgii-pack-workers;
    }
    ++ lib.optional cfg.packs.gastown.enable {
      name = "pgii-gastown";
      drv = pkgs.pgii-pack-gastown;
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
      pr-support.enable = lib.mkEnableOption ''
        pgii-pack-pr-support (PR review / triage / self-fix agents + pr-watcher
        and wake-on-work orders + PR-related doctor checks). Pack scripts depend
        on `pg-pr` in PATH — enable via `phillipgreenii.programs.pg-pr.enable`.
      '';
      dolt-hacks.enable = lib.mkEnableOption ''
        pgii-pack-dolt-hacks (HACK orders + scripts for dolt storage/lifecycle
        issues and gascity 1.1.0 supervisor regressions: HACK 2, 10, 11, 12, 14,
        15 and hack-daily-summary).
      '';
      workers.enable = lib.mkEnableOption ''
        pgii-pack-workers (rig-scoped generic worker pool — claims open beads
        with acceptance_criteria set; ambiguous work labeled `needs-foreman`).
        Auto-binds to every rig via [defaults.rig.imports.pgii-workers]; per-rig
        session concurrency is overridden via city.toml [[rigs.patches]].
      '';
      gastown.enable = lib.mkEnableOption ''
        pgii-pack-gastown (mayor/deacon/operator/foreman city-scope agents
        + mol-deacon-patrol formula + 3 doctor checks). Locally-customized
        copies of gastown's defaults; replaces enabling the gastown system
        pack outright (which would also try to manage other defaults).
      '';
    };
  };

  config = lib.mkMerge [

    # Pack derivations are symlinked into ~/.local/share/pgii-packs/<name>
    # only when at least one pack is enabled.
    (lib.mkIf anyPackEnabled {
      home.file = lib.mkMerge (
        map (p: {
          ".local/share/pgii-packs/${p.name}".source = p.drv;
        }) enabledPacks
      );
    })

    # The activation script runs whenever a city is configured — even when
    # no packs are enabled — so it can strip leftover managed blocks on the
    # packs-enabled → none transition. activation.sh handles `--packs '{}'`
    # by removing all managed pgii-pack:* blocks; see test_remove_on_disable.
    (lib.mkIf (cfg.gascity.cities != [ ]) {
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
    })

    # Assertions are evaluated unconditionally so users who enable a pack
    # without configuring a city see the error at eval time.
    {
      assertions = [
        {
          assertion = !cfg.packs.pr-support.enable || (config.phillipgreenii.programs.pg-pr.enable or false);
          message = ''
            phillipgreenii.programs.pgii.packs.pr-support.enable requires
            phillipgreenii.programs.pg-pr.enable = true (pack scripts call
            pg-pr).
          '';
        }
        {
          assertion = !anyPackEnabled || cfg.gascity.cities != [ ];
          message = ''
            Enabling any pgii pack requires at least one city in
            phillipgreenii.programs.pgii.gascity.cities.
          '';
        }
      ];
    }
  ];
}
