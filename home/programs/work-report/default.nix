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

  yamlFormat = pkgs.formats.yaml { };

  # generators.<name>: command is always rendered (it is required); timeout,
  # inputs and env only when set, so a generator that sets none renders the
  # same as the loader's own defaults (5m, no inputs, no env names).
  renderedGenerators = lib.mapAttrs (
    _: g:
    {
      inherit (g) command;
    }
    // lib.optionalAttrs (g.timeout != null) { inherit (g) timeout; }
    // lib.optionalAttrs (g.inputs != [ ]) { inherit (g) inputs; }
    // lib.optionalAttrs (g.env != [ ]) { inherit (g) env; }
  ) cfg.generators;

  # reports.<name>: generator is always rendered (it is required); every
  # other key only when set, and narrowing only for the dimensions that carry
  # values (an empty dimension is the loader's default).
  renderedReports = lib.mapAttrs (
    _: r:
    let
      narrowing = lib.filterAttrs (_: v: v != [ ]) r.narrowing;
    in
    {
      inherit (r) generator;
    }
    // lib.optionalAttrs (r.range != null) { inherit (r) range; }
    // lib.optionalAttrs (narrowing != { }) { inherit narrowing; }
    // lib.optionalAttrs (r.config != { }) { inherit (r) config; }
    // lib.optionalAttrs (r.output.dir != null) { output.dir = r.output.dir; }
  ) cfg.reports;

  # The complete rendered document: only keys
  # packages/work-report/internal/config accepts (timezone, sources,
  # schedule, store.path, generators, reports), because the loader's decode
  # is strict and rejects an unknown key. The retired `kinds` key is never rendered: the loader
  # rejects it in every verb. timezone, sources and store.path are rendered
  # only when set, so the loader's own defaults (system zone, every backend
  # enabled, $XDG_STATE_HOME/work-report/store.db) apply otherwise and shells
  # without XDG_STATE_HOME agree with the scheduler.
  renderedConfig =
    lib.optionalAttrs (cfg.timezone != null) { inherit (cfg) timezone; }
    // lib.optionalAttrs (cfg.sources != { }) { sources = renderedSources; }
    // {
      schedule = { inherit (cfg.schedule) interval window; };
    }
    // lib.optionalAttrs (cfg.store.path != null) { store.path = cfg.store.path; }
    // lib.optionalAttrs (cfg.generators != { }) { generators = renderedGenerators; }
    // lib.optionalAttrs (cfg.reports != { }) { reports = renderedReports; };

  # An environment variable NAME (never a value): the loader's own rule.
  envName = lib.types.strMatching "^[A-Za-z_][A-Za-z0-9_]*$";

  # Names the narrative generator passes through the runner's env allowlist
  # on top of the runner's built-in base list (PATH HOME USER LANG TMPDIR
  # XDG_*). Subscription auth needs nothing beyond that base list (verified
  # in packet pg2-6oop0.6); these are the proxy, certificate and Claude
  # configuration names a claude call can need elsewhere, passed through only
  # when the variable is set. Secret-bearing cloud credentials (AWS_*,
  # GOOGLE_APPLICATION_CREDENTIALS) are NOT here: a deployment that
  # authenticates that way appends them to
  # generators.narrative.env itself.
  narrativeDefaultEnv = [
    "HTTPS_PROXY"
    "https_proxy"
    "HTTP_PROXY"
    "http_proxy"
    "NO_PROXY"
    "no_proxy"
    "ALL_PROXY"
    "all_proxy"
    "SSL_CERT_FILE"
    "SSL_CERT_DIR"
    "NODE_EXTRA_CA_CERTS"
    "CLAUDE_CONFIG_DIR"
    "ANTHROPIC_API_KEY"
    "ANTHROPIC_AUTH_TOKEN"
    "ANTHROPIC_BASE_URL"
    "ANTHROPIC_MODEL"
    "CLAUDE_CODE_USE_BEDROCK"
    "CLAUDE_CODE_USE_VERTEX"
  ];
