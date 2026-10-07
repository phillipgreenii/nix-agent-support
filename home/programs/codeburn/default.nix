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
  #   - menubar:  the macOS menubar app, darwin-only, a hash-pinned nix fetch of the upstream
  #               notarized release, copied into ~/Applications at activation (no network)
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

    menubar = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Install and launch the macOS menubar app. macOS-only; enabling it on another platform
          is a hard eval error. The app is the upstream release zip fetched by nix with a pinned
          hash (`menubar.package`) and copied into ~/Applications at activation, so activation
          needs no network and nothing is downloaded or quarantine-stripped at apply time.
          Requires the CLI on the profile PATH (`terminal.enable` or `web.enable`): the menubar
          spawns it.
        '';
      };
      package = lib.mkOption {
        type = lib.types.package;
        default = pkgs.codeburn-menubar;
        defaultText = lib.literalExpression "pkgs.codeburn-menubar";
        description = "Package providing Applications/CodeBurnMenubar.app (macOS only).";
      };
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
          {
            assertion = !cfg.menubar.enable || cfg.terminal.enable || cfg.web.enable;
            message = "phillipgreenii.programs.codeburn.menubar.enable needs the CLI on the profile PATH: enable terminal or web too (the menubar app spawns the codeburn command).";
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

      # Menubar app (darwin only). The bundle is a hash-pinned nix fetch of the upstream notarized
      # release (`cfg.menubar.package`), copied UNMODIFIED into ~/Applications at activation — no
      # network, no quarantine stripping. This replaces running `codeburn menubar --force` here,
      # which downloaded the zip at apply time and took its checksum from the same release.
      #
      # We copy only when the app is MISSING or the installed copy was placed from a different
      # store path (a stamp records it), so an unrelated switch is a no-op: no copy, no relaunch.
      # The copy is verified with `codesign --verify --deep --strict` before it replaces anything
      # live, and a failure is logged and surfaced (never a silent `|| true`).
      #
      # The menubar finds the CLI through a record file the upstream installer used to write
      # (Library/Application Support/CodeBurn/codeburn-cli-path.v1: one absolute path to a
      # persistent `codeburn`, which the app checks is executable). We write the same file,
      # pointing at the GC-rooted profile bin, a stable symlink that retargets on version bumps.
      # That file is upstream-internal state (read from codeburn's src/menubar-installer.ts and
      # the app's CodeburnCLI.persistedCLIPath): re-check it when bumping codeburn.
      (lib.mkIf (isDarwin && cfg.menubar.enable) {
        home.activation.codeburnMenubar = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
          _cb_src="${cfg.menubar.package}/Applications/CodeBurnMenubar.app"
          _cb_dst="$HOME/Applications/CodeBurnMenubar.app"
          _cb_stamp="$HOME/.cache/codeburn/.menubar-nix-store-path"
          _cb_record_dir="$HOME/Library/Application Support/CodeBurn"
          _cb_record="$_cb_record_dir/codeburn-cli-path.v1"
          _cb_cli="${config.home.profileDirectory}/bin/codeburn"
          _cb_have="$(cat "$_cb_stamp" 2>/dev/null || echo none)"

          $DRY_RUN_CMD mkdir -p "$HOME/Applications" "$HOME/.cache/codeburn" "$_cb_record_dir"
          if [ "$(cat "$_cb_record" 2>/dev/null)" != "$_cb_cli" ]; then
            $DRY_RUN_CMD sh -c 'printf "%s\n" "$1" > "$2" && chmod 600 "$2"' sh "$_cb_cli" "$_cb_record"
          fi

          if [ ! -e "$_cb_dst" ] || [ "$_cb_have" != "$_cb_src" ]; then
            $DRY_RUN_CMD rm -rf "$_cb_dst.nix-new"
            if $DRY_RUN_CMD /usr/bin/ditto "$_cb_src" "$_cb_dst.nix-new" \
               && $DRY_RUN_CMD chmod -R u+w "$_cb_dst.nix-new" \
               && $DRY_RUN_CMD /usr/bin/codesign --verify --deep --strict "$_cb_dst.nix-new"; then
              # Name-exact match, as upstream's installer does: only the app's own process.
              $DRY_RUN_CMD /usr/bin/pkill -x CodeBurnMenubar || true
              $DRY_RUN_CMD rm -rf "$_cb_dst"
              $DRY_RUN_CMD mv "$_cb_dst.nix-new" "$_cb_dst"
              $DRY_RUN_CMD sh -c 'printf %s "$1" > "$2"' sh "$_cb_src" "$_cb_stamp"
              $DRY_RUN_CMD /usr/bin/open "$_cb_dst"
            else
              echo "codeburn: menubar install from $_cb_src failed (copy or signature check); the installed app, if any, was left untouched" >&2
              $DRY_RUN_CMD rm -rf "$_cb_dst.nix-new"
            fi
          fi
        '';
      })
    ]
  );
}
