{
  config,
  lib,
  pkgs,
  options,
  ...
}:

# pg-assistants: the minimal supervisor for permanent Claude Code assistants
# (ADR 0128). This module renders the daemon's config.toml from the options
# below, checks it at build time with the real `pg-assistants config check`
# (strict: any problem fails the build), installs the tool through the per-user
# profile (an assistant runs `pg-assistants exit` from its Bash tool, so the
# command MUST be on the session's PATH), registers its tldr page, marks every
# assistant's directory trusted at activation (best effort), and registers the
# daemon as an HM-scoped launchd user agent through
# `phillipgreenii.programs.launchdServices.userAgents` (this repo's CLAUDE.md,
# "HM-scoped launchd agents"; the pg-keep-awake and pg-claude-code-recorder
# precedents).
#
# HM-scoped launchd rules this module states and honors:
#   - the registry option is declared by phillipgreenii-nix-personal's home
#     module; its DECLARATION gates the attribute (never pkgs, which can be a
#     _module.args value: choosing attribute names from it is infinite
#     recursion), and the platform gates the entry's value;
#   - every runtime dependency (tmux, claude, pg-claude-control, the login
#     shell) is an absolute path in the rendered config or the script, never
#     launchd's bare PATH;
#   - the launchd stdout/stderr files live in
#     $XDG_STATE_HOME/pg-assistants-launchd, outside the daemon's 0700 state
#     directory (`pg-assistants status` names that stderr path in its warning);
#   - `manageLogs` stays at its default (on).
#
# Platforms: darwin is supervised by launchd. Linux has no supervisor on day 1:
# the module registers nothing and warns, and still renders the config.

