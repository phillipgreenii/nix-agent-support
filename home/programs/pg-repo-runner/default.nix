{
  config,
  lib,
  pkgs,
  ...
}:

# pg-repo-runner (bead pg2-zj2v7m, F1 of the async-runners design): renders
# ${XDG_CONFIG_HOME:-~/.config}/pg-repo-runner/config.toml, the file the runner
# kernel reads (packages/pg-repo-runner). The allow-list is the ONLY source of
# repos a runner may touch; its keys MUST be repo keys of the workspace's
# pn-workspace.toml (the kernel checks that at run time, since evaluation cannot
# read the workspace file).
#
# NO repo is enabled for push here or by default: `repos.<name>.push` is false
# unless an operator ruling recorded on a bead enables it for a repo (ADR 0108's
# decision 1 and 2). The module asserts that the option's default stays false.
#
# The pg-router wiring (queries, roles, argv) is the deployment repo's job; this
# module installs the tool and renders its config only.

let
  cfg = config.phillipgreenii.programs.pg-repo-runner;
  tomlFormat = pkgs.formats.toml { };

  # One allow-list entry. Every flag defaults to false; `push` is the one that
  # publishes, and the assertion below pins its default.
  repoModule = _: {
    options = {
      verify = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Run the verify role (runner B: `nix flake check` of the immutable
          primary-branch tip) for this repo.
        '';
      };
      sync = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Run the sync role (runner A: fetch, classify, fast-forward a behind
          clone, surface divergence) for this repo.
        '';
      };
      push = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Let the sync role PUSH this repo's primary branch to its remote,
          within the scope of ADR 0108 and nowhere else. Defaults to false for
          every repo, and this module (like the tests and the deployment
          config in this repository) enables it for none. Enabling a repo
          needs an operator ruling recorded on a bead (ADR 0108, decision 1);
          the first enablement runs dry-run (decision 6).
        '';
      };
    };
  };

  pushDefault = (repoModule { }).options.push.default;

  settings = {
    workspace_root = cfg.workspaceRoot;
    kill_switch = cfg.killSwitchPath;
    thresholds = {
      nice_level = cfg.thresholds.niceLevel;
    }
    // lib.optionalAttrs (cfg.thresholds.maxLoad != null) { max_load = cfg.thresholds.maxLoad; };
    repos = lib.mapAttrs (_: r: { inherit (r) verify sync push; }) cfg.repos;
  };

  configFile = tomlFormat.generate "pg-repo-runner-config.toml" settings;

  validKey = k: builtins.match "[A-Za-z0-9][A-Za-z0-9._-]*" k != null && !(lib.hasInfix ".." k);
in
{
  options.phillipgreenii.programs.pg-repo-runner = {
    enable = lib.mkEnableOption ''
      pg-repo-runner: the kernel and facade of the pg-router-managed async repo
      runners. Installs the tool and renders its `config.toml`. It starts nothing:
      the pg-router queries and roles that drive it belong to the deployment
      repository
    '';

    package = lib.mkPackageOption pkgs "pg-repo-runner" { };

    workspaceRoot = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "/home/me/workspace";
      description = ''
        Absolute path of the directory that holds `pn-workspace.toml` and one
        clone per repo key. The kernel refuses any repo whose real path is not
        `<workspaceRoot>/<key>`. REQUIRED when the module is enabled (asserted).
        Renders as `workspace_root`.
      '';
    };

    repos = lib.mkOption {
      type = lib.types.attrsOf (lib.types.submodule repoModule);
      default = { };
      example = {
        nix-repo-base = {
          verify = true;
          sync = true;
        };
      };
      description = ''
        The allow-list: the ONLY repos a runner may touch. Each key MUST be a
        repo key of the workspace's `pn-workspace.toml` (checked by the kernel at
        run time). Every flag, `push` included, defaults to false.
      '';
    };

    thresholds = {
      maxLoad = lib.mkOption {
        type = lib.types.nullOr lib.types.numbers.positive;
        default = null;
        description = ''
          A runner is admitted only while the 1-minute load average is strictly
          below this value. null (the default) means the machine's core count.
          Renders as `thresholds.max_load`. A tunable, to be calibrated from the
          load sampled while a check runs (see the package README).
        '';
      };
      niceLevel = lib.mkOption {
        type = lib.types.ints.between 0 19;
        default = 10;
        description = "The `nice` level of every nix run a runner starts. Renders as `thresholds.nice_level`.";
      };
    };

    killSwitchPath = lib.mkOption {
      type = lib.types.str;
      default = "${config.xdg.stateHome}/pg-repo-runner/kill";
      defaultText = lib.literalExpression ''"''${config.xdg.stateHome}/pg-repo-runner/kill"'';
      description = ''
        While a file exists at this path every runner role exits 9 ("not now").
        A file `push.disabled` in the runner's state directory does the same.
        Renders as `kill_switch`.
      '';
    };

    internal.configFile = lib.mkOption {
      type = lib.types.path;
      internal = true;
      readOnly = true;
      default = configFile;
      description = "The rendered configuration file in the store. Read-only; used by tests.";
    };
  };

  config = lib.mkIf cfg.enable {
    xdg.configFile."pg-repo-runner/config.toml".source = configFile;

    home.packages = [ cfg.package ];

    programs.tldr.customPages.pg-repo-runner = lib.mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.package}/share/tldr/pages.common/pg-repo-runner.md";
    };

    assertions = [
      {
        assertion = pushDefault == false;
        message = "phillipgreenii.programs.pg-repo-runner.repos.<name>.push MUST default to false for every repo (ADR 0108, decision 1): enabling a repo for push needs an operator ruling recorded on a bead, never a default.";
      }
      {
        assertion = cfg.workspaceRoot != null && lib.hasPrefix "/" cfg.workspaceRoot;
        message = "phillipgreenii.programs.pg-repo-runner.workspaceRoot MUST be set to an absolute path while the module is enabled.";
      }
      {
        assertion = lib.all validKey (lib.attrNames cfg.repos);
        message = "phillipgreenii.programs.pg-repo-runner.repos keys MUST be plain repo keys of pn-workspace.toml (no slash, no dot-dot, no leading dot); got: ${
          lib.concatStringsSep ", " (lib.filter (k: !(validKey k)) (lib.attrNames cfg.repos))
        }.";
      }
    ];
  };
}
