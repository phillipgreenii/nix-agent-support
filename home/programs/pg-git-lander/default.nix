{
  config,
  lib,
  pkgs,
  ...
}:

# pg-git-lander: installs the binary and renders the XDG configuration,
# `${XDG_CONFIG_HOME:-~/.config}/pg-git-lander/config.toml`, from Nix options.
# The per-repo trust entries (`trust_repo_config`, `ignore_repo_config`) and the
# lock settings live ONLY in that file: commands named in a repository's own
# `.pg-git-lander.toml` run only for a repo the operator trusts here (a refusal
# otherwise; there is no trust subcommand). The tool registers its tldr page
# like the pg-keep-awake module. The capability that enables the module lives in
# home/capabilities/default.nix.
#
# The rendered file is NOT validated at build time: `pg-git-lander config check`
# requires every [[repo]] root to be inside a git clone on the building machine.
# tests/pg-git-lander-hm.nix and checks.<system>.test-pg-git-lander-hm run the
# real check against a render with the roots pointed at a throwaway clone.

let
  cfg = config.phillipgreenii.programs.pg-git-lander;
  tomlFormat = pkgs.formats.toml { };

  lockOpts = {
    leaseTtl = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "30s";
      description = "How long a lock lease lives before another run may take it over. Renders as `lock.lease_ttl`; null leaves the built-in default.";
    };
    wait = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "60s";
      description = "How long a run waits for a busy lock. Renders as `lock.wait`; null leaves the built-in default.";
    };
  };

  # The keys the XDG [defaults] table and each [[repo]] entry share.
  commonOpts = {
    primary = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "main";
      description = "The primary branch (XDG-only key `primary`). Null leaves detection to the tool.";
    };
    lock = lockOpts;
  };

  renderCommon =
    v:
    lib.optionalAttrs (v.primary != null) { inherit (v) primary; }
    // lib.optionalAttrs (v.lock.leaseTtl != null || v.lock.wait != null) {
      lock =
        lib.optionalAttrs (v.lock.leaseTtl != null) { lease_ttl = v.lock.leaseTtl; }
        // lib.optionalAttrs (v.lock.wait != null) { inherit (v.lock) wait; };
    };

  repoType = lib.types.submodule {
    options = commonOpts // {
      root = lib.mkOption {
        type = lib.types.str;
        example = "/src/example-repo";
        description = "Absolute path of a directory inside the repository's clone. Must be non-empty.";
      };
      trustRepoConfig = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Allow the commands (run, resolve, verify) named in this repository's
          `.pg-git-lander.toml` to run. Without it such a file makes the
          operation refuse with untrusted-config.
        '';
      };
      ignoreRepoConfig = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Skip the repository's `.pg-git-lander.toml` entirely (the escape for a broken or too-new repo file).";
      };
    };
  };

  renderRepo =
    r:
    renderCommon r
    // {
      inherit (r) root;
    }
    // lib.optionalAttrs r.trustRepoConfig { trust_repo_config = true; }
    // lib.optionalAttrs r.ignoreRepoConfig { ignore_repo_config = true; };

  settings =
    lib.optionalAttrs (renderCommon cfg.defaults != { }) { defaults = renderCommon cfg.defaults; }
    // lib.optionalAttrs (cfg.repos != [ ]) { repo = map renderRepo cfg.repos; };

  configFile = tomlFormat.generate "pg-git-lander-config.toml" settings;
in
{
  options.phillipgreenii.programs.pg-git-lander = {
    enable = lib.mkEnableOption ''
      pg-git-lander: the lock-safe rebase and land tool. Installs the binary and
      renders its XDG configuration from these options
    '';

    package = lib.mkPackageOption pkgs "pg-git-lander" { };

    defaults = commonOpts;

    repos = lib.mkOption {
      type = lib.types.listOf repoType;
      default = [ ];
      description = "The XDG `[[repo]]` entries: the per-repository trust decisions.";
      example = lib.literalExpression ''
        [
          { root = "/src/example-repo"; trustRepoConfig = true; }
          { root = "/src/very-large-repo"; ignoreRepoConfig = true; }
        ]
      '';
    };

    internal.configFile = lib.mkOption {
      type = lib.types.path;
      internal = true;
      readOnly = true;
      default = configFile;
      defaultText = lib.literalMD "the rendered config.toml";
      description = "The rendered configuration file in the store. Read-only; used by tests.";
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    # An empty root is rejected by `config check`; fail the evaluation first.
    assertions = [
      {
        assertion = lib.all (r: r.root != "") cfg.repos;
        message = "phillipgreenii.programs.pg-git-lander.repos: every entry needs a non-empty root.";
      }
    ];

    xdg.configFile."pg-git-lander/config.toml" = lib.mkIf (settings != { }) { source = configFile; };

    programs.tldr.customPages.pg-git-lander = lib.mkIf config.programs.tldr.enable {
      source = "${cfg.package}/share/tldr/pages.common/pg-git-lander.md";
    };
  };
}
