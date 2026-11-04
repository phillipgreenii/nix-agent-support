{
  config,
  lib,
  pkgs,
  options,
  ...
}:

let
  cfg = config.phillipgreenii.programs.pg-disk-reclaimer;
  isDarwin = pkgs.stdenv.hostPlatform.isDarwin;

  # Read from the `options` argument, never from `config`/`pkgs` (using those to
  # choose attribute NAMES is infinite recursion; see home/programs/pa-monitor).
  hasLaunchdRegistry = options.phillipgreenii.programs ? launchdServices;

  # Directory of the scheduled run's logs. The stdout file is JSON lines and its
  # *.jsonl name matches the default log-source glob
  # ${XDG_STATE_HOME}/pg-disk-reclaimer/*.jsonl (darwin/modules/pg-disk-reclaimer),
  # not the stderr file or a rotated archive.
  autoLogDir = "${config.xdg.stateHome}/pg-disk-reclaimer";

  # Registry items for the largest growers on the Data volume (measured on
  # 2026-11-03). Each `path` exists independently of its contents so the CLI's
  # existence guard works (it does not expand globs); the size commands and
  # variants spell out the globs themselves.
  growerEntries = [
    {
      id = "pg-claude-code-recorder-store";
      description = "pg-claude-code-recorder store: the recorder's own retention command";
      path = "~/.local/share/pg-claude-code-recorder";
      displayCommand = "du -sh ~/.local/share/pg-claude-code-recorder";
      displayTimeoutSeconds = 300;
      sizeKind = "upper_bound";
      sizeMethod = "du-whole-store";
      sizeBasis = "du -sk over the whole recorder store, which also holds live data; the retention command removes only settled Conversations older than the recorder's own remove.after, so it frees at most this much.";
      variants = [
        {
          # Level 3 (deep): a removed Conversation is restorable with the
          # recorder's `extract`, but restoring costs a manual step, so this is
          # not cheap enough for level 2. The recorder refuses to remove anything
          # unless its own remove.enable is set, and it never bypasses its age.
          aggressiveness = 3;
          variantDescription = "recorder retention: remove every settled Conversation older than remove.after (restorable with extract)";
          dryRunCommand = "pg-claude-code-recorder remove --dry-run";
          removeCommand = "pg-claude-code-recorder remove --all-eligible --yes";
        }
      ];
    }
    {
      id = "claude-projects";
      description = "~/.claude/projects: Claude Code session transcripts untouched for 30 days";
      path = "~/.claude/projects";
      displayCommand = "du -sh ~/.claude/projects";
      displayTimeoutSeconds = 300;
      sizeKind = "upper_bound";
      sizeMethod = "du-whole-root";
      sizeBasis = "du -sk over the whole projects directory, which also counts transcripts newer than 30 days; the variant removes only files not modified for 30 days, so it frees at most this much.";
      variants = [
        {
          # Level 4 (costly or irreversible): a deleted transcript cannot be
          # resumed with `claude --resume`; there is no refill. Never part of the
          # scheduled run (levels 1 to 3 only).
          aggressiveness = 4;
          variantDescription = "delete *.jsonl transcripts not modified for 30 days (no undo)";
          dryRunCommand = "find ~/.claude/projects -type f -name '*.jsonl' -mtime +30 | wc -l";
          removeCommand = "find ~/.claude/projects -type f -name '*.jsonl' -mtime +30 -delete";
        }
      ];
    }
    {
      id = "pg-ccaudit-index";
      description = "pg-ccaudit index: files whose newest event is older than 90 days";
      path = "~/.local/share/pg-ccaudit";
      displayCommand = "du -sh ~/.local/share/pg-ccaudit";
      displayTimeoutSeconds = 300;
      sizeCommand = "du -sk ~/.local/share/pg-ccaudit/transcripts.db* 2>/dev/null | awk '{s+=$1} END {print s+0}'";
      sizeKind = "upper_bound";
      sizeMethod = "du-whole-db";
      sizeBasis = "du -sk over transcripts.db and its WAL/SHM files (the gold set and lock file are excluded); the prune keeps every file whose newest event is within 90 days, so it frees at most this much.";
      variants = [
        {
          # Level 4 (costly or irreversible): pruned rows are gone from the index.
          # The index is fed by the recorder's change feed with a cursor kept in the
          # index, so removed history is not re-read, and a recorder resync could only
          # restore what the recorder itself still holds. Retention window: 90 days,
          # the widest look-back of the analysis skills (improvement-retro,
          # tool-error-waste-review) plus about a quarter of margin; see
          # docs/retention.md in packages/pg-ccaudit. VACUUM needs free space about the
          # size of the database, so this is not a low-disk-space emergency tool and
          # is never part of the scheduled run (levels 1 to 3 only).
          aggressiveness = 4;
          variantDescription = "pg-ccaudit prune: drop indexed files whose newest event is older than 90 days, then VACUUM (no undo)";
          dryRunCommand = "pg-ccaudit prune --days 90";
          removeCommand = "pg-ccaudit prune --days 90 --yes";
        }
      ];
    }
    {
      id = "ceta-asks-db";
      description = "claude-extended-tool-approver decision log: rows older than 90 days, sampled into fixtures first";
      path = "~/.local/share/claude-extended-tool-approver";
      displayCommand = "du -sh ~/.local/share/claude-extended-tool-approver";
      displayTimeoutSeconds = 300;
      sizeCommand = "du -sk ~/.local/share/claude-extended-tool-approver/asks.db* 2>/dev/null | awk '{s+=$1} END {print s+0}'";
      sizeKind = "upper_bound";
      sizeMethod = "du-whole-db";
      sizeBasis = "du -sk over asks.db and its WAL/SHM files (the backups directory is excluded); archive keeps every decision row newer than 90 days, so it frees at most this much.";
      variants = [
        {
          # Level 4 (costly or irreversible): the archive command deletes the old
          # decision rows for good; only a representative sample (every annotated
          # miss and match, plus a spread per tool and outcome) survives in the
          # fixtures file it writes first. Retention window: 90 days, enough
          # history for identify-hook-misses and engine A/B replays, which work on
          # recent behaviour, and annotated ground truth survives in the fixtures.
          # VACUUM needs free space about the size of the database. Never part of
          # the scheduled run (levels 1 to 3 only). See
          # packages/claude-extended-tool-approver/docs/retention.md.
          aggressiveness = 4;
          variantDescription = "CETA archive: sample fixtures, delete decision rows older than 90 days, then VACUUM (no undo beyond the fixtures)";
          dryRunCommand = "claude-extended-tool-approver archive --days 90 --dry-run";
          removeCommand = "mkdir -p ~/.local/share/claude-extended-tool-approver/archive-fixtures && claude-extended-tool-approver archive --days 90 --yes --fixtures-out ~/.local/share/claude-extended-tool-approver/archive-fixtures/archive-$(date +%Y-%m-%d).json";
        }
      ];
    }
    {
      id = "pg2-scratch-caches";
      description = "~/.cache/pg2-*-scratch: per-bead scratch directories untouched for 7 days";
      path = "~/.cache";
      displayCommand = "du -sch ~/.cache/pg2-*-scratch | tail -1";
      displayTimeoutSeconds = 300;
      sizeCommand = "du -sk ~/.cache/pg2-*-scratch 2>/dev/null | awk '{s+=$1} END {print s+0}'";
      sizeKind = "upper_bound";
      sizeMethod = "du-all-scratch-dirs";
      sizeBasis = "du -sk summed over every pg2-*-scratch directory, including ones modified in the last 7 days that the variant keeps, so a reclaim frees at most this much.";
      variants = [
        {
          # Level 2 (caches): scratch is by definition disposable and nothing
          # needs it after a week; a deleted file is simply regenerated or lost
          # scratch, which no later step depends on. Directories modified within
          # 7 days are left alone.
          aggressiveness = 2;
          variantDescription = "delete files in pg2-*-scratch directories not modified for 7 days";
          dryRunCommand = "find ~/.cache/pg2-*-scratch -type f -mtime +7 2>/dev/null | wc -l";
          removeCommand = "find ~/.cache/pg2-*-scratch -type f -mtime +7 -delete 2>/dev/null; true";
        }
      ];
    }
    {
      id = "library-caches";
      description = "~/Library/Caches: per-app caches not accessed for 30 days";
      path = "~/Library/Caches";
      displayCommand = "du -sh ~/Library/Caches";
      displayTimeoutSeconds = 300;
      sizeKind = "upper_bound";
      sizeMethod = "du-whole-root";
      sizeBasis = "du -sk over the whole caches directory, including files accessed within 30 days that the variant keeps, so a reclaim frees at most this much.";
      variants = [
        {
          # Level 2 (caches): apps refill their own caches on demand (a slow
          # first launch, a re-download of cached data). Only files not accessed
          # for 30 days are removed, so anything in active use stays.
          aggressiveness = 2;
          variantDescription = "delete cache files not accessed for 30 days";
          dryRunCommand = "find ~/Library/Caches -type f -atime +30 2>/dev/null | wc -l";
          removeCommand = "find ~/Library/Caches -type f -atime +30 -delete 2>/dev/null; true";
        }
      ];
    }
  ];

  # The CLI's schema validator rejects a present-but-null displayTimeoutSeconds,
  # sizeCommand, sizeKind, sizeMethod, sizeBasis or heldSizeCommand (absent
  # means "use the default"), so an unset optional
  # (null) field must be DROPPED from the rendered entry, never written as
  # `null`. Applies to the item level only: variants carry no optional fields.
  dropNullFields = lib.filterAttrs (_name: value: value != null);
  renderedRegistryEntries = map dropNullFields cfg.registryEntries;
