{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.phillipgreenii.programs.pg-disk-reclaimer;

  # The CLI's schema validator rejects a present-but-null displayTimeoutSeconds
  # or sizeCommand (absent means "use the default"), so an unset optional
  # (null) field must be DROPPED from the rendered entry, never written as
  # `null`. Applies to the item level only: variants carry no optional fields.
  dropNullFields = lib.filterAttrs (_name: value: value != null);
  renderedRegistryEntries = map dropNullFields cfg.registryEntries;
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
            displayTimeoutSeconds = lib.mkOption {
              type = lib.types.nullOr lib.types.ints.positive;
              default = null;
              description = ''
                Per-item ceiling, in wall-clock seconds, for running this item's
                `displayCommand` (`list`) and for sizing it in `reclaim`. `null`
                (the default) leaves the key out of the registry JSON, so the CLI
                applies its global ceilings (`PGDR_DISPLAY_TIMEOUT_SECONDS` for
                `list`, `PGDR_SIZE_TIMEOUT_SECONDS` for `reclaim`). Set it on an
                item whose size genuinely takes longer, such as a very large cache.
              '';
            };
            sizeCommand = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              description = ''
                Shell command `reclaim` runs to size this item instead of
                `du -sk <path>` -- for an item where `du` over `path` is not what
                a reclaim frees, or is too slow. Its first output line MUST start
                with the reclaimable size as an integer count of KiB (the first
                whitespace-delimited field is read). `null` (the default) leaves
                the key out of the registry JSON, so the CLI sizes the item with
                `du -sk <path>`.
              '';
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
        renderedRegistryEntries;
  };
}
