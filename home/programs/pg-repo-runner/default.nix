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

  # One allow-list entry. Every flag defaults to false (`dryRun` defaults to true
  # and only matters once `push` is on); `push` is the one that
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
      build = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Let this repo take part in the workspace build (runner C: the
          `workspace-tips` source and the `build` role). The build runs in a
          throwaway coordinated workforest set that holds EVERY repo of the
          workspace's `pn-workspace.toml`, so the role refuses the whole run
          unless every one of them is on the allow-list with `build = true`.
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
      dryRun = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          While true (the default) a repo with `push = true` only LOGS the commit
          range it would push (ADR 0108, decision 6). The operator reviews that
          range, then sets this to false (or creates the flag file
          `push-live/<repo>` in the runner's state directory). Renders as
          `dry_run`. Has no effect while `push` is false.
        '';
      };
      consumes = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        example = [ "nix-repo-base" ];
        description = ''
          Workspace repo keys whose published state this repo's locked flake
          inputs depend on. When this repo's tip is red under its locked inputs
          while one of them has unpublished commits, the push is reported as
          "blocked, needs sibling push and relock: run pn workspace push" (the
          runner never pushes the sibling). Renders as `consumes`.
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
      retry_window = cfg.thresholds.retryWindow;
      quiet_window = cfg.thresholds.quietWindow;
    }
    // lib.optionalAttrs (cfg.thresholds.maxLoad != null) { max_load = cfg.thresholds.maxLoad; };
    repos = lib.mapAttrs (_: r: {
      inherit (r)
        verify
        sync
        build
        push
        consumes
        ;
      dry_run = r.dryRun;
    }) cfg.repos;
  }
  // lib.optionalAttrs cfg.surface.enable {
    surface = {
      enabled = true;
      repo_labels = cfg.surface.repoLabels;
      inherit (cfg.surface) notify;
    }
    // lib.optionalAttrs (cfg.surface.trackerDir != null) { tracker_dir = cfg.surface.trackerDir; };
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
      retryWindow = lib.mkOption {
        type = lib.types.ints.positive;
        default = 3600;
        description = ''
          Seconds a repo-tip item the `tips` source emits stays offered to
          pg-router (its `expiresAt`): the retry window for a run that answered
          "not now", and the de-duplication window of the item id. Renders as
          `thresholds.retry_window`.
        '';
      };
      quietWindow = lib.mkOption {
        type = lib.types.ints.unsigned;
        default = 900;
        description = ''
          Seconds the workspace's tip tuple must stay unchanged before the
          `workspace-tips` source offers it for a build (the Debounce of runner
          C): 3 ticks of a 5 minute poll by default, so a burst of landings
          yields one build. Renders as `thresholds.quiet_window`.
        '';
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

    surface = {
      enable = lib.mkEnableOption ''
        failure surfacing: one P1 bead (the repo's label, metadata
        `runner = pg-repo-runner`) and one macOS notification per TRANSITION of a
        repo's tip (green to red, diverged, push blocked: needs relock, a red
        build), and the runner closes its OWN bead once a later tip that descends
        from the red one is green. Off by default; the runner never reverts,
        resets, edits or pushes a repo to repair a red tip
      '';

      trackerDir = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "/home/me/workspace/some-repo";
        description = ''
          Absolute path of the directory whose `.beads` tracker receives the
          beads (the shared tracker of the workspace; BF-1: the repo label, not
          the tracker, tells the repos apart). REQUIRED while `surface.enable` is
          set. If the tracker cannot be reached the transition stays pending and
          is retried on the next run; the runner never files in another tracker.
          Renders as `surface.tracker_dir`.
        '';
      };

      repoLabels = lib.mkOption {
        type = lib.types.attrsOf lib.types.str;
        default = { };
        example = {
          nix-repo-base = "base";
        };
        description = ''
          The beads label of each allow-listed repo (the workspace's repo label
          lookup table). Machine- and workspace-specific, so it has no default.
          A repo with no entry is never filed label-less: its bead stays pending.
          A red workspace build is filed under an optional `workspace` entry, else
          under the label of the workspace's terminal repo.
          Every key of `repos` MUST have an entry (asserted). Renders as
          `surface.repo_labels`.
        '';
      };

      notify = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Post one macOS notification per transition. Renders as `surface.notify`.";
      };
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
        assertion =
          !cfg.surface.enable || (cfg.surface.trackerDir != null && lib.hasPrefix "/" cfg.surface.trackerDir);
        message = "phillipgreenii.programs.pg-repo-runner.surface.trackerDir MUST be an absolute path while surface.enable is set (BF-2: a runner never guesses a tracker).";
      }
      {
        assertion =
          !cfg.surface.enable || lib.all (k: cfg.surface.repoLabels ? ${k}) (lib.attrNames cfg.repos);
        message = "phillipgreenii.programs.pg-repo-runner.surface.repoLabels MUST name a label for every repo of `repos` while surface.enable is set (BF-1: a bead is never filed label-less); missing: ${
          lib.concatStringsSep ", " (
            lib.filter (k: !(cfg.surface.repoLabels ? ${k})) (lib.attrNames cfg.repos)
          )
        }.";
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