in
{
  options.phillipgreenii.programs.pg-disk-reclaimer = {
    enable = lib.mkEnableOption "pg-disk-reclaimer";
    package = lib.mkPackageOption pkgs "pg-disk-reclaimer" { };

    growerEntries.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Contribute the registry items for the largest growers on the Data
        volume: the pg-claude-code-recorder store (through the recorder's own
        retention command only), `~/.claude/projects`, the pg-ccaudit index and
        the claude-extended-tool-approver decision log (each pruned to 90 days,
        level 4), `~/.cache/pg2-*-scratch` and `~/Library/Caches`. Each has a size, a dry-run command and an age
        cutoff.
      '';
    };

    auto = {
      enable = lib.mkEnableOption ''
        a scheduled launchd job (darwin only) that runs `pg-disk-reclaimer auto`:
        it picks aggressiveness 1, 2 or 3 from the free space on the Data
        volume, reclaims, logs one JSON line per run, and logs an ERROR line
        (the alert) when free space is below the lowest threshold. Levels 4 and
        5 are never run by it. Needs phillipgreenii-nix-personal's HM
        `launchdServices` registry
      '';

      thresholds = {
        level1BelowGb = lib.mkOption {
          type = lib.types.ints.positive;
          default = 60;
          description = "Run aggressiveness 1 when the Data volume has less than this many GiB free.";
        };
        level2BelowGb = lib.mkOption {
          type = lib.types.ints.positive;
          default = 45;
          description = "Run aggressiveness 2 when the Data volume has less than this many GiB free.";
        };
        level3BelowGb = lib.mkOption {
          type = lib.types.ints.positive;
          default = 30;
          description = ''
            Run aggressiveness 3 when the Data volume has less than this many GiB
            free, and raise the alert (an ERROR line in the run log). This is the
            lowest threshold. MUST be less than or equal to level2BelowGb, which
            MUST be less than or equal to level1BelowGb.
          '';
        };
      };

      intervalSeconds = lib.mkOption {
        type = lib.types.ints.positive;
        default = 900;
        description = "How often launchd runs the job (StartInterval).";
      };
    };

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
                whitespace-delimited field of the FIRST line is read; later lines
                are ignored). `null` (the default) leaves the key out of the
                registry JSON, so the CLI sizes the item with `du -sk <path>`.
              '';
            };
            sizeKind = lib.mkOption {
              type = lib.types.nullOr (
                lib.types.enum [
                  "exact"
                  "estimate"
                  "upper_bound"
                  "lower_bound"
                ]
              );
              default = null;
              description = ''
                What the number printed by `reclaim` means for this item (the size
                contract): `exact` (the CLI's default when unset), `estimate`,
                `upper_bound` (a reclaim frees at most this much) or `lower_bound`
                (at least this much). A non-exact kind is rendered with a prefix
                and its method, e.g. `~972.8M (estimate: sqlite-closure)`, and the
                total keeps one bucket per kind rather than merging them. A
                non-exact kind REQUIRES `sizeBasis`; the CLI's `validate`
                rejects it otherwise. For an item with several variants use the
                weakest case any variant needs. `null` (the default) leaves the
                key out of the registry JSON.
              '';
            };
            sizeMethod = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              description = ''
                Short tag naming how the size is derived (the CLI prints it for a
                non-exact kind, e.g. `sqlite-closure`). `null` (the default)
                leaves the key out, and the CLI assumes `du`.
              '';
            };
            sizeBasis = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              description = ''
                Documentation only, never printed by `reclaim`: what the size
                measures, why the approximation is acceptable, and what to
                revisit. REQUIRED (non-null) whenever `sizeKind` is not `exact`.
                `null` (the default) leaves the key out of the registry JSON.
              '';
            };
            heldSizeCommand = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              description = ''
                Shell command `reclaim` runs to size data that is removable but
                for a safety guard -- e.g. worktrees skipped because they are
                dirty or locked ("not stale" and "protected" data is NOT held).
                Same contract as `sizeCommand`: a bare integer count of KiB on
                the first line. It runs only after the main size succeeded, under
                its own timeout, so a held failure never voids the main size.
                Reported as `<id>: held, not removable: <size>` and as a separate
                held total, never added into the reclaimable total. `null` (the
                default) leaves the key out of the registry JSON.
              '';
            };
            variants = lib.mkOption {
              type = lib.types.listOf (
                lib.types.submodule {
                  options = {
                    aggressiveness = lib.mkOption {
                      type = lib.types.ints.unsigned;
                      description = ''
                        Aggressiveness level (0 to 5); unique within this item's variants.
                        `reclaim --aggressiveness N` selects variants at level N or below,
                        and a level of 4 or above asks for interactive confirmation under
                        `--apply`: ONE prompt per run, at most once, before anything runs,
                        and that one answer gates the whole run (declining exits 2, no
                        terminal to ask on exits 3, both with nothing run; no command
                        prompts mid-run; `--apply --yes` or `--apply --no` answers it without asking). The authoritative definition of each level, and the rule
                        that a non-obvious level needs a justifying comment, is
                        packages/pg-disk-reclaimer/pg-disk-reclaimer/docs/aggressiveness-scale.md
                        in the phillipgreenii-nix-agent-support repository.
                      '';
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

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
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
    })

    {
      phillipgreenii.programs.pg-disk-reclaimer.registryEntries = lib.mkIf (
        cfg.enable && cfg.growerEntries.enable
      ) growerEntries;
    }

    (lib.mkIf (cfg.enable && cfg.auto.enable) {
      assertions = [
        {
          assertion =
            cfg.auto.thresholds.level1BelowGb >= cfg.auto.thresholds.level2BelowGb
            && cfg.auto.thresholds.level2BelowGb >= cfg.auto.thresholds.level3BelowGb;
          message = "phillipgreenii.programs.pg-disk-reclaimer.auto.thresholds must satisfy level1BelowGb >= level2BelowGb >= level3BelowGb.";
        }
        {
          assertion = !isDarwin || hasLaunchdRegistry;
          message = "phillipgreenii.programs.pg-disk-reclaimer.auto.enable on darwin needs phillipgreenii.programs.launchdServices, which phillipgreenii-nix-personal's home module declares (personal ADR 0055); import it alongside this module.";
        }
      ];
    })

    # LaunchAgent registration via the HM-scoped launchdServices registry (the
    # tmp-sweep pattern): gated on the flag and the platform at ENTRY level (mkIf)
    # and on the option's declaration at attribute level (optionalAttrs).
    (lib.optionalAttrs hasLaunchdRegistry {
      phillipgreenii.programs.launchdServices.userAgents.pg-disk-reclaimer-auto =
        lib.mkIf (cfg.enable && cfg.auto.enable && isDarwin)
          {
            label = "com.phillipg.pg-disk-reclaimer-auto";
            # --apply is safe here: `auto` only ever chooses levels 1 to 3, and the
            # confirmation gate is level 4.
            script = ''
              exec ${cfg.package}/bin/pg-disk-reclaimer auto --apply \
                --below1-gb ${toString cfg.auto.thresholds.level1BelowGb} \
                --below2-gb ${toString cfg.auto.thresholds.level2BelowGb} \
                --below3-gb ${toString cfg.auto.thresholds.level3BelowGb}
            '';
            runAtLoad = true;
            keepAlive = false;
            # Not a service that stays running: the activation health check would
            # misread the gap between runs as a failure.
            healthCheck = false;
            serviceConfig = {
              StartInterval = cfg.auto.intervalSeconds;
              ProcessType = "Background";
              StandardOutPath = "${autoLogDir}/auto.jsonl";
              StandardErrorPath = "${autoLogDir}/launchd-stderr.log";
              EnvironmentVariables = {
                HOME = config.home.homeDirectory;
                XDG_CONFIG_HOME = config.xdg.configHome;
                # launchd's default PATH lacks the tools the registry commands
                # call (git, go, nix, brew, the recorder CLI).
                PATH = lib.concatStringsSep ":" [
                  "${config.home.profileDirectory}/bin"
                  "/etc/profiles/per-user/${config.home.username}/bin"
                  "/run/current-system/sw/bin"
                  "/nix/var/nix/profiles/default/bin"
                  "/opt/homebrew/bin"
                  "/usr/bin"
                  "/bin"
                ];
              };
            };
          };
    })
  ];
}
