# Eval-level test for home/programs/pg-desk's `attention.plugin` option (bead
# pg2-5l0x4.5). Evaluates the pg-desk and pg-connector modules together
# (stubbing only home.packages and xdg.configFile, like
# tests/pg-connector-home-render.nix) and returns, per scenario, what the
# plugin option did to pg-connector's `attention.sources` and to
# home.packages. Consumed by checks.test-pg-desk-attention-registration.
#
# All values are EXAMPLE values for fixtures only; the real registration lives
# in the consuming machine flake.
{ lib, pkgs }:
let
  evaluate =
    {
      pluginEnable,
      connectorSources ? [ ],
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
            };
          }
        )
        {
          phillipgreenii.programs.pg-desk = {
            enable = true;
            selfLogin = "example-login";
            attention.plugin.enable = pluginEnable;
          };
          phillipgreenii.programs.pg-connector = {
            enable = true;
            attention.sources = connectorSources;
          };
        }
      ];
    }).config;

  summarize = c: {
    sources = c.phillipgreenii.programs.pg-connector.attention.sources;
    hasPlugin = builtins.elem pkgs.pg-desk-attention c.home.packages;
    hasDesk = builtins.elem pkgs.pg-desk c.home.packages;
  };
in
{
  # Plugin on, connector already registers a source of its own.
  enabled = summarize (evaluate {
    pluginEnable = true;
    connectorSources = [ "pg-connector-alert-grafana" ];
  });
  # Plugin on, no other source.
  enabledAlone = summarize (evaluate {
    pluginEnable = true;
  });
  # Default: the option is off and changes nothing.
  disabled = summarize (evaluate {
    pluginEnable = false;
    connectorSources = [ "pg-connector-alert-grafana" ];
  });
}
