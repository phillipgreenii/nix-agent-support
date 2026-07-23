{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.codeburn;
  jsonFormat = pkgs.formats.json { };
  isDarwin = pkgs.stdenv.hostPlatform.isDarwin;
in
{
  # CodeBurn (https://github.com/getagentseal/codeburn): a local AI-coding token/cost tracker.
  # Three surfaces, each independently toggleable and gated by what codeburn supports:
  #   - terminal: the CLI/TUI, any system (the packaged npm binary on PATH)
  #   - web:      `codeburn web`, any system for the CLI; on darwin also run as a launchd user
  #               agent (see darwin/modules/codeburn) and tied into the phillipg.localhost portal
  #   - menubar:  the macOS menubar app, darwin-only, installed via codeburn's own downloader
  #
  # terminal and web are the SAME binary (web is a subcommand), so enabling either puts the CLI
  # on PATH. Config is nix-owned and read-only: codeburn only reads ~/.config/codeburn/config.json
  # (verified — it never writes it at runtime; its writable state lives in ~/.cache/codeburn).
  options.phillipgreenii.programs.codeburn = {
    enable = lib.mkEnableOption "CodeBurn AI coding token usage & cost tracker";

    package = lib.mkPackageOption pkgs "codeburn" { };

    terminal.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Install the codeburn CLI (interactive TUI + report/optimize/etc. subcommands) on PATH.";
    };

    web = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Run the codeburn web dashboard as a background localhost service. On darwin this
          registers a launchd user agent (`codeburn web --port <port> --no-open`) via
          `phillipgreenii.system.launchdServices` (see darwin/modules/codeburn); a consuming
          machine ties `<port>` into its local reverse-proxy/landing page separately. On
          non-darwin this only ensures the CLI is installed — there is no service wiring.
        '';
      };
      port = lib.mkOption {
        type = lib.types.port;
        default = 4747;
        description = "Loopback port the web dashboard service listens on (codeburn's default is 4747).";
      };
    };

    menubar.enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Install and launch the macOS menubar app via codeburn's own installer
        (`codeburn menubar`, which downloads a signed `.app` into ~/Applications). macOS-only;
        enabling it on another platform is a hard eval error. This is an escape-hatch
        (network-at-activation, not a pure derivation) — codeburn does not distribute the
        menubar app through nixpkgs.
      '';
    };

    settings = lib.mkOption {
      inherit (jsonFormat) type;
      default = { };
      example = {
        currency = {
          code = "GBP";
        };
      };
      description = ''
        Written verbatim to ~/.config/codeburn/config.json (read-only, nix-owned). Only
        rendered when non-empty; otherwise codeburn uses its built-in defaults. See codeburn's
        docs for the schema. NOTE: `currency` must be an OBJECT (`{ code = "EUR"; symbol =
        "€"; }`), never a bare string — a string crashes codeburn on startup. Because the file
        is read-only, `codeburn config set …`/`codeburn currency …` won't persist at runtime;
        change this option instead.
      '';
    };
  };

  config = lib.mkIf cfg.enable (
    lib.mkMerge [
      {
        assertions = [
          {
            assertion = !cfg.menubar.enable || isDarwin;
            message = "phillipgreenii.programs.codeburn.menubar.enable is macOS-only (codeburn ships the menubar app for darwin only).";
          }
        ];

        # terminal and web share one binary; install it if either surface is on.
        home.packages = lib.optional (cfg.terminal.enable || cfg.web.enable) cfg.package;

        # Declarative, read-only config. Only manage the file when the user actually declares
        # something — an absent config.json is valid (codeburn falls back to defaults).
        xdg.configFile."codeburn/config.json" = lib.mkIf (cfg.settings != { }) {
          source = jsonFormat.generate "codeburn-config.json" cfg.settings;
        };
      }

      # Menubar escape-hatch (darwin only). `codeburn menubar` downloads + installs the signed
      # `.app` matching the CLI's own version into ~/Applications and launches it. HM activation
      # runs in the user's GUI session (nix-darwin's `launchctl asuser <uid> sudo -u <user>
      # --set-home`), so the download and launch work there.
      #
      # We reinstall (with --force — codeburn will not overwrite an existing copy otherwise) only
      # when the app is MISSING or its installed version differs from the nix-packaged CLI version.
      # A stamp file records the last-installed version. So a codeburn version bump updates the
      # menubar in lockstep with the CLI, while an unrelated switch (same version, app present) is
      # a no-op — no network, no relaunch. Failures are logged and surfaced (never a silent
      # `|| true`).
      (lib.mkIf (isDarwin && cfg.menubar.enable) {
        home.activation.codeburnMenubar = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
          _cb_want="${cfg.package.version}"
          _cb_stamp="$HOME/.cache/codeburn/.menubar-nix-version"
          _cb_have="$(cat "$_cb_stamp" 2>/dev/null || echo none)"
          if [ ! -e "$HOME/Applications/CodeBurnMenubar.app" ] || [ "$_cb_have" != "$_cb_want" ]; then
            $DRY_RUN_CMD mkdir -p "$HOME/Library/Logs" "$HOME/.cache/codeburn"
            if $DRY_RUN_CMD ${cfg.package}/bin/codeburn menubar --force \
                 > "$HOME/Library/Logs/codeburn-menubar-install.log" 2>&1; then
              $DRY_RUN_CMD sh -c "printf %s \"$_cb_want\" > \"$_cb_stamp\""
            else
              echo "codeburn: menubar install/update to $_cb_want failed — see ~/Library/Logs/codeburn-menubar-install.log; run 'codeburn menubar --force' manually" >&2
            fi
          fi
        '';
      })
    ]
  );
}
