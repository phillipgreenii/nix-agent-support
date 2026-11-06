# Nix check for home/programs/pg-review (checks.<system>.pg-review-module).
#
# Evaluates the module against stubbed home-manager options with SYNTHETIC
# sample values (/ws, /ws/.worktrees), then builds a derivation that
#   * runs the real `pg-review job doctor --config-only` on the rendered
#     config.toml (against a hermetic XDG_CONFIG_HOME),
#   * asserts the rendered File 1 holds the scopes and a moved built-in
#     provider as a command-only entry (so the built-in queries stay in effect),
#   * asserts the generated router stanzas (timer queries with static metadata,
#     calendar triggers, the 5 minute maintain period trigger, the two roles
#     with explicit gates, max_dispatch_retries 0 and adapter argv, an absolute
#     handler store path, at least one role, every bound event emitted by a
#     generated query),
#   * asserts the module adds nothing with enable = false.
{ lib, pkgs }:
let
  evaluate =
    cfg:
    lib.evalModules {
      specialArgs = { inherit pkgs lib; };
      modules = [
        ../../../home/programs/pg-review/default.nix
        (
          { lib, ... }:
          {
            options = {
              xdg.configFile = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
              home.packages = lib.mkOption {
                type = lib.types.listOf lib.types.package;
                default = [ ];
              };
              assertions = lib.mkOption {
                type = lib.types.listOf lib.types.anything;
                default = [ ];
              };
            };
          }
        )
        { phillipgreenii.programs.pg-review = cfg; }
      ];
    };

  pkg = pkgs.pg-review;

  sample = evaluate {
    enable = true;
    scopes.workspace.prefixes = [
      "/ws"
      "/ws/.worktrees"
    ];
    profiles.deep-review = {
      model = "claude-opus-5-5";
      effort = "high";
    };
    lenses.config-health.profile = "health-check";
    providers.ccaudit.command = [ "${pkg}/bin/pg-review-provider-ccaudit" ];
    schedules = {
      agent-workflow-workspace-daily = {
        lens = "agent-workflow";
        scope = "workspace";
        schedule = "daily";
        at = "02:00";
      };
      config-health-workspace-weekly = {
        lens = "config-health";
        scope = "workspace";
        schedule = "weekly";
        days = [ "sun" ];
        at = "03:30";
        window = "7d";
      };
      agent-workflow-workspace-weekly = {
        lens = "agent-workflow";
        scope = "workspace";
        schedule = "weekly";
        days = [ "mon" ];
        at = "04:00";
        window = "3d";
        onMissed = "run-now";
      };
    };
  };
  sampleOpts = sample.config.phillipgreenii.programs.pg-review;

  disabled = evaluate { enable = false; };
  noScope = evaluate { enable = true; };
  badWeekly = evaluate {
    enable = true;
    scopes.w.prefixes = [ "/ws" ];
    schedules.bad = {
      lens = "agent-workflow";
      scope = "w";
      schedule = "weekly";
      at = "01:00";
    };
  };

  failedAssertions = c: lib.filter (a: !a.assertion) c.config.assertions;

  rc = sampleOpts.routerConfig;
  queryByName = n: lib.findFirst (q: q.name == n) null rc.query;
  roleByName = n: lib.findFirst (r: r.name == n) null rc.role;
  daily = queryByName "agent-workflow-workspace-daily";
  weekly = queryByName "config-health-workspace-weekly";
  weeklyOverride = queryByName "agent-workflow-workspace-weekly";
  maintain = queryByName "review-maintain";
  runRole = roleByName "review-run";
  maintainRole = roleByName "review-maintain";
  emitted = lib.concatMap (q: q.emits) rc.query;

  cases = {
    "sample has no failed assertions" = failedAssertions sample == [ ];
    "no-scope config has no failed assertions" = failedAssertions noScope == [ ];
    "a weekly schedule without a day is rejected" = lib.length (failedAssertions badWeekly) == 1;

    "disabled renders no config file" = disabled.config.xdg.configFile == { };
    "disabled installs nothing" = disabled.config.home.packages == [ ];
    "disabled has no assertions" = disabled.config.assertions == [ ];
    "enabled renders the config file" = sample.config.xdg.configFile ? "pg-review/config.toml";

    "daily: calendar trigger, required on_missed, 24h window" =
      daily.type == "timer"
      && daily.emits == [ "review.run" ]
      &&
        daily.trigger == {
          kind = "calendar";
          at = "02:00";
          on_missed = "next-slot";
        }
      &&
        daily.metadata == {
          schedule_id = "agent-workflow-workspace-daily";
          lens = "agent-workflow";
          scope = "workspace";
          window = "24h";
        };
    "weekly: one weekday, explicit window" =
      weekly.trigger == {
        kind = "calendar";
        at = "03:30";
        days = [ "sun" ];
        on_missed = "next-slot";
      }
      && weekly.metadata.window == "7d";
    "an explicit window overrides the default and onMissed is honoured" =
      weeklyOverride.metadata.window == "3d" && weeklyOverride.trigger.on_missed == "run-now";
    "maintain: 5 minute period trigger emitting review.maintain" =
      maintain.emits == [ "review.maintain" ]
      &&
        maintain.trigger == {
          kind = "period";
          every = "5m";
        };

    "at least one role is declared" = lib.length rc.role >= 1;
    "review-run binds review.run and blocks on every gate" =
      runRole.binds == [ "review.run" ]
      && runRole.non_blocking_gates == [ ]
      && runRole.max_dispatch_retries == 0;
    "review-maintain binds review.maintain, non-blocking on low disk" =
      maintainRole.binds == [ "review.maintain" ]
      && maintainRole.non_blocking_gates == [ "LOW_DISK_USAGE" ]
      && maintainRole.max_dispatch_retries == 0;
    "every role handler is an absolute store path" = lib.all (
      r: lib.hasPrefix "/nix/store/" r.handler
    ) rc.role;
    "review-run argv is the adapter run command" =
      lib.take 3 runRole.config.command.argv == [
        "${pkgs.pg-review-router-adapter}/bin/pg-review-router-adapter"
        "run"
        "{{.Item.ID}}"
      ]
      && lib.length runRole.config.command.argv == 7;
    "review-maintain argv is the adapter maintain command" =
      maintainRole.config.command.argv == [
        "${pkgs.pg-review-router-adapter}/bin/pg-review-router-adapter"
        "maintain"
      ];
    "every role's bound event is emitted by a generated query" = lib.all (
      r: lib.all (e: lib.elem e emitted) r.binds
    ) rc.role;
  };

  failures = lib.attrNames (lib.filterAttrs (_: ok: !ok) cases);
