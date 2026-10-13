# Stub of phillipgreenii-nix-personal's HM launchd registry option
# (phillipgreenii.programs.launchdServices.userAgents), for eval tests of the
# home modules that register an agent through it. This flake has NO input on
# personal, so the option is declared here with the field NAMES and TYPES of
# personal's lib/launchd-service-submodule.nix (personal ADR 0055); the real
# composition is exercised at the terminal flake. Defaults are personal's too,
# including manageLogs (enabled, its files defaulting to the entry's own
# StandardOutPath and StandardErrorPath) and logCollection.
{ lib, ... }:
let
  userAgentSubmodule = lib.types.submodule (
    { name, config, ... }:
    {
      options = {
        enable = lib.mkOption {
          type = lib.types.bool;
          default = true;
        };
        label = lib.mkOption {
          type = lib.types.str;
          default = "org.nixos.${name}";
        };
        script = lib.mkOption {
          type = lib.types.nullOr lib.types.lines;
          default = null;
        };
        # unsupported in the HM scope, but present on the shared submodule
        execPath = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
        };
        extraArgs = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = [ ];
        };
        keepAlive = lib.mkOption {
          type = lib.types.either lib.types.bool (lib.types.attrsOf lib.types.bool);
          default = true;
        };
        runAtLoad = lib.mkOption {
          type = lib.types.bool;
          default = true;
        };
        launchOnlyOnce = lib.mkOption {
          type = lib.types.bool;
          default = false;
        };
        serviceConfig = lib.mkOption {
          type = lib.types.attrs;
          default = { };
        };
        manageLogs = lib.mkOption {
          type = lib.types.submodule {
            options = {
              enable = lib.mkOption {
                type = lib.types.bool;
                default = true;
              };
              files = lib.mkOption {
                type = lib.types.listOf lib.types.str;
                default = lib.filter (p: p != null) [
                  (config.serviceConfig.StandardOutPath or null)
                  (config.serviceConfig.StandardErrorPath or null)
                ];
              };
              thresholdBytes = lib.mkOption {
                type = lib.types.ints.positive;
                default = 20 * 1024 * 1024;
              };
              archiveCount = lib.mkOption {
                type = lib.types.ints.unsigned;
                default = 1;
              };
            };
          };
          default = { };
        };
        logCollection = lib.mkOption {
          type = lib.types.submodule {
            options.enable = lib.mkOption {
              type = lib.types.bool;
              default = true;
            };
          };
          default = { };
        };
        checkMode = lib.mkOption {
          type = lib.types.enum [
            "continuous"
            "periodic"
            "once"
          ];
          default = "continuous";
        };
        healthCheck = lib.mkOption {
          type = lib.types.bool;
          default = true;
        };
        healthCheckTimeoutSeconds = lib.mkOption {
          type = lib.types.ints.positive;
          default = 10;
        };
        bootstrapSettleSeconds = lib.mkOption {
          type = lib.types.ints.positive;
          default = 10;
        };
      };
    }
  );
in
{
  options.phillipgreenii.programs.launchdServices.userAgents = lib.mkOption {
    type = lib.types.attrsOf userAgentSubmodule;
    default = { };
  };
}
