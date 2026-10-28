# Eval-level test of the pg-repo-runner home module (bead pg2-zj2v7m, F1) in
# home/programs/pg-repo-runner. Consumed by checks.<system>.test-pg-repo-runner-hm-render,
# which also builds the rendered config.toml and runs the real package against it.
#
# What it proves, by evaluating the REAL module against stubs of the few
# home-manager options it writes (xdg.configFile, xdg.stateHome, home.packages,
# programs.tldr, assertions):
#
#   * `repos.<name>.push` DEFAULTS to false, for every repo and in the option's
#     own sub-option declaration, and so do `verify` and `sync`;
#   * no repo in any case below enables push (ADR 0108, decision 1: enabling a
#     repo needs an operator ruling recorded on a bead; this test, like the module
#     defaults, enables none);
#   * the module renders $XDG_CONFIG_HOME/pg-repo-runner/config.toml;
#   * the module's assertions fire for a missing workspace root and a path-shaped
#     repo key, and stay quiet otherwise.
#
# All values are EXAMPLE values.
{ lib, pkgs }:
let
  evaluate =
    cfg:
    lib.evalModules {
      specialArgs = { inherit pkgs lib; };
      modules = [
        ../home/programs/pg-repo-runner/default.nix
        (
          { lib, ... }:
          {
            options = {
              xdg.configFile = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
              xdg.stateHome = lib.mkOption {
                type = lib.types.str;
                default = "/example/home/.local/state";
              };
              home.packages = lib.mkOption {
                type = lib.types.listOf lib.types.package;
                default = [ ];
              };
              programs.tldr.enable = lib.mkOption {
                type = lib.types.bool;
                default = true;
              };
              programs.tldr.customPages = lib.mkOption {
                type = lib.types.attrsOf lib.types.anything;
                default = { };
              };
              assertions = lib.mkOption {
                type = lib.types.listOf lib.types.anything;
                default = [ ];
              };
            };
          }
        )
        { phillipgreenii.programs.pg-repo-runner = cfg; }
      ];
    };

  opts = c: c.config.phillipgreenii.programs.pg-repo-runner;
  failedAssertions = c: lib.filter (a: !a.assertion) c.config.assertions;
  messages = c: map (a: a.message) (failedAssertions c);
  configFileOf = c: c.config.xdg.configFile."pg-repo-runner/config.toml".source;
  mentions = needle: msgs: lib.any (m: lib.hasInfix needle m) msgs;

  # an allow-list that leaves every flag at its default, and one that sets the
  # two non-publishing flags: push is never set anywhere
  defaultsOnly = evaluate {
    enable = true;
    workspaceRoot = "/example/ws";
    repos = {
      nix-alpha = { };
      nix-beta = { };
      nix-gamma = { };
    };
  };
  flagged = evaluate {
    enable = true;
    workspaceRoot = "/example/ws";
    thresholds = {
      maxLoad = 8;
      niceLevel = 15;
      retryWindow = 900;
    };
    killSwitchPath = "/example/kill";
    repos = {
      nix-alpha = {
        verify = true;
        sync = true;
      };
      nix-beta.verify = true;
      nix-gamma = { };
    };
  };
  surfaced = evaluate {
    enable = true;
    workspaceRoot = "/example/ws";
    repos = {
      nix-alpha.verify = true;
      nix-beta.verify = true;
    };
    surface = {
      enable = true;
      trackerDir = "/example/tracker";
      repoLabels = {
        nix-alpha = "example-label-a";
        nix-beta = "example-label-b";
      };
    };
  };
  surfaceNoLabel = evaluate {
    enable = true;
    workspaceRoot = "/example/ws";
    repos = {
      nix-alpha.verify = true;
      nix-beta.verify = true;
    };
    surface = {
      enable = true;
      trackerDir = "/example/tracker";
      repoLabels.nix-alpha = "example-label-a";
    };
  };
  surfaceNoTracker = evaluate {
    enable = true;
    workspaceRoot = "/example/ws";
    repos.nix-alpha = { };
    surface = {
      enable = true;
      repoLabels.nix-alpha = "example-label-a";
    };
  };
  disabled = evaluate {
    enable = false;
  };
  noRoot = evaluate {
    enable = true;
    repos.nix-alpha = { };
  };
  badKey = evaluate {
    enable = true;
    workspaceRoot = "/example/ws";
    repos."../escape" = { };
  };
  relativeRoot = evaluate {
    enable = true;
    workspaceRoot = "relative/ws";
  };

  pushOf = c: lib.mapAttrs (_: r: r.push) (opts c).repos;
  noPush = c: lib.all (v: v == false) (lib.attrValues (pushOf c));

  # the declaration's own defaults, read from the option's sub-options
  declared = defaultsOnly.options.phillipgreenii.programs.pg-repo-runner.repos.type.getSubOptions [ ];

  checks = {
    "push default declared false" = declared.push.default == false;
    "verify default declared false" = declared.verify.default == false;
    "sync default declared false" = declared.sync.default == false;
    "dryRun default declared true" = declared.dryRun.default == true;
    "consumes default declared empty" = declared.consumes.default == [ ];
    "defaults only: every repo dry-run on" = lib.all (r: r.dryRun) (
      lib.attrValues (opts defaultsOnly).repos
    );
    "defaults only: every repo push false" =
      noPush defaultsOnly && lib.length (lib.attrNames (opts defaultsOnly).repos) == 3;
    "defaults only: every repo verify and sync false" = lib.all (r: !r.verify && !r.sync) (
      lib.attrValues (opts defaultsOnly).repos
    );
    "flagged: push still false everywhere" = noPush flagged;
    "flagged: verify and sync as set" =
      (opts flagged).repos.nix-alpha.verify
      && (opts flagged).repos.nix-alpha.sync
      && (opts flagged).repos.nix-beta.verify
      && !(opts flagged).repos.nix-beta.sync;
    "defaults only: no failed assertion" = failedAssertions defaultsOnly == [ ];
    "flagged: no failed assertion" = failedAssertions flagged == [ ];
    "enabled renders the config.toml through xdg.configFile" =
      lib.hasSuffix "pg-repo-runner-config.toml" (toString (configFileOf defaultsOnly));
    "enabled installs the package" =
      defaultsOnly.config.home.packages == [ (opts defaultsOnly).package ];
    "enabled registers the tldr page" = defaultsOnly.config.programs.tldr.customPages ? pg-repo-runner;
    "disabled renders nothing" = !(disabled.config.xdg.configFile ? "pg-repo-runner/config.toml");
    "disabled installs nothing" = disabled.config.home.packages == [ ];
    "default kill switch is under the state home" =
      (opts defaultsOnly).killSwitchPath == "/example/home/.local/state/pg-repo-runner/kill";
    "surface is off by default" = !(opts defaultsOnly).surface.enable;
    "surface: no failed assertion when complete" = failedAssertions surfaced == [ ];
    "surface: push still false everywhere" = noPush surfaced;
    "surface: a repo without a label is asserted" = mentions "never filed label-less" (
      messages surfaceNoLabel
    );
    "surface: a missing tracker is asserted" = mentions "surface.trackerDir MUST be an absolute path" (
      messages surfaceNoTracker
    );
    "no workspace root: asserted" = mentions "workspaceRoot MUST be set" (messages noRoot);
    "relative workspace root: asserted" = mentions "workspaceRoot MUST be set" (messages relativeRoot);
    "path-shaped repo key: asserted" = mentions "plain repo keys" (messages badKey);
    "the push-default assertion is present and holds" = lib.any (
      a: lib.hasInfix "MUST default to false" a.message && a.assertion
    ) defaultsOnly.config.assertions;
  };

  failures = lib.attrNames (lib.filterAttrs (_: ok: !ok) checks);
in
{
  inherit failures;
  names = lib.attrNames checks;
  # for the check derivation: the rendered files of the two enabled cases
  live = {
    configDefaults = configFileOf defaultsOnly;
    configFlagged = configFileOf flagged;
    configSurfaced = configFileOf surfaced;
    inherit ((opts defaultsOnly)) package;
  };
}
