# Eval-level test for home/programs/pg-desk's `routerSource` and `shadow`
# install options (bead pg2-wmv6p). Both packages were overlay-only, so neither
# reached the per-user PATH. Evaluates the pg-desk and pg-connector modules
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
    hasShadow = builtins.elem pkgs.pg-desk-shadow c.home.packages;
  };
in
{
  # pg-desk enabled with defaults: the adapter rides along, the one-off
  # measurement tool does not.
  defaults = summarize (evaluate {
    enable = true;
  });
  # Both explicitly on.
  both = summarize (evaluate {
    enable = true;
    shadow.enable = true;
  });
  # Adapter opted out, shadow on.
  shadowOnly = summarize (evaluate {
    enable = true;
    routerSource.enable = false;
    shadow.enable = true;
  });
  # pg-desk itself disabled: nothing installs, whatever the sub-options say.
  disabled = summarize (evaluate {
    enable = false;
    shadow.enable = true;
  });
}