in
pkgs.runCommand "pg-review-module"
  {
    nativeBuildInputs = [
      pkg
      pkgs.jq
      pkgs.yq-go
      pkgs.coreutils
      pkgs.gnugrep
    ];
  }
  ''
    ${lib.optionalString (failures != [ ]) ''
      echo "pg-review module eval cases failed: ${lib.concatStringsSep "; " failures}" >&2
      exit 1
    ''}

    # --- File 1: the real binary accepts the rendered config.toml ------------
    cfgHome="$PWD/xdg"
    mkdir -p "$cfgHome/pg-review"
    cp ${sampleOpts.configFile} "$cfgHome/pg-review/config.toml"
    export XDG_CONFIG_HOME="$cfgHome" XDG_STATE_HOME="$PWD/state" XDG_CACHE_HOME="$PWD/cache" HOME="$PWD/home"
    mkdir -p "$HOME"
    pg-review job doctor --config-only > doctor.txt 2>&1 \
      || { echo "doctor --config-only rejected the rendered config:" >&2; cat doctor.txt >&2; exit 1; }

    yq -p toml -o json ${sampleOpts.configFile} > config.json
    jq -e '
      .schema_version == 1
      and (.scope | length) == 1 and .scope[0].name == "workspace"
      and .scope[0].prefixes == ["/ws", "/ws/.worktrees"]
      and (.provider | length) == 1
      and .provider[0] == { name: "ccaudit", command: ["${pkg}/bin/pg-review-provider-ccaudit"] }
      and .profile["deep-review"].model == "claude-opus-5-5"
      and (has("schedule") | not) and (has("query") | not) and (has("role") | not)
    ' config.json >/dev/null || { echo "unexpected rendered config.toml:" >&2; cat config.json >&2; exit 1; }

    # negative control: the same command rejects a broken file (not vacuous)
    printf '[[scope]]\nname = "s"\n' > "$cfgHome/pg-review/config.toml"
    rc=0
    pg-review job doctor --config-only > bad.txt 2>&1 || rc=$?
    [ "$rc" = 4 ] || { echo "FAIL: doctor accepted a scope with no prefix (exit $rc):" >&2; cat bad.txt >&2; exit 1; }

    # --- File 2: the generated router stanza text ----------------------------
    yq -p toml -o json ${sampleOpts.routerConfigFile} > router.json
    jq -e '
      (.query | length) == 4
      and ([.query[].name] | sort) == ["agent-workflow-workspace-daily", "agent-workflow-workspace-weekly", "config-health-workspace-weekly", "review-maintain"]
      and (.role | length) == 2
      and ([.role[].handler] | all(startswith("/nix/store/")))
      and ([.role[].max_dispatch_retries] == [0, 0])
      and (.role[] | select(.name == "review-run") | .binds == ["review.run"] and .non_blocking_gates == [] and .config.command.argv[1] == "run")
      and (.role[] | select(.name == "review-maintain") | .config.command.argv[1] == "maintain" and .non_blocking_gates == ["LOW_DISK_USAGE"])
      and (.query[] | select(.name == "agent-workflow-workspace-daily") | .metadata == {schedule_id: "agent-workflow-workspace-daily", lens: "agent-workflow", scope: "workspace", window: "24h"} and .trigger.kind == "calendar" and .trigger.on_missed == "next-slot")
      and ([.role[].binds[]] - [.query[].emits[]] == [])
    ' router.json >/dev/null || { echo "unexpected router stanzas:" >&2; cat router.json >&2; exit 1; }
    grep -q '{{index .Item.Metadata "schedule_id"}}' ${sampleOpts.routerConfigFile} \
      || { echo "FAIL: argv template for schedule_id missing" >&2; cat ${sampleOpts.routerConfigFile} >&2; exit 1; }

    touch $out
  ''
