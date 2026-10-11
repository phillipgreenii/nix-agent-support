# Eval-level test for home/programs/pg-desk's tldr registration (docket
# pg2-2j5ac.44, the focus help packet). Evaluates the pg-desk and pg-connector
# modules together (stubbing only home.packages, xdg.configFile and
# home-manager's programs.tldr, like tests/pg-desk-tool-install.nix) and
# returns, per scenario, which custom pages the module registered. Consumed by
# checks.test-pg-desk-tldr-registration.
#
# All values are EXAMPLE values for fixtures only.
{ lib, pkgs }:
let
  evaluate =
    {
      deskEnable,
      tldrEnable,
    }:
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
            };
          }
        )
        {
          programs.tldr.enable = tldrEnable;
          phillipgreenii.programs.pg-desk = {
            enable = deskEnable;
            selfLogin = "example-login";
          };
        }
      ];
    }).config;

  summarize = c: {
    pages = builtins.attrNames c.programs.tldr.customPages;
    platform = c.programs.tldr.customPages.pg-desk-focus.platform or null;
    source = c.programs.tldr.customPages.pg-desk-focus.source or null;
  };
in
{
  # tldr and pg-desk both on: the page is registered from the package.
  tldrOn = summarize (evaluate {
    deskEnable = true;
    tldrEnable = true;
  });
  # tldr off: nothing is registered.
  tldrOff = summarize (evaluate {
    deskEnable = true;
    tldrEnable = false;
  });
  # pg-desk off: nothing is registered, whatever tldr does.
  deskDisabled = summarize (evaluate {
    deskEnable = false;
    tldrEnable = true;
  });
}
