{
  config,
  lib,
  pkgs,
  ...
}:

# pg-review (optional Nix module): renders the tool's `config.toml` into the XDG
# config directory and generates the pg-router query and role stanzas from the
# declared schedule entries. The tool works the same from a hand-written
# config.toml; this module is a convenience and every `providers.*` entry is
# optional (the built-in providers, lenses and profiles ship embedded in the
# binary, and a config entry with a built-in id is merged onto it key by key).
#
# Two outputs:
#   * config.toml (File 1): scopes, profile and lens overrides and moved
#     providers only. Schedules and the router are NOT in it, so the CLI never
#     reads them. A Nix build validates it with `pg-review job doctor
#     --config-only` (see packages/pg-review/tests/module-check.nix).
#   * `routerConfig` / `routerConfigFile` (File 2, read-only): the timer
#     queries (static `metadata`, a `calendar` trigger) and the two roles. The
#     pg-router module has no queries option, so a deployment appends these to
#     its router configuration. Nothing here is a Go template and there is no
#     `router-config` command.

let
  cfg = config.phillipgreenii.programs.pg-review;
  tomlFormat = pkgs.formats.toml { };
  inherit (lib) types mkOption;

  nonNull = lib.filterAttrs (_: v: v != null);

  # --- File 1: config.toml -------------------------------------------------
  scopeEntries = lib.mapAttrsToList (
    name: s:
    {
      inherit name;
      inherit (s) prefixes;
    }
    // lib.optionalAttrs (s.layers != null) { inherit (s) layers; }
  ) cfg.scopes;

  profileTables = lib.mapAttrs (
    _: p:
    nonNull {
      inherit (p) backend model effort;
      inherit (p) minutes;
      queries_max = p.queriesMax;
    }
  ) cfg.profiles;

  # A lens entry carries only the id and the keys the module overrides.
  lensEntries = lib.mapAttrsToList (id: l: { inherit id; } // nonNull { inherit (l) profile; }) (
    lib.filterAttrs (_: l: l.profile != null) cfg.lenses
  );

  # A moved built-in provider holds only its command (an absolute store path)
  # and, when declared, the `queries` table: nothing else is repeated.
  providerEntries = lib.mapAttrsToList (
    name: p:
    {
      inherit name;
      inherit (p) command;
    }
    // lib.optionalAttrs (p.queries != null) { inherit (p) queries; }
  ) cfg.providers;

  configSettings = {
    schema_version = 1;
  }
  // lib.optionalAttrs (scopeEntries != [ ]) { scope = scopeEntries; }
  // lib.optionalAttrs (profileTables != { }) { profile = profileTables; }
  // lib.optionalAttrs (providerEntries != [ ]) { provider = providerEntries; }
  // lib.optionalAttrs (lensEntries != [ ]) { lens = lensEntries; };

  configFile = tomlFormat.generate "pg-review-config.toml" configSettings;

  # --- File 2: router stanzas ----------------------------------------------
  defaultWindow = s: if s.schedule == "daily" then "24h" else "7d";

  scheduleQuery = id: s: {
    name = id;
    type = "timer";
    emits = [ "review.run" ];
    trigger = {
      kind = "calendar";
      inherit (s) at;
      on_missed = s.onMissed;
    }
    // lib.optionalAttrs (s.schedule == "weekly") { inherit (s) days; };
    metadata = {
      schedule_id = id;
      inherit (s) lens scope;
      window = if s.window != null then s.window else defaultWindow s;
    };
  };

  maintainQuery = {
    name = "review-maintain";
    type = "timer";
    emits = [ "review.maintain" ];
    trigger = {
      kind = "period";
      every = "5m";
    };
  };

  handler = "${cfg.handlerPackage}/bin/pg-router-handler-command";
  adapter = "${cfg.adapterPackage}/bin/pg-review-router-adapter";

  # Gates and retry keys are set explicitly (no defaults assumed).
  # `review-run` blocks on every gate (a paused router or low disk holds it);
  # `review-maintain` is non-blocking on the low-disk gate.
  roles = [
    {
      name = "review-run";
      enabled = true;
      binds = [ "review.run" ];
      inherit handler;
      non_blocking_gates = [ ];
      max_dispatch_retries = 0;
      config.command.argv = [
        adapter
        "run"
        "{{.Item.ID}}"
        ''{{index .Item.Metadata "schedule_id"}}''
        ''{{index .Item.Metadata "lens"}}''
        ''{{index .Item.Metadata "scope"}}''
        ''{{index .Item.Metadata "window"}}''
      ];
    }
    {
      name = "review-maintain";
      enabled = true;
      binds = [ "review.maintain" ];
      inherit handler;
      non_blocking_gates = [ "LOW_DISK_USAGE" ];
      max_dispatch_retries = 0;
      config.command.argv = [
        adapter
        "maintain"
      ];
    }
  ];

  queries = lib.mapAttrsToList scheduleQuery cfg.schedules ++ [ maintainQuery ];

  routerConfig = {
    query = queries;
    role = roles;
  };
  routerConfigFile = tomlFormat.generate "pg-review-router.toml" routerConfig;

  emittedEvents = lib.concatMap (q: q.emits) queries;
  unemittedBinds = lib.filter (e: !(lib.elem e emittedEvents)) (lib.concatMap (r: r.binds) roles);

  scheduleAssertions = lib.concatLists (
    lib.mapAttrsToList (id: s: [
      {
        assertion = builtins.match "([01][0-9]|2[0-3]):[0-5][0-9]" s.at != null;
        message = "phillipgreenii.programs.pg-review.schedules.${id}.at (\"${s.at}\") must be a 24-hour HH:MM time.";
      }
      {
        assertion = s.schedule == "weekly" -> lib.length s.days == 1;
        message = "phillipgreenii.programs.pg-review.schedules.${id}: a weekly schedule names exactly one weekday in `days`.";
      }
      {
        assertion = s.schedule == "daily" -> s.days == [ ];
        message = "phillipgreenii.programs.pg-review.schedules.${id}: a daily schedule takes no `days`.";
      }
      {
        assertion = cfg.scopes ? ${s.scope};
        message = "phillipgreenii.programs.pg-review.schedules.${id}.scope \"${s.scope}\" is not a declared scope.";
      }
    ]) cfg.schedules
  );

  profileModule = _: {
    options = {
      backend = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Analyst backend (`ccpool`, `interactive`, `none`). Null leaves the built-in value.";
      };
      model = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Model name passed through to ccpool. Null leaves the built-in value.";
      };
      effort = mkOption {
        type = types.nullOr (
          types.enum [
            "low"
            "medium"
            "high"
            "xhigh"
            "max"
          ]
        );
        default = null;
        description = "Effort level. Null leaves the built-in value.";
      };
      minutes = mkOption {
        type = types.nullOr types.ints.positive;
        default = null;
        description = "Wall-clock limit for one run, in minutes. Null leaves the built-in value.";
      };
      queriesMax = mkOption {
        type = types.nullOr types.ints.positive;
        default = null;
        description = "Drill-down query limit (`queries_max`). Null leaves the built-in value.";
      };
    };
  };

  scheduleModule = _: {
    options = {
      lens = mkOption {
        type = types.str;
        description = "Lens id the schedule runs.";
      };
      scope = mkOption {
        type = types.str;
        description = "Scope name the schedule runs against (must be declared in `scopes`).";
      };
      schedule = mkOption {
        type = types.enum [
          "daily"
          "weekly"
        ];
        description = ''
          `daily` is a calendar trigger every day with a 24 hour window ending
          at the fire time; `weekly` is one weekday with a 7 day window.
        '';
      };
      at = mkOption {
        type = types.str;
        description = "Clock time of the trigger, 24-hour HH:MM (system time zone).";
      };
      days = mkOption {
        type = types.listOf (
          types.enum [
            "mon"
            "tue"
            "wed"
            "thu"
            "fri"
            "sat"
            "sun"
          ]
        );
        default = [ ];
        description = "The weekday of a weekly schedule (exactly one); empty for a daily one.";
      };
      window = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = "7d";
        description = "Explicit window length; overrides the 24h (daily) or 7d (weekly) default.";
      };
      onMissed = mkOption {
        type = types.enum [
          "next-slot"
          "run-now"
        ];
        default = "next-slot";
        description = "The router's `on_missed` policy for a missed slot (required by the router; always rendered).";
      };
    };
  };
