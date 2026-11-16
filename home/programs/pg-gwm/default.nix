{
  config,
  lib,
  pkgs,
  options,
  ...
}:

let
  cfg = config.phillipgreenii.programs.pg-gwm;
  tomlFormat = pkgs.formats.toml { };
  isDarwin = pkgs.stdenv.hostPlatform.isDarwin;

  # Read from the `options` argument, never from `config`/`pkgs` (using those to
  # choose attribute NAMES is infinite recursion; see home/programs/pa-monitor).
  hasLaunchdRegistry = options.phillipgreenii.programs ? launchdServices;

  # One table per repository, keyed by the main checkout's real path, with the
  # behavior README's keys verbatim: [repos."/abs/path"].
  configUnchecked = tomlFormat.generate "pg-gwm-config-unchecked.toml" { inherit (cfg) repos; };

  # Checked at BUILD time by the package's own `config check --config`, which
  # runs only the pure checks (no repository lookups), so a malformed file fails
  # the build and never reaches the machine. Warnings are allowed (exit 0).
  configFile =
    pkgs.runCommand "pg-gwm-config.toml"
      {
        nativeBuildInputs = [ cfg.package ];
      }
      ''
        pg-gwm config check --config ${configUnchecked}
        cp ${configUnchecked} "$out"
      '';

  reaperLogDir = "${config.xdg.stateHome}/pg-gwm-reaper";
in
{
  options.phillipgreenii.programs.pg-gwm = {
    enable = lib.mkEnableOption ''
      pg-gwm: leases of git worktrees, kept bounded and reaped. Installs the CLI,
      its shell completion and tldr page, and renders the configuration file
    '';

    package = lib.mkPackageOption pkgs "pg-gwm" { };

    repos = lib.mkOption {
      inherit (tomlFormat) type;
      default = { };
      example = lib.literalExpression ''
        {
          "/Users/me/src/project" = {
            min = 2;
            max = 6;
            reuse-reclaimed-worktrees = true;
            clean-reused-worktree = true;
          };
        }
      '';
      description = ''
        Per-repository settings, keyed by the repository's main checkout (its
        real path). Each value is an attribute set of the keys in the behavior
        README's "Repository configuration" table (`min`, `max`, `worktree-root`,
        `default-ttl`, ...), rendered verbatim to
        `$XDG_CONFIG_HOME/pg-gwm/config.toml`. The file is checked at build time
        with `pg-gwm config check --config`.
      '';
    };

    reaper = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          Run `pg-gwm reap --all-repos` on a schedule (launchd user agent
          `com.phillipg.pg-gwm-reaper`, darwin only). It needs
          `phillipgreenii.programs.launchdServices`, which
          phillipgreenii-nix-personal's home module declares; without that
          option the agent is skipped.
        '';
      };

      intervalSeconds = lib.mkOption {
        type = lib.types.ints.positive;
        default = 600;
        description = "How often launchd runs the reaper (StartInterval). One machine runs one reaper, so this is not a per-repository setting.";
      };
    };
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      home.packages = [ cfg.package ];

      xdg.configFile."pg-gwm/config.toml".source = configFile;

      programs = {
        bash.initExtra = lib.mkIf config.phillipgreenii.programs.bash.enable ''
          source <(pg-gwm completion bash)
        '';

        zsh.initContent = lib.mkIf config.phillipgreenii.programs.zsh.enable ''
          source <(pg-gwm completion zsh)
        '';

        tldr.customPages.pg-gwm = lib.mkIf config.programs.tldr.enable {
          platform = "common";
          source = "${cfg.package}/share/tldr/pages.common/pg-gwm.md";
        };
      };
    })

    # Gated on the option's DECLARATION (optionalAttrs), never isDarwin alone: a
    # definition of an undeclared option errors even under mkIf false.
    (lib.optionalAttrs hasLaunchdRegistry {
      phillipgreenii.programs.launchdServices.userAgents.pg-gwm-reaper =
        lib.mkIf (cfg.enable && cfg.reaper.enable && isDarwin)
          {
            label = "com.phillipg.pg-gwm-reaper";
            script = ''
              exec ${cfg.package}/bin/pg-gwm reap --all-repos --deadline 5m --format=json
            '';
            runAtLoad = false;
            keepAlive = false;
            # Not a service that stays running: the activation health check would
            # misread the gap between runs as a failure.
            healthCheck = false;
            serviceConfig = {
              StartInterval = cfg.reaper.intervalSeconds;
              Nice = 10;
              ProcessType = "Background";
              StandardOutPath = "${reaperLogDir}/reap.jsonl";
              StandardErrorPath = "${reaperLogDir}/launchd-stderr.log";
              EnvironmentVariables = {
                HOME = config.home.homeDirectory;
                XDG_CONFIG_HOME = config.xdg.configHome;
                XDG_STATE_HOME = config.xdg.stateHome;
                # So git and the repository's hooks resolve under launchd's bare PATH.
                PATH = lib.concatStringsSep ":" [
                  "${config.home.profileDirectory}/bin"
                  "/etc/profiles/per-user/${config.home.username}/bin"
                  "/run/current-system/sw/bin"
                  "/nix/var/nix/profiles/default/bin"
                  "/usr/bin"
                  "/bin"
                ];
              };
            };
          };
    })
  ];
}