let
  cfg = config.phillipgreenii.programs.pg-assistants;
  tomlFormat = pkgs.formats.toml { };
  isDarwin = pkgs.stdenv.hostPlatform.isDarwin;
  home = config.home.homeDirectory;

  # Whether the composing flake imports personal's home module, which declares
  # the HM launchdServices registry. Read from the module `options` argument,
  # never from `config`/`pkgs`; see home/programs/pa-monitor.
  hasLaunchdRegistry = options.phillipgreenii.programs ? launchdServices;

  # The control module and the marketplace override are read only where they
  # are declared (the recorder module's gating), so this module also evaluates
  # in a composition without them.
  hasControl = options.phillipgreenii.programs ? pg-claude-control;
  control = if hasControl then config.phillipgreenii.programs.pg-claude-control else null;
  hasMarketplaces =
    options.phillipgreenii.programs ? claude-code
    && options.phillipgreenii.programs.claude-code ? marketplaces;
  overrides =
    if hasMarketplaces then config.phillipgreenii.programs.claude-code.marketplaces.overrides else { };
  # An override is keyed by the bare plugin name or by "<plugin>@<marketplace>".
  controlPluginKeys = [
    "pg-claude-control"
    "pg-claude-control@phillipgreenii-nix-agent-support-marketplace-local"
  ];
  controlPluginOff = lib.any (k: (overrides.${k} or null) == false) controlPluginKeys;

  controlPackage = if hasControl then control.package else pkgs.pg-claude-control;

  # Every [paths] entry MUST be a literal absolute path: the daemon reads the
  # file under launchd, where no shell expands a variable. A `$HOME` or
  # `${HOME}` in CLAUDE_CONFIG_DIR is expanded here, at evaluation.
  expandHome = lib.replaceStrings [ "\${HOME}" "$HOME" ] [ home home ];
  claudeConfigDir = config.home.sessionVariables.CLAUDE_CONFIG_DIR or null;
  defaultClaudeJson =
    if claudeConfigDir != null then
      "${expandHome (toString claudeConfigDir)}/.claude.json"
    else
      "${home}/.claude.json";

  stateDir = "${config.xdg.stateHome}/pg-assistants";
  logDir = "${config.xdg.stateHome}/pg-assistants-launchd";
  pprofSocket = "${stateDir}/pprof.sock";

  # A hash of the operator's session variables, so an apply that changes one
  # restarts the daemon (whose own login shell, and so every pg-claude-control
  # call it makes, then sees the new value). Its string context is dropped: it
  # is a value in the job definition, not a dependency, and nothing is built.
  sessionVarsHash = builtins.hashString "sha256" (
    builtins.unsafeDiscardStringContext (builtins.toJSON config.home.sessionVariables)
  );

  # The variables the launchd agent sets for the daemon alone (HOME aside) and
  # launchd's own job name. The tmux server keeps the daemon's environment and
  # hands it to every session, so each is rendered into blank_env.
  daemonOnlyEnv = [
    "PG_ASSISTANTS_CONFIG_HASH"
    "PG_ASSISTANTS_PPROF_ADDR"
    "PG_ASSISTANTS_SESSION_VARS_HASH"
    "XPC_SERVICE_NAME"
  ];

  # The rendered configuration. Only keys the loader knows are rendered (an
  # unknown key fails `config check`); null options are omitted; the
  # assistants are an array of tables ([[assistant]]), ordered by id.
  renderAssistant =
    id:
    let
      a = cfg.assistants.${id};
    in
    {
      inherit id;
      inherit (a)
        name
        enabled
        model
        effort
        cwd
        prompt
        ;
      context = {
        inherit (a.context)
          limit
          nudge
          interrupt
          kill
          ;
      }
      // lib.optionalAttrs (a.context.window != null) { inherit (a.context) window; };
    }
    // lib.optionalAttrs (a.tick != null) { inherit (a) tick; }
    // lib.optionalAttrs (a.env != { }) { inherit (a) env; };

  settings = {
    inherit (cfg) tick;
    blank_env = lib.unique (cfg.sessionBlankEnv ++ daemonOnlyEnv);
    grace = {
      inherit (cfg.grace) interrupt kill;
    };
    blocks = {
      inherit (cfg.blocks) nudge interrupt kill;
      unknown_reset_hold = cfg.blocks.unknownResetHold;
    };
    restart = {
      min_uptime = cfg.restart.minUptime;
      backoff_initial = cfg.restart.backoffInitial;
      backoff_max = cfg.restart.backoffMax;
      start_timeout = cfg.restart.startTimeout;
    };
    paths = {
      tmux = "${pkgs.tmux}/bin/tmux";
      claude = cfg.claudePath;
      pg_claude_control = "${controlPackage}/bin/pg-claude-control";
      shell = cfg.loginShell;
      state_dir = stateDir;
      claude_json = cfg.claudeJson;
    };
  }
  # An empty `assistant = []` would be an array, not an array of tables.
  // lib.optionalAttrs (cfg.assistants != { }) {
    assistant = map renderAssistant (lib.attrNames cfg.assistants);
  };

  configUnchecked = tomlFormat.generate "pg-assistants-config-unchecked.toml" settings;

  # Checked at BUILD time by the package's own strict `config check`, so a
  # config any rule rejects (an unknown effort, `limit * kill` at or past the
  # window, a shared cwd, a value ending in `\;`, a `#` in a cwd) fails the
  # build and never reaches launchd.
  configFile =
    pkgs.runCommand "pg-assistants-config.toml"
      {
        nativeBuildInputs = [ cfg.package ];
      }
      ''
        pg-assistants config check --config ${configUnchecked}
        cp ${configUnchecked} "$out"
      '';

  # A hash of the rendered config for the job definition: nothing restarts a
  # launchd agent because a file it read at startup changed, so this value
  # rides in the agent's environment and any config change restarts the
  # daemon (the sessions survive it: AbandonProcessGroup, and the next daemon
  # adopts them). It hashes the generated file's store path, a pure function of
  # the file's content, so no build is forced at evaluation time.
  configHash = builtins.hashString "sha256" (
    builtins.unsafeDiscardStringContext (toString configUnchecked)
  );

  # The one shutdown deadline, owned here: passed to the daemon
  # (`--shutdown-deadline`) and launchd's ExitTimeOut derived from it, so the
  # daemon's own hard exit always comes before launchd's SIGKILL.
  shutdownDeadlineSeconds = 8;
  exitTimeOutMarginSeconds = 7;
  exitTimeOutSeconds = shutdownDeadlineSeconds + exitTimeOutMarginSeconds;

  shell = lib.escapeShellArg cfg.loginShell;
  cwds = map (id: cfg.assistants.${id}.cwd) (lib.attrNames cfg.assistants);
  isAbsolute = lib.hasPrefix "/";
  relativeCwds = lib.filter (id: !isAbsolute cfg.assistants.${id}.cwd) (lib.attrNames cfg.assistants);

  # A value that reaches tmux as an argument MUST NOT end in `\;`, and a cwd
  # MUST NOT contain `#`; the loader enforces both and the build fails.
  tmuxNote = ''
    It reaches tmux as an argument, so it MUST NOT end in `\;` (tmux cannot
    pass that on intact); `config check` rejects it and the build fails.
  '';

  durationOption =
    default: description:
    lib.mkOption {
      type = lib.types.str;
      inherit default;
      description = ''
        ${description} A positive Go duration string (`"30s"`, `"5m"`);
        `config check` rejects anything else and the build fails.
      '';
    };

  pointOption =
    default: description:
    lib.mkOption {
      type = lib.types.number;
      inherit default;
      inherit description;
    };

  assistantModule =
    { name, ... }:
    {
      options = {
        name = lib.mkOption {
          type = lib.types.str;
          default = name;
          defaultText = lib.literalMD "the assistant's id (the attribute name)";
          description = ''
            The session's display name (`claude --name`). ${tmuxNote}
          '';
        };
        enabled = lib.mkOption {
          type = lib.types.bool;
          default = true;
          description = ''
            Whether the daemon keeps this assistant running. `false` stops a
            running session through the disabled ladder (nudge, interrupt,
            kill). To remove an assistant, set this to false, deploy, wait
            until `pg-assistants status` shows it stopped, then delete the
            entry (deleting it at once also works: the orphan sweep stops it).
          '';
        };
        model = lib.mkOption {
          type = lib.types.str;
          example = "claude-sonnet-5-5";
          description = ''
            The model id (`claude --model`). An id the package's context-window
            table does not know needs `context.window`. ${tmuxNote}
          '';
        };
        effort = lib.mkOption {
          type = lib.types.str;
          default = "medium";
          description = ''
            The effort level (`claude --effort`): `low`, `medium`, `high`,
            `xhigh` or `max`; `config check` rejects anything else.
          '';
        };
        cwd = lib.mkOption {
          type = lib.types.str;
          example = "/Users/example/assistants/inbox-keeper";
          description = ''
            The assistant's working directory: its identity and duties live in
            the `AGENTS.md` / `CLAUDE.md` there, and it keeps its notes there.
            MUST be absolute (asserted) and unique across assistants (one
            assistant per directory), and MUST NOT contain `#` (tmux expands a
            format in the start directory, and a directory it then cannot find
            silently starts the session in `$HOME`). ${tmuxNote} Whether it
            exists is checked by the daemon before each start, not here.
          '';
        };
        prompt = lib.mkOption {
          type = lib.types.str;
          example = "Resume your duties per AGENTS.md.";
          description = ''
            The first message of every session. MUST NOT start with `-`.
            ${tmuxNote}
          '';
        };
        tick = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          example = "5m";
          description = ''
            This assistant's own reconcile interval (a Go duration); null uses
            the global `tick`. An assistant that should poll less often gets a
            longer tick: after it exits, its next session starts on its next
            tick.
          '';
        };
        env = lib.mkOption {
          type = lib.types.attrsOf lib.types.str;
          default = { };
          example = {
            EXAMPLE = "value";
          };
          description = ''
            Extra session environment, on top of the operator's own login-shell
            environment. Each value ${tmuxNote}
          '';
        };
        context = {
          limit = lib.mkOption {
            type = lib.types.ints.positive;
            example = 200000;
            description = ''
              The session's context budget, in tokens. The ladder's points are
              fractions of it, and `limit * kill` MUST be strictly below the
              model's context window (auto-compact is off, so a kill point at or
              past the window never fires); `config check` enforces it.
            '';
          };
          window = lib.mkOption {
            type = lib.types.nullOr lib.types.ints.positive;
            default = null;
            description = ''
              The model's context window, in tokens. Null takes it from the
              package's table of exact model ids; required only for a model the
              table does not know, and accepted for any model.
            '';
          };
          nudge = pointOption 0.8 ''
            The fill (`tokens / limit`) at which the session is asked to finish,
            write its notes and exit. A module default: the config file itself
            has none. `0 < nudge < interrupt <= kill`.
          '';
          interrupt = pointOption 0.95 ''
            The fill at which the turn is interrupted and the session told to
            stop now. A module default: the config file itself has none.
          '';
          kill = pointOption 1.0 ''
            The fill at which the session is terminated. MAY exceed 1.0.
          '';
        };
      };
    };