in
{
  options.phillipgreenii.programs.pg-review = {
    enable = lib.mkEnableOption ''
      pg-review: renders the tool's config.toml and generates the pg-router
      query and role stanzas (`routerConfig`) from the declared schedules
    '';

    package = lib.mkPackageOption pkgs "pg-review" { };
    adapterPackage = lib.mkPackageOption pkgs "pg-review-router-adapter" { };
    handlerPackage = lib.mkPackageOption pkgs "pg-router-ccpool-handler" { };

    scopes = mkOption {
      type = types.attrsOf (
        types.submodule {
          options = {
            prefixes = mkOption {
              type = types.listOf types.str;
              default = [ ];
              description = "Path prefixes (each root plus worktree and forest locations).";
            };
            layers = mkOption {
              type = types.nullOr (types.listOf types.str);
              default = null;
              description = "Layers that load in this scope. Null leaves the tool's default.";
            };
          };
        }
      );
      default = { };
      description = "Scopes rendered as `[[scope]]` entries (scopes have no built-in).";
    };

    profiles = mkOption {
      type = types.attrsOf (types.submodule profileModule);
      default = { };
      description = "Analyst profile overrides, merged onto the built-in profile of the same name.";
    };

    lenses = mkOption {
      type = types.attrsOf (
        types.submodule {
          options.profile = mkOption {
            type = types.nullOr types.str;
            default = null;
            description = "Profile the lens uses. Null leaves the built-in value.";
          };
        }
      );
      default = { };
      description = "Lens overrides, merged onto the built-in lens of the same name.";
    };

    schedules = mkOption {
      type = types.attrsOf (types.submodule scheduleModule);
      default = { };
      description = "Schedules, each rendered as one router timer query. Never written to config.toml.";
    };

    providers = mkOption {
      type = types.attrsOf (
        types.submodule {
          options = {
            command = mkOption {
              type = types.listOf types.str;
              description = "Absolute command (store path) of the provider binary.";
            };
            queries = mkOption {
              type = types.nullOr (types.attrsOf (types.listOf types.str));
              default = null;
              description = "Canned queries per lens; replaces the built-in table whole. Null keeps the built-in.";
            };
          };
        }
      );
      default = { };
      description = ''
        Optional. A provider moved from its built-in place; rendered with only
        its `command` (and `queries` when set). A hand-written entry works the
        same, and the owning tool's module may contribute here.
      '';
    };

    routerConfig = mkOption {
      type = types.attrs;
      readOnly = true;
      default = routerConfig;
      description = "Read-only: the router `[[query]]` and `[[role]]` tables, to append to a router configuration.";
    };

    routerConfigFile = mkOption {
      type = types.path;
      readOnly = true;
      default = routerConfigFile;
      description = "Read-only: `routerConfig` as a TOML file in the store.";
    };

    configFile = mkOption {
      type = types.path;
      readOnly = true;
      internal = true;
      default = configFile;
      description = "Read-only: the rendered config.toml in the store.";
    };
  };

  config = lib.mkIf cfg.enable {
    xdg.configFile."pg-review/config.toml".source = configFile;

    home.packages = [
      cfg.package
      cfg.adapterPackage
    ];

    assertions = scheduleAssertions ++ [
      {
        assertion = roles != [ ];
        message = "phillipgreenii.programs.pg-review: a router configuration with no [[role]] runs zero roles and zero queries.";
      }
      {
        assertion = unemittedBinds == [ ];
        message = "phillipgreenii.programs.pg-review: role(s) bind event(s) no generated query emits: ${lib.concatStringsSep ", " unemittedBinds}.";
      }
    ];
  };
}
