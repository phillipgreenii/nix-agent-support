# Eval-level test of home/programs/pg-git-lander: the module is evaluated against
# stubs of the home-manager options it writes (xdg.configFile, home.packages,
# programs.tldr, assertions). Consumed by checks.<system>.test-pg-git-lander-hm,
# which also runs the REAL `pg-git-lander config check` on the rendered files.
#
# All values are EXAMPLE values.
{ lib, pkgs }:
let
  evaluate =
    {
      tldrEnable ? true,
    }:
    cfg:
    lib.evalModules {
      specialArgs = { inherit pkgs lib; };
      modules = [
        ../home/programs/pg-git-lander/default.nix
        (
          { lib, ... }:
          {
            options = {
              xdg.configFile = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
              home.packages = lib.mkOption {
                type = lib.types.listOf lib.types.package;
                default = [ ];
              };
              programs.tldr.enable = lib.mkOption {
                type = lib.types.bool;
                default = tldrEnable;
              };
              programs.tldr.customPages = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
              assertions = lib.mkOption {
                type = lib.types.listOf lib.types.anything;
                default = [ ];
              };
            };
          }
        )
        {
          phillipgreenii.programs.pg-git-lander = cfg // {
            enable = cfg.enable or true;
          };
        }
      ];
    };

  opts = c: c.config.phillipgreenii.programs.pg-git-lander;
  failed = c: lib.filter (a: !a.assertion) c.config.assertions;
  fileOf = c: c.config.xdg.configFile."pg-git-lander/config.toml".source;

  exampleCfg = {
    defaults.lock = {
      leaseTtl = "30s";
      wait = "60s";
    };
    repos = [
      {
        root = "/src/example-repo";
        trustRepoConfig = true;
      }
      {
        root = "/src/very-large-repo";
        ignoreRepoConfig = true;
      }
    ];
  };

  good = evaluate { } exampleCfg;
  bare = evaluate { } { };
  disabled = evaluate { } (exampleCfg // { enable = false; });
  tldrOff = evaluate { tldrEnable = false; } exampleCfg;
  emptyRoot = evaluate { } { repos = [ { root = ""; } ]; };

  cases = {
    installsTheBinary = map toString good.config.home.packages == [ (toString pkgs.pg-git-lander) ];
    rendersTheFile = toString (fileOf good) == toString (opts good).internal.configFile;
    noAssertionFails = failed good == [ ];
    bareRendersNoFile = bare.config.xdg.configFile == { } && failed bare == [ ];
    disabledDoesNothing =
      disabled.config.xdg.configFile == { }
      && disabled.config.home.packages == [ ]
      && disabled.config.programs.tldr.customPages == { };
    tldrRegistered =
      good.config.programs.tldr.customPages.pg-git-lander.source
      == "${pkgs.pg-git-lander}/share/tldr/pages.common/pg-git-lander.md";
    tldrOffRegistersNothing = tldrOff.config.programs.tldr.customPages == { };
    emptyRootAsserts = builtins.length (failed emptyRoot) == 1;
  };
in
{
  failures = lib.attrNames (lib.filterAttrs (_: ok: !ok) cases);
  inherit cases;
  live = {
    package = pkgs.pg-git-lander;
    goodFile = (opts good).internal.configFile;
    # an empty root is stopped by the module's assertion; this is the file it
    # would have rendered, for the binary's own rejection
    emptyRootFile = (opts emptyRoot).internal.configFile;
    tldrSource = "${pkgs.pg-git-lander}/share/tldr/pages.common/pg-git-lander.md";
  };
}