in
{
  # Mirrors home/programs/pg-desk's shape (typed options ->
  # `pkgs.formats.yaml {}`.generate -> xdg.configFile). No organization
  # identifiers appear here: every deployment value (which backends, their
  # labels, the tracker pin) is the consuming flake's configuration. The
  # tracker pin (WORK_REPORT_ISSUE_BACKEND) is optional: unset, work-report
  # discovers the one registered issue backend that defines the
  # `escalated-all` dedupe query (pg2-sqc5v).
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

    generators = lib.mkOption {
      type = lib.types.attrsOf (
        lib.types.submodule {
          options = {
            command = lib.mkOption {
              type = lib.types.nonEmptyListOf lib.types.str;
              description = ''
                The generator's argv. A bare first element is resolved on PATH
                when a run starts.
              '';
            };
            timeout = lib.mkOption {
              type = lib.types.nullOr (lib.types.strMatching "^([0-9]+(\\.[0-9]+)?(ns|us|ms|s|m|h))+$");
              default = null;
              description = ''
                The run limit, a positive Go duration such as 10m. Null renders
                no key and work-report applies its default of 5m.
              '';
            };
            inputs = lib.mkOption {
              type = lib.types.listOf (lib.types.enum [ "baseline" ]);
              default = [ ];
              description = ''
                Extra inputs the generator asks for. The only legal value is
                baseline (the baseline report, written beside the run).
              '';
            };
            env = lib.mkOption {
              type = lib.types.listOf envName;
              default = [ ];
              description = ''
                Environment variable NAMES passed through to the generator on
                top of the runner's base list. Names only: a secret is passed
                by naming its variable here, never by value in a config.
              '';
            };
          };
        }
      );
      default = { };
      description = ''
        config.yaml's generators.<name>: the registered external report
        generators. The name `baseline` is built in and MUST NOT be defined
        here. `narrative` is registered by default (see below).
      '';
    };

    reports = lib.mkOption {
      type = lib.types.attrsOf (
        lib.types.submodule {
          options = {
            generator = lib.mkOption {
              type = lib.types.str;
              description = "The registered generator this preset runs (`baseline` or a name in generators).";
            };
            config = lib.mkOption {
              inherit (yamlFormat) type;
              default = { };
              description = ''
                The generator's free-form config, rendered to YAML as given.
                Secrets SHOULD NOT go here: pass them through the generator's
                env names. Not allowed on a baseline preset.
              '';
            };
            range = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              example = "yesterday";
              description = "A work-report range spec. Null renders no key and the preset covers today.";
            };
            narrowing = {
              labels = lib.mkOption {
                type = lib.types.listOf lib.types.str;
                default = [ ];
                description = "Label values (OR within the dimension, AND across dimensions).";
              };
              sources = lib.mkOption {
                type = lib.types.listOf lib.types.str;
                default = [ ];
                description = "Source values (OR within the dimension, AND across dimensions).";
              };
              types = lib.mkOption {
                type = lib.types.listOf lib.types.str;
                default = [ ];
                description = "Entry type values (OR within the dimension, AND across dimensions).";
              };
            };
            output.dir = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              example = "~/reports/example-daily";
              description = ''
                The PARENT directory of the per-run directories; a leading ~
                and $VAR are expanded and the result MUST be absolute. Null
                renders no key and work-report uses
                $XDG_STATE_HOME/work-report/reports/<preset>.
              '';
            };
          };
        }
      );
      default = { };
      description = ''
        config.yaml's reports.<name>: the named report presets, run with
        `work-report report <name>`. The name `baseline` is built in and
        MUST NOT be defined here. Nothing is rendered for a key left unset.
      '';
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
    {
      # The narrative generator is registered by default, field by field at
      # mkDefault priority so a deployment overrides any one field (or adds
      # env names) without restating the rest. It is a registration only: no
      # preset uses it until the deployment defines one in `reports`.
      phillipgreenii.programs.work-report.generators.narrative = {
        command = lib.mkDefault [ "${cfg.package}/bin/work-report-narrative" ];
        timeout = lib.mkDefault "10m";
        inputs = lib.mkDefault [ "baseline" ];
        env = lib.mkDefault narrativeDefaultEnv;
      };
    }
    (lib.mkIf cfg.enable {
      home.packages = [ cfg.package ];

      xdg.configFile."work-report/config.yaml".source =
        yamlFormat.generate "work-report-config.yaml" renderedConfig;
    })
  ];
}
