# Eval-level test of the beads bundle switch (design note
# docs/superpowers/specs/2026-11-07-mod-bundle-grouping-design.md, "beads"): with
# phillipgreenii.programs.beads.enable off the pb plugin is not enabled and pgd's
# components.beads is off; with it on both are on; an explicit pb.enable or
# components.beads setting wins over the new default. The pb and pgd modules and the
# marketplaces module are the real ones; the beads module (it pulls packages) and the
# home-manager options are stubbed. Consumed by checks.<system>.test-bundle-beads-switch.
# All values are EXAMPLE values.
{ lib, pkgs }:
let
  # A built-marketplace stand-in carrying the passthru the marketplaces module reads,
  # with the pb manifest's real requires/defaultEnabled.
  marketplace = pkgs.runCommand "example-marketplace" {
    passthru = {
      marketplaceName = "example-marketplace-local";
      plugins = [
        {
          name = "pb";
          version = "1.0.0";
          key = "pb@example-marketplace-local";
          defaultEnabled = true;
          requires = [
            "pb"
            "session-mode"
          ];
        }
      ];
    };
  } "mkdir $out";

  evaluate =
    {
      beads ? null,
      pb ? { },
      pgd ? { },
    }:
    lib.evalModules {
      specialArgs = { inherit pkgs lib; };
      modules = [
        ../home/programs/pb/default.nix
        ../home/programs/pgd/default.nix
        ../home/programs/claude-marketplaces/default.nix
        (
          { lib, ... }:
          {
            options = {
              # stub of home/programs/beads (the real one installs packages)
              phillipgreenii.programs = {
                beads.enable = lib.mkEnableOption "beads";
                session-mode.enable = lib.mkEnableOption "session-mode";
                claude-code = {
                  enable = lib.mkEnableOption "claude-code";
                  settings = lib.mkOption {
                    type = lib.types.attrsOf lib.types.anything;
                    default = { };
                  };
                };
              };
              home = {
                homeDirectory = lib.mkOption {
                  type = lib.types.str;
                  default = "/example/home";
                };
                file = lib.mkOption {
                  type = lib.types.attrsOf lib.types.anything;
                  default = { };
                };
                packages = lib.mkOption {
                  type = lib.types.listOf lib.types.package;
                  default = [ ];
                };
                sessionVariables = lib.mkOption {
                  type = lib.types.attrsOf lib.types.str;
                  default = { };
                };
              };
            };
          }
        )
        (
          { lib, ... }:
          {
            phillipgreenii.programs = {
              claude-code.enable = true;
              session-mode.enable = true;
              claude-code.marketplaces.nixProvided = [ marketplace ];
              inherit pb pgd;
            }
            // lib.optionalAttrs (beads != null) { beads.enable = beads; };
          }
        )
      ];
    };

  pbPluginOn =
    c:
    c.config.phillipgreenii.programs.claude-code.settings.enabledPlugins."pb@example-marketplace-local";
  pgdBeads =
    c: (builtins.fromJSON c.config.home.sessionVariables.PG_CLAUDE_DISPLAY_OPTIONS).components.beads;

  beadsOff = evaluate { };
  beadsOffExplicit = evaluate { beads = false; };
  beadsOn = evaluate { beads = true; };
  pbForcedOff = evaluate {
    beads = true;
    pb.enable = false;
  };
  pbForcedOn = evaluate { pb.enable = true; };
  pgdForcedOff = evaluate {
    beads = true;
    pgd.components.beads = false;
  };
  pgdForcedOn = evaluate { pgd.components.beads = true; };

  cases = {
    # ---- beads off (unset and explicit): pb plugin and pgd beads component are off ----
    pbPluginOffWithBeadsOff = pbPluginOn beadsOff == false && pbPluginOn beadsOffExplicit == false;
    pbPackageAbsentWithBeadsOff = beadsOff.config.home.packages == [ ];
    pgdBeadsOffWithBeadsOff = pgdBeads beadsOff == false && pgdBeads beadsOffExplicit == false;

    # ---- beads on: both are on --------------------------------------------------
    pbPluginOnWithBeadsOn = pbPluginOn beadsOn == true;
    pbPackageWithBeadsOn = beadsOn.config.home.packages == [ pkgs.pb ];
    pgdBeadsOnWithBeadsOn = pgdBeads beadsOn == true;

    # ---- an explicit setting wins ----------------------------------------------
    explicitPbOffWins = pbPluginOn pbForcedOff == false;
    explicitPbOnWins = pbPluginOn pbForcedOn == true;
    explicitPgdBeadsOffWins = pgdBeads pgdForcedOff == false;
    explicitPgdBeadsOnWins = pgdBeads pgdForcedOn == true;

    # ---- the other pgd components keep their own default ------------------------
    otherComponentsStayOn =
      let
        comps =
          (builtins.fromJSON beadsOff.config.home.sessionVariables.PG_CLAUDE_DISPLAY_OPTIONS).components;
      in
      lib.all (n: comps.${n}) (lib.filter (n: n != "beads") (builtins.attrNames comps));
  };
in
{
  inherit cases;
  failures = builtins.attrNames (lib.filterAttrs (_: ok: !ok) cases);
}
