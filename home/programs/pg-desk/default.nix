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
  # links (bead pg2-apuyx): each key only when set.
  renderedLinks = lib.filterAttrs (_: v: v != null) {
    issue_url_template = cfg.links.issueUrlTemplate;
  };
  # Entity-change-flow keys (watch, sweep, hydration, change_log_retention,
  # consumer_stale_after): each key, and each block, only when the module was
  # given something for it, so an existing consumer's config.yaml is
  # byte-identical. The names are the loader's own
  # (packages/pg-desk/internal/config/config.go).
  renderedWatchThread = lib.filterAttrs (_: v: v != null && v != [ ]) {
    queries = cfg.watch.thread.queries;
    active_window = cfg.watch.thread.activeWindow;
  };
  # watch.issue.queries and watch.issue.backends, derived from the SAME
  # declaration as the connector registry's query blocks (INV-CONFIG-6 and -7),
  # so a deployment declares a watched query once. A query is watched exactly
  # when written in the object form with `watched = true` under an ISSUE backend
  # instance's backends.<name>.queries; the shorthand forms (a bare string, a
  # list) are unwatched. A backend registered under connector.pr or connector.ci
  # is not an issue backend (its watched queries feed the pr flow), so it
  # contributes nothing here. Nothing is derived while the connector module is
  # disabled.
  connectorCfg = config.phillipgreenii.programs.pg-connector;
  registeredName = e: if builtins.isString e then e else e.name;
  nonIssueInstances = map registeredName (connectorCfg.connector.pr ++ connectorCfg.connector.ci);
  watchedQueryNames =
    instance:
    lib.attrNames (
      lib.filterAttrs (_: q: builtins.isAttrs q && (q.watched or false) == true) (
        (connectorCfg.backends.${instance} or { }).queries or { }
      )
    );
  derivedWatchPairs = lib.optionals connectorCfg.enable (
    lib.concatMap (
      instance:
      map (query: {
        inherit query instance;
      }) (watchedQueryNames instance)
    ) (lib.filter (n: !(lib.elem n nonIssueInstances)) (lib.attrNames connectorCfg.backends))
  );
  derivedWatchBackends = lib.listToAttrs (
    map (p: {
      name = p.query;
      value = {
        inherit (p) instance;
        owns_changes = connectorCfg.ownsChanges.${p.instance} or false;
      };
    }) derivedWatchPairs
  );
  # Exact-watched narrowing (INV-CONFIG-7): with the connector module enabled
  # the rendered list is EXACTLY the derived watched names, so it cannot drift
  # from the backend's query block. A manual entry that is not one of them is
  # rejected by an assertion below (never silently dropped, which would hide
  # the missing `watched = true`); the manual list is rendered as written only
  # while the connector module is disabled, where nothing is derived.
  nonDerivedManualQueries = lib.optionals connectorCfg.enable (
    lib.filter (q: !(derivedWatchBackends ? ${q})) cfg.watch.issue.queries
  );
  watchIssueQueries =
    if connectorCfg.enable then
      lib.attrNames derivedWatchBackends
    else
      lib.unique cfg.watch.issue.queries;

  renderedWatch = lib.filterAttrs (_: v: v != { }) {
    issue =
      lib.optionalAttrs (watchIssueQueries != [ ]) { queries = watchIssueQueries; }
      // lib.optionalAttrs (derivedWatchBackends != { }) { backends = derivedWatchBackends; };
    thread = renderedWatchThread;
  };
  renderedSweep = lib.filterAttrs (_: v: v != null) {
    max_age = cfg.sweep.maxAge;
    max_per_poll = cfg.sweep.maxPerPoll;
    reconcile_age = cfg.sweep.reconcileAge;
  };
  renderedHydration = lib.filterAttrs (_: v: v != null) {
    max_per_poll = cfg.hydration.maxPerPoll;
    read_issue_deps = cfg.hydration.readIssueDeps;
    ingest_failed_max_age = cfg.hydration.ingestFailedMaxAge;
  };

  # Focus keys (focus.*, bead_id_pattern): each key only when set, and the
  # focus block only when any key in it is set, so an existing consumer's
  # config.yaml is byte-identical. The names are the loader's own
  # (packages/pg-desk/internal/config/config.go, FocusConfig).
  renderedFocus = lib.filterAttrs (_: v: v != null) {
    time_zone = cfg.focus.timeZone;
    coverage_backlog_max = cfg.focus.coverageBacklogMax;
    pending_gate_age = cfg.focus.pendingGateAge;
    operator_identities = cfg.focus.operatorIdentities;
    priority_map = cfg.focus.priorityMap;
    suppressed_kind = cfg.focus.suppressedKind;
  };

  # The only attention.ordering.ties value pg-desk's config loader accepts
  # (besides unset). MUST equal AttentionTiesDefault in
  # packages/pg-desk/internal/config/config.go; test-pg-desk-module greps that
  # file for this exact string, so drift fails the check.
  attentionTiesDefault = "severity descending, then group size descending, then entity id";

  # attention (bead pg2-xaqag): rendered only when the option is set. Within
  # the block each rule kind keeps only the keys that were given (a null
  # enabled/severity/staleAfterDays falls through to the rule's built-in default), and
  # ordering is rendered only when ties is set.
  renderedAttention =
    if cfg.attention == null then
      null
    else
      let
        renderedRules = lib.mapAttrs (
          _: r:
          lib.filterAttrs (_: v: v != null) {
            inherit (r) enabled severity;
            stale_after_days = r.staleAfterDays;
            due_soon_days = r.dueSoonDays;
          }
        ) cfg.attention.rules;
        renderedOrdering = lib.filterAttrs (_: v: v != null) {
          inherit (cfg.attention.ordering) ties;
        };
      in
      lib.optionalAttrs (renderedRules != { }) { rules = renderedRules; }
      // lib.optionalAttrs (renderedOrdering != { }) { ordering = renderedOrdering; };

  # The complete rendered document: every section-7.8 config key, each
  # included only when this module was actually given something for it —
  # mirrors home/programs/pg-connector's own "omit rather than render
  # null/empty" convention. pr_lane.mode is the one exception to "only when
  # given something": it is a mandatory enum with its own default ("off"), so
  # it is never null/empty and is rendered unconditionally below.
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
  // lib.optionalAttrs (cfg.reviewExemptChecks != [ ]) {
    review_exempt_checks = cfg.reviewExemptChecks;
  }
  // lib.optionalAttrs (cfg.areaLabels != [ ]) { area_labels = cfg.areaLabels; }
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
    # pr_lane.mode (PR lane refactor, P0b): a mandatory enum with its own
    # default ("off", which is also what pg-desk reads for an absent or invalid
    # value), so it is rendered unconditionally.
    pr_lane = {
      inherit (cfg.prLane) mode;
    };
  }
  // lib.optionalAttrs (cfg.heartbeatPeriod != null) { heartbeat_period = cfg.heartbeatPeriod; }
  // lib.optionalAttrs (cfg.staleAfter != null) { stale_after = cfg.staleAfter; }
  // lib.optionalAttrs (renderedServe != { }) { serve = renderedServe; }
  // lib.optionalAttrs (renderedOpen != { }) { open = renderedOpen; }
  // lib.optionalAttrs (renderedLinks != { }) { links = renderedLinks; }
  // lib.optionalAttrs (renderedWatch != { }) { watch = renderedWatch; }
  // lib.optionalAttrs (renderedSweep != { }) { sweep = renderedSweep; }
  // lib.optionalAttrs (renderedHydration != { }) { hydration = renderedHydration; }
  // lib.optionalAttrs (renderedFocus != { }) { focus = renderedFocus; }
  // lib.optionalAttrs (cfg.beadIdPattern != null) { bead_id_pattern = cfg.beadIdPattern; }
  // lib.optionalAttrs (cfg.changeLogRetention != null) {
    change_log_retention = cfg.changeLogRetention;
  }
  // lib.optionalAttrs (cfg.consumerStaleAfter != null) {
    consumer_stale_after = cfg.consumerStaleAfter;
  }
  // lib.optionalAttrs (renderedAttention != null && renderedAttention != { }) {
    attention = renderedAttention;
  };
