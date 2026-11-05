{
  config,
  lib,
  pkgs,
  options,
  # Set by the nix-darwin / NixOS home-manager integration; null for a
  # standalone home-manager activation (no system scope to read from).
  osConfig ? null,
  ...
}:

# pg-claude-code-recorder module (docket pg2-v77tsx, phase 7a): renders the
# daemon's config.toml, checks it at build time with the REAL binary, installs
# the package, and registers the daemon as an HM-scoped launchd user agent
# through `phillipgreenii.programs.launchdServices.userAgents` (the pg-keep-awake
# and pg-task-focus precedents; this repo's CLAUDE.md, "HM-scoped launchd agents").
#
# HM-scoped launchd rules this module states and honors:
#   - a terminal flake MUST NOT take this module without a personal revision whose
#     home module declares `phillipgreenii.programs.launchdServices.userAgents`
#     (asserted on darwin when `daemon.enable` is set);
#   - `execPath` is unsupported in the HM scope: the entry is a `script`, and the
#     binary is referenced by store path, never through launchd's bare PATH;
#   - OTel/observability settings are read null-safely from
#     `osConfig.phillipgreenii.observability` (null osConfig, an absent option
#     and a disabled stack all fall back to "off");
#   - `manageLogs` stays at its default (on): the shared hourly rotator rotates
#     the launchd stdout/stderr files, so there is no `logs.maxBytes` option and
#     the daemon never rotates its own logs.
#
# Platforms: darwin is supervised by launchd. Linux builds and runs the daemon
# UNSUPERVISED (the module registers nothing and warns when `daemon.enable` is
# set); its watcher is inotify, which needs one watch per watched directory
# under the Claude directory, so raise `fs.inotify.max_user_watches` (and
# `max_user_instances`) on a Linux host with a large Claude directory.
#
# Build-time check of the rendered file (the implementer's choice, recorded
# here): the phase-1d loader has no `config check` command and tolerates keys a
# later phase owns (remove.*, held.*, feed.*, wait.*), so the check
# runs the real `pg-claude-code-recorder status --json --config <file>` offline
# against a socket that does not exist. A malformed file or a bad duration is a
# usage error (exit 2); a loadable file answers "daemon offline" (exit 3).
# Later-phase keys are therefore accepted now and validated when the phase that
# reads them lands.

let
  cfg = config.phillipgreenii.programs.pg-claude-code-recorder;
  tomlFormat = pkgs.formats.toml { };
  isDarwin = pkgs.stdenv.hostPlatform.isDarwin;

  # Whether the composing flake imports personal's home module, which declares
  # the HM launchdServices registry. Read from the module `options` argument,
  # never from `config`/`pkgs` (choosing attribute NAMES from those is infinite
  # recursion); see home/programs/pa-monitor.
  hasLaunchdRegistry = options.phillipgreenii.programs ? launchdServices;

  # The control module (phase 8) may not exist yet: its coupling is gated on
  # the option's DECLARATION.
  hasControl = options.phillipgreenii.programs ? pg-claude-control;
  control = if hasControl then config.phillipgreenii.programs.pg-claude-control else null;
  controlOn = hasControl && (control.enable or false);

  # The system observability stack, read null-safely.
  obs = if osConfig == null then { } else osConfig.phillipgreenii.observability or { };

  suffix = lib.optionalString (cfg.instance != "") "-${cfg.instance}";
  agentName = "pg-claude-code-recorder${suffix}";
  label = "com.phillipg.pg-claude-code-recorder${
    lib.optionalString (cfg.instance != "") ".${cfg.instance}"
  }";

  # The rendered daemon configuration. An option a.b is the key b in the table
  # [a]; durations are Go duration strings with a `d` suffix for days.
  rendered = {
    inherit (cfg) claudeDir stateDir minFreeBytes;
    socket = cfg.socketPath;
    inherit (cfg) controlStateDir;
    blockList = cfg.blockList.extra;
    settle = {
      after = cfg.settle.after;
      mode = cfg.settle.mode;
      reconcile = cfg.settle.reconcile;
      min_wake = cfg.settle.min_wake;
    };
    doctor = {
      bytes_per_slice = cfg.doctor.bytes_per_slice;
      slice_every = cfg.doctor.slice_every;
      allow_slow_cycle = cfg.doctor.allow_slow_cycle;
    };
    wal.truncate_above = cfg.wal.truncate_above;
    sweep.interval = cfg.sweep.interval;
    feed.retention = cfg.feed.retention;
    wait = {
      max = cfg.wait.max;
      maxWaiters = cfg.wait.maxWaiters;
    };
    query.max_connections = cfg.query.maxConnections;
    held = {
      recheck = cfg.held.recheck;
      alertAfter = cfg.held.alertAfter;
    };
    remove = {
      enable = cfg.remove.enable;
      after = cfg.remove.after;
      maxPerRun = cfg.remove.maxPerRun;
      maxPerHour = cfg.remove.maxPerHour;
    };
  };
  # The Prometheus exporter is off in the daemon unless `metrics.addr` is set; it
  # is rendered only while observability is on, on loopback at `metrics.port`
  # (the darwin module darwin/modules/pg-claude-code-recorder scrapes that port).
  settings = lib.recursiveUpdate (
    rendered
    // lib.optionalAttrs cfg.observability.enable {
      metrics.addr = "127.0.0.1:${toString cfg.metrics.port}";
    }
  ) cfg.settings;

  configUnchecked = tomlFormat.generate "pg-claude-code-recorder-config-unchecked.toml" settings;

  configFile =
    pkgs.runCommand "pg-claude-code-recorder-config.toml"
      {
        nativeBuildInputs = [ cfg.package ];
      }
      ''
        export HOME="$TMPDIR"
        rc=0
        pg-claude-code-recorder status --json --config ${configUnchecked} \
          --socket "$TMPDIR/absent.sock" >status.out 2>status.err || rc=$?
        if [ "$rc" != 3 ]; then
          echo "pg-claude-code-recorder rejected the rendered config (exit $rc, want 3: offline):" >&2
          cat status.out status.err >&2
          exit 1
        fi
        cp ${configUnchecked} "$out"
      '';

  logDir = "${config.xdg.stateHome}/pg-claude-code-recorder-launchd${suffix}";
  pprofSocket = "${config.xdg.stateHome}/pg-claude-code-recorder${suffix}/pprof.sock";

  durationOption =
    default: description:
    lib.mkOption {
      type = lib.types.strMatching "([0-9]+(\\.[0-9]+)?(d|h|m|s|ms))+";
      inherit default description;
    };
