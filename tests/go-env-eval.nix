# Eval-level test of the go-env home-manager module against the REAL home-manager
# `programs.go` module: home/default.nix must import it, and enabled it must render
# GOFLAGS=-p=3 -trimpath and GOTOOLCHAIN=local into the Go env file that `go env GOENV` names
# on the host platform. A custom toolchain value renders as given; toolchain = null
# and enable = false each leave GOTOOLCHAIN out (enable = false drops GOFLAGS too).
# Consumed by checks.<system>.test-go-env-eval in flake.nix.
#
# Returns { failures; caseCount; }.
{
  lib,
  pkgs,
  home-manager,
}:
let
  isDarwin = pkgs.stdenv.hostPlatform.isDarwin;
  envFileKey = if isDarwin then "Library/Application Support/go/env" else ".config/go/env";

  evalWith =
    cfg:
    (lib.evalModules {
      specialArgs = { inherit pkgs; };
      modules = [
        "${home-manager}/modules/programs/go.nix"
        ../home/programs/go-env/default.nix
        {
          options = {
            meta = lib.mkOption {
              type = lib.types.attrsOf lib.types.unspecified;
              default = { };
            };
            home = {
              homeDirectory = lib.mkOption { default = "/example/home"; };
              packages = lib.mkOption {
                type = lib.types.listOf lib.types.unspecified;
                default = [ ];
              };
              file = lib.mkOption {
                type = lib.types.attrsOf lib.types.unspecified;
                default = { };
              };
            };
            xdg.configFile = lib.mkOption {
              type = lib.types.attrsOf lib.types.unspecified;
              default = { };
            };
            warnings = lib.mkOption {
              type = lib.types.listOf lib.types.unspecified;
              default = [ ];
            };
            assertions = lib.mkOption {
              type = lib.types.listOf lib.types.unspecified;
              default = [ ];
            };
          };
        }
        { programs.go.enable = true; }
        cfg
      ];
    }).config;

  fileText =
    c:
    let
      f = if isDarwin then c.home.file.${envFileKey} else c.xdg.configFile."go/env";
    in
    builtins.readFile f.source;

  on = evalWith { };
  three = evalWith { phillipgreenii.programs.go-env.parallelism = 5; };
  off = evalWith { phillipgreenii.programs.go-env.enable = false; };
  customToolchain = evalWith { phillipgreenii.programs.go-env.toolchain = "go1.26.10"; };
  noTrimpath = evalWith { phillipgreenii.programs.go-env.trimpath = false; };
  noToolchain = evalWith { phillipgreenii.programs.go-env.toolchain = null; };

  cases = [
    {
      name = "home/default.nix imports the go-env module";
      ok = lib.elem ../home/programs/go-env (import ../home/default.nix { }).imports;
    }
    {
      name = "enabled by default: the Go env file contains GOFLAGS=-p=3 -trimpath";
      ok = lib.hasInfix "GOFLAGS=-p=3 -trimpath\n" (fileText on);
    }
    {
      name = "parallelism option changes the value";
      ok = lib.hasInfix "GOFLAGS=-p=5 -trimpath\n" (fileText three);
    }
    {
      name = "trimpath = false: the Go env file contains GOFLAGS=-p=3 with no -trimpath";
      ok = lib.hasInfix "GOFLAGS=-p=3\n" (fileText noTrimpath);
    }
    {
      name = "disabled: no GOFLAGS in the Go env file";
      ok = !(lib.hasInfix "GOFLAGS" (fileText off));
    }
    {
      name = "enabled by default: the Go env file contains GOTOOLCHAIN=local";
      ok = lib.hasInfix "GOTOOLCHAIN=local\n" (fileText on);
    }
    {
      name = "toolchain option changes the value";
      ok = lib.hasInfix "GOTOOLCHAIN=go1.26.10\n" (fileText customToolchain);
    }
    {
      name = "toolchain = null: no GOTOOLCHAIN in the Go env file, GOFLAGS kept";
      ok =
        !(lib.hasInfix "GOTOOLCHAIN" (fileText noToolchain))
        && lib.hasInfix "GOFLAGS=-p=3 -trimpath\n" (fileText noToolchain);
    }
    {
      name = "disabled: no GOTOOLCHAIN in the Go env file";
      ok = !(lib.hasInfix "GOTOOLCHAIN" (fileText off));
    }
  ];
in
{
  failures = map (c: c.name) (lib.filter (c: !c.ok) cases);
  caseCount = lib.length cases;
}
