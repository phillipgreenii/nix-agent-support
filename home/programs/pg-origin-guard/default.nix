{
  config,
  lib,
  pkgs,
  ...
}:

# pg-origin-guard: installs the binary on the per-user PATH and renders
# ${XDG_CONFIG_HOME}/pg-origin-guard/config.toml. The tool itself needs no nix
# (any machine config can write the same file); this module is one way to render
# it. Contract: packages/pg-origin-guard/docs/contract.md (section 5, Config).
#
# The module does NOT rewire any ssh config: a consuming flake renders the
# `ProxyCommand pg-origin-guard proxy --realm <name> -- ...` line itself.
#
# The log source for the event files is registered at darwin/system scope
# (darwin/modules/pg-origin-guard), because logSources is declared there.

let
  cfg = config.phillipgreenii.programs.pg-origin-guard;
  tomlFormat = pkgs.formats.toml { };

  prune = lib.filterAttrs (_: v: v != null);

  realmSettings =
    _: r:
    {
      login = {
        attempt_timeout = r.login.attemptTimeout;
        backoff = r.login.backoff;
      };
    }
    // lib.optionalAttrs (r.match.host != null || r.match.user != null) {
      match = prune {
        host = r.match.host;
        user = r.match.user;
      };
    }
    // lib.optionalAttrs (r.agentSocket != null) { agent_socket = r.agentSocket; };

  settings = {
    presence = prune {
      file = cfg.presence.file;
      max_age = cfg.presence.maxAge;
      refresh_seconds = cfg.presence.refreshSeconds;
    };
    breaker = {
      failure_threshold = cfg.breaker.failureThreshold;
      open_duration = cfg.breaker.openDuration;
      startup_window = cfg.breaker.startupWindow;
      established_after = cfg.breaker.establishedAfter;
    };
    events = {
      retention_days = cfg.events.retentionDays;
      heartbeat_interval = cfg.events.heartbeatInterval;
    };
    realm = lib.mapAttrs realmSettings cfg.realms;
  };

  configUnchecked = tomlFormat.generate "pg-origin-guard-config-unchecked.toml" settings;

  # Validated at BUILD time by the package's own config loader: `status` loads
  # the config first and exits 7 on a bad one (any other exit means it loaded).
  # HOME and the state base point at a scratch dir so nothing real is read.
  configFile =
    pkgs.runCommand "pg-origin-guard-config.toml"
      {
        nativeBuildInputs = [ cfg.package ];
      }
      ''
        export HOME="$TMPDIR/home" XDG_STATE_HOME="$TMPDIR/state" XDG_CONFIG_HOME="$TMPDIR/config"
        mkdir -p "$HOME" "$XDG_CONFIG_HOME/pg-origin-guard"
        cp ${configUnchecked} "$XDG_CONFIG_HOME/pg-origin-guard/config.toml"
        rc=0
        pg-origin-guard status >out.txt 2>&1 || rc=$?
        if [ "" = 7 ]; then
          echo "pg-origin-guard config rejected:" >&2
          cat out.txt >&2
          exit 1
        fi
        cp ${configUnchecked} "$out"
      '';

  durationType = lib.types.strMatching "[0-9]+(ns|us|ms|s|m|h)([0-9]+(ns|us|ms|s|m|h))*";
  mkDuration =
    default: description:
    lib.mkOption {
      type = durationType;
      inherit default description;
      example = default;
    };
in
{
  options.phillipgreenii.programs.pg-origin-guard = {
    enable = lib.mkEnableOption ''
      pg-origin-guard: stops unattended git-auth retries from opening browser login windows
      and reports whether auth to a realm is available. Installs the binary and renders
      `$XDG_CONFIG_HOME/pg-origin-guard/config.toml`. Contract:
      `packages/pg-origin-guard/docs/contract.md`
    '';

    package = lib.mkPackageOption pkgs "pg-origin-guard" { };

    presence = {
      file = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "/Users/me/.local/state/host-activity/userpresence.json";
        description = ''
          Absolute path of the presence file. Null keeps the tool's default,
          `<state base>/host-activity/userpresence.json`.
        '';
      };
      maxAge = mkDuration "60s" "Floor of the presence-file staleness rule (effective limit: max of this and twice refreshSeconds).";
      refreshSeconds = lib.mkOption {
        type = lib.types.ints.positive;
        default = 30;
        description = "The presence writer's refresh period, in seconds (mirrors its presenceFile.refreshSeconds).";
      };
    };

    breaker = {
      failureThreshold = lib.mkOption {
        type = lib.types.ints.positive;
        default = 2;
        description = "Consecutive non-auth startup failures that open a realm's breaker.";
      };
      openDuration = mkDuration "60s" "How long an open breaker refuses terminal-less callers.";
      startupWindow = mkDuration "10s" "A non-zero exit within this window is a startup failure.";
      establishedAfter = mkDuration "1s" "A child still running after this long is an established relay (must not exceed startupWindow).";
    };

    events = {
      retentionDays = lib.mkOption {
        type = lib.types.ints.positive;
        default = 14;
        description = "Event files (`events-YYYY-MM-DD.jsonl`) older than this many days are deleted on the first write of a day.";
      };
      heartbeatInterval = mkDuration "5m" "While a realm is unavailable, a state event is repeated at this interval by whichever caller runs.";
    };

    realms = lib.mkOption {
      default = { };
      description = ''
        Realms by name (one authentication domain, one certificate; the name is lowercase
        letters, digits, `_` and `-`). The ssh `ProxyCommand` of a realm names it with
        `--realm`.
      '';
      example = lib.literalExpression ''
        {
          zr = {
            match = {
              host = "github.com";
              user = "org-*";
            };
          };
        }
      '';
      type = lib.types.attrsOf (
        lib.types.submodule {
          options = {
            match = {
              host = lib.mkOption {
                type = lib.types.nullOr lib.types.str;
                default = null;
                description = "Glob on the remote host, used only by `check --remote`.";
              };
              user = lib.mkOption {
                type = lib.types.nullOr lib.types.str;
                default = null;
                description = "Glob on the remote user, used only by `check --remote`.";
              };
            };
            agentSocket = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              description = "Absolute path of the ssh-agent socket tried before `SSH_AUTH_SOCK`. Its owner must be the current user.";
            };
            login = {
              attemptTimeout = mkDuration "3m" "Bound on one terminal-less login attempt.";
              backoff = lib.mkOption {
                type = lib.types.listOf durationType;
                default = [
                  "5m"
                  "10m"
                  "30m"
                ];
                description = "Spacing between failed login attempts; the last step repeats.";
              };
            };
          };
        }
      );
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    xdg.configFile."pg-origin-guard/config.toml".source = configFile;

    programs.tldr.customPages = lib.mkIf config.programs.tldr.enable {
      pg-origin-guard = {
        platform = "common";
        source = "${cfg.package}/share/tldr/pages.common/pg-origin-guard.md";
      };
    };
  };
}
