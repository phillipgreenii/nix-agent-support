{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pg-desk;

  renderedRepos = map (
    r:
    lib.filterAttrs (_: v: v != null) {
      inherit (r) remote;
      beads_dir = r.beadsDir;
    }
  ) cfg.repos;
  renderedAgents = map (
    a:
    lib.filterAttrs (_: v: v != null) {
      inherit (a) login;
      approval_regex = a.approvalRegex;
      inherit (a) policy;
    }
  ) cfg.agents;
  renderedUrgency =
    if cfg.urgency == null then
      null
    else
      lib.filterAttrs (_: v: v != [ ] && v != { }) {
        inherit (cfg.urgency) labels keywords thresholds;
      };
  renderedServe = lib.filterAttrs (_: v: v != null) {
    inherit (cfg.serve) addr log;
  };
  renderedOpen = lib.filterAttrs (_: v: v != null) {
    chrome_bin = cfg.open.chromeBin;
  };

  # The complete rendered document: every section-7.8 config key, each
  # included only when this module was actually given something for it —
  # mirrors home/programs/pg-connector's own "omit rather than render
  # null/empty" convention. sync.mode is the one exception to "only when
  # given something": it is a mandatory enum with its own default ("off",
  # matching packages/pg-desk/internal/sync's own fallback when the key is
  # absent), so it is never null/empty and is rendered unconditionally
  # below.
  renderedConfig = {
    self_login = cfg.selfLogin;
  }
  // lib.optionalAttrs (cfg.teamMembers != [ ]) { team_members = cfg.teamMembers; }
  // lib.optionalAttrs (cfg.watchLabels != [ ]) { watch_labels = cfg.watchLabels; }
  // {
    repos = renderedRepos;
  }
  // lib.optionalAttrs (cfg.ticketPatterns != [ ]) { ticket_patterns = cfg.ticketPatterns; }
  // lib.optionalAttrs (renderedAgents != [ ]) { agents = renderedAgents; }
  // lib.optionalAttrs (cfg.approverAllowlist != [ ]) { approver_allowlist = cfg.approverAllowlist; }
  // lib.optionalAttrs (cfg.verdictGenerations != [ ]) {
    verdict_generations = cfg.verdictGenerations;
  }
  // lib.optionalAttrs (cfg.checkInterpreters != [ ]) { check_interpreters = cfg.checkInterpreters; }
  // lib.optionalAttrs (cfg.ciOnlyAttemptsThreshold != null) {
    ci_only_attempts_threshold = cfg.ciOnlyAttemptsThreshold;
  }
  // lib.optionalAttrs (cfg.jira != null) { inherit (cfg) jira; }
  // lib.optionalAttrs (cfg.categoryVocabulary != { }) {
    category_vocabulary = cfg.categoryVocabulary;
  }
  // lib.optionalAttrs (renderedUrgency != null) { urgency = renderedUrgency; }
  // lib.optionalAttrs (cfg.agentTrackerBackend != null) {
    agent_tracker_backend = cfg.agentTrackerBackend;
  }
  // lib.optionalAttrs (cfg.actor != null) { inherit (cfg) actor; }
  // {
    sync = { inherit (cfg.sync) mode; };
  }
  // lib.optionalAttrs (cfg.heartbeatPeriod != null) { heartbeat_period = cfg.heartbeatPeriod; }
  // lib.optionalAttrs (cfg.staleAfter != null) { stale_after = cfg.staleAfter; }
  // lib.optionalAttrs (renderedServe != { }) { serve = renderedServe; }
  // lib.optionalAttrs (renderedOpen != { }) { open = renderedOpen; };
