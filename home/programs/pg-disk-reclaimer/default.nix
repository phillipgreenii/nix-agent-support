{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.pg-disk-reclaimer;
in
{
  options.phillipgreenii.programs.pg-disk-reclaimer = {
    enable = lib.mkEnableOption "pg-disk-reclaimer";
    package = lib.mkPackageOption pkgs "pg-disk-reclaimer" { };

    # bead pg2-9lfsj: build-time aggregation option for the tool's registry,
    # mirroring phillipg-nix-ziprecruiter's `services.localProxy.registrations`
    # precedent (structured submodule data, no ordering requirement, so plain
    # `listOf` concatenation is enough -- unlike status-line-parts, which needs
    # mkBefore/mkOrder/mkAfter banding because render order is user-visible).
    # Any module MAY append entries here, gated on its own feature's
    # `phillipgreenii.programs.<x>.enable` (capability-model skill: an
    # integration fragment gates on a feature flag, never on
    # capabilities.*/bundles.* directly) -- e.g. a generic tool's own feature
    # module contributing its cache-cleanup entry. The materialization below
    # (`xdg.configFile."pg-disk-reclaimer/registry.json"`) is colocated in this
    # same public module (single ownership); only entry DATA that is genuinely
    # ZR-specific stays in the private phillipg-nix-ziprecruiter repo -- this
    # schema discloses nothing ZR-specific.
    registryEntries = lib.mkOption {
      type = lib.types.listOf (
        lib.types.submodule {
          options = {
            id = lib.mkOption {
              type = lib.types.str;
              description = "Unique identifier for this reclaimable area.";
            };
            description = lib.mkOption {
              type = lib.types.str;
              description = "Human-facing description of this area.";
            };
            path = lib.mkOption {
              type = lib.types.str;
              description = ''
                Filesystem path this area occupies. Informational only -- `~` is
                written literally here and shell-expanded at CLI runtime, not by Nix.
              '';
            };
            displayCommand = lib.mkOption {
              type = lib.types.str;
              description = "Shell command run to display this area's current size/state.";
            };
            variants = lib.mkOption {
              type = lib.types.listOf (
                lib.types.submodule {
                  options = {
                    aggressiveness = lib.mkOption {
                      type = lib.types.ints.unsigned;
                      description = "Aggressiveness level; unique within this item's variants.";
                    };
                    variantDescription = lib.mkOption {
                      type = lib.types.str;
                      description = "Human-facing description of what this variant reclaims.";
                    };
                    dryRunCommand = lib.mkOption {
                      type = lib.types.str;
                      description = "Shell command that previews this variant's reclaim without applying it.";
                    };
                    removeCommand = lib.mkOption {
                      type = lib.types.str;
                      description = "Shell command that actually applies this variant's reclaim.";
                    };
                  };
                }
              );
              default = [ ];
              description = ''
                Reclaim variants for this area, in the order the CLI should present
                them. An empty list means informational only -- never reclaimable.
              '';
            };
          };
        }
      );
      default = [ ];
      description = ''
        Reclaimable-area entries for pg-disk-reclaimer's registry, rendered to
        `xdg.configFile."pg-disk-reclaimer/registry.json"`. Any module MAY append
        entries here (a `listOf` merges by concatenation) rather than
        hand-authoring the registry JSON directly. Shape mirrors the tool's own
        schema fixture
        (packages/pg-disk-reclaimer/pg-disk-reclaimer/tests/fixtures/valid.json).
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    programs.tldr.customPages = lib.mkIf config.programs.tldr.enable {
      pg-disk-reclaimer = {
        platform = "common";
        source = "${cfg.package}/share/tldr/pages.common/pg-disk-reclaimer.md";
      };
    };

    # Colocated with the option above (single ownership) -- was previously
    # hand-authored per-consumer (phillipg-nix-ziprecruiter's machine config);
    # every consumer now gets this materialization for free just by setting
    # `enable` and appending to `registryEntries`.
    xdg.configFile."pg-disk-reclaimer/registry.json".source =
      (pkgs.formats.json { }).generate "pg-disk-reclaimer-registry.json"
        cfg.registryEntries;
  };
}
