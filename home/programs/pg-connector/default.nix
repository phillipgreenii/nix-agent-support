{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pg-connector;

  # connector.<type> as pg-connector's own registry.go actually requires it:
  # pr/issue/ci MUST be either a non-empty list or ABSENT entirely
  # (validateBackendList rejects an explicit connector.<type>: [] with
  # "omit the key entirely if no backend should be registered"), and scm
  # MUST be either a bare binary name or absent (never a literal `null`
  # scalar). So an unset/empty entry here is dropped from the rendered
  # connector: mapping rather than rendered as `[]`/`null` — this matters
  # for any host that only registers some capabilities, not just one that
  # registers all four the way ZR's machine config does.
  renderedConnector = lib.filterAttrs (_: v: v != null) {
    pr = if cfg.connector.pr == [ ] then null else cfg.connector.pr;
    issue = if cfg.connector.issue == [ ] then null else cfg.connector.issue;
    ci = if cfg.connector.ci == [ ] then null else cfg.connector.ci;
    scm = cfg.connector.scm;
  };

  # attention.sources/search.sources (bead pg2-8hcnx) are top-level,
  # always-list-valued registrations, independent of connector.<type> --
  # see registry.go's own AttentionSources/SearchSources doc comments.
  # Like connector.<type>'s own list entries, pg-connector's registry.go
  # rejects an explicit `sources: []` (validateBackendList's "omit the key
  # entirely if no backend should be registered" error), so an empty list
  # here means the whole attention:/search: mapping is omitted entirely
  # rather than rendered with an empty sources: [].
  renderedAttention = lib.optionalAttrs (cfg.attention.sources != [ ]) {
    inherit (cfg.attention) sources;
  };
  renderedSearch = lib.optionalAttrs (cfg.search.sources != [ ]) {
    inherit (cfg.search) sources;
  };

  # attentionBackendExtra (bead pg2-7wqkr) renders one
  # attention.perBackend.<name> entry onto the wire's own opaque
  # per-backend config vocabulary (attention_threshold/attention_exclude
  # -- each backend's own list_attention implementation documents these
  # keys itself), omitting whichever of the two is unset (null) rather
  # than rendering it as a literal `null` on the wire -- mirrors
  # renderedAttention/renderedSearch's identical "omit rather than render
  # null" convention above.
  attentionBackendExtra =
    entry:
    lib.filterAttrs (_: v: v != null) {
      attention_threshold = entry.threshold;
      attention_exclude = entry.exclude;
    };

  # renderedBackends folds attention.perBackend's own per-backend entries
  # into cfg.backends' existing opaque per-backend blocks (bead pg2-7wqkr):
  # a backend named under EITHER map contributes an entry in the result: a
  # name present only in attention.perBackend (not yet given any other
  # backends.<name> config) still gets one, and a name present in both
  # gets attention.perBackend's own keys merged alongside (never
  # clobbering) whatever cfg.backends already set for it directly.
  renderedBackends = lib.listToAttrs (
    map (name: {
      inherit name;
      value =
        (cfg.backends.${name} or { })
        // attentionBackendExtra (
          cfg.attention.perBackend.${name} or {
            threshold = null;
            exclude = null;
          }
        );
    }) (lib.unique (lib.attrNames cfg.backends ++ lib.attrNames cfg.attention.perBackend))
  );

  # The complete rendered document: extraConfig's keys (pg-pr's own,
  # during the overlap window) plus this module's own connector:/
  # attention:/search:/backends:/state:/configSchemaVersion: keys, each
  # included only when it has something to say -- an empty backends:/
  # state: block is harmless to pg-connector's own unknown-fields-tolerant
  # top-level decode, but there is no reason to render noise for a host
  # that leaves them unset, and configSchemaVersion specifically MUST be
  # omitted (not rendered as `null`) so its absence keeps meaning schema
  # version 1.
  renderedConfig =
    cfg.extraConfig
    // lib.optionalAttrs (renderedConnector != { }) { connector = renderedConnector; }
    // lib.optionalAttrs (renderedAttention != { }) { attention = renderedAttention; }
    // lib.optionalAttrs (renderedSearch != { }) { search = renderedSearch; }
    // lib.optionalAttrs (renderedBackends != { }) { backends = renderedBackends; }
    // lib.optionalAttrs (cfg.state != { }) { inherit (cfg) state; }
    // lib.optionalAttrs (cfg.configSchemaVersion != null) { inherit (cfg) configSchemaVersion; };