in
{
  # Renders pg-desk's config.yaml from the docket design's section 7.8 key
  # table (docs/superpowers/specs/2026-09-09-pg-desk-and-connector-discovery-design.md
  # lines 997-1011; packages/pg-desk/internal/config/config.go's own doc
  # comment lists the same 21 keys) — all 21 keys, including `sync.mode`
  # (the Go side's stub landed in Phase 10 [Binding decisions]; this option
  # was added in the Phase 11 cutover flip, pg2-2j5ac.36.1). Mirrors
  # home/programs/pg-connector's own config-rendering shape (typed options
  # -> `pkgs.formats.yaml {}`.generate -> xdg.configFile), one repo/tool
  # over.
  #
  # No ZR-specific identifiers appear here — every value is config, and
  # `phillipg-nix-ziprecruiter` (packet 11) supplies its own deployment
  # values, including the Phase 11 cutover's `sync.mode = "apply"`.
  options.phillipgreenii.programs.pg-desk = {
    enable = lib.mkEnableOption "pg-desk (operator triage desk for PR/issue work surfaced through pg-connector)";
    package = lib.mkPackageOption pkgs "pg-desk" { };

    # self_login/team_members [Binding decisions]: NOT freshly-defined
    # options carrying their own default — plain pass-throughs. Identity
    # and team membership are "written once in nix and rendered into every
    # place that needs them" [design: Configuration] — the pr-github
    # mine/team expressions and pg-router's own [pool].self_login already
    # consume that SAME Phase-7 shared source. This module's job is only to
    # expose the options packet 11 (ZR wiring) feeds from that same
    # source; it MUST NOT invent an independent default value of its own
    # (never a second copy of the shared identity).
    selfLogin = lib.mkOption {
      type = lib.types.str;
      description = "config.yaml's self_login. No default — deployment-specific (see the option group's own doc comment above).";
    };
    teamMembers = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "config.yaml's team_members. See selfLogin's doc comment: the SAME shared Phase-7 identity source, never a second copy.";
    };

    watchLabels = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "config.yaml's watch_labels.";
    };

    repos = lib.mkOption {
      type = lib.types.listOf (
        lib.types.submodule {
          options = {
            remote = lib.mkOption {
              type = lib.types.str;
              description = "config.yaml's repos[].remote.";
            };
            beadsDir = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              description = "config.yaml's repos[].beads_dir.";
            };
          };
        }
      );
      default = [ ];
      description = ''
        config.yaml's repos[] list. Phase 9 supports exactly one entry
        (docs/behavior/pg-desk/README.md's "Scope" section); this module
        does not itself enforce that (packages/pg-desk's own config
        validation does).
      '';
    };

    ticketPatterns = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "config.yaml's ticket_patterns (unconsumed by pg-desk until the cross-reference step, Phase 13).";
    };

    agents = lib.mkOption {
      type = lib.types.listOf (
        lib.types.submodule {
          options = {
            login = lib.mkOption {
              type = lib.types.str;
              description = "config.yaml's agents[].login.";
            };
            approvalRegex = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              description = "config.yaml's agents[].approval_regex.";
            };
            policy = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              description = "config.yaml's agents[].policy.";
            };
          };
        }
      );
      default = [ ];
      description = "config.yaml's agents[] list.";
    };

    approverAllowlist = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "config.yaml's approver_allowlist.";
    };

    # verdict_generations/check_interpreters render opaquely (each entry's
    # own shape is pg-desk's, not this module's, to know) — mirrors
    # home/programs/pg-connector's own `backends` opaque-passthrough
    # convention for the same reason.
    verdictGenerations = lib.mkOption {
      type = lib.types.listOf (lib.types.attrsOf lib.types.anything);
      default = [ ];
      description = ''
        config.yaml's verdict_generations list, rendered verbatim (each
        entry: id, body_marker, findings_patterns, authority_patterns).
        Unconsumed by this packet's own pg-desk code.
      '';
    };
    checkInterpreters = lib.mkOption {
      type = lib.types.listOf (lib.types.attrsOf lib.types.anything);
      default = [ ];
      description = ''
        config.yaml's check_interpreters list, rendered verbatim (each
        entry: patterns, type). Unconsumed by this packet's own pg-desk
        code.
      '';
    };

    ciOnlyAttemptsThreshold = lib.mkOption {
      type = lib.types.nullOr lib.types.int;
      default = null;
      description = "config.yaml's ci_only_attempts_threshold.";
    };

    jira = lib.mkOption {
      type = lib.types.nullOr (lib.types.attrsOf lib.types.anything);
      default = null;
      description = ''
        config.yaml's jira block (high_priority_values, incident_labels,
        incident_issue_types), rendered verbatim. Unconsumed until Phase
        13; carries no organization identifiers by construction (an opaque
        passthrough of whatever this option is given).
      '';
    };

    categoryVocabulary = lib.mkOption {
      type = lib.types.attrsOf (lib.types.listOf lib.types.str);
      default = { };
      description = "config.yaml's category_vocabulary map (category name -> keyword patterns).";
    };

    urgency = lib.mkOption {
      type = lib.types.nullOr (
        lib.types.submodule {
          options = {
            labels = lib.mkOption {
              type = lib.types.listOf lib.types.str;
              default = [ ];
              description = "config.yaml's urgency.labels.";
            };
            keywords = lib.mkOption {
              type = lib.types.listOf lib.types.str;
              default = [ ];
              description = "config.yaml's urgency.keywords.";
            };
            thresholds = lib.mkOption {
              type = lib.types.attrsOf lib.types.int;
              default = { };
              description = "config.yaml's urgency.thresholds map (level name -> numeric cutoff).";
            };
          };
        }
      );
      default = null;
      description = "config.yaml's urgency block.";
    };

    agentTrackerBackend = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "config.yaml's agent_tracker_backend. Unconsumed by pg-desk until Phase 10.";
    };

    actor = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "config.yaml's actor — the identity pg-desk attributes its own writes to.";
    };

    sync = {
      mode = lib.mkOption {
        type = lib.types.enum [
          "off"
          "plan"
          "apply"
        ];
        default = "off";
        description = ''
          config.yaml's sync.mode (design section 7.5, D17). `off` runs no
          sync. `plan` runs the full adoption/rule logic and records every
          intended bead write without calling `pg-connector issue` — the
          parity check for sync. `apply` performs the writes; switched on
          only at the phase 11 cutover flip. Matches
          packages/pg-desk/internal/sync's own fallback to "off" when this
          key is absent from config.yaml.
        '';
      };
    };

    heartbeatPeriod = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "config.yaml's heartbeat_period (a Go time.ParseDuration string, e.g. \"5m\").";
    };
    staleAfter = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "config.yaml's stale_after (a Go time.ParseDuration string, e.g. \"30m\").";
    };

    serve = {
      addr = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          config.yaml's serve.addr (host:port). Left unset, pg-desk itself
          has no built-in fallback for this key — `services.pg-desk-serve`
          (the darwin launchd module) documents today's legacy default
          (127.0.0.1:9818) as the value a consumer typically sets here.
          Independent of that darwin module's own `soak.port`, which
          overrides the EFFECTIVE listen address at the CLI (`--port`)
          without touching this rendered value at all [Binding decisions:
          "these are two distinct options ... do not collapse them into
          one"].
        '';
      };
      log = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          config.yaml's serve.log. Left unset, pg-desk's own `serve`
          command falls back to `~/Library/Logs/pg-desk-serve.log`
          (cmd/pg-desk/serve.go's defaultServeLogPathSuffix).
        '';
      };
    };

    open = {
      chromeBin = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          config.yaml's open.chrome_bin — the ONE operator-configured
          browser binary the composition rule (D10) permits pg-desk to
          exec besides pg-connector.
        '';
      };
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    xdg.configFile."pg-desk/config.yaml".source =
      (pkgs.formats.yaml { }).generate "pg-desk-config.yaml"
        renderedConfig;
  };
}
