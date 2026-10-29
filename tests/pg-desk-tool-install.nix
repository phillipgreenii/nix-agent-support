# Eval-level test for home/programs/pg-desk's `routerSource` install option and
# the retired `shadow.enable` option (bead pg2-wmv6p). The adapter was
# overlay-only, so it did not reach the per-user PATH. Evaluates the pg-desk and pg-connector modules
# together (stubbing only home.packages and xdg.configFile, like
# tests/pg-desk-attention-registration.nix) and returns, per scenario, which of
# the tools landed in home.packages. Consumed by
# checks.test-pg-desk-tool-install.
#
# All values are EXAMPLE values for fixtures only.
{ lib, pkgs }:
let
  evaluate =
    deskCfg:
    (lib.evalModules {
      specialArgs = { inherit pkgs lib; };
      modules = [
        ../home/programs/pg-desk/default.nix
        ../home/programs/pg-connector/default.nix
        (
          { lib, ... }:
          {
            options = {
              home.packages = lib.mkOption {
                type = lib.types.listOf lib.types.anything;
                default = [ ];
              };
              xdg.configFile = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
              programs.tldr.enable = lib.mkEnableOption "tldr (stub)";
              programs.tldr.customPages = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
              # The pg-connector module (loaded alongside pg-desk) reads the home
              # directory and writes warnings and assertions (home-manager declares all three).
              home.homeDirectory = lib.mkOption {
                type = lib.types.str;
                default = "/example/home";
              };
              warnings = lib.mkOption {
                type = lib.types.listOf lib.types.str;
                default = [ ];
              };
              assertions = lib.mkOption {
                type = lib.types.listOf lib.types.anything;
                default = [ ];
              };
            };
          }
        )
        {
          phillipgreenii.programs.pg-desk = {
            selfLogin = "example-login";
          }
          // deskCfg;
        }
      ];
    }).config;

  summarize = c: {
    packages = c.home.packages;
    hasDesk = builtins.elem pkgs.pg-desk c.home.packages;
    hasRouterSource = builtins.elem pkgs.pg-router-source-pg-desk c.home.packages;
    inherit (c) warnings;
  };
in
{
  # pg-desk enabled with defaults: the adapter rides along, no warning.
  defaults = summarize (evaluate {
    enable = true;
  });
  # The retired shadow flag still evaluates, installs nothing, and warns once.
  retiredShadow = summarize (evaluate {
    enable = true;
    shadow.enable = true;
  });
  # Adapter opted out.
  routerSourceOff = summarize (evaluate {
    enable = true;
    routerSource.enable = false;
  });
  # pg-desk itself disabled: nothing installs, whatever the sub-options say.
  disabled = summarize (evaluate {
    enable = false;
    shadow.enable = true;
  });
}