in
{
  options.phillipgreenii.programs.pg-assistants = {
    enable = lib.mkEnableOption ''
      pg-assistants: a minimal supervisor that keeps permanent Claude Code
      assistants running, each in its own session on the `tmux -L pg-assistants`
      server, and recycles each session through a nudge, interrupt, kill ladder
      as its context or the account's usage blocks fill (ADR 0128). Renders
      `config.toml` (checked at build time), installs the command, pre-trusts the
      assistants' directories and, on darwin, registers the daemon as a
      keep-alive launchd user agent through
      `phillipgreenii.programs.launchdServices.userAgents`, which
      phillipgreenii-nix-personal's home module declares and the composing flake
      MUST also import. Needs `phillipgreenii.programs.pg-claude-control` (and its
      plugin) enabled
    '';

    package = lib.mkPackageOption pkgs "pg-assistants" { };

    pprof.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        The workspace's opt-in profiling endpoint (`docs/go-daemons.md` in
        `phillipg-nix-repo-base`): the agent's environment sets
        `PG_ASSISTANTS_PPROF_ADDR` to `pprof.sock` in the state directory.
      '';
    };

    tick = durationOption "1m" "The global reconcile interval; an assistant MAY override it.";

    grace = {
      interrupt = durationOption "5m" "Time from a nudge to the interrupt, on every ladder.";
      kill = durationOption "2m" "Time from an interrupt to the kill, on every ladder.";
    };

    blocks = {
      nudge = pointOption 0.95 ''
        Fraction of the account's 5-hour or 7-day block at which every running
        session is nudged to stop and new starts are held until the block
        resets.
      '';
      interrupt = pointOption 0.98 "Block fraction at which every running session is interrupted.";
      kill = pointOption 1.0 "Block fraction at which every running session is terminated.";
      unknownResetHold = durationOption "1h" "How long starts are held when a held block reports no reset time.";
    };

    restart = {
      minUptime = durationOption "1m" "A session that dies sooner (and did not ask to exit) counts as a crash.";
      backoffInitial = durationOption "1m" "The first crash backoff; it doubles per crash.";
      backoffMax = durationOption "30m" "The largest crash backoff.";
      startTimeout = durationOption "5m" ''
        A session whose control mod has not answered `usage` by then is a
        stalled start: it is killed and counted as a crash.
      '';
    };

    claudePath = lib.mkOption {
      type = lib.types.str;
      default = "${pkgs.llm-agentsPkgs.claude-code}/bin/claude";
      defaultText = lib.literalExpression ''"''${pkgs.llm-agentsPkgs.claude-code}/bin/claude"'';
      description = ''
        The absolute path of the `claude` the sessions run (rendered as
        `paths.claude`); by default the package `home/programs/claude-code`
        installs. ${tmuxNote}
      '';
    };

    loginShell = lib.mkOption {
      type = lib.types.str;
      default = "/bin/zsh";
      description = ''
        The operator's login shell, as an absolute path (asserted; rendered as
        `paths.shell`). Every session runs claude through it as an interactive
        login shell (`-l -i`) and the daemon runs through it as a login shell
        (`-l`), so both get the operator's own `PATH` and every
        `home.sessionVariables` entry. That holds only where home-manager
        manages this shell's startup files. ${tmuxNote}
      '';
    };

    sessionBlankEnv = lib.mkOption {
      type = lib.types.listOf (lib.types.strMatching "[A-Za-z_][A-Za-z0-9_]*");
      default = [
        "__HM_SESS_VARS_SOURCED"
        "__HM_ZSH_SESS_VARS_SOURCED"
        "__NIX_DARWIN_SET_ENVIRONMENT_DONE"
      ];
      description = ''
        Variables every session starts with set and empty (rendered as
        `blank_env`, followed by the variables the launchd agent sets for the
        daemon alone and launchd's `XPC_SERVICE_NAME`). The tmux server keeps
        the environment of the daemon that started it, whose login shell has
        already run the operator's startup files and exported their run-once
        guards; a session that inherits a guard skips that file and keeps the
        server's old `PATH` and session variables. The defaults are the guards
        of home-manager's `hm-session-vars.sh` and zsh session variables (each
        file returns or skips when its guard is non-empty) and of nix-darwin's
        `set-environment` (sourced by `/etc/zshenv` only when its guard is
        empty), so an empty value runs each file again. Add the guard of any
        other startup file the login shell runs once.
      '';
    };

    claudeJson = lib.mkOption {
      type = lib.types.str;
      default = defaultClaudeJson;
      defaultText = lib.literalMD ''
        `''${CLAUDE_CONFIG_DIR}/.claude.json` when `home.sessionVariables.CLAUDE_CONFIG_DIR`
        is set (a `$HOME` in it expanded at evaluation), else
        `''${config.home.homeDirectory}/.claude.json`
      '';
      description = ''
        Claude's `.claude.json`, where folder trust is written (rendered as
        `paths.claude_json`): an untrusted directory stops claude on the trust
        dialog before any hook runs. MUST be a literal absolute path.
      '';
    };

    assistants = lib.mkOption {
      type = lib.types.attrsOf (lib.types.submodule assistantModule);
      default = { };
      example = lib.literalExpression ''
        {
          inbox-keeper = {
            name = "Inbox Keeper";
            model = "claude-sonnet-5-5";
            cwd = "/Users/example/assistants/inbox-keeper";
            prompt = "Resume your duties per AGENTS.md.";
            context.limit = 200000;
          };
        }
      '';
      description = ''
        The assistants, keyed by id: `[a-z0-9-]+`, unique, and the name of the
        assistant's tmux session (`pg-assistants attach <id>`). An id is part of
        file paths, so `config check` rejects any other form.
      '';
    };

    internal = {
      settings = lib.mkOption {
        type = lib.types.attrs;
        internal = true;
        readOnly = true;
        default = settings;
        description = "The rendered configuration as data. Read-only; used by tests.";
      };

      configUnchecked = lib.mkOption {
        type = lib.types.path;
        internal = true;
        readOnly = true;
        default = configUnchecked;
        defaultText = lib.literalMD "the rendered config.toml before `config check` runs on it";
        description = "The rendered configuration file, before the build-time check. Read-only; used by tests.";
      };

      configFile = lib.mkOption {
        type = lib.types.path;
        internal = true;
        readOnly = true;
        default = configFile;
        defaultText = lib.literalMD "the rendered, build-time-checked config.toml";
        description = "The rendered, build-time-checked configuration file in the store. Read-only.";
      };

      configHash = lib.mkOption {
        type = lib.types.str;
        internal = true;
        readOnly = true;
        default = configHash;
        defaultText = lib.literalMD "the hash carried in the launchd job definition";
        description = "The config hash the launchd job definition carries. Read-only.";
      };
    };
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      # `status` and `attach` read ${XDG_CONFIG_HOME:-~/.config}/pg-assistants/config.toml
      # by default; the daemon is launched with the store path explicitly.
      xdg.configFile."pg-assistants/config.toml".source = configFile;

      # Through the per-user profile: an assistant runs `pg-assistants exit`
      # from its Bash tool, so the command MUST be on the session's PATH.
      home.packages = [ cfg.package ];

      programs.tldr.customPages.pg-assistants = lib.mkIf config.programs.tldr.enable {
        platform = "common";
        source = "${cfg.package}/share/tldr/pages.common/pg-assistants.md";
      };

      # Pre-trust every assistant's directory, the shape of ccpool's activation.
      # Best effort: a failure must not break activation, and the daemon checks
      # trust before each start and writes it again when it is missing (Claude's
      # own rewrites of the file can lose this write).
      home.activation = lib.mkIf (cwds != [ ]) {
        pgAssistantsTrust = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
          $DRY_RUN_CMD ${cfg.package}/bin/pg-assistants trust --claude-json ${lib.escapeShellArg cfg.claudeJson} ${lib.escapeShellArgs cwds} || true
        '';
      };

      assertions = [
        {
          assertion = exitTimeOutSeconds <= 60;
          message = "pg-assistants: shutdown deadline (${toString shutdownDeadlineSeconds}s) plus margin (${toString exitTimeOutMarginSeconds}s) exceeds launchd's 60s ExitTimeOut clamp.";
        }
        {
          assertion = !hasControl || control.enable;
          message = "phillipgreenii.programs.pg-assistants needs phillipgreenii.programs.pg-claude-control.enable: every action the daemon takes on a session (usage, submit, interrupt, stop) goes through pg-claude-control and its control mod.";
        }
        {
          assertion = !controlPluginOff;
          message = "phillipgreenii.programs.pg-assistants needs the pg-claude-control plugin, but phillipgreenii.programs.claude-code.marketplaces.overrides turns it off: the binary would stay while no session ever loads the control mod, so every start would stall.";
        }
        {
          assertion = relativeCwds == [ ];
          message = "phillipgreenii.programs.pg-assistants.assistants: every cwd must be an absolute path; not absolute: ${
            lib.concatMapStringsSep ", " (id: "${id} (${cfg.assistants.${id}.cwd})") relativeCwds
          }.";
        }
        {
          assertion = isAbsolute cfg.loginShell;
          message = "phillipgreenii.programs.pg-assistants.loginShell (${cfg.loginShell}) must be an absolute path: the daemon runs under launchd, which has no useful PATH.";
        }
        {
          assertion = !isDarwin || hasLaunchdRegistry;
          message = "phillipgreenii.programs.pg-assistants.enable on darwin needs phillipgreenii.programs.launchdServices, which phillipgreenii-nix-personal's home module declares (personal ADR 0055); import it alongside this module.";
        }
      ];

      warnings =
        lib.optional (!isDarwin)
          "phillipgreenii.programs.pg-assistants.enable: there is no supervisor on this platform, so no agent is registered; run `pg-assistants daemon --config ~/.config/pg-assistants/config.toml` yourself.";
    })

    # LaunchAgent registration via the HM-scoped launchdServices registry
    # (personal ADR 0055): gated on `enable` and the platform at ENTRY level
    # (mkIf) and on the option's declaration at attribute level (optionalAttrs).
    (lib.optionalAttrs hasLaunchdRegistry {
      phillipgreenii.programs.launchdServices.userAgents.pg-assistants-daemon =
        lib.mkIf (cfg.enable && isDarwin)
          {
            label = "com.phillipg.pg-assistants-daemon";
            # The daemon runs through the operator's login shell (`-l`), so it and
            # every pg-claude-control call it makes see the operator's session
            # variables (PG_CLAUDE_CONTROL_STATE_DIR and the rest). The binary and
            # the config's store paths are in the command, so a changed binary or
            # config changes the job.
            script = ''
              exec ${shell} -l -c 'exec "$@"' ${shell} ${cfg.package}/bin/pg-assistants daemon --config ${configFile} --shutdown-deadline ${toString shutdownDeadlineSeconds}s
            '';
            runAtLoad = true;
            keepAlive = true;
            serviceConfig = {
              # The tmux server and the assistants are in the daemon's process
              # group. launchd.plist(5): when a job dies launchd kills the rest of
              # its process group unless this key is true. A daemon restart (a
              # deploy, a crash) MUST NOT kill the assistants; the next daemon
              # adopts their sessions.
              AbandonProcessGroup = true;
              # launchd waits this long after SIGTERM before SIGKILL, derived from
              # the shutdown deadline passed to the daemon (see above). Change the
              # deadline, never ExitTimeOut alone.
              ExitTimeOut = exitTimeOutSeconds;
              StandardOutPath = "${logDir}/launchd-stdout.log";
              StandardErrorPath = "${logDir}/launchd-stderr.log";
              EnvironmentVariables = {
                # launchd's bare environment: HOME is how the login shell finds
                # its startup files.
                HOME = home;
                # The hash of the rendered config (see configHash above).
                PG_ASSISTANTS_CONFIG_HASH = configHash;
                # The hash of the session variables (see sessionVarsHash above).
                PG_ASSISTANTS_SESSION_VARS_HASH = sessionVarsHash;
              }
              // lib.optionalAttrs cfg.pprof.enable {
                PG_ASSISTANTS_PPROF_ADDR = pprofSocket;
              };
            };
          };
    })
  ];
}
