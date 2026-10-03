{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pg-rescue;
  tomlFormat = pkgs.formats.toml { };

  # One `[handler.NAME]` instance (design 2026-10-02-pg-rescue-design.md,
  # section 2.4). Every key except `command` is optional; an unset key is
  # omitted from the rendered TOML so pg-rescue's own default applies
  # (timeout 5m, no env, no tags, ...) instead of this module restating it.
  handlerType = lib.types.submodule {
    options = {
      command = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        description = ''
          The handler's argv. `command[0]` is resolved on `PATH` when the
          handler is spawned.
        '';
        example = [
          "pg-rescue-notify"
        ];
      };
      timeout = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          Go `time.ParseDuration` syntax. When it expires the handler's process
          group gets SIGTERM, then SIGKILL 5s later, and the attempt is
          `failed`. `null` leaves pg-rescue's own default (`5m`).
        '';
        example = "3m";
      };
      env = lib.mkOption {
        type = lib.types.attrsOf lib.types.str;
        default = { };
        description = ''
          Non-secret variables merged into the handler's environment.

          WARNING: the generated config is world-readable. It lives in
          `/nix/store`, so anything set here is readable by every user on the
          machine. Never put a secret here; use `env_file`.
        '';
      };
      env_file = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          Path to a dotenv file read when the handler is spawned. Use it for
          secrets: the generated config lives in the world-readable
          `/nix/store`, but this file is not part of it. pg-rescue treats a
          group- or world-readable file as a config error, so keep it `0600`
          and manage it outside nix.
        '';
        example = "~/.config/pg-rescue/fix-large.env";
      };
      description = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Shown on `-vv` delimiter lines.";
      };
      tags = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = ''
          Free-form strings. pg-rescue never interprets them; they are copied
          into each run-log attempt for measurement. The default instances use
          `deterministic`, `agent` and `deferral`; the common-case rate in the
          design (section 7.2) is computed from `deterministic`, so tag any
          handler you add that is a deterministic script with it.
        '';
      };
    };
  };

  renderHandler =
    h:
    {
      inherit (h) command;
    }
    // lib.optionalAttrs (h.timeout != null) { inherit (h) timeout; }
    // lib.optionalAttrs (h.env != { }) { inherit (h) env; }
    // lib.optionalAttrs (h.env_file != null) { inherit (h) env_file; }
    // lib.optionalAttrs (h.description != null) { inherit (h) description; }
    // lib.optionalAttrs (h.tags != [ ]) { inherit (h) tags; };

  configAttrs = {
    inherit (cfg) redact;
    handler = lib.mapAttrs (_: renderHandler) cfg.handlers;
    chain = lib.mapAttrs (_: c: { inherit (c) handlers; }) cfg.chains;
  };

  # The default instances (design section 2.4's example, minus anything
  # machine- or employer-specific). Each FIELD is set with `mkDefault`, not
  # each instance, so a consumer can override one field of a default instance
  # (say `handlers.fix-small.timeout`) and keep the rest. Overriding a whole
  # instance's `command` is likewise a plain assignment.
  defaultHandlers = {
    flake-lock-conflict = {
      command = [ "pg-rescue-flake-lock-conflict" ];
      timeout = "2m";
      description = "deterministic flake.lock-only rebase resolver";
      tags = [ "deterministic" ];
    };
    fix-small = {
      command = [
        "pg-rescue-claude"
        "--model"
        "haiku"
        "--time-limit"
        "2m"
        "--strict-mcp-config"
      ];
      timeout = "3m";
      description = "haiku, 2m, no MCP";
      tags = [ "agent" ];
    };
    fix-large = {
      command = [
        "pg-rescue-claude"
        "--model"
        "sonnet"
        "--time-limit"
        "8m"
        "--allowed-tools"
        "Bash,Read,Edit"
      ];
      timeout = "10m";
      description = "sonnet, 8m";
      tags = [ "agent" ];
    };
    p1-later = {
      command = [
        "pg-rescue-bead"
        "--priority"
        "1"
        "--dedup-query"
        "pg-rescue-open"
      ];
      timeout = "1m";
      description = "file a P1 deferral item, deduplicated by fingerprint";
      tags = [ "deferral" ];
    };
    notify = {
      command = [ "pg-rescue-notify" ];
      timeout = "10s";
      description = "macOS notification; always declines";
    };
  };

  defaultChains = {
    sync.handlers = [
      "flake-lock-conflict"
      "fix-small"
      "fix-large"
      "p1-later"
      "notify"
    ];
  };
