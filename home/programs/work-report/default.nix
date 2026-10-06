{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.work-report;

  # sources.<backend>: `enable` is always rendered (it is the one key the
  # loader reads for a configured backend); `labels` only when non-empty, so a
  # backend that carries no extra labels renders the same as the loader's own
  # default.
  renderedSources = lib.mapAttrs (
    _: s: { inherit (s) enable; } // lib.optionalAttrs (s.labels != [ ]) { inherit (s) labels; }
  ) cfg.sources;

  # The complete rendered document: exactly the keys
  # packages/work-report/internal/config accepts (timezone, sources,
  # schedule, store.path, kinds.narrative.{model,systemPromptFile}) and no
  # others, because the loader's decode is strict and rejects an unknown key.
  # timezone, sources, store.path and each kinds.narrative option are rendered
  # only when set, so the loader's own defaults (system zone, every backend
  # enabled, $XDG_STATE_HOME/work-report/store.db, no kinds key) apply
  # otherwise and shells without XDG_STATE_HOME agree with the scheduler.
  renderedNarrative =
    lib.optionalAttrs (cfg.kinds.narrative.model != null) { inherit (cfg.kinds.narrative) model; }
    // lib.optionalAttrs (cfg.kinds.narrative.systemPromptFile != null) {
      inherit (cfg.kinds.narrative) systemPromptFile;
    };

  renderedConfig =
    lib.optionalAttrs (cfg.timezone != null) { inherit (cfg) timezone; }
    // lib.optionalAttrs (cfg.sources != { }) { sources = renderedSources; }
    // {
      schedule = { inherit (cfg.schedule) interval window; };
    }
    // lib.optionalAttrs (cfg.store.path != null) { store.path = cfg.store.path; }
    // lib.optionalAttrs (renderedNarrative != { }) { kinds.narrative = renderedNarrative; };
in
{
  # Mirrors home/programs/pg-desk's shape (typed options ->
  # `pkgs.formats.yaml {}`.generate -> xdg.configFile). No organization
  # identifiers appear here: every deployment value (which backends, their
  # labels, the tracker pin) is the consuming flake's configuration.
  options.phillipgreenii.programs.work-report = {
    enable = lib.mkEnableOption "work-report (durable store and reports over pg-connector's activity sources)";
    package = lib.mkPackageOption pkgs "work-report" { };

    timezone = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        config.yaml's timezone: an IANA zone name, the day boundary for
        today/yesterday/this-week range specs. Null renders no key and
        work-report uses the system zone.
      '';
    };

    sources = lib.mkOption {
      type = lib.types.attrsOf (
        lib.types.submodule {
          options = {
            enable = lib.mkOption {
              type = lib.types.bool;
              default = true;
              description = "Whether work-report pulls this backend. Every backend pg-connector exposes is enabled unless set false here.";
            };
            labels = lib.mkOption {
              type = lib.types.listOf lib.types.str;
              default = [ ];
              description = "Extra labels attached to every entry from this backend.";
            };
          };
        }
      );
      default = { };
      description = ''
        config.yaml's sources.<backend>: what to do with each pg-connector
        activity backend, keyed by backend binary name. A backend with no
        entry is enabled with no extra labels.
      '';
    };

    schedule = {
      interval = lib.mkOption {
        type = lib.types.strMatching "^([0-9]+(\\.[0-9]+)?(ns|us|ms|s|m|h))+$";
        default = "1h";
        description = ''
          config.yaml's schedule.interval: the pg-router period trigger of the
          scheduled pull, a positive Go duration such as 1h or 30m.
        '';
      };
      window = lib.mkOption {
        type = lib.types.strMatching "^[0-9]+[hd]$";
        default = "48h";
        description = ''
          config.yaml's schedule.window: the overlap range every scheduled pull
          covers, a whole number of hours or days such as 48h or 7d. The
          rendered pg-router stanza pulls `last-<window>`.
        '';
      };
    };

    store.path = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        config.yaml's store.path. Rendered only when set; otherwise the
        loader's default $XDG_STATE_HOME/work-report/store.db applies, so an
        interactive shell and the scheduler agree on the store.
      '';
    };

    kinds.narrative = {
      model = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          config.yaml's kinds.narrative.model: the value passed to
          `claude -p --model` by `work-report report --kind narrative`. Rendered
          only when set; otherwise the loader's default (no --model flag)
          applies.
        '';
      };
      systemPromptFile = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          config.yaml's kinds.narrative.systemPromptFile: a file whose contents
          override the narrative generator's built-in system prompt. Rendered
          only when set; otherwise the built-in prompt applies.
        '';
      };
    };

    pgRouterConfigText = lib.mkOption {
      type = lib.types.str;
      readOnly = true;
      description = ''
        Read-only output: the pg-router [[query]] stanza that schedules
        `work-report pull` from schedule.interval and schedule.window. It
        equals `work-report config pg-router-query` for the same values. The
        deployment appends it to pg-router's configText; it emits the
        escalated.pg2 event type, so the deployment's existing triager role
        binding needs no change.
      '';
    };
  };

  config = lib.mkMerge [
    {
      # Defined unconditionally (readOnly + no `enable` gate), so a consumer
      # can read it while composing pg-router's configText without enabling
      # the rest of the module. Keep the template byte-identical to
      # packages/work-report/internal/config's PGRouterQueryText.
      phillipgreenii.programs.work-report.pgRouterConfigText = ''
        [[query]]
        name = "work-report-pull"
        emits = ["escalated.pg2"]
        type = "command"
        [query.command]
        argv = ["work-report", "pull", "--range", "last-${cfg.schedule.window}", "--output", "pg-router"]
        format = "json"
        [query.trigger]
        kind = "period"
        every = "${cfg.schedule.interval}"
      '';
    }
    (lib.mkIf cfg.enable {
      home.packages = [ cfg.package ];

      xdg.configFile."work-report/config.yaml".source =
        (pkgs.formats.yaml { }).generate "work-report-config.yaml"
          renderedConfig;
    })
  ];
}