in
{
  # Renders pg-desk's config.yaml from the docket design's section 7.8 key
  # table (docs/superpowers/specs/2026-09-09-pg-desk-and-connector-discovery-design.md
  # lines 997-1011; packages/pg-desk/internal/config/config.go's own doc
  # comment lists the same 21 keys; the sync: block was removed with the
  # sync stage, PR lane refactor packet P9a-2). Mirrors
  # home/programs/pg-connector's own config-rendering shape (typed options
  # -> `pkgs.formats.yaml {}`.generate -> xdg.configFile), one repo/tool
  # over.
  #
  # No ZR-specific identifiers appear here — every value is config, and
  # `phillipg-nix-ziprecruiter` (packet 11) supplies its own deployment
  # values.
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

    reviewExemptChecks = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = ''
        config.yaml's review_exempt_checks: CI JOB names (matched exactly,
        case-sensitive) whose failure alone does not make a PR unreviewable.
        When every failed job of every failed run on the PR's head is in
        this list, the PR is treated as reviewable while its CI state still
        reads failed. A failed run whose job results were not fetched is
        never exempt. Empty (the default) exempts nothing.
      '';
    };

    areaLabels = lib.mkOption {
      type = lib.types.listOf (
        lib.types.submodule {
          options = {
            pattern = lib.mkOption {
              type = lib.types.str;
              description = "Go (RE2) regexp searched in the PR field.";
            };
            field = lib.mkOption {
              type = lib.types.enum [
                "title"
                "branch"
              ];
              default = "title";
              description = "PR field the pattern is searched in.";
            };
            labels = lib.mkOption {
              type = lib.types.listOf lib.types.str;
              description = "Labels added to a matching PR's merge-request bead and its children.";
            };
          };
        }
      );
      default = [ ];
      description = ''
        config.yaml's area_labels: rules deriving area labels for the
        merge-request bead from the PR title or branch; its review-pr and
        process-feedback children copy them. Sync only ever adds these
        labels. Empty (the default) labels nothing. The vocabulary is
        deployment-specific and supplied by the consuming flake.
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
        incident_issue_types, in_progress_statuses, done_statuses), rendered verbatim;
        in_progress_statuses defaults to "In Progress"; done_statuses defaults to
        Done, Closed, Resolved, Cancelled and Canceled (due-date rules skip those). Both lists are only
        a fallback for an issue whose stored facts carry no tracker status category; a Jira status
        category, when present, decides instead. Carries no organization identifiers by construction (an opaque
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

    # pr_lane.mode (PR lane refactor, P0b): the lane mode the deployment gave the
    # PR lane's roles. It is the one source of the lane mode that pg-desk can
    # read (the router's per-role JSON is not readable by pg-desk): pg-desk
    # exports it as the pg_desk_pr_lane_mode gauge and prints it in the pr lane
    # line of status and doctor. The deployment sets it beside the role's own
    # mode; nothing here changes what any role does.
    prLane.mode = lib.mkOption {
      type = lib.types.enum [
        "off"
        "plan"
        "apply"
      ];
      default = "off";
      description = ''
        config.yaml's pr_lane.mode: the PR lane's mode (`off`, `plan` or
        `apply`), rendered as pr_lane.mode. It only REPORTS the mode (status,
        doctor and the pg_desk_pr_lane_mode gauge); it does not switch the
        lane. A missing or invalid value reads as `off` in pg-desk itself.
      '';
    };

    # Entity-change-flow keys (watch, sweep, hydration, change_log_retention,
    # consumer_stale_after). All optional: an unset or empty option renders
    # nothing, so pg-desk's own defaults apply. Duration values are the
    # loader's duration strings (a Go time.ParseDuration string with an
    # optional day component, such as "7d", "6h" or "30m").
    watch = {
      pr.queries = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = ''
          RETIRED (PR lane refactor, packet P9a-3): pg-desk no longer reads
          watch.pr.queries (the daemon is the PR feed), so this option renders
          nothing. It stays only so a consuming flake that still sets it
          evaluates; remove the entry from the consuming flake.
        '';
      };
      issue.queries = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = ''
          config.yaml's watch.issue.queries: the named pg-connector queries
          whose results form the watched set of issues. With
          `phillipgreenii.programs.pg-connector` enabled, the names derived
          from every issue backend's queries written as
          `{ expr = ...; watched = true; }` are rendered FIRST (with a
          watch.issue.backends entry each, naming the backend instance and its
          `ownsChanges`), and NOTHING else: the rendered list is exactly the
          queries marked watched, so it cannot drift from the backend's query
          block. A name listed here that is not one of those derived names is
          rejected by an assertion (declare it once, as
          `{ expr = ...; watched = true; }` under the backend's queries). With
          the pg-connector module disabled nothing is derived and this list
          renders as written. Empty with nothing derived (the default)
          renders no watch.issue block.
        '';
      };
      thread = {
        queries = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = [ ];
          description = ''
            config.yaml's watch.thread.queries: the named pg-connector
            queries whose results form the watched set of threads. Empty (the
            default) renders no watch.thread.queries key.
          '';
        };
        activeWindow = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = ''
            config.yaml's watch.thread.active_window (a duration such as
            "7d"): how long a thread stays active after its last activity.
            Null uses pg-desk's default, "7d".
          '';
        };
      };
    };

    sweep = {
      maxAge = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          config.yaml's sweep.max_age (a duration such as "6h"): how stale a
          row may be before the sweep re-hydrates it. Null uses pg-desk's
          default, "6h".
        '';
      };
      maxPerPoll = lib.mkOption {
        type = lib.types.nullOr lib.types.ints.positive;
        default = null;
        description = ''
          config.yaml's sweep.max_per_poll: the cap on sweep re-hydrations per
          poll (and on the local reconcile tier's re-emits per poll). Positive
          only: pg-desk rejects an explicit 0, so none is renderable. Null
          uses pg-desk's default, 20.
        '';
      };
      reconcileAge = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          config.yaml's sweep.reconcile_age (a duration such as "30m"): how
          old an active entity's latest change_log row may be before the local
          reconcile tier re-emits a reconcile record for it. Null uses
          pg-desk's default, "30m".
        '';
      };
    };

    hydration = {
      maxPerPoll = lib.mkOption {
        type = lib.types.nullOr lib.types.ints.positive;
        default = null;
        description = ''
          config.yaml's hydration.max_per_poll: the cap on hydrations per
          poll. Positive only: pg-desk rejects an explicit 0, so none is
          renderable. Null uses pg-desk's default, 50.
        '';
      };
      readIssueDeps = lib.mkOption {
        type = lib.types.nullOr lib.types.bool;
        default = null;
        description = ''
          config.yaml's hydration.read_issue_deps: when true, every hydrated
          issue's stored facts also carry `pg-connector issue deps <id> --full`
          (the recursive set the issue is blocked by), which the daily-focus
          rank's unblocks key reads. Costs one extra connector read per issue
          hydration. Null (and false) leave it off: pg-desk's default.
        '';
      };
      ingestFailedMaxAge = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          config.yaml's hydration.ingest_failed_max_age (a duration such as
          "30m"): how old the oldest failed-ingest entry may be before
          `pg-desk doctor` gates. It does not move the
          pg-desk-ingest-failed-old alert, whose threshold is fixed at 1800
          seconds. Null uses pg-desk's default, "30m".
        '';
      };
    };

    # Focus keys (daily-focus design, section 12 (m)). All null by default and
    # omitted from the rendered config.yaml when null; pg-desk's own defaults
    # then apply.
    focus = {
      timeZone = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          config.yaml's focus.time_zone (an IANA zone name such as
          "America/New_York"): the zone in which the focus day rolls over.
          Null uses the process-local zone.
        '';
      };
      coverageBacklogMax = lib.mkOption {
        type = lib.types.nullOr lib.types.ints.positive;
        default = null;
        description = ''
          config.yaml's focus.coverage_backlog_max: the backlog count above
          which coverage is reported incomplete. Positive only: pg-desk
          rejects an explicit 0, so none is renderable. Null uses 10% of the
          active count of the type, rounded up.
        '';
      };
      pendingGateAge = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          config.yaml's focus.pending_gate_age (a duration such as "30m"):
          how old an unminted selection may be before `doctor` gates on it.
          Null uses pg-desk's default, "30m".
        '';
      };
      operatorIdentities = lib.mkOption {
        type = lib.types.nullOr (lib.types.listOf lib.types.str);
        default = null;
        description = ''
          config.yaml's focus.operator_identities: the assignee strings (a
          Jira display name or email) that make a Jira issue the operator's
          for focus candidacy. Serves Jira only. Null renders nothing; an
          empty list is valid and means no Jira issue is a candidate by
          assignment.
        '';
      };
      priorityMap = lib.mkOption {
        type = lib.types.nullOr (
          lib.types.attrsOf (
            lib.types.enum [
              "P0"
              "P1"
              "P2"
              "P3"
              "P4"
            ]
          )
        );
        default = null;
        example = {
          Blocker = "P0";
          Major = "P2";
        };
        description = ''
          config.yaml's focus.priority_map: maps a tracker priority value (a
          Jira priority name, matched case-insensitively) to one of P0..P4
          for the daily-focus rank's priority key. A value that is neither in
          the map nor already of the form P0..P4 sorts after P4. A non-empty
          map REPLACES pg-desk's default (Highest = P0, High = P1,
          Medium = P2, Low = P3, Lowest = P4) entirely, so list every value
          the tracker uses. Null renders nothing and the default applies.
        '';
      };
      suppressedKind = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          config.yaml's focus.suppressed_kind: the work-kind name the decider
          uses for focus beads. pg-desk carries no work-kind literal, so the
          deployment supplies the name; the bead column of `focus show` reads
          a source whose kind is suppressed as "pending (suppressed)". Null
          renders nothing and suppression is never reported.
        '';
      };
    };
    beadIdPattern = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        config.yaml's bead_id_pattern (a regular expression): tells a bead id
        from any other issue id; an issue whose id matches is a bead. The
        match is unanchored, so supply an anchored pattern for an exact
        match. Null renders nothing.
      '';
    };

    changeLogRetention = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        config.yaml's change_log_retention (a duration such as "14d"): how
        long change_log rows are kept. Null uses the store's own default.
      '';
    };
    consumerStaleAfter = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        config.yaml's consumer_stale_after (a duration such as "7d"): how long
        a change_log consumer may go without advancing before it is treated as
        stale. Null uses the store's own default.
      '';
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

    # attention (bead pg2-xaqag): mirrors packages/pg-desk/internal/config's
    # AttentionConfig. The set of valid rule kinds is owned by pg-desk's
    # attention registry (an unknown kind is rejected at config load), so
    # `rules` is a free-form attrset keyed by rule kind here rather than an
    # enumeration this module would have to keep in sync.
    attention = lib.mkOption {
      type = lib.types.nullOr (
        lib.types.submodule {
          options = {
            rules = lib.mkOption {
              type = lib.types.attrsOf (
                lib.types.submodule {
                  options = {
                    enabled = lib.mkOption {
                      type = lib.types.nullOr lib.types.bool;
                      default = null;
                      description = "config.yaml's attention.rules.<kind>.enabled. Null keeps the rule's built-in default (enabled).";
                    };
                    severity = lib.mkOption {
                      type = lib.types.nullOr (
                        lib.types.enum [
                          "low"
                          "medium"
                          "high"
                        ]
                      );
                      default = null;
                      description = "config.yaml's attention.rules.<kind>.severity. Null keeps the rule's built-in default severity.";
                    };
                    dueSoonDays = lib.mkOption {
                      type = lib.types.nullOr lib.types.ints.positive;
                      default = null;
                      description = ''
                        config.yaml's attention.rules.<kind>.due_soon_days: the
                        look-ahead window, in calendar days, of `issue.due-soon`.
                        Null keeps the rule's built-in default (2). pg-desk
                        rejects it on a rule kind that has no such parameter.
                      '';
                    };
                    staleAfterDays = lib.mkOption {
                      type = lib.types.nullOr lib.types.ints.positive;
                      default = null;
                      description = ''
                        config.yaml's attention.rules.<kind>.stale_after_days: the
                        threshold, in calendar days, of a time-based rule such as
                        `issue.stale-in-progress`. Null keeps the rule's built-in
                        default (7). pg-desk rejects it on a rule kind that has no
                        such parameter.
                      '';
                    };
                  };
                }
              );
              default = { };
              description = ''
                config.yaml's attention.rules: per-rule-kind tuning, keyed by
                rule kind (for example `pr.own-ci-failing`). A kind left out
                takes its built-in default. pg-desk rejects an unknown kind
                at config load.
              '';
            };
            ordering = {
              ties = lib.mkOption {
                type = lib.types.nullOr (lib.types.enum [ attentionTiesDefault ]);
                default = null;
                description = ''
                  config.yaml's attention.ordering.ties. pg-desk accepts only
                  its default rule, "${attentionTiesDefault}" (the same as
                  leaving it unset) and rejects any other value at config
                  load, so that is the only value this option admits. Null
                  renders nothing.
                '';
              };
            };
          };
        }
      );
      default = null;
      description = ''
        config.yaml's attention block (docs/behavior/pg-desk/attention.md,
        "Configuration"). Null (the default) renders no attention block, so
        every rule kind takes its built-in default.
      '';
    };

    # attentionPlugin (bead pg2-5l0x4.5): the `pg-desk-attention` plugin, a
    # scriptout-only `list_attention` backend that answers "which entities
    # need the operator now" from pg-desk's local store
    # (docs/behavior/pg-desk/attention.md). It is a separate binary, not a
    # subcommand of pg-desk, so enabling it both installs it and registers its
    # bare name in pg-connector's `attention.sources` — the umbrella knows the
    # plugin only as that registry entry (ADR 0077's runtime carve-out).
    #
    # Deliberately a SIBLING of `attention`, not `attention.plugin` (bead
    # pg2-9yuqa): `attention` above is a leaf `nullOr submodule` option, and Nix
    # refuses to declare a leaf option and a nested option set at the same
    # path. Nesting `plugin` inside that submodule instead would make setting
    # `plugin.enable` flip `attention` from null to non-null and render an
    # empty `attention:` block into config.yaml, which the null default exists
    # to avoid. These options are module-level wiring, not config.yaml keys.
    attentionPlugin = {
      enable = lib.mkEnableOption "the pg-desk-attention plugin: install it and register it in `phillipgreenii.programs.pg-connector.attention.sources`";
      package = lib.mkPackageOption pkgs "pg-desk-attention" { };
    };

    # routerSource (bead pg2-wmv6p): the `pg-router-source-pg-desk` adapter,
    # which exposes pg-desk's changes envelope as pg-router command-query
    # items. Both packages were overlay-only until this option, so neither
    # reached the per-user PATH. It follows pg-connector's always-installed
    # backend precedent (home/programs/pg-connector installs its adapters
    # whenever the program is enabled): on by default whenever pg-desk is
    # enabled, since it is the adapter for the very tool this module installs,
    # with an opt-out for a host that only wants the pg-desk CLI.
    routerSource = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          Install the `pg-router-source-pg-desk` adapter (pg-desk's changes
          envelope as pg-router command-query items) alongside pg-desk, so the
          bare name resolves on PATH. Its wrapper already carries `pg-desk` on
          its own PATH. Set false to opt out.
        '';
      };
      package = lib.mkPackageOption pkgs "pg-router-source-pg-desk" { };
    };

    # shadow: RETIRED (PR lane refactor, packet P9a-3). The `pg-desk-shadow`
    # one-off measurement tool ran `pg-desk pr changes`, which the daemon
    # design retires with the PR half of the change flow, so the package, its
    # runbook and its behavior doc are deleted. The option stays only so a
    # consuming flake that still sets `shadow.enable` evaluates (with a
    # warning) until it drops the flag; it installs nothing.
    shadow.enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      visible = false;
      description = "Retired: pg-desk-shadow no longer exists. Setting this only produces a warning; remove it.";
    };

    links = {
      issueUrlTemplate = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          config.yaml's links.issue_url_template: how the read-only
          `pg-desk links` verb turns an issue-tracker key (one matching
          ticketPatterns) into its web URL. An absolute http(s) URL with
          exactly one `%s`, which pg-desk replaces with the URL-escaped
          key. Null renders no template; the verb then falls back to the
          URL stored with the issue's snapshot. Deployment-specific, so no
          default (pg-desk validates the value at config load).
        '';
      };
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [
      cfg.package
    ]
    ++ lib.optional cfg.attentionPlugin.enable cfg.attentionPlugin.package
    ++ lib.optional cfg.routerSource.enable cfg.routerSource.package;

    warnings = lib.optional cfg.shadow.enable "programs.pg-desk.shadow.enable is retired (pg-desk-shadow was deleted with the PR half of pg-desk's change flow); it installs nothing. Remove it.";

    # Registered LAST in config order: `pg-connector attention list` breaks a
    # dedup tie at equal severity in favor of the earliest source, so the
    # plugin yields to the connector-owned sources on an {type, id} they both
    # report. The option lives in home/programs/pg-connector, which
    # home/default.nix imports alongside this module; with pg-connector itself
    # disabled the entry renders nowhere.
    phillipgreenii.programs.pg-connector.attention.sources = lib.mkIf cfg.attentionPlugin.enable (
      lib.mkAfter [ "pg-desk-attention" ]
    );

    # The pg-desk-focus tldr page ships in the package (postInstall in
    # packages/pg-desk/default.nix); same registration shape as
    # home/programs/wtdone. `config.programs.tldr` is home-manager's, so a
    # standalone eval of this module must declare it (tests/pg-desk-*.nix do).
    programs.tldr.customPages.pg-desk-focus = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.package}/share/tldr/pages.common/pg-desk-focus.md";
    };

    # Two watched queries of the same name on different backends cannot both
    # be pinned (watch.issue.backends is keyed by query name).
    assertions = [
      {
        assertion = lib.length derivedWatchPairs == lib.length (lib.attrNames derivedWatchBackends);
        message = "phillipgreenii.programs.pg-desk: a watched query name is declared under more than one pg-connector issue backend; watched query names MUST be unique across backends, because watch.issue.backends is keyed by query name.";
      }
      {
        assertion = nonDerivedManualQueries == [ ];
        message = "phillipgreenii.programs.pg-desk: watch.issue.queries lists ${lib.concatStringsSep ", " nonDerivedManualQueries}, which is not a watched query of any pg-connector issue backend. With phillipgreenii.programs.pg-connector enabled, watch.issue.queries is exactly the queries marked watched; declare each one once, as { expr = ...; watched = true; } under the backend's queries, and remove it from phillipgreenii.programs.pg-desk.watch.issue.queries.";
      }
    ];

    xdg.configFile."pg-desk/config.yaml".source =
      (pkgs.formats.yaml { }).generate "pg-desk-config.yaml"
        renderedConfig;
  };
}
