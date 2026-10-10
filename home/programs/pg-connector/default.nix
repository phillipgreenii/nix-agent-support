{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pg-connector;

  # A registration (bead pg2-91y12, ADR 0062 amendment, INV-REG-4): either a
  # plain string (a bare binary name on PATH, name = binary, exactly as
  # before) or an instance `{ name, command }`. `name` is the instance's
  # identity everywhere pg-connector uses one (sources[] label, --backend
  # pin, backends.<name> key, cache and ledger key); `command` is an argv
  # LIST whose first word is a bare binary name on PATH and whose remaining
  # words are its arguments (never a shell string), so one backend binary
  # can be registered more than once, e.g. the beads backend once per
  # tracker: { name = "pg-connector-issue-beads-pg2"; command = [
  # "pg-connector-issue-beads" "--beads-dir" "/path/to/tracker" ]; }. The
  # rendering below passes values through untouched: a string renders as a
  # string and the submodule as exactly { name, command }.
  backendEntry = lib.types.either lib.types.str (
    lib.types.submodule {
      options = {
        name = lib.mkOption {
          type = lib.types.str;
          description = "Instance name: the sources[] label, the `--backend` pin, the `backends.<name>` key, and the cache/ledger key. MUST NOT contain a path separator or `__`.";
        };
        command = lib.mkOption {
          type = lib.types.nonEmptyListOf lib.types.str;
          description = "argv run for this instance: the first word is a bare binary name resolved on PATH, the rest are its arguments. The JSON request still arrives on stdin.";
        };
        previousNames = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = [ ];
          example = [ "pg-connector-issue-beads" ];
          description = "Former names of this instance (bead pg2-ik9ew), rendered as `previous_names`. On first use under the new name, pg-connector adopts the change ledger a previous name left behind (same type, query and instance discriminator), so a rename does not orphan it and re-emit every live entry as `added`. Renaming a backend WITHOUT declaring its old name here orphans the old ledger. Empty (the default) renders no key.";
        };
      };
    }
  );

  # An instance renders as { name, command } plus previous_names only when it
  # declares some (an empty list renders no key, so a rename-free config is
  # byte-for-byte what it was before); a plain string renders untouched.
  renderEntry =
    entry:
    if builtins.isString entry then
      entry
    else
      {
        inherit (entry) name command;
      }
      // lib.optionalAttrs (entry.previousNames != [ ]) { previous_names = entry.previousNames; };
  renderEntries =
    v:
    if builtins.isList v then
      map renderEntry v
    else if v == null then
      null
    else
      renderEntry v;

  # connector.<type> as pg-connector's own registry.go actually requires it:
  # pr/issue/ci MUST be either a non-empty list or ABSENT entirely
  # (validateBackendList rejects an explicit connector.<type>: [] with
  # "omit the key entirely if no backend should be registered"), and scm
  # MUST be either a bare binary name or absent (never a literal `null`
  # scalar). So an unset/empty entry here is dropped from the rendered
  # connector: mapping rather than rendered as `[]`/`null` — this matters
  # for any host that only registers some capabilities, not just one that
  # registers all four the way ZR's machine config does.
  renderedConnector = lib.mapAttrs (_: renderEntries) (
    lib.filterAttrs (_: v: v != null) {
      pr = if cfg.connector.pr == [ ] then null else cfg.connector.pr;
      issue = if cfg.connector.issue == [ ] then null else cfg.connector.issue;
      ci = if cfg.connector.ci == [ ] then null else cfg.connector.ci;
      scm = cfg.connector.scm;
      # thread (bead pg2-2j5ac.40.3): mirrors pr/issue/ci's identical
      # empty-list-omission pattern above -- registry.go's
      # validateBackendList rejects an explicit `connector.thread: []`.
      thread = if cfg.connector.thread == [ ] then null else cfg.connector.thread;
      # calendar (docket pg2-o2dmu): mirrors thread's identical
      # empty-list-omission pattern -- registry.go's validateBackendList
      # rejects an explicit `connector.calendar: []` the same way. Default
      # stays [ ] in THIS repo's module (public flake, no ZR-specific
      # calendar names hardcoded here) -- the real registration lives in the
      # consuming machine flake (phillipg-nix-ziprecruiter).
      calendar = if cfg.connector.calendar == [ ] then null else cfg.connector.calendar;
      # agentsession (docket pg2-eezd1): mirrors thread/calendar's identical
      # empty-list-omission pattern above -- registry.go's
      # validateBackendList rejects an explicit `connector.agentsession: []`
      # the same way.
      agentsession = if cfg.connector.agentsession == [ ] then null else cfg.connector.agentsession;
      # alert (bead pg2-9tql6): mirrors thread/calendar/agentsession's
      # identical empty-list-omission pattern above -- registry.go's
      # validateBackendList rejects an explicit `connector.alert: []` the
      # same way.
      alert = if cfg.connector.alert == [ ] then null else cfg.connector.alert;
      # mail (docket pg2-qc5uc): mirrors thread/calendar's identical
      # empty-list-omission pattern -- registry.go's validateBackendList
      # rejects an explicit `connector.mail: []` the same way. Default stays
      # [ ] in THIS repo's module (public flake, no machine-specific mailbox
      # or person values hardcoded here) -- the real registration lives in
      # the consuming machine flake (phillipg-nix-ziprecruiter).
      mail = if cfg.connector.mail == [ ] then null else cfg.connector.mail;
    }
  );

  # attention.sources/search.sources (bead pg2-8hcnx) are top-level,
  # always-list-valued registrations, independent of connector.<type> --
  # see registry.go's own AttentionSources/SearchSources doc comments.
  # Like connector.<type>'s own list entries, pg-connector's registry.go
  # rejects an explicit `sources: []` (validateBackendList's "omit the key
  # entirely if no backend should be registered" error), so an empty list
  # here means the whole attention:/search: mapping is omitted entirely
  # rather than rendered with an empty sources: [].
  renderedAttention = lib.optionalAttrs (cfg.attention.sources != [ ]) {
    sources = renderEntries cfg.attention.sources;
  };
  renderedSearch = lib.optionalAttrs (cfg.search.sources != [ ]) {
    sources = renderEntries cfg.search.sources;
  };
  # activity.sources (docket pg2-vfmp7.1): same top-level, always-list-valued
  # shape and the same empty-list omission as attention/search -- the registry
  # rejects an explicit `activity: {sources: []}`, so an empty list omits the
  # whole activity: mapping.
  renderedActivity = lib.optionalAttrs (cfg.activity.sources != [ ]) {
    sources = renderEntries cfg.activity.sources;
  };

  # attentionBackendExtra renders one attention.perBackend.<name> entry onto
  # the wire's own opaque per-backend config vocabulary, omitting the key when
  # it is unset (null) rather than rendering a literal `null` on the wire --
  # mirrors renderedAttention/renderedSearch's identical "omit rather than
  # render null" convention above. Two backends read a per-backend attention
  # key: the alerts backend (`attention_query`) and the beads backend
  # (`attention_labels`, bead pg2-wyeq4). The deadline keys
  # (`attention_threshold`/`attention_exclude`) were retired with the PR, Jira
  # and beads backends' former deadline-based `list_attention` code, since
  # `pg-desk` evaluates entity attention; the beads label-driven op is new.
  attentionBackendExtra =
    entry:
    lib.filterAttrs (_: v: v != null) {
      # attention_query (bead pg2-9tql6): names the alerts backend's own
      # `queries` entry that `list_attention` runs.
      attention_query = entry.attentionQuery;
      # attention_labels (bead pg2-wyeq4): the labels that mark a bead for the
      # beads backend's `list_attention`. A null (unset) value omits the key;
      # an explicit empty list renders `[ ]`, which the backend answers as
      # unavailable naming the key.
      attention_labels = entry.attentionLabels;
    };

  # alertBackendExtra (bead pg2-9tql6) renders one alertBackends.<name>
  # entry: its freeform backend-native keys (e.g. a Grafana `base_url`)
  # verbatim, plus `queries` (caller-facing name -> QueryExpr, i.e. a
  # string or a list of strings) only when non-empty -- an empty `queries`
  # is omitted rather than rendered as `{}`, mirroring the "omit rather
  # than render empty" convention above. No query names are built in.
  alertBackendExtra =
    entry:
    (removeAttrs entry [ "queries" ])
    // lib.optionalAttrs (entry.queries != { }) { inherit (entry) queries; };

  # renderedBackends folds attention.perBackend's own per-backend entries
  # into cfg.backends' existing opaque per-backend blocks (bead pg2-7wqkr):
  # a backend named under EITHER map contributes an entry in the result: a
  # name present only in attention.perBackend (not yet given any other
  # backends.<name> config) still gets one, and a name present in both
  # gets attention.perBackend's own keys merged alongside (never
  # clobbering) whatever cfg.backends already set for it directly.
  #
  # alertBackends (bead pg2-9tql6) folds in the same way, layered between
  # the two: cfg.backends < alertBackends < attention.perBackend.
  renderedBackends = lib.listToAttrs (
    map
      (name: {
        inherit name;
        value =
          (cfg.backends.${name} or { })
          // alertBackendExtra (
            cfg.alertBackends.${name} or {
              queries = { };
            }
          )
          // attentionBackendExtra (
            cfg.attention.perBackend.${name} or {
              attentionQuery = null;
              attentionLabels = null;
            }
          );
      })
      (
        lib.unique (
          lib.attrNames cfg.backends
          ++ lib.attrNames cfg.alertBackends
          ++ lib.attrNames cfg.attention.perBackend
        )
      )
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
    // lib.optionalAttrs (renderedActivity != { }) { activity = renderedActivity; }
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
            type = lib.types.listOf backendEntry;
            default = [ ];
            description = "Registered `pr` capability backends (bare binary names resolved on PATH, or `{ name, command }` instances), in fan-out order.";
          };
          issue = lib.mkOption {
            type = lib.types.listOf backendEntry;
            default = [ ];
            description = "Registered `issue` capability backends (bare binary names resolved on PATH, or `{ name, command }` instances), in fan-out order.";
          };
          ci = lib.mkOption {
            type = lib.types.listOf backendEntry;
            default = [ ];
            description = "Registered `ci` capability backends (bare binary names resolved on PATH, or `{ name, command }` instances), in fan-out order.";
          };
          scm = lib.mkOption {
            type = lib.types.nullOr backendEntry;
            default = null;
            description = "Registered `scm` capability backend (bare binary name or one `{ name, command }` instance; single-valued -- no analogous second-backend future today).";
          };
          # thread (bead pg2-2j5ac.40.3, Phase 13): a WHOLLY NEW capability
          # with no existing field to reuse -- list-of-str, default [ ],
          # mirroring pr/issue/ci's exact shape (never scm's single-valued
          # one). Without this field, a machine config setting
          # `connector.thread = [ "pg-connector-thread-slack" ]` is a hard
          # Nix eval error ("option does not exist").
          thread = lib.mkOption {
            type = lib.types.listOf backendEntry;
            default = [ ];
            description = "Registered `thread` capability backends (bare binary names resolved on PATH, or `{ name, command }` instances), in fan-out order.";
          };
          # calendar (docket pg2-o2dmu): a WHOLLY NEW capability, mirroring
          # thread's exact shape (list-of-str, default [ ]) rather than
          # scm's single-valued one. Without this field, a machine config
          # setting `connector.calendar = [ "pg-connector-calendar-osx-bridge" ]`
          # is a hard Nix eval error ("option does not exist"). Default
          # MUST stay [ ] here (this repo is a public flake) -- the real
          # registration plus real calendars/important_people values live
          # in the consuming machine flake (phillipg-nix-ziprecruiter).
          calendar = lib.mkOption {
            type = lib.types.listOf backendEntry;
            default = [ ];
            description = "Registered `calendar` capability backends (bare binary names resolved on PATH, or `{ name, command }` instances), in fan-out order.";
          };
          # agentsession (docket pg2-eezd1): a WHOLLY NEW capability,
          # mirroring thread's exact shape (list-of-str, default [ ])
          # rather than scm's single-valued one. Without this field, a
          # machine config setting
          # `connector.agentsession = [ "pg-connector-agentsession-pa-monitor" ]`
          # is a hard Nix eval error ("option does not exist"). Default
          # stays [ ] here (this repo is a public flake) -- the real
          # registration lives in the consuming machine flake(s).
          agentsession = lib.mkOption {
            type = lib.types.listOf backendEntry;
            default = [ ];
            description = "Registered `agentsession` capability backends (bare binary names resolved on PATH, or `{ name, command }` instances), in fan-out order.";
          };
          # alert (bead pg2-9tql6): a WHOLLY NEW capability, mirroring
          # thread's exact shape (list-of-str, default [ ]). Without this
          # field, a machine config setting
          # `connector.alert = [ "pg-connector-alert-grafana" ]` is a hard
          # Nix eval error ("option does not exist"). Default MUST stay
          # [ ] here (this repo is a public flake) -- the real
          # registration plus real base_url/queries values live in the
          # consuming machine flake.
          alert = lib.mkOption {
            type = lib.types.listOf backendEntry;
            default = [ ];
            description = "Registered `alert` capability backends (bare binary names resolved on PATH, or `{ name, command }` instances), in fan-out order.";
          };
          # mail (docket pg2-qc5uc): a WHOLLY NEW capability, mirroring
          # thread's exact shape (list-of-str, default [ ]). Without this
          # field, a machine config setting
          # `connector.mail = [ "pg-connector-mail-osx-bridge" ]` is a hard
          # Nix eval error ("option does not exist"). Default MUST stay
          # [ ] here (this repo is a public flake) -- the real
          # registration plus real per-backend config live in the
          # consuming machine flake (rendered via the free-form `backends`
          # option, so no new option is needed for them).
          mail = lib.mkOption {
            type = lib.types.listOf backendEntry;
            default = [ ];
            description = "Registered `mail` capability backends (bare binary names resolved on PATH, or `{ name, command }` instances), in fan-out order.";
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
            type = lib.types.listOf backendEntry;
            default = [ ];
            description = "Registered `attention.sources` backends (bare binary names resolved on PATH, or `{ name, command }` instances), fanned out to by `pg-connector attention list`, in config order.";
          };

          # perBackend (bead pg2-7wqkr): attention.sources (above) only
          # says WHICH backends `pg-connector attention list` fans out to
          # -- it carries no per-backend SEMANTICS. This is that shape,
          # rendered onto each named backend's own opaque backends.<name>
          # config block (see attentionBackendExtra/renderedBackends
          # above) rather than requiring a host to hand-write raw
          # backends.<name>.attention_query attrs itself. The alerts backend
          # reads `attentionQuery` and the beads backend reads
          # `attentionLabels` (bead pg2-wyeq4); the deadline
          # `threshold`/`exclude` options were removed with the PR, Jira and
          # beads backends' former deadline-based `list_attention` code
          # (`pg-desk` evaluates entity attention).
          perBackend = lib.mkOption {
            type = lib.types.attrsOf (
              lib.types.submodule {
                options = {
                  # attentionQuery (bead pg2-9tql6): an alerts backend's
                  # `list_attention` runs one named query from its own
                  # `queries` (see `alertBackends`).
                  attentionQuery = lib.mkOption {
                    type = lib.types.nullOr lib.types.str;
                    default = null;
                    description = ''
                      The name of the entry in this backend's
                      `alertBackends.<name>.queries` that `list_attention`
                      runs; rendered as `attention_query`. `null` (the
                      default) omits the key, in which case the backend
                      returns its unfiltered firing set. Only consulted by
                      an alerts backend (e.g. `pg-connector-alert-grafana`).
                    '';
                  };
                  # attentionLabels (bead pg2-wyeq4): the beads backend's
                  # `list_attention` is a label-driven to-do list.
                  attentionLabels = lib.mkOption {
                    type = lib.types.nullOr (lib.types.listOf lib.types.str);
                    default = null;
                    example = [
                      "attention"
                      "human-focus"
                    ];
                    description = ''
                      The bead labels that put a bead on this instance's
                      attention list; rendered as `attention_labels`. A bead
                      carrying ANY of them, in status open, in_progress or
                      blocked, is reported by `list_attention`. There is no
                      built-in default: `null` omits the key, and an empty list
                      or a missing key makes the backend answer `unavailable`
                      naming `attention_labels` rather than return an unscoped
                      result. Set per beads instance (the name given in
                      `attention.sources`), so the two trackers can use
                      different labels. Only consulted by the beads backend
                      (`pg-connector-issue-beads`).
                    '';
                  };
                };
              }
            );
            default = { };
            description = ''
              Per-backend attention semantics (the alerts backend's named
              query, the beads backend's label set), keyed by backend name
              (the binary name, or the instance `name` for a `{ name,
              command }` registration).
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
            type = lib.types.listOf backendEntry;
            default = [ ];
            description = "Registered `search.sources` backends (bare binary names resolved on PATH, or `{ name, command }` instances), fanned out to by `pg-connector search <query>`, in config order.";
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

    # activity.sources (docket pg2-vfmp7.1): the top-level registration read
    # independently of connector.<type> by the registry's activity parse.
    # Shaped as a submodule (like `search`) so a later `perBackend` option can
    # be added without changing this option path; per-backend activity
    # semantics are, for now, ordinary keys on each backend's opaque
    # `backends.<name>` block.
    activity = lib.mkOption {
      type = lib.types.submodule {
        options = {
          sources = lib.mkOption {
            type = lib.types.listOf backendEntry;
            default = [ ];
            description = "Registered `activity.sources` backends (bare binary names resolved on PATH, or `{ name, command }` instances) serving the `activity` capability's `list_activity`, in config order.";
          };
        };
      };
      default = { };
      description = ''
        The `activity:` registry rendered into the shared config file: which
        backend binaries serve the `activity` capability. Independent of
        `connector.<type>`. An empty `sources` omits the whole `activity:`
        mapping (the registry rejects an explicit empty list).
      '';
    };

    # alertBackends (bead pg2-9tql6): per-alert-backend config rendered under
    # backends.<name> -- design section 5.1's registry entry. Only the
    # backend-independent `queries` shape (name -> QueryExpr, i.e. a string or
    # a list of strings; a list means run each and union) is typed here;
    # every other key is backend-native and passes through verbatim
    # (freeform), e.g. a Grafana `base_url`. This module MUST NOT bake in
    # query names or machine values -- those belong in the consuming
    # machine flake.
    alertBackends = lib.mkOption {
      type = lib.types.attrsOf (
        lib.types.submodule {
          freeformType = lib.types.attrsOf lib.types.anything;
          options.queries = lib.mkOption {
            type = lib.types.attrsOf (lib.types.either lib.types.str (lib.types.listOf lib.types.str));
            default = { };
            description = ''
              Named queries for this alerts backend: a caller-facing name
              mapped to a query expression, either one string or a list of
              strings (a list runs each and unions the results). What a
              query string means is backend-native (e.g. an Alertmanager
              matcher set for Grafana). The module ships NO built-in names.
              Empty (the default) omits `queries` from the rendered block.
            '';
          };
        }
      );
      default = { };
      description = ''
        Per-alerts-backend config blocks, keyed by registered backend NAME (the
        binary name for a plain string, the instance name for a
        `{ name, command }` instance; e.g. `pg-connector-alert-grafana`), rendered under `backends.<name>:` in
        the shared config file. Declare `queries` here and any backend-native
        keys (e.g. `base_url`) as plain attributes; name the query
        `list_attention` runs via `attention.perBackend.<name>.attentionQuery`.
        Layered over `backends.<name>` and under `attention.perBackend`
        when the same name appears in more than one.
      '';
    };

    backends = lib.mkOption {
      type = lib.types.attrsOf (lib.types.attrsOf lib.types.anything);
      default = { };
      description = ''
        Per-backend opaque config blocks, keyed by registered backend NAME (the
        binary name for a plain string, the instance name for a
        `{ name, command }` instance; e.g. `pg-connector-pr-github`). Each value is rendered verbatim under
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
      description = "The `state:` block rendered into the shared config file (e.g. `consumer_prune_after`, `fanout_concurrency`).";
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
    # linkage between the binaries. All the backends therefore MUST ship
    # alongside cfg.package whenever this module is enabled, or dispatch to
    # that capability fails at runtime with "executable file not found in
    # $PATH". None of the seven has an independent CLI identity of its own
    # (each package's own meta.description says so — they speak only the
    # scriptout wire protocol) or a tldr page, so — unlike cfg.package — they
    # are not exposed as separate mkPackageOption overrides here; they are
    # read straight from pkgs, matching flake.nix's own package-attr names.
    # pg-connector-issue-jira joined the other four here (pg2-2j5ac.28.5):
    # the module previously omitted it entirely, alongside ZR's own
    # connector.issue registry. pg-connector-thread-slack joins in turn
    # (bead pg2-2j5ac.40.3): without this, the binary is never actually
    # installed even though its nix output exists. pg-connector-calendar-
    # osx-bridge joins in turn (docket pg2-o2dmu): installed
    # unconditionally, independent of whether connector.calendar is itself
    # populated -- mirrors every other Tier-2 backend's own
    # always-installed convention. pg-connector-agentsession-pa-monitor
    # joins in turn (docket pg2-eezd1): also always-installed, independent
    # of whether connector.agentsession is itself populated -- it answers
    # the attention and search capabilities too, not only agentsession.
    # pg-connector-alert-grafana joins last (bead pg2-rejc3): also
    # always-installed, independent of whether connector.alert is itself
    # populated -- it answers the attention capability too.
    # pg-connector-mail-osx-bridge joins last (docket pg2-qc5uc): also
    # always-installed, independent of whether connector.mail is itself
    # populated -- it answers the attention and search capabilities too.
    # pg-connector-calendar-task-focus joins (bead pg2-t7me1.4): also
    # always-installed, independent of whether connector.calendar or
    # attention.sources name it -- it answers the attention capability too.
    home.packages = [
      cfg.package
      pkgs.pg-connector-pr-github
      pkgs.pg-connector-ci-github-actions
      pkgs.pg-connector-issue-beads
      pkgs.pg-connector-issue-jira
      pkgs.pg-connector-scm-git
      pkgs.pg-connector-activity-git
      pkgs.pg-connector-thread-slack
      pkgs.pg-connector-calendar-osx-bridge
      pkgs.pg-connector-calendar-task-focus
      pkgs.pg-connector-agentsession-pa-monitor
      pkgs.pg-connector-alert-grafana
      pkgs.pg-connector-mail-osx-bridge
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