in
{
  options.phillipgreenii.programs.pg-connector = {
    enable = lib.mkEnableOption "pg-connector CLI (unified pluggable connector umbrella CLI)";
    package = lib.mkPackageOption pkgs "pg-connector" { };

    # Phase 7 (pg2-2j5ac.28.5): this module now OWNS the shared config file
    # pg-connector and pg-pr both read (registry.go's own header comment:
    # "pg-connector and pg-pr can share a single config.yaml on a host
    # running both"). connector/backends/state/configSchemaVersion mirror
    # the shape the design of record's section 4.7/4.8 gives that file;
    # extraConfig carries every key that belongs to pg-pr (or any other
    # future co-tenant) rather than to pg-connector itself.
    connector = lib.mkOption {
      type = lib.types.submodule {
        options = {
          pr = lib.mkOption {
            type = lib.types.listOf lib.types.str;
            default = [ ];
            description = "Registered `pr` capability backends (bare binary names, resolved on PATH), in fan-out order.";
          };
          issue = lib.mkOption {
            type = lib.types.listOf lib.types.str;
            default = [ ];
            description = "Registered `issue` capability backends (bare binary names, resolved on PATH), in fan-out order.";
          };
          ci = lib.mkOption {
            type = lib.types.listOf lib.types.str;
            default = [ ];
            description = "Registered `ci` capability backends (bare binary names, resolved on PATH), in fan-out order.";
          };
          scm = lib.mkOption {
            type = lib.types.nullOr lib.types.str;
            default = null;
            description = "Registered `scm` capability backend (bare binary name; single-valued -- no analogous second-backend future today).";
          };
        };
      };
      default = { };
      description = ''
        The `connector:` registry rendered into the shared config file: which
        backend binary serves each capability. Mirrors the hand-written
        registry ZR's machine config previously wrote directly into its own
        `xdg.configFile` entry.
      '';
    };

    # attention.sources/search.sources (bead pg2-8hcnx): the two
    # always-list-valued, top-level registrations pg-connector's own
    # registry.go reads INDEPENDENTLY of connector.<type>
    # (Registry.AttentionSources/Registry.SearchSources) -- this module
    # previously exposed no option for either key, so a host's
    # `pg-connector attention list`/`pg-connector search <query>` always
    # saw zero registered backends regardless of connector: contents.
    attention = lib.mkOption {
      type = lib.types.submodule {
        options = {
          sources = lib.mkOption {
            type = lib.types.listOf lib.types.str;
            default = [ ];
            description = "Registered `attention.sources` backends (bare binary names, resolved on PATH), fanned out to by `pg-connector attention list`, in config order.";
          };

          # perBackend (bead pg2-7wqkr): attention.sources (above) only
          # says WHICH backends `pg-connector attention list` fans out to
          # -- it carries no per-backend SEMANTICS. This is that new
          # shape: a deadline threshold plus an optional exclude filter,
          # per backend, rendered onto each named backend's own opaque
          # backends.<name> config block (see attentionBackendExtra/
          # renderedBackends above) rather than requiring a host to
          # hand-write raw backends.<name>.attention_threshold/
          # attention_exclude attrs itself.
          perBackend = lib.mkOption {
            type = lib.types.attrsOf (
              lib.types.submodule {
                options = {
                  threshold = lib.mkOption {
                    type = lib.types.nullOr lib.types.str;
                    default = null;
                    description = ''
                      How close to (or how far past) its due date an item
                      from this backend must be before `list_attention`
                      raises it -- a Go `time.ParseDuration` string (e.g.
                      `"72h"`; there is no `d`/`w` unit, only
                      ns/us/ms/s/m/h). `null` (the default) means this
                      backend's own `list_attention` implementation
                      applies its own built-in default rather than a
                      configured one. Only consulted by a deadline-based
                      backend (`pg-connector-issue-beads`,
                      `pg-connector-issue-jira`) -- `pg-connector-pr-github`'s
                      own PR attention is not deadline-based and ignores
                      this.
                    '';
                  };
                  exclude = lib.mkOption {
                    type = lib.types.nullOr lib.types.str;
                    default = null;
                    description = ''
                      An additional, backend-native exclude filter applied
                      on top of the threshold -- e.g. a `bd
                      --exclude-label`-shaped value for
                      `pg-connector-issue-beads`, or a JQL boolean fragment
                      ANDed-out for `pg-connector-issue-jira` (e.g.
                      `"labels = no-attention"`). `null` (the default)
                      applies no additional exclusion. Backend-specific
                      grammar; see that backend's own doc comments.
                    '';
                  };
                };
              }
            );
            default = { };
            description = ''
              Per-backend attention semantics (deadline threshold plus
              optional exclude filter), keyed by backend binary name.
              Independent of `attention.sources` -- a backend configured
              here has no effect on `pg-connector attention list` unless
              it is ALSO registered under `attention.sources`.
            '';
          };
        };
      };
      default = { };
      description = ''
        The `attention:` registry rendered into the shared config file:
        which backend binaries `pg-connector attention list` fans out to,
        plus each backend's own attention semantics (`perBackend`).
        Independent of `connector.<type>` -- a backend may be registered
        here as well as under `connector.<type>`.
      '';
    };

    search = lib.mkOption {
      type = lib.types.submodule {
        options = {
          sources = lib.mkOption {
            type = lib.types.listOf lib.types.str;
            default = [ ];
            description = "Registered `search.sources` backends (bare binary names, resolved on PATH), fanned out to by `pg-connector search <query>`, in config order.";
          };
        };
      };
      default = { };
      description = ''
        The `search:` registry rendered into the shared config file: which
        backend binaries `pg-connector search <query>` fans out to.
        Independent of `connector.<type>` -- a backend may be registered
        here as well as under `connector.<type>`.
      '';
    };

    backends = lib.mkOption {
      type = lib.types.attrsOf (lib.types.attrsOf lib.types.anything);
      default = { };
      description = ''
        Per-backend opaque config blocks, keyed by backend binary name (e.g.
        `pg-connector-pr-github`). Each value is rendered verbatim under
        `backends.<name>:` in the shared config file; pg-connector copies it
        into every wire request sent to that backend without interpreting
        it, treating it as fully opaque (mirrors the umbrella's own
        "treat config as opaque" rule -- this module MUST NOT validate a
        backend's own keys either). A backend documents its own recognized
        keys itself.
      '';
    };

    state = lib.mkOption {
      type = lib.types.attrsOf lib.types.anything;
      default = { };
      description = "The `state:` block rendered into the shared config file (e.g. `consumer_prune_after`).";
    };

    configSchemaVersion = lib.mkOption {
      type = lib.types.nullOr lib.types.ints.positive;
      default = null;
      description = ''
        The `configSchemaVersion:` rendered into the shared config file.
        Absent (the default, `null`) means schema version 1, for backward
        compatibility with configs written before this option existed --
        leaving it unset is not an error.
      '';
    };

    extraConfig = lib.mkOption {
      type = lib.types.attrsOf lib.types.anything;
      default = { };
      description = ''
        Extra top-level keys merged into the rendered shared config file
        verbatim, alongside `connector:`/`backends:`/`state:`/
        `configSchemaVersion:`. Exists for pg-pr's own keys (`self_login`,
        `worktree_root`, `review`, `claude_bin`, `repos`, `agents`, etc.)
        during the overlap window where pg-pr and pg-connector share one
        config file; pg-connector's own decode ignores everything outside
        the keys above.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    # cfg.package (the Tier-1 umbrella binary) dispatches to its Tier-2
    # capability backends by execing a bare binary name found on $PATH —
    # packages/pg-connector/pkg/scriptout/exec.go's runInvoke resolves the
    # binary named in the connector.<type> registry
    # (packages/pg-connector/cmd/pg-connector/registry.go) with no compiled-in
    # linkage between the binaries. All five backends therefore MUST ship
    # alongside cfg.package whenever this module is enabled, or dispatch to
    # that capability fails at runtime with "executable file not found in
    # $PATH". None of the five has an independent CLI identity of its own
    # (each package's own meta.description says so — they speak only the
    # scriptout wire protocol) or a tldr page, so — unlike cfg.package — they
    # are not exposed as separate mkPackageOption overrides here; they are
    # read straight from pkgs, matching flake.nix's own package-attr names.
    # pg-connector-issue-jira joins the other four here (pg2-2j5ac.28.5):
    # the module previously omitted it entirely, alongside ZR's own
    # connector.issue registry.
    home.packages = [
      cfg.package
      pkgs.pg-connector-pr-github
      pkgs.pg-connector-ci-github-actions
      pkgs.pg-connector-issue-beads
      pkgs.pg-connector-issue-jira
      pkgs.pg-connector-scm-git
    ];

    # No programs.tldr.customPages entry: unlike pg-pr's own module,
    # packages/pg-connector/default.nix's postInstall generates only a man
    # page + completions — it does not copy/generate a tldr page the way
    # packages/pg-pr/default.nix does from its own committed pg-pr.md — so
    # there is nothing at ${cfg.package}/share/tldr/pages.common/pg-connector.md
    # to reference yet.

    # The shared config file (pg2-2j5ac.28.5): the path stays "pg-pr/config.yaml"
    # deliberately -- registry.go's own resolution order ($PG_PR_CONFIG ->
    # $XDG_CONFIG_HOME/pg-pr/config.yaml -> ~/.config/pg-pr/config.yaml) is
    # unchanged from pg-pr's own, and pg-pr's decode at the top level is
    # unknown-fields-tolerant, so this module owning the write is safe for a
    # host that also enables phillipgreenii.programs.pg-pr (whose own module
    # renders no config file of its own today).
    xdg.configFile."pg-pr/config.yaml".source =
      (pkgs.formats.yaml { }).generate "pg-pr-config.yaml"
        renderedConfig;
  };
}
