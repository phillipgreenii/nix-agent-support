# Eval-level test of darwin/modules/pg-disk-reclaimer: the log source's error alert
# and the dashboard registration. Consumed by
# checks.<system>.test-pg-disk-reclaimer-darwin-module. The real module is evaluated
# against a stub of the observability surface that mirrors the real logSources
# options this module sets (emitInterval, errorAlert.{threshold,window,severity}) and
# a stub of home-manager.users carrying only the options the module reads.
#
# Returns { failures; caseCount; }.
{ lib, pkgs }:
let
  logSourceSubmodule = _: {
    options = {
      format = lib.mkOption {
        type = lib.types.enum [
          "jsonl"
          "raw"
        ];
        default = "jsonl";
      };
      emitInterval = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
      };
      errorAlert = {
        threshold = lib.mkOption {
          type = lib.types.int;
          default = 10;
        };
        window = lib.mkOption {
          type = lib.types.str;
          default = "10m";
        };
        severity = lib.mkOption {
          type = lib.types.str;
          default = "warning";
        };
      };
    };
  };

  user = interval: {
    phillipgreenii.programs.pg-disk-reclaimer = {
      enable = true;
      auto = {
        enable = true;
        intervalSeconds = interval;
      };
    };
  };

  evalWith =
    {
      obsEnable ? true,
      users,
    }:
    (lib.evalModules {
      specialArgs = { inherit pkgs lib; };
      modules = [
        ../darwin/modules/pg-disk-reclaimer/default.nix
        {
          options = {
            assertions = lib.mkOption {
              type = lib.types.listOf lib.types.unspecified;
              default = [ ];
            };
            home-manager.users = lib.mkOption {
              type = lib.types.attrsOf lib.types.anything;
              default = { };
            };
            phillipgreenii.observability = {
              enable = lib.mkOption {
                type = lib.types.bool;
                default = false;
              };
              logSources = lib.mkOption {
                type = lib.types.attrsOf (lib.types.submodule logSourceSubmodule);
                default = { };
              };
              dashboardProviders = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
            };
          };
          config = {
            phillipgreenii.observability.enable = obsEnable;
            home-manager.users = users;
          };
        }
      ];
    }).config;

  c900 = evalWith { users.alice = user 900; };
  c3600 = evalWith { users.alice = user 3600; };
  mismatch = evalWith {
    users = {
      alice = user 900;
      bob = user 600;
    };
  };
  noAuto = evalWith { users.alice.phillipgreenii.programs.pg-disk-reclaimer.enable = true; };
  obsOff = evalWith {
    obsEnable = false;
    users.alice = user 900;
  };

  src = c: c.phillipgreenii.observability.logSources.pg-disk-reclaimer;
  failed = c: lib.filter (a: !a.assertion) c.assertions;

  cases = [
    {
      name = "900 s: threshold 1, critical, emitInterval 900s, window 2700s (3 x interval)";
      ok =
        (src c900).errorAlert.threshold == 1
        && (src c900).errorAlert.severity == "critical"
        && (src c900).emitInterval == "900s"
        && (src c900).errorAlert.window == "2700s"
        && (src c900).format == "jsonl";
    }
    {
      name = "3600 s evaluates too: emitInterval 3600s, window 10800s (derived, never literals)";
      ok =
        (src c3600).emitInterval == "3600s"
        && (src c3600).errorAlert.window == "10800s"
        && (src c3600).errorAlert.threshold == 1
        && failed c3600 == [ ];
    }
    {
      name = "the alert stays reachable: window / interval > threshold at both intervals";
      ok =
        lib.all
          (
            c:
            let
              i = lib.toInt (lib.removeSuffix "s" (src c).emitInterval);
              w = lib.toInt (lib.removeSuffix "s" (src c).errorAlert.window);
            in
            w / i > (src c).errorAlert.threshold
          )
          [
            c900
            c3600
          ];
    }
    {
      name = "two auto users with different intervals fail the assertion";
      ok = lib.any (a: lib.hasInfix "same auto.intervalSeconds" a.message) (failed mismatch);
    }
    {
      name = "one interval across several users passes";
      ok =
        failed (evalWith {
          users = {
            a = user 900;
            b = user 900;
          };
        }) == [ ];
    }
    {
      name = "the dashboard is registered with the auto log source and only then";
      ok =
        c900.phillipgreenii.observability.dashboardProviders.pg-disk-reclaimer.dashboards
        == [ ../packages/pg-disk-reclaimer/grafana/pg-disk-reclaimer.json ]
        && noAuto.phillipgreenii.observability.dashboardProviders == { }
        && noAuto.phillipgreenii.observability.logSources == { }
        && obsOff.phillipgreenii.observability.dashboardProviders == { }
        && obsOff.phillipgreenii.observability.logSources == { };
    }
  ];
in
{
  failures = map (c: c.name) (lib.filter (c: !c.ok) cases);
  caseCount = lib.length cases;
}
