# Eval-level test of home/programs/pg-gwm: the scheduled reaper's launchd entry,
# the rendered configuration file and the options. Consumed by
# checks.<system>.test-pg-gwm-hm. The real module is evaluated against stubbed
# home-manager options and personal's HM launchd registry option
# (tests/launchd-registry-stub.nix); the stub cannot render a plist, so these cases
# pin the entry the module hands the registry, not what launchd does with it.
#
# Returns { failures; caseCount; script; config; }: `script` is the reaper's
# rendered command and `config` the rendered, build-time-checked config.toml, which
# the check runs and reads with the real CLI.
{ lib, pkgs }:
let
  withPlatform =
    isDarwin:
    pkgs
    // {
      stdenv = pkgs.stdenv // {
        hostPlatform = pkgs.stdenv.hostPlatform // {
          inherit isDarwin;
        };
      };
    };

  stubs =
    { lib, ... }:
    {
      options = {
        programs = {
          tldr = {
            enable = lib.mkOption {
              type = lib.types.bool;
              default = true;
            };
            customPages = lib.mkOption {
              type = lib.types.attrsOf lib.types.anything;
              default = { };
            };
          };
          bash.initExtra = lib.mkOption {
            type = lib.types.lines;
            default = "";
          };
          zsh.initContent = lib.mkOption {
            type = lib.types.lines;
            default = "";
          };
        };
        # personal's shell flags, which the completion lines are gated on
        phillipgreenii.programs.bash.enable = lib.mkOption {
          type = lib.types.bool;
          default = true;
        };
        phillipgreenii.programs.zsh.enable = lib.mkOption {
          type = lib.types.bool;
          default = true;
        };
        home = {
          packages = lib.mkOption {
            type = lib.types.listOf lib.types.package;
            default = [ ];
          };
          homeDirectory = lib.mkOption {
            type = lib.types.str;
            default = "/example/home";
          };
          username = lib.mkOption {
            type = lib.types.str;
            default = "example";
          };
          profileDirectory = lib.mkOption {
            type = lib.types.str;
            default = "/etc/profiles/per-user/example";
          };
        };
        xdg = {
          stateHome = lib.mkOption {
            type = lib.types.str;
            default = "/example/home/.local/state";
          };
          configHome = lib.mkOption {
            type = lib.types.str;
            default = "/example/home/.config";
          };
          configFile = lib.mkOption {
            type = lib.types.attrsOf (
              lib.types.submodule { options.source = lib.mkOption { type = lib.types.path; }; }
            );
            default = { };
          };
        };
      };
    };

  evalWith =
    {
      isDarwin ? true,
      declareLaunchd ? true,
      cfg ? { },
    }:
    (lib.evalModules {
      specialArgs = {
        pkgs = withPlatform isDarwin;
        inherit lib;
      };
      modules = [
        ../home/programs/pg-gwm/default.nix
        stubs
        {
          phillipgreenii.programs.pg-gwm = {
            enable = true;
            repos."/x".min = 2;
          };
        }
        cfg
      ]
      ++ lib.optional declareLaunchd ./launchd-registry-stub.nix;
    }).config;

  defaultC = evalWith { };
  customC = evalWith { cfg.phillipgreenii.programs.pg-gwm.reaper.intervalSeconds = 300; };
  offC = evalWith { cfg.phillipgreenii.programs.pg-gwm.reaper.enable = false; };
  disabledC = evalWith { cfg.phillipgreenii.programs.pg-gwm.enable = lib.mkForce false; };
  noRegistryC = evalWith { declareLaunchd = false; };
  linuxC = evalWith { isDarwin = false; };

  agents = c: c.phillipgreenii.programs.launchdServices.userAgents;
  agent = c: (agents c).pg-gwm-reaper;
  has = needle: hay: lib.hasInfix needle hay;
  toml = c: builtins.readFile c.xdg.configFile."pg-gwm/config.toml".source;

  cases = [
    {
      name = "the reaper runs every 600 seconds by default and 300 when set";
      ok =
        (agent defaultC).serviceConfig.StartInterval == 600
        && (agent customC).serviceConfig.StartInterval == 300;
    }
    {
      name = "the reaper is not keep-alive and runs at Nice 10";
      ok = (agent defaultC).keepAlive == false && (agent defaultC).serviceConfig.Nice == 10;
    }
    {
      name = "the reaper has the label and a PATH with the per-user profile, /usr/bin and /bin";
      ok =
        let
          p = (agent defaultC).serviceConfig.EnvironmentVariables.PATH;
        in
        (agent defaultC).label == "com.phillipg.pg-gwm-reaper"
        && has "/etc/profiles/per-user/example/bin" p
        && has "/usr/bin" p
        && has "/bin" p;
    }
    {
      name = "the reaper command reaps all repositories with a 10m deadline in JSON, longer than one measured 7m40s create";
      ok = has "reap --all-repos --deadline 10m --format=json" (agent defaultC).script;
    }
    {
      name = "the unmanaged audit runs weekly (Monday 09:00 by default), audits every configured repository and writes the exporter's count file";
      ok =
        let
          a = (agents defaultC).pg-gwm-unmanaged-audit;
        in
        a.label == "com.phillipg.pg-gwm-unmanaged-audit"
        && a.keepAlive == false
        &&
          a.serviceConfig.StartCalendarInterval == {
            Weekday = 1;
            Hour = 9;
            Minute = 0;
          }
        && has "unmanaged --repo /x" a.script
        && has "--metrics-file /example/home/.local/state/pg-gwm/unmanaged-worktrees.json" a.script
        && lib.hasSuffix "/audit.log" a.serviceConfig.StandardOutPath;
    }
    {
      name = "the unmanaged audit can be switched off without touching the reaper";
      ok =
        let
          c = evalWith { cfg.phillipgreenii.programs.pg-gwm.unmanagedAudit.enable = false; };
        in
        !((agents c) ? pg-gwm-unmanaged-audit) && (agents c) ? pg-gwm-reaper;
    }
    {
      name = "a disabled reaper produces no entry";
      ok = !((agents offC) ? pg-gwm-reaper);
    }
    {
      name = "a disabled module produces no entry, no package and no config file";
      ok =
        agents disabledC == { }
        && disabledC.home.packages == [ ]
        && !(disabledC.xdg.configFile ? "pg-gwm/config.toml");
    }
    {
      name = "without the launchd registry, or off darwin, the module evaluates with no entry";
      ok = noRegistryC.home.packages != [ ] && agents linuxC == { };
    }
    {
      name = "the rendered TOML has [repos.\"/x\"] with min = 2";
      ok = has "[repos.\"/x\"]" (toml defaultC) && has "min = 2" (toml defaultC);
    }
    {
      name = "the package, completion and tldr page are installed";
      ok =
        lib.length defaultC.home.packages == 1
        && has "pg-gwm completion bash" defaultC.programs.bash.initExtra
        && has "pg-gwm completion zsh" defaultC.programs.zsh.initContent
        && defaultC.programs.tldr.customPages.pg-gwm.platform == "common";
    }
  ];
in
{
  failures = map (c: c.name) (lib.filter (c: !c.ok) cases);
  caseCount = lib.length cases;
  inherit ((agent defaultC)) script;
  config = defaultC.xdg.configFile."pg-gwm/config.toml".source;
}
