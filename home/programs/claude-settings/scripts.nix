# Pure builders for the claude-settings activation helper scripts.
#
# Each script is built via the mkBashScript framework so it sources the shared
# act_* activation-output helpers (repo-base ADR 0014 — a standalone script run
# from home.activation obtains act_* via the framework's `libraries`). The
# binary names (`claude-settings-*`) are preserved: default.nix invokes them by
# name, the eval-check asserts on them, and the bats tests resolve them via
# `command -v claude-settings-*`. mkBashScript reads `<name>.sh` from `src`, so
# each builder picks up its own source file and ignores the siblings.
#
# Consumed by default.nix (home.activation) and the flake's
# test-claude-settings-* checks (which run the bats tests separately via
# testBashScripts, so the `.check` returned here is unused).
{
  pkgs,
  mkBashScript,
  activation-lib,
}:
let
  replaceManagedKeys = mkBashScript {
    name = "claude-settings-replace-managed-keys";
    src = ./.;
    public = false;
    description = "Replace enabledPlugins and extraKnownMarketplaces in Claude Code settings.json with the Nix-declared sets";
    libraries = [ activation-lib ];
    runtimeDeps = [
      pkgs.jq
      pkgs.coreutils
    ];
    # Internal activation script with no --help; skip the help2man man-page step.
    manPage = false;
  };

  installPlugin = mkBashScript {
    name = "claude-settings-install-plugin";
    src = ./.;
    public = false;
    description = "Install/update a Claude Code plugin during home-manager activation, validating cached manifests";
    libraries = [ activation-lib ];
    runtimeDeps = [
      pkgs.jq
      pkgs.coreutils
      # git is REQUIRED, not incidental: `claude plugin install` clones a
      # `url`/`github`-source plugin by shelling out to `git` BY NAME, and
      # home-manager's activation script REPLACES PATH outright with a set that
      # has no git — while on darwin git is home-manager-provided, so there is no
      # /run/current-system/sw/bin/git to fall back on either. Without this the
      # clone fails as `Failed to clone repository:` with an EMPTY reason (no git
      # stderr to quote), which is what made bead pg2-ly6a6 hard to read.
      # runtimeDeps are added with `--suffix PATH`, i.e. APPENDED as a fallback so
      # an ambient git still wins; that is exactly what is wanted here, since the
      # activation PATH supplies none. Guarded by
      # `checks.<system>.test-claude-settings-install-plugin-has-git`, which
      # resolves git through the built wrapper's own PATH prologue.
      pkgs.git
    ];
    manPage = false;
  };

  registerMarketplace = mkBashScript {
    name = "claude-settings-register-marketplace";
    src = ./.;
    public = false;
    description = "Register a nix-provided directory-source Claude Code marketplace before the per-plugin install loop";
    libraries = [ activation-lib ];
    runtimeDeps = [
      pkgs.coreutils
    ];
    manPage = false;
  };

  gcPluginCache = mkBashScript {
    name = "claude-settings-gc-plugin-cache";
    src = ./.;
    public = false;
    description = "Garbage-collect stale plugin cache version directories after the per-plugin install loop (pg2-x3a3t)";
    libraries = [ activation-lib ];
    runtimeDeps = [
      pkgs.jq
      pkgs.coreutils
    ];
    manPage = false;
  };
in
{
  inherit
    replaceManagedKeys
    installPlugin
    registerMarketplace
    gcPluginCache
    ;
}