in
{
  options.phillipgreenii.programs.pg-claude-code-recorder = {
    enable = lib.mkEnableOption ''
      pg-claude-code-recorder: install the package and the command. The daemon
      itself is opted into per host with `daemon.enable`
    '';

    package = lib.mkPackageOption pkgs "pg-claude-code-recorder" { };

    instance = lib.mkOption {
      type = lib.types.str;
      default = "";
      example = "work";
      description = ''
        Suffix added to the launch agent label (`com.phillipg.pg-claude-code-recorder.<instance>`),
        the agent's name, its launchd log directory, its rendered config path
        and its pprof socket. A second Claude directory means a second daemon
        with its own `stateDir` and `socketPath`; the control mod's end files
        carry no daemon name, so every recorder reads them and joins by session
        id. Empty (the default) is the single, unsuffixed daemon.
      '';
    };

    daemon.enable = lib.mkEnableOption ''
      the LaunchAgent that runs the daemon (opt in per host, as pa-monitor does).
      Needs `phillipgreenii.programs.launchdServices` from
      phillipgreenii-nix-personal's home module on darwin. Linux has no
      supervisor: the module registers nothing and warns, and the daemon must be
      run by hand (raise `fs.inotify.max_user_watches` first)
    '';

    pprof.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        The workspace's profiling endpoint (on by default, per
        `docs/go-daemons.md` in `phillipg-nix-repo-base`): the launchd command
        passes `--pprof`, which listens on
        `$XDG_STATE_HOME/pg-claude-code-recorder/pprof.sock`, never in `stateDir`.
      '';
    };

    claudeDir = lib.mkOption {
      type = lib.types.str;
      default = config.home.sessionVariables.CLAUDE_CONFIG_DIR or "${config.home.homeDirectory}/.claude";
      defaultText = lib.literalExpression ''CLAUDE_CONFIG_DIR, else "''${config.home.homeDirectory}/.claude"'';
      description = "The Claude directory.";
    };

    stateDir = lib.mkOption {
      type = lib.types.str;
      default = "${config.xdg.dataHome}/pg-claude-code-recorder${suffix}";
      defaultText = lib.literalExpression ''"''${config.xdg.dataHome}/pg-claude-code-recorder"'';
      description = "Copies, index, journal and archives (the socket is `socketPath`, in the state home).";
    };

    socketPath = lib.mkOption {
      type = lib.types.str;
      default = "${config.xdg.stateHome}/pg-claude-code-recorder${suffix}/recorder.sock";
      defaultText = lib.literalExpression ''"''${config.xdg.stateHome}/pg-claude-code-recorder/recorder.sock"'';
      description = ''
        The Unix socket the daemon listens on. It sits in the state home, next
        to the pprof socket and not in `stateDir`, because the clients
        (`client.DefaultSocket`) dial
        `$XDG_STATE_HOME/pg-claude-code-recorder/recorder.sock` when nothing
        overrides it. The daemon creates the directory before it listens.
      '';
    };

    blockList.extra = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Block-list additions. Rendered as `blockList`.";
    };

    settle = {
      after = durationOption "24h" "Quiet time before settling. Rendered as `settle.after`.";
      mode = lib.mkOption {
        type = lib.types.enum [
          "scan"
          "event"
        ];
        default = "scan";
        description = ''
          The settle loop: `scan` runs the whole-store pass every minute;
          `event` settles from a dirty set and a due-time queue and runs the
          scan only as the reconciliation. Changing it needs a daemon restart.
          Rendered as `settle.mode`.
        '';
      };
      reconcile = durationOption "1h" ''
        Interval of the full reconciliation scan in `event` mode (at least
        1m); a missed event delays a settle by at most this long. Rendered as
        `settle.reconcile`.
      '';
      min_wake = durationOption "5s" ''
        Shortest time between two event wakes in `event` mode (at least 1s).
        Rendered as `settle.min_wake`.
      '';
    };

    doctor = {
      bytes_per_slice = lib.mkOption {
        type = lib.types.ints.unsigned;
        default = 268435456;
        description = ''
          Bytes of the copy one rolling doctor slice hashes (default 256 MiB).
          The budget counts one side, the copy. `0` selects the old synchronous
          doctor, which holds the copier lock across every file. Rendered as
          `doctor.bytes_per_slice`.
        '';
      };
      slice_every = durationOption "1m" ''
        How often the rolling doctor runs a slice while a cycle is open (at
        least 1s). Rendered as `doctor.slice_every`.
      '';
      allow_slow_cycle = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Start even when the projected cycle (copy bytes over
          `doctor.bytes_per_slice`, times `doctor.slice_every`) is longer than
          `doctor.interval`. Rendered as `doctor.allow_slow_cycle`.
        '';
      };
    };

    wal = {
      truncate_above = lib.mkOption {
        type = lib.types.ints.unsigned;
        default = 536870912;
        description = ''
          WAL size in bytes above which the checkpoint loop may attempt a
          TRUNCATE checkpoint (default 512 MiB), at most once an hour and only
          after a fresh passive checkpoint reached every frame while the daemon is
          caught up. A TRUNCATE blocks writers while it waits for readers. `0`
          disables TRUNCATE. The `wal-large` alert fires at twice this value.
          Rendered as `wal.truncate_above`.
        '';
      };
    };

    remove = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Removal from the Claude directory. Off by default: with it off the
          recorder never removes a file, so Claude Code behaves exactly as it
          does without the recorder. Once on, resuming a removed Conversation
          needs an extract, and so the recorder.
        '';
      };
      after = durationOption "30d" "Quiet time before removal. Rendered as `remove.after`.";
      maxPerRun = lib.mkOption {
        type = lib.types.ints.positive;
        default = 50;
        description = "Cap on removals per run, so a backlog is spread out.";
      };
      maxPerHour = lib.mkOption {
        type = lib.types.ints.positive;
        default = 200;
        description = "Cap on removals per hour.";
      };
    };

    minFreeBytes = lib.mkOption {
      type = lib.types.ints.unsigned;
      default = 20 * 1024 * 1024 * 1024;
      defaultText = lib.literalExpression "20 * 1024 * 1024 * 1024";
      description = "Free-space floor for low-space mode (20 GiB).";
    };

    sweep.interval = durationOption "15m" "Backstop sweep of the Claude directory.";

    feed.retention = durationOption "14d" "How long feed entries (and resets) are kept, by commit time.";

    wait = {
      max = durationOption "60s" "Longest wait.";
      maxWaiters = lib.mkOption {
        type = lib.types.ints.positive;
        default = 256;
        description = "Waiter limit.";
      };
    };

    query.maxConnections = lib.mkOption {
      type = lib.types.addCheck lib.types.int (n: n == 0 || n >= 2);
      default = 8;
      description = ''
        Most connections the read pool opens at once (config key
        `query.max_connections`). Readers beyond it queue on the pool, so the
        memory of the read connections stops growing with the number of
        concurrent readers. At least 2; 0 means unbounded (the way back).
      '';
    };

    held = {
      recheck = durationOption "1h" "Re-check interval for a held Conversation.";
      alertAfter = durationOption "3d" "The age that raises the held alert.";
    };

    controlStateDir = lib.mkOption {
      type = lib.types.str;
      default = if controlOn then control.stateDir else "${config.xdg.stateHome}/pg-claude-control";
      defaultText = lib.literalExpression ''the control module's stateDir when it is enabled, else "''${config.xdg.stateHome}/pg-claude-control"'';
      description = ''
        The control mod's inbox and end files (a second watched root: the
        recorder reads only `ends/`). Defaults from
        `phillipgreenii.programs.pg-claude-control.stateDir` when that module is
        declared and enabled; an assertion fails on a mismatch. Passed to the
        daemon so `coverage` reports the `ends/` root.
      '';
    };

    observability.enable = lib.mkOption {
      type = lib.types.bool;
      default = obs.enable or false;
      defaultText = lib.literalExpression "osConfig.phillipgreenii.observability.enable or false";
      description = ''
        Metrics, log source, alert rules and dashboard (wired in
        `darwin/modules/pg-claude-code-recorder`). Follows the system option,
        read null-safely from `osConfig`.
      '';
    };

    metrics.port = lib.mkOption {
      type = lib.types.port;
      default = 49320;
      description = ''
        Loopback port of the daemon's Prometheus exporter. Rendered as
        `metrics.addr = "127.0.0.1:<port>"` only while `observability.enable` is
        set; the darwin module registers the scrape target on the same port.
      '';
    };

    settings = lib.mkOption {
      inherit (tomlFormat) type;
      default = { };
      example = {
        doctor.interval = "6h";
      };
      description = ''
        Extra daemon keys merged over the rendered config (recursively), e.g.
        `doctor.interval`. The module registers no doctor schedule: the daemon
        runs doctor itself.
      '';
    };

    configFile = lib.mkOption {
      type = lib.types.path;
      readOnly = true;
      default = configFile;
      defaultText = lib.literalMD "the rendered, build-time-checked config.toml";
      description = "The rendered, build-time-checked configuration file in the store. Read-only.";
    };

    internal.settings = lib.mkOption {
      type = lib.types.attrs;
      internal = true;
      readOnly = true;
      default = settings;
      description = "The rendered configuration as data. Read-only; used by tests.";
    };

    internal.configUnchecked = lib.mkOption {
      type = lib.types.path;
      internal = true;
      readOnly = true;
      default = configUnchecked;
      description = "The rendered configuration before the build-time check. Read-only; used by tests.";
    };
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      home.packages = [ cfg.package ];

      # The clients (status, coverage) read the XDG config path to find the
      # socket; the daemon is launched with the store path explicitly.
      xdg.configFile."pg-claude-code-recorder${suffix}/config.toml".source = configFile;

      assertions = [
        {
          assertion = !(controlOn && cfg.controlStateDir != control.stateDir);
          message = "phillipgreenii.programs.pg-claude-code-recorder.controlStateDir (${cfg.controlStateDir}) must equal phillipgreenii.programs.pg-claude-control.stateDir (${
            if controlOn then control.stateDir else ""
          }): the mod writes its end files there and the recorder reads them there.";
        }
        {
          assertion = !(cfg.daemon.enable && isDarwin) || hasLaunchdRegistry;
          message = "phillipgreenii.programs.pg-claude-code-recorder.daemon.enable on darwin needs phillipgreenii.programs.launchdServices, which phillipgreenii-nix-personal's home module declares (personal ADR 0055); import it alongside this module.";
        }
      ];

      warnings =
        lib.optional (cfg.daemon.enable && !isDarwin)
          "phillipgreenii.programs.pg-claude-code-recorder.daemon.enable: there is no supervisor on this platform, so no agent is registered and the daemon runs unsupervised; run `pg-claude-code-recorder serve --config <configFile>` yourself and raise fs.inotify.max_user_watches (and max_user_instances) for the watcher.";
    })

    # LaunchAgent registration via the HM-scoped launchdServices registry:
    # gated on `daemon.enable` and the platform at ENTRY level (mkIf) and on the
    # option's declaration at attribute level (optionalAttrs).
    (lib.optionalAttrs hasLaunchdRegistry {
      phillipgreenii.programs.launchdServices.userAgents.${agentName} =
        lib.mkIf (cfg.enable && cfg.daemon.enable && isDarwin)
          {
            inherit label;
            # The whole command; the config's store path carries the config hash,
            # so a changed config or binary changes the job and restarts it.
            script = ''
              exec ${cfg.package}/bin/pg-claude-code-recorder serve --config ${configFile}${lib.optionalString cfg.pprof.enable " --pprof"}
            '';
            runAtLoad = true;
            keepAlive = true;
            serviceConfig = {
              StandardOutPath = "${logDir}/launchd-stdout.log";
              StandardErrorPath = "${logDir}/launchd-stderr.log";
              EnvironmentVariables = {
                # launchd's bare environment: HOME and XDG_STATE_HOME locate the
                # pprof socket; nothing is inherited from the user's shell.
                HOME = config.home.homeDirectory;
                XDG_STATE_HOME = config.xdg.stateHome;
                PG_CLAUDE_CODE_RECORDER_LAUNCH_LABEL = label;
              }
              // lib.optionalAttrs (cfg.pprof.enable && cfg.instance != "") {
                # a second instance gets its own profiler socket
                PG_CLAUDE_CODE_RECORDER_PPROF_ADDR = pprofSocket;
              };
            };
          };
    })
  ];
}
