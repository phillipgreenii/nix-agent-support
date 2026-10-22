# Shared HM-side eval harness for home/programs/pg-router-ccpool-handler
# (beads pg2-pteab, pg2-12gr4.8.12): a hand-rolled `lib.evalModules` over the
# module with exactly the options it reads/writes stubbed. Shared by
# checks.<system>.test-pg-router-ccpool-handler-module and
# checks.<system>.pg-router-ccpool-handler-spec-fixtures, so both evaluate the
# module the same way. Returns `{ evalHM }`, where
# `evalHM <handler option attrs>` is the evaluated `config`.
{
  lib,
  pkgs,
  llm-agents,
}:
let
  # The module's default `spec.exec` reads `pkgs.llm-agentsPkgs.claude-code`,
  # which the flake's check `pkgs` lacks (a real consumer gets it from the
  # overlay); supply it from the llm-agents input, as flake.nix does for beads.
  hmPkgs = pkgs // {
    llm-agentsPkgs =
      pkgs.llm-agentsPkgs or {
        inherit (llm-agents.packages.${pkgs.stdenv.hostPlatform.system}) claude-code;
      };
  };

  hmAssertionSubmodule = lib.types.submodule {
    options = {
      assertion = lib.mkOption { type = lib.types.bool; };
      message = lib.mkOption { type = lib.types.str; };
    };
  };

  # hmLib (pg2-1p4yp): the module's new `pool.enable`
  # activation step calls `lib.hm.dag.entryAfter`
  # (home-manager's extended lib, absent from the bare
  # nixpkgs `lib` this hand-rolled `lib.evalModules` call
  # would otherwise pass) -- stub it to return the raw
  # activation text, exactly like
  # test-claude-settings-activation-marketplace-add's own
  # `hmLib` does, so the generated script can be inspected
  # without a full HM harness.
  hmLib = lib // {
    hm = (lib.hm or { }) // {
      dag = (lib.hm.dag or { }) // {
        entryAfter = _deps: text: text;
      };
    };
  };

  # HM-side eval: stub exactly what
  # home/programs/pg-router-ccpool-handler/default.nix
  # reads/writes -- home.packages, home.homeDirectory (the
  # `pool.dir` option's default),
  # home.activation (the `pool.enable` bootstrap step,
  # pg2-1p4yp), systemd.user.{services,timers}, assertions
  # (mirroring test-pg-router-module's own evalHM).
  # `phillipgreenii.programs.ccpool.enable` (bead pg2-mr0sl):
  # stubbed `true` -- this module's OWN "ccpool.enable
  # required" assertion reads it, and unlike before this
  # bead (when nothing here ever forced `.assertions` at
  # all), the new `firedPoolMetricsAssertion`/
  # `firedMissingDirAssertion` bindings below use
  # `lib.findFirst` over the WHOLE assertions list, which
  # evaluates every earlier assertion's own `.assertion`
  # field along the way -- an unstubbed option here would
  # throw "attribute missing" the first time that happens,
  # not merely stay unforced.
  evalHM =
    ccpoolHandler:
    (lib.evalModules {
      specialArgs = {
        pkgs = hmPkgs;
        lib = hmLib;
      };
      modules = [
        ../home/programs/pg-router-ccpool-handler/default.nix
        (
          { lib, ... }:
          {
            options = {
              home = {
                packages = lib.mkOption {
                  type = lib.types.listOf lib.types.package;
                  default = [ ];
                };
                homeDirectory = lib.mkOption {
                  type = lib.types.str;
                  default = "/home/tester";
                };
                activation = lib.mkOption {
                  type = lib.types.attrsOf lib.types.anything;
                  default = { };
                };
              };
              systemd.user.services = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
              systemd.user.timers = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
              assertions = lib.mkOption {
                type = lib.types.listOf hmAssertionSubmodule;
                default = [ ];
              };
              # pg2-4roho decision item 4: this module now
              # appends to pg-disk-reclaimer's own registry
              # option -- stubbed here with the SAME shape
              # `home/programs/pg-disk-reclaimer/default.nix`
              # itself declares (not the real module, mirroring
              # this harness's own convention of hand-stubbing
              # every bare option this module reads/writes
              # rather than importing upstream modules), so a
              # real type mismatch in the appended entry is
              # still caught here.
              phillipgreenii.programs.ccpool.enable = lib.mkOption {
                type = lib.types.bool;
                default = true;
              };
              phillipgreenii.programs.pg-disk-reclaimer.registryEntries = lib.mkOption {
                type = lib.types.listOf (
                  lib.types.submodule {
                    options = {
                      id = lib.mkOption { type = lib.types.str; };
                      description = lib.mkOption { type = lib.types.str; };
                      path = lib.mkOption { type = lib.types.str; };
                      displayCommand = lib.mkOption { type = lib.types.str; };
                      displayTimeoutSeconds = lib.mkOption {
                        type = lib.types.nullOr lib.types.ints.positive;
                        default = null;
                      };
                      sizeCommand = lib.mkOption {
                        type = lib.types.nullOr lib.types.str;
                        default = null;
                      };
                      sizeKind = lib.mkOption {
                        type = lib.types.nullOr lib.types.str;
                        default = null;
                      };
                      sizeMethod = lib.mkOption {
                        type = lib.types.nullOr lib.types.str;
                        default = null;
                      };
                      sizeBasis = lib.mkOption {
                        type = lib.types.nullOr lib.types.str;
                        default = null;
                      };
                      heldSizeCommand = lib.mkOption {
                        type = lib.types.nullOr lib.types.str;
                        default = null;
                      };
                      variants = lib.mkOption {
                        type = lib.types.listOf (
                          lib.types.submodule {
                            options = {
                              aggressiveness = lib.mkOption { type = lib.types.ints.unsigned; };
                              variantDescription = lib.mkOption { type = lib.types.str; };
                              dryRunCommand = lib.mkOption { type = lib.types.str; };
                              removeCommand = lib.mkOption { type = lib.types.str; };
                            };
                          }
                        );
                        default = [ ];
                      };
                    };
                  }
                );
                default = [ ];
              };
            };
          }
        )
        { phillipgreenii.programs.pg-router-ccpool-handler = ccpoolHandler; }
      ];
    }).config;
in
{
  inherit evalHM;
  # What a spec's default `exec` resolves to.
  claudeBin = "${hmPkgs.llm-agentsPkgs.claude-code}/bin/claude";
}
