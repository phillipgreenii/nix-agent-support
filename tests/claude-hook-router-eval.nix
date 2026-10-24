# Shared eval harness for home/programs/claude-hook-router: a hand-rolled
# `lib.evalModules` over the module with exactly the options it reads/writes
# stubbed, plus the one-delegate configuration the tests evaluate it with.
# Shared by checks.<system>.test-claude-hook-router (which builds the result and
# reads the built plugin) and checks.<system>.test-claude-hook-router-no-ifd
# (which evaluates the same module in a nix process that forbids import from
# derivation), so both exercise the module the same way (pg2-8l6hx).
#
# Takes `inputs` as an argument, and only needs `inputs.phillipgreenii-nix-base.lib`
# (`mkClaudeMarketplaceBuilders`, `mkSrcDigest`), so a caller outside the flake can
# supply a hand-built stand-in for it.
#
# Returns `{ evalCfg, withDelegate, marketplaceDrv }`.
{
  lib,
  pkgs,
  inputs,
}:
let
  evalCfg =
    cfg:
    (lib.evalModules {
      specialArgs = { inherit pkgs lib inputs; };
      modules = [
        ../home/programs/claude-hook-router/default.nix
        (
          { lib, ... }:
          {
            # Minimal stubs for the config surface this module reads and
            # contributes to (the real options live in claude-code /
            # claude-marketplaces, not pulled in here).
            options = {
              # Real home-manager/NixOS module sets provide `assertions`
              # (base "misc" module); this standalone evalModules harness
              # does not pull that in, so stub it -- unlike
              # test-claude-marketplaces, this module DOES define
              # assertions (delegate contract/matcher validation).
              assertions = lib.mkOption {
                type = lib.types.listOf lib.types.unspecified;
                default = [ ];
              };
              phillipgreenii = {
                programs.claude-code = {
                  enable = lib.mkEnableOption "claude (stub)";
                  settings = {
                    extraKnownMarketplaces = lib.mkOption {
                      type = lib.types.attrsOf (lib.types.attrsOf lib.types.anything);
                      default = { };
                    };
                    enabledPlugins = lib.mkOption {
                      type = lib.types.attrsOf lib.types.bool;
                      default = { };
                    };
                    plugins = lib.mkOption {
                      type = lib.types.listOf lib.types.str;
                      default = [ ];
                    };
                  };
                  marketplaces.nixProvided = lib.mkOption {
                    type = lib.types.listOf lib.types.package;
                    default = [ ];
                  };
                };
              };
              home.homeDirectory = lib.mkOption {
                type = lib.types.str;
                default = "/home/test";
              };
              home.packages = lib.mkOption {
                type = lib.types.listOf lib.types.package;
                default = [ ];
              };
            };
          }
        )
        cfg
      ];
    }).config;

  withDelegate = evalCfg {
    phillipgreenii.programs.claude-code.enable = true;
    phillipgreenii.programs.claude-hook-router = {
      enable = true;
      delegates = lib.mkOrder 1000 [
        {
          name = "test-tool";
          event = "PreToolUse";
          matcher = null;
          command = "test-tool-hook";
          contract = "decide";
          priority = 1000;
        }
      ];
    };
  };

  marketplaceDrv = builtins.elemAt withDelegate.phillipgreenii.programs.claude-code.marketplaces.nixProvided 0;
in
{
  inherit evalCfg withDelegate marketplaceDrv;
}