in
{
  options.phillipgreenii.programs.pg-rescue = {
    enable = lib.mkEnableOption ''
      pg-rescue (script-first command runner: wraps a command and, when it
      fails, walks a named chain of failure handlers). Installs `pg-rescue`, its
      reference handlers (`pg-rescue-claude`, `pg-rescue-bead`,
      `pg-rescue-notify`), `pg-rescue-flake-lock-conflict` and its first
      consumer `sync-projects` (rebase every workspace repo under the `sync`
      chain, then `pn workspace push`), and generates
      `$XDG_CONFIG_HOME/pg-rescue/config.toml`.

      WARNING: the generated config.toml is a `/nix/store` path and therefore
      WORLD-READABLE. Never put a secret in `redact`, `handlers.<name>.env` or
      a command argument; point `handlers.<name>.env_file` at a private file
      instead
    '';

    package = lib.mkPackageOption pkgs "pg-rescue" { };

    flakeLockConflictPackage = lib.mkPackageOption pkgs "pg-rescue-flake-lock-conflict" { };

    syncProjectsPackage = lib.mkPackageOption pkgs "sync-projects" { };

    redact = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = ''
        Go RE2 regexes. Matches are replaced with `[REDACTED]` in every copy of
        captured text: the report's output tail, handler details, prompts and
        bead bodies rendered by the reference handlers, the display and the run
        log. `output.log` and the per-attempt files stay raw.

        The patterns themselves land in the world-readable `/nix/store`, so
        write patterns that describe the SHAPE of a secret, never a literal
        secret.
      '';
      example = [
        "ghp_[A-Za-z0-9]{36}"
        "Authorization: \\S+"
      ];
    };

    handlers = lib.mkOption {
      type = lib.types.attrsOf handlerType;
      default = { };
      description = ''
        Named handler instances, rendered as `[handler.NAME]` tables. The
        defaults (`flake-lock-conflict`, `fix-small`, `fix-large`, `p1-later`,
        `notify`) are set per field with `mkDefault`, so any of them can be
        overridden wholesale or one field at a time, and further instances can
        be added.

        WARNING: the generated config is world-readable (it lives in
        `/nix/store`). Keep secrets out of `command` and `env`; use `env_file`.
      '';
      example = lib.literalExpression ''
        {
          fix-small.timeout = "5m";
          fix-large.env_file = "/Users/me/.config/pg-rescue/fix-large.env";
        }
      '';
    };

    chains = lib.mkOption {
      type = lib.types.attrsOf (
        lib.types.submodule {
          options.handlers = lib.mkOption {
            type = lib.types.nonEmptyListOf lib.types.str;
            description = "Ordered, non-empty list of handler instance names.";
          };
        }
      );
      default = { };
      description = ''
        Named chains, rendered as `[chain.NAME]` tables and selected with
        `pg-rescue --chain NAME`. The default `sync` chain runs
        `flake-lock-conflict`, `fix-small`, `fix-large`, `p1-later`, `notify`
        in that order.
      '';
      example = {
        quick.handlers = [
          "flake-lock-conflict"
          "notify"
        ];
      };
    };
  };

  config = lib.mkIf cfg.enable {
    phillipgreenii.programs.pg-rescue = {
      handlers = lib.mapAttrs (_: lib.mapAttrs (_: lib.mkDefault)) defaultHandlers;
      chains = lib.mapAttrs (_: lib.mapAttrs (_: lib.mkDefault)) defaultChains;
    };

    home.packages = [
      cfg.package
      cfg.flakeLockConflictPackage
      cfg.syncProjectsPackage
    ];

    xdg.configFile."pg-rescue/config.toml".source =
      tomlFormat.generate "pg-rescue-config.toml" configAttrs;

    programs.tldr.customPages.pg-rescue = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.package}/share/tldr/pages.common/pg-rescue.md";
    };

    programs.tldr.customPages.sync-projects = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.syncProjectsPackage}/share/tldr/pages.common/sync-projects.md";
    };
  };
}
