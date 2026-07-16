{
  description = "Agent and AI tooling for macOS and Linux (nix-darwin + NixOS)";

  nixConfig = {
    extra-substituters = [ "https://cache.numtide.com" ];
    extra-trusted-public-keys = [
      "niks3.numtide.com-1:DTx8wZduET09hRmMtKdQDxNNthLQETkc/yaX7M4qK0g="
    ];
  };

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-26.05-darwin";
    # Track the Hydra-tested nixpkgs-unstable channel, not the raw master tip.
    # Fleet convention (matches phillipgreenii-nix-personal): master is the
    # unvetted development tip, while nixpkgs-unstable only advances after the
    # channel test suite + binary cache population, so composed and standalone
    # builds resolve the same tested revision.
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    # llm-agents.nix — AI-agent packages with a binary cache (cache.numtide.com,
    # rebuilt 4x daily). Intentionally NOT followed onto our nixpkgs: llm-agents
    # pins its own nixpkgs for the cache hit, and a `nixpkgs.follows` override
    # forces a from-source rebuild that defeats the cache (same rationale as the
    # flox input in ziprecruiter). Fleet policy — keep this bare in every repo;
    # the llm-agents node itself is deduped to a single node by base's
    # llm-agents-overlay flakeModule (alignment.requires) + consumer-input-alignment.
    llm-agents.url = "github:numtide/llm-agents.nix";
    phillipgreenii-nix-overlay = {
      url = "github:phillipgreenii/nix-overlay";
      inputs = {
        nixpkgs.follows = "nixpkgs";
        phillipgreenii-nix-base.follows = "phillipgreenii-nix-base";
        flake-parts.follows = "phillipgreenii-nix-base/flake-parts";
      };
    };
    phillipgreenii-nix-base = {
      url = "github:phillipgreenii/nix-repo-base";
      inputs = {
        nixpkgs.follows = "nixpkgs";
        git-hooks.follows = "git-hooks";
        treefmt-nix.follows = "treefmt-nix";
      };
    };
    # flake-parts: framework for the consumed nix-base flakeModules. Deduped onto
    # nix-base's pin so it is a single shared node (inherits nix-base's
    # nixpkgs-lib follow; no extra wiring needed).
    flake-parts.follows = "phillipgreenii-nix-base/flake-parts";
    nix-darwin.url = "github:LnL7/nix-darwin/nix-darwin-26.05";
    nix-darwin.inputs.nixpkgs.follows = "nixpkgs";
    home-manager.url = "github:nix-community/home-manager/release-26.05";
    home-manager.inputs.nixpkgs.follows = "nixpkgs";
    flake-utils.url = "github:numtide/flake-utils";
    git-hooks.url = "github:cachix/git-hooks.nix";
    git-hooks.inputs.nixpkgs.follows = "nixpkgs";
    treefmt-nix.url = "github:numtide/treefmt-nix";
    treefmt-nix.inputs.nixpkgs.follows = "nixpkgs";
    gomod2nix = {
      url = "github:nix-community/gomod2nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    stylix = {
      url = "github:danth/stylix/release-26.05";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    serena = {
      url = "github:oraios/serena";
      inputs.flake-utils.follows = "flake-utils";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    inputs@{
      self,
      nixpkgs,
      nixpkgs-unstable,
      llm-agents,
      phillipgreenii-nix-overlay,
      phillipgreenii-nix-base,
      flake-parts,
      gomod2nix,
      ...
    }:
    let
      # Overlay populated incrementally as packages are migrated.
      overlay =
        final: _prev:
        let
          bashBuilders = phillipgreenii-nix-base.lib.mkBashBuilders {
            pkgs = final;
            inherit (final) lib;
            inherit self;
          };
          goBuilders = phillipgreenii-nix-base.lib.mkGoBuilders {
            pkgs = final;
            inherit (final) lib;
            inherit self;
          };
          pythonBuilders = phillipgreenii-nix-base.lib.mkPythonBuilders {
            pkgs = final;
            inherit (final) lib;
            inherit (phillipgreenii-nix-base.lib) mkSrcDigest;
          };
        in
        {
          # packages added in later tasks
          _agentSupportBashBuilders = bashBuilders; # expose for modules
          _agentSupportPythonBuilders = pythonBuilders; # expose for modules
          _agentSupportGoBuilders = goBuilders; # expose for checks (mirrors bash/python)
          pg-pr = final.callPackage ./packages/pg-pr {
            inherit (goBuilders) mkGoApp;
          };
          claude-extended-tool-approver = final.callPackage ./packages/claude-extended-tool-approver {
            inherit (goBuilders) mkGoApp;
          };
          ccpool = final.callPackage ./packages/ccpool {
            inherit (goBuilders) mkGoApp;
          };
          pr-pool = final.callPackage ./packages/pr-pool {
            inherit (goBuilders) mkGoApp;
            # No top-level bd/beads overlay attr — resolve it directly here (mirrors pb below).
            bd = final.llm-agentsPkgs.beads or llm-agents.packages.${final.stdenv.hostPlatform.system}.beads;
          };
          pb = final.callPackage ./packages/pb {
            inherit (goBuilders) mkGoApp;
            # No top-level bd/beads overlay attr — source it like pr-pool above.
            bd = final.llm-agentsPkgs.beads or llm-agents.packages.${final.stdenv.hostPlatform.system}.beads;
            # git is pkgs.git (auto via callPackage); `pn` is NOT passed — it is an
            # ambient runtime PATH dep (agent-support is standalone/no-external-flake-deps
            # so final.pn does not resolve). See packages/pb/default.nix + ADR 0018.
          };
          pa-monitor = final.callPackage ./packages/pa-monitor {
            inherit (goBuilders) mkGoApp;
          };
          pa-monitor-decorator-scope = final.callPackage ./packages/pa-monitor-decorator-scope {
            inherit (goBuilders) mkGoApp;
          };
          claude-activity =
            let
              result = import ./packages/claude-activity {
                pkgs = final;
                inherit bashBuilders;
              };
            in
            final.symlinkJoin {
              name = "claude-activity-0.0.0-${phillipgreenii-nix-base.lib.mkSrcDigest result.packages}";
              paths = result.packages;
            };
          agent-activity =
            let
              result = import ./packages/agent-activity {
                pkgs = final;
                inherit bashBuilders;
                inherit (final) claude-activity;
              };
            in
            final.symlinkJoin {
              name = "agent-activity-0.0.0-${phillipgreenii-nix-base.lib.mkSrcDigest result.packages}";
              paths = result.packages;
              postBuild = ''
                ln -s agent-activity-api $out/bin/agent-activity
              '';
            };
          wait-for-agents =
            let
              result = import ./packages/wait-for-agents {
                pkgs = final;
                inherit bashBuilders;
                inherit (final) pa-monitor;
              };
            in
            final.symlinkJoin {
              name = "wait-for-agents-0.0.0-${phillipgreenii-nix-base.lib.mkSrcDigest result.packages}";
              paths = result.packages;
            };
          git-tools =
            let
              result = import ./packages/git-tools {
                pkgs = final;
                inherit bashBuilders;
              };
            in
            final.symlinkJoin {
              name = "git-tools-0.0.0-${phillipgreenii-nix-base.lib.mkSrcDigest result.packages}";
              paths = result.packages;
            };
          integrate-branch-support =
            let
              result = import ./packages/integrate-branch-support {
                pkgs = final;
                inherit bashBuilders;
              };
            in
            final.symlinkJoin {
              name = "integrate-branch-support-0.0.0-${phillipgreenii-nix-base.lib.mkSrcDigest result.packages}";
              paths = result.packages;
            };
          pw-reset-agents = final.callPackage ./packages/pw-reset-agents { };
          pw-agent-activity = final.callPackage ./packages/pw-agent-activity { };
        };

    in
    flake-parts.lib.mkFlake { inherit inputs; } {
      # Mirror flake-utils.lib.eachDefaultSystem verbatim — standalone,
      # multi-system (darwin + linux).
      systems = [
        "aarch64-darwin"
        "aarch64-linux"
        "x86_64-darwin"
        "x86_64-linux"
      ];

      imports = [
        # pre-commit transitively imports treefmt; do NOT import treefmt separately.
        inputs.phillipgreenii-nix-base.flakeModules.pre-commit
        inputs.phillipgreenii-nix-base.flakeModules.devshell
        inputs.phillipgreenii-nix-base.flakeModules.checks
      ];

      perSystem =
        {
          system,
          checksHelpers,
          ...
        }:
        let
          pkgs = import nixpkgs {
            inherit system;
            overlays = [
              # gomod2nix overlay (ADR 0008): provides pkgs.buildGoApplication so
              # the dual-engine mkGoApp can reach the gomod2nix engine. No package
              # opts in yet; this only makes the builder available.
              gomod2nix.overlays.default
              phillipgreenii-nix-overlay.overlays.default
              # Provide `unstable` for STANDALONE agent-support builds so the
              # exported overlay's `final.unstable` resolves here too.
              # Deliberately NOT part of overlays.default, so it never
              # clobbers a consumer's own `unstable` overlay.
              (_final: _prev: {
                unstable = import nixpkgs-unstable {
                  inherit system;
                  config.allowUnfree = true;
                };
              })
              overlay
            ];
          };
          inherit (pkgs) lib;

          # This repo's Claude Code marketplace derivation, hoisted so both the
          # `packages` output and the `test-pg-pr-hook-registered` check (bead
          # pg2-o3eyk) reference the SAME build (no second builder eval).
          agentSupportMarketplace =
            (phillipgreenii-nix-base.lib.mkClaudeMarketplaceBuilders { inherit pkgs lib; }).mkClaudeMarketplace
              {
                src = lib.fileset.toSource {
                  root = ./claude-marketplace;
                  fileset = ./claude-marketplace;
                };
              };
        in
        {
          # The perSystem pkgs carries the full agent-support overlay stack
          # (gomod2nix + nix-overlay + unstable + this
          # flake's overlay). flake-parts' own `pkgs` arg is overridden so the
          # auto-contributed checks (formatting, linting, pre-commit) and the
          # checks below all see the overlaid package set.
          _module.args.pkgs = pkgs;

          # formatter, devShells.default, packages.install-pre-commit-hooks,
          # checks.{formatting, linting, pre-commit, consumer-input-alignment}
          # — all auto-contributed by the imported flakeModules.

          # go is needed in the devShell (Go packages / pre-commit Go hooks at
          # commit time). Uses the perSystem overlaid pkgs so the package matches
          # the current system's architecture.
          phillipgreenii.devshell.extraInputs = [ pkgs.go ];

          # Exclude generated protobuf Go from treefmt. pa-monitor's *.pb.go are
          # tool-owned (`// Code generated by protoc-gen-go. DO NOT EDIT.`) and
          # gofumpt is non-idempotent on them (adds a blank line on every re-run),
          # so treefmt's --fail-on-change gate — hit by `nix flake check` and by
          # update-locks' commit — fails on every run. gofumpt's own directory
          # walk already skips generated files; treefmt invokes it per-path, which
          # bypasses that skip, so exclude the files here instead. Concatenates
          # with repo-base's shared `_sources/*` exclude (definitions extend) and
          # mirrors gofumpt's built-in vendor/* skip.
          treefmt.settings.global.excludes = [ "*.pb.go" ];

          checks =
            let
              # Build the claude-settings framework scripts the same way the
              # home module does: activation-lib from repo-base's lib/activation,
              # then the 3 scripts via scripts.nix. The bats tests run separately
              # via testBashScripts below (the `.check` on each is unused here).
              claudeSettingsActivationLib = pkgs._agentSupportBashBuilders.mkBashLibrary {
                name = "activation-lib";
                src = phillipgreenii-nix-base + "/lib/activation";
                description = "Shared act_* activation-output helpers (single source with system.activationScripts)";
              };
              claudeSettingsScripts = import ./home/programs/claude-settings/scripts.nix {
                inherit pkgs;
                inherit (pkgs._agentSupportBashBuilders) mkBashScript;
                activation-lib = claudeSettingsActivationLib;
              };
              # Each claude-settings bats file does `load test_helper` to resolve
              # the script under test (packaged binary on PATH here in the sandbox,
              # else a lib-sourcing wrapper for a local `bats tests/`). A single
              # `.bats` path would copy only that file to the store, so the sibling
              # test_helper.bash would be absent and `load` would fail. Pair each
              # bats file with the helper in a minimal store dir; `bats <dir>` then
              # runs just that one file with the helper alongside.
              claudeSettingsTestSrc =
                bats:
                lib.fileset.toSource {
                  root = ./home/programs/claude-settings/tests;
                  fileset = lib.fileset.unions [
                    (./home/programs/claude-settings/tests + "/${bats}")
                    ./home/programs/claude-settings/tests/test_helper.bash
                  ];
                };

              # Go test gate (bead pg2-adhga). The package builds pin
              # `subPackages = [ "cmd/<name>" ]`, and the gomod2nix builder's
              # goCheckHook scopes `go test` to `$subPackages` when set — so the
              # shipped-binary build (and thus `nix flake check` via that build)
              # only tests `cmd/`, leaving every `internal/`+`pkg/` suite ungated
              # (ceta's rule tests, pg-pr's sync/store/auth seams, …). These
              # dedicated checks call mkGoApp WITHOUT `subPackages`, so
              # `getGoDirs` (go-config-hook.sh) falls back to `find … *test.go`
              # and the check phase exercises the FULL module. The shipped-binary
              # builds keep `subPackages` (stay scoped) — only this gate pays the
              # test cost, and only under `nix flake check`, never a system build.
              mkGoTestCheck = pkgs._agentSupportGoBuilders.mkGoApp;

              # golangci-lint (offline, gomod2nix vendor env) per Go module — the
              # base-sanctioned replacement (pg2-6wly, pg2-2cuzv) for the removed
              # network-dependent golangci-lint pre-commit hooks. Runs INSIDE the
              # `nix flake check` sandbox (no NIX_BUILD_TOP skip), so it is a real
              # Tier-1 gate. Mirrors base's pn-golangci/pjira-golangci. mkGoLint
              # reads <pwd>/gomod2nix.toml from src; `config` lives outside src, so
              # it MUST be passed explicitly. Pattern-B modRoot forwarding is base
              # bead pg2-sjxhy.
              goLint =
                {
                  module,
                  modRoot ? null,
                  src ? (./packages + "/${module}"),
                }:
                {
                  name = "${module}-golangci";
                  value = pkgs._agentSupportGoBuilders.mkGoLint {
                    pname = module;
                    inherit src modRoot;
                    gomod2nixToml = ./packages + "/${module}/gomod2nix.toml";
                    config = ./.golangci.yml;
                  };
                };

              # Pattern A (no local `replace`): flat src at the module dir. One
              # entry per go.mod module without a sibling replace.
              simpleGoLintModules = [
                "pg-pr"
                "pb"
                "claude-extended-tool-approver"
                "pa-monitor-decorator-scope"
                "claude-transcript"
              ];

              # Pattern B (local `replace => ../sibling`): root the fileset at
              # packages/ so every replaced sibling lives in one store tree, and
              # pass modRoot. Filesets mirror each module's own default.nix.
              patternBGoLints = [
                (goLint {
                  module = "ccpool";
                  modRoot = "ccpool";
                  src = lib.fileset.toSource {
                    root = ./packages;
                    fileset = lib.fileset.unions [
                      ./packages/ccpool
                      ./packages/claude-transcript
                    ];
                  };
                })
                (goLint {
                  module = "pa-monitor";
                  modRoot = "pa-monitor";
                  src = lib.fileset.toSource {
                    root = ./packages;
                    fileset = lib.fileset.unions [
                      ./packages/pa-monitor
                      ./packages/claude-transcript
                    ];
                  };
                })
                (goLint {
                  module = "pr-pool";
                  modRoot = "pr-pool";
                  src = lib.fileset.toSource {
                    root = ./packages;
                    fileset = lib.fileset.unions [
                      # ./docs holds behavior docs, not build inputs — mirror
                      # pr-pool/default.nix and exclude it from the digest.
                      (lib.fileset.difference ./packages/pr-pool ./packages/pr-pool/docs)
                      ./packages/ccpool
                      ./packages/claude-transcript
                    ];
                  };
                })
              ];
            in
            {
              test-update-locks-lib = checksHelpers.testUpdateLocksLib { };

              # pg-pr agent-marker PreToolUse hook (bead pg2-o3eyk). Drives the
              # fixed script over CC-shaped stdin JSON; gates the tool-name-from-
              # stdin and byte-escaped-marker fixes (red on the pre-fix behaviour).
              test-pg-pr-marker-hook = checksHelpers.testBashScripts {
                package = pkgs.writeShellScriptBin "require-agent-pr-comment-marker" ''
                  exec ${./claude-marketplace/pg-pr/hooks/require-agent-pr-comment-marker.sh} "$@"
                '';
                tests = ./tests/pg-pr-marker.bats;
                extraInputs = [ pkgs.jq ];
              };

              # Structural gate for the "never invoked" bug (pg2-o3eyk): the bats
              # suite cannot test CC auto-discovery, and mkClaudeMarketplace only
              # cp's hooks.json (never parses it). Assert the BUILT marketplace
              # ships a valid-JSON hooks.json with a PreToolUse/Bash matcher whose
              # command targets the script, and that the script is executable at
              # the install path. Red on the current tree (no hooks.json) and on
              # a lost exec bit / rename / wrong matcher the copy would pass silently.
              test-pg-pr-hook-registered =
                pkgs.runCommand "test-pg-pr-hook-registered" { nativeBuildInputs = [ pkgs.jq ]; }
                  ''
                    hooks="${agentSupportMarketplace}/pg-pr/hooks/hooks.json"
                    script="${agentSupportMarketplace}/pg-pr/hooks/require-agent-pr-comment-marker.sh"

                    test -f "$hooks" || {
                      echo "FAIL: pg-pr hooks.json not bundled" >&2
                      exit 1
                    }
                    jq -e . "$hooks" >/dev/null || {
                      echo "FAIL: hooks.json is not valid JSON" >&2
                      exit 1
                    }

                    matcher="$(jq -r '.hooks.PreToolUse[0].matcher' "$hooks")"
                    [ "$matcher" = "Bash" ] || {
                      echo "FAIL: PreToolUse matcher != Bash ($matcher)" >&2
                      exit 1
                    }

                    cmd="$(jq -r '.hooks.PreToolUse[0].hooks[0].command' "$hooks")"
                    case "$cmd" in
                    *'require-agent-pr-comment-marker.sh') ;;
                    *)
                      echo "FAIL: hook command does not target the marker script ($cmd)" >&2
                      exit 1
                      ;;
                    esac

                    test -x "$script" || {
                      echo "FAIL: hook script not executable at install path" >&2
                      exit 1
                    }
                    touch $out
                  '';

              # ceta — the finding's primary motivation: 31 internal rule / engine
              # / patheval security tests, all sandbox-safe (zero net/exec).
              claude-extended-tool-approver-go-tests = mkGoTestCheck {
                pname = "claude-extended-tool-approver-go-tests";
                src = lib.cleanSource ./packages/claude-extended-tool-approver; # matches default.nix
                gomod2nixToml = ./packages/claude-extended-tool-approver/gomod2nix.toml;
              };

              # pb — 10 internal suites (gate ×4, bd, pn, patchid, discover,
              # duration, run). git on PATH for the real-git unit tests; bd/pn
              # tests t.Skip when their tool is absent (matches pb/default.nix
              # nativeCheckInputs). contract/smoke-tagged files stay off by default.
              pb-go-tests = mkGoTestCheck {
                pname = "pb-go-tests";
                src = lib.cleanSource ./packages/pb; # matches default.nix
                gomod2nixToml = ./packages/pb/gomod2nix.toml;
                nativeCheckInputs = [ pkgs.git ];
              };

              # pg-pr — 100+ internal/pkg suites incl. sync/store/auth security
              # seams. exec is temp-repo git (git on PATH) + in-process httptest
              # (loopback, sandbox-ok); the github.com URLs in the fixtures are
              # struct data, not live calls.
              pg-pr-go-tests = mkGoTestCheck {
                pname = "pg-pr-go-tests";
                src = ./packages/pg-pr; # matches default.nix (raw ./., no cleanSource)
                gomod2nixToml = ./packages/pg-pr/gomod2nix.toml;
                nativeCheckInputs = [ pkgs.git ];
              };

              # pa-monitor — the largest suite (bead pg2-ymi3l, fast-follow to
              # pg2-adhga / ADR 0021). Pattern-B module (local replace
              # ../claude-transcript), so root the fileset at packages/ and pass
              # modRoot, mirroring the pa-monitor goLint + default.nix. Uses base
              # `mkGoTest` directly (ADR 0021's preferred builder, not the mkGoApp
              # fallback the other checks still use). Sandbox-hostile tests are
              # guarded two ways (both sanctioned by ADR 0021): (1) tests that
              # spawn the built daemon binary / send real OS signals across
              # processes are split into `*_hostile_test.go` files carrying
              # `//go:build hostile` and stay OFF the default `go test ./...`
              # (developers run `go test -tags hostile ./...`); (2) tests that
              # merely shell out to a system tool absent from the sandbox PATH
              # (`caffeinate`, `ps`) `t.Skip` when the tool is missing, matching
              # the repo's tool-absent skip idiom (pb's bd/pn tests) so a dev
              # machine still exercises them.
              pa-monitor-go-tests = pkgs._agentSupportGoBuilders.mkGoTest {
                pname = "pa-monitor-go-tests";
                src = lib.fileset.toSource {
                  root = ./packages;
                  fileset = lib.fileset.unions [
                    ./packages/pa-monitor
                    ./packages/claude-transcript
                  ];
                };
                modRoot = "pa-monitor";
                gomod2nixToml = ./packages/pa-monitor/gomod2nix.toml;
              };

              # Durable eval test for the claude-marketplaces consumer module
              # (pg2-7j5j). Uses a MOCK marketplace derivation carrying the same
              # passthru shape repo-base's mkClaudeMarketplace produces — no build
              # needed to read passthru. Asserts: registration (extraKnownMarketplaces
              # directory source + enabledPlugins resolved from defaultEnabled +
              # plugins list), the per-plugin override flip, and the per-marketplace
              # disable removing all keys. Pure module eval — no HM/NixOS harness.
              test-claude-marketplaces =
                let
                  # Mock built marketplace: a trivial derivation with the expected
                  # passthru. mkClaudeMarketplace's real output carries identical keys.
                  mockMarketplace = pkgs.runCommand "mock-marketplace" {
                    passthru = {
                      marketplaceName = "mock-repo-marketplace-local";
                      plugins = [
                        {
                          name = "on-plugin";
                          version = "1.0.0+aaaaaaaa";
                          key = "on-plugin@mock-repo-marketplace-local";
                          defaultEnabled = true;
                        }
                        {
                          name = "off-plugin";
                          version = "1.0.0+bbbbbbbb";
                          key = "off-plugin@mock-repo-marketplace-local";
                          defaultEnabled = false;
                        }
                      ];
                    };
                  } "mkdir -p $out/.claude-plugin; echo '{}' > $out/.claude-plugin/marketplace.json";

                  evalCfg =
                    cfg:
                    (lib.evalModules {
                      specialArgs = { inherit pkgs lib; };
                      modules = [
                        ./home/programs/claude-marketplaces/default.nix
                        (
                          { lib, ... }:
                          {
                            # Minimal stubs for the config surface the module reads
                            # and contributes to (the real options live in
                            # claude/claude-settings, not pulled in here).
                            options = {
                              phillipgreenii.programs.claude.enable = lib.mkEnableOption "claude (stub)";
                              phillipgreenii.programs.claude.settings = {
                                extraKnownMarketplaces = lib.mkOption {
                                  type = lib.types.attrsOf (lib.types.attrsOf lib.types.anything);
                                  default = { };
                                };
                                enabledPlugins = lib.mkOption {
                                  type = lib.types.attrsOf lib.types.bool;
                                  default = { };
                                };
                                plugins = lib.mkOption {
                                  type = lib.types.listOf lib.types.str;
                                  default = [ ];
                                };
                              };
                              home.homeDirectory = lib.mkOption {
                                type = lib.types.str;
                                default = "/home/test";
                              };
                              home.file = lib.mkOption {
                                type = lib.types.attrsOf lib.types.anything;
                                default = { };
                              };
                            };
                          }
                        )
                        cfg
                      ];
                    }).config;

                  # Baseline: registered, claude enabled, no overrides.
                  base = evalCfg {
                    phillipgreenii.programs.claude = {
                      enable = true;
                      marketplaces.nixProvided = [ mockMarketplace ];
                    };
                  };
                  baseSettings = base.phillipgreenii.programs.claude.settings;

                  # Per-plugin override flips on-plugin off.
                  overridden = evalCfg {
                    phillipgreenii.programs.claude = {
                      enable = true;
                      marketplaces.nixProvided = [ mockMarketplace ];
                      marketplaces.overrides."on-plugin@mock-repo-marketplace-local" = false;
                    };
                  };

                  # Per-marketplace disable removes all keys.
                  disabled = evalCfg {
                    phillipgreenii.programs.claude = {
                      enable = true;
                      marketplaces.nixProvided = [ mockMarketplace ];
                      marketplaces.enabled."mock-repo-marketplace-local" = false;
                    };
                  };
                  disabledSettings = disabled.phillipgreenii.programs.claude.settings;
                in
                # Registration: directory source + on-disk path.
                assert baseSettings.extraKnownMarketplaces ? "mock-repo-marketplace-local";
                assert
                  baseSettings.extraKnownMarketplaces."mock-repo-marketplace-local".source.source == "directory";
                assert
                  baseSettings.extraKnownMarketplaces."mock-repo-marketplace-local".source.path
                  == "/home/test/.local/share/pgii-marketplaces/mock-repo-marketplace-local";
                # enabledPlugins resolved from defaultEnabled.
                assert baseSettings.enabledPlugins."on-plugin@mock-repo-marketplace-local" == true;
                assert baseSettings.enabledPlugins."off-plugin@mock-repo-marketplace-local" == false;
                # plugins lists all keys regardless of enable state.
                assert lib.elem "on-plugin@mock-repo-marketplace-local" baseSettings.plugins;
                assert lib.elem "off-plugin@mock-repo-marketplace-local" baseSettings.plugins;
                # Symlink under the marketplace root.
                assert base.home.file ? ".local/share/pgii-marketplaces/mock-repo-marketplace-local";
                # Override flips on-plugin off.
                assert
                  overridden.phillipgreenii.programs.claude.settings.enabledPlugins."on-plugin@mock-repo-marketplace-local"
                  == false;
                # Per-marketplace disable removes ALL keys (settings + symlink).
                assert disabledSettings.extraKnownMarketplaces == { };
                assert disabledSettings.enabledPlugins == { };
                assert disabledSettings.plugins == [ ];
                assert disabled.home.file == { };
                pkgs.runCommand "claude-marketplaces-ok" { } "touch $out";

              # Durable eval test (pg2-sikj3) for the integrate-branch-support enable
              # DEFAULT: the CLI (the detector the integrate-branch plugin's dispatcher
              # invokes as a bare PATH command) must ship exactly when the integrate-branch
              # PLUGIN is enabled, so skill + detector can't drift apart (the "CLI not on
              # PATH after apply" incident). Mirrors the mock-marketplace approach of
              # test-claude-marketplaces; reads ONLY the resolved `enable` so cfg.package is
              # never forced.
              test-integrate-branch-support-enable-default =
                let
                  mockMarketplace = pkgs.runCommand "mock-ib-marketplace" {
                    passthru = {
                      marketplaceName = "mock-agent-support-marketplace-local";
                      plugins = [
                        {
                          name = "integrate-branch";
                          version = "0.1.0+aaaaaaaa";
                          key = "integrate-branch@mock-agent-support-marketplace-local";
                          defaultEnabled = true;
                        }
                      ];
                    };
                  } "mkdir -p $out/.claude-plugin; echo '{}' > $out/.claude-plugin/marketplace.json";

                  evalEnable =
                    cfg:
                    (lib.evalModules {
                      specialArgs = { inherit pkgs lib; };
                      modules = [
                        ./home/programs/integrate-branch-support/default.nix
                        (
                          { lib, ... }:
                          {
                            # Stubs for the config surface the module reads/writes; the
                            # real options live in claude / claude-marketplaces /
                            # home-manager, not pulled in here.
                            options = {
                              phillipgreenii.programs.claude.enable = lib.mkEnableOption "claude (stub)";
                              phillipgreenii.programs.claude.marketplaces = {
                                nixProvided = lib.mkOption {
                                  type = lib.types.listOf lib.types.package;
                                  default = [ ];
                                };
                                enabled = lib.mkOption {
                                  type = lib.types.attrsOf lib.types.bool;
                                  default = { };
                                };
                                overrides = lib.mkOption {
                                  type = lib.types.attrsOf lib.types.bool;
                                  default = { };
                                };
                              };
                              home.packages = lib.mkOption {
                                type = lib.types.listOf lib.types.package;
                                default = [ ];
                              };
                              programs.tldr.enable = lib.mkEnableOption "tldr (stub)";
                              programs.tldr.customPages = lib.mkOption {
                                type = lib.types.attrsOf lib.types.anything;
                                default = { };
                              };
                            };
                          }
                        )
                        cfg
                      ];
                    }).config.phillipgreenii.programs.integrate-branch-support.enable;

                  # claude on + integrate-branch plugin present (defaultEnabled) => on.
                  onDefault = evalEnable {
                    phillipgreenii.programs.claude = {
                      enable = true;
                      marketplaces.nixProvided = [ mockMarketplace ];
                    };
                  };
                  # plugin explicitly overridden off => off, even with claude on.
                  overriddenOff = evalEnable {
                    phillipgreenii.programs.claude = {
                      enable = true;
                      marketplaces.nixProvided = [ mockMarketplace ];
                      marketplaces.overrides."integrate-branch@mock-agent-support-marketplace-local" = false;
                    };
                  };
                  # claude disabled => off, even though plugin metadata is present.
                  claudeOff = evalEnable {
                    phillipgreenii.programs.claude = {
                      enable = false;
                      marketplaces.nixProvided = [ mockMarketplace ];
                    };
                  };
                  # explicit opt-in still wins upward.
                  explicitOn = evalEnable {
                    phillipgreenii.programs.claude.enable = false;
                    phillipgreenii.programs.integrate-branch-support.enable = true;
                  };
                in
                assert onDefault == true;
                assert overriddenOff == false;
                assert claudeOff == false;
                assert explicitOn == true;
                pkgs.runCommand "integrate-branch-support-enable-default-ok" { } "touch $out";

              # Regression guard for pg2-1ygj: the claude-settings activation must
              # `marketplace add` every DIRECTORY-source extraKnownMarketplaces entry
              # BEFORE the per-plugin install loop (otherwise the first apply fails
              # "Plugin not found"), and must NOT add github-source marketplaces
              # (those are left to the existing update + install flow). Pure module
              # eval inspecting the generated activation string — no HM harness.
              test-claude-settings-activation-marketplace-add =
                let
                  # The module calls `lib.hm.dag.entryAfter` (home-manager's
                  # extended lib). Stub it to return the raw activation text so we
                  # can inspect the generated script without a full HM harness.
                  hmLib = lib // {
                    hm = (lib.hm or { }) // {
                      dag = (lib.hm.dag or { }) // {
                        entryAfter = _deps: text: text;
                      };
                    };
                  };
                  evalActivation =
                    cfg:
                    (lib.evalModules {
                      specialArgs = {
                        inherit pkgs;
                        lib = hmLib;
                        # The directly-imported claude-settings module builds its
                        # framework scripts (activation-lib + the 3 mkBashScripts),
                        # so it needs these args the homeModules.default wrapper
                        # normally threads via _module.args.
                        inherit inputs;
                        mkBashBuildersFor =
                          p:
                          inputs.phillipgreenii-nix-base.lib.mkBashBuilders {
                            pkgs = p;
                            inherit self;
                            inherit (p) lib;
                          };
                      };
                      modules = [
                        ./home/programs/claude-settings/default.nix
                        (
                          { lib, ... }:
                          {
                            options = {
                              phillipgreenii.programs.claude.enable = lib.mkEnableOption "claude (stub)";
                              home.activation = lib.mkOption {
                                type = lib.types.attrsOf lib.types.anything;
                                default = { };
                              };
                            };
                          }
                        )
                        cfg
                      ];
                    }).config;

                  # A directory marketplace + a github marketplace + the matching
                  # plugin so the install loop (and thus the CLAUDE block) renders.
                  activation =
                    (evalActivation {
                      phillipgreenii.programs.claude = {
                        enable = true;
                        settings = {
                          claudeCodePackage = pkgs.writeShellScriptBin "claude" "exit 0";
                          plugins = [ "some-plugin@dir-mkt" ];
                          extraKnownMarketplaces = {
                            dir-mkt.source = {
                              source = "directory";
                              path = "/home/test/.local/share/pgii-marketplaces/dir-mkt";
                            };
                            gh-mkt.source = {
                              source = "github";
                              repo = "x/y";
                            };
                          };
                        };
                      };
                    }).home.activation.claude-settings;

                  # When there is no directory marketplace, no register-marketplace
                  # invocation should be emitted at all.
                  activationNoDir =
                    (evalActivation {
                      phillipgreenii.programs.claude = {
                        enable = true;
                        settings = {
                          claudeCodePackage = pkgs.writeShellScriptBin "claude" "exit 0";
                          plugins = [ "some-plugin@gh-mkt" ];
                          extraKnownMarketplaces = {
                            gh-mkt.source = {
                              source = "github";
                              repo = "x/y";
                            };
                          };
                        };
                      };
                    }).home.activation.claude-settings;

                  hasSub = needle: haystack: lib.hasInfix needle haystack;

                  # The register-marketplace invocation block (everything after the
                  # "registering directory marketplaces" echo, up to the install
                  # loop). github marketplace names CAN legitimately appear elsewhere
                  # in the activation (e.g. the extraKnownMarketplaces JSON passed to
                  # the replace script), so target the register block specifically.
                  registerBlock =
                    let
                      afterEcho = lib.last (lib.splitString ''act_info "registering directory marketplaces"'' activation);
                    in
                    lib.head (lib.splitString "claude-settings-install-plugin" afterEcho);
                in
                # The directory marketplace is registered (name + on-disk path appear
                # as arguments to the register-marketplace script).
                assert hasSub "claude-settings-register-marketplace" activation;
                assert hasSub ''"dir-mkt"'' registerBlock;
                assert hasSub "/home/test/.local/share/pgii-marketplaces/dir-mkt" registerBlock;
                # The github marketplace is NOT passed to register-marketplace.
                assert !(hasSub ''"gh-mkt"'' registerBlock);
                # The existing global `marketplace update` call is preserved.
                assert hasSub "plugin marketplace update" activation;
                # No directory marketplaces ⇒ no register-marketplace invocation at all.
                assert !(hasSub "claude-settings-register-marketplace" activationNoDir);
                assert hasSub "plugin marketplace update" activationNoDir;
                pkgs.runCommand "claude-settings-activation-marketplace-add-ok" { } "touch $out";

              # Regression guard for pg2-64uu and pg2-e46e: promptCacheTtl writes
              # the correct mutually-exclusive prompt-cache env var into
              # settings.json's `.env` (docs: code.claude.com/docs/en/prompt-caching).
              # "1h" sets ENABLE_PROMPT_CACHING_1H and deletes FORCE_PROMPT_CACHING_5M;
              # "5m" is the inverse; null scrubs BOTH keys (del) so a prior "1h"/"5m"
              # write does not linger, EXCEPT a key pinned via the generic `env`
              # attrset, which null preserves. Pure module eval inspecting the
              # generated activation string — no HM harness.
              test-claude-settings-prompt-cache-ttl =
                let
                  hmLib = lib // {
                    hm = (lib.hm or { }) // {
                      dag = (lib.hm.dag or { }) // {
                        entryAfter = _deps: text: text;
                      };
                    };
                  };
                  evalActivation =
                    cfg:
                    (lib.evalModules {
                      specialArgs = {
                        inherit pkgs inputs;
                        lib = hmLib;
                        mkBashBuildersFor =
                          p:
                          inputs.phillipgreenii-nix-base.lib.mkBashBuilders {
                            pkgs = p;
                            inherit self;
                            inherit (p) lib;
                          };
                      };
                      modules = [
                        ./home/programs/claude-settings/default.nix
                        (
                          { lib, ... }:
                          {
                            options = {
                              phillipgreenii.programs.claude.enable = lib.mkEnableOption "claude (stub)";
                              home.activation = lib.mkOption {
                                type = lib.types.attrsOf lib.types.anything;
                                default = { };
                              };
                            };
                          }
                        )
                        cfg
                      ];
                    }).config;

                  activationFor =
                    ttl:
                    (evalActivation {
                      phillipgreenii.programs.claude = {
                        enable = true;
                        settings.promptCacheTtl = ttl;
                      };
                    }).home.activation.claude-settings;

                  oneHour = activationFor "1h";
                  fiveMin = activationFor "5m";
                  unset = activationFor null;

                  # A conflicting generic `env` entry alongside promptCacheTtl, to
                  # prove the dedicated filter is emitted AFTER the env attrset and
                  # therefore wins (jq's left-to-right `|` chain, last write wins).
                  ordered =
                    (evalActivation {
                      phillipgreenii.programs.claude = {
                        enable = true;
                        settings = {
                          env.ENABLE_PROMPT_CACHING_1H = "bogus";
                          promptCacheTtl = "1h";
                        };
                      };
                    }).home.activation.claude-settings;

                  # promptCacheTtl = null while a cache key is pinned via the
                  # generic `env` attrset: the null cleanup must PRESERVE the pinned
                  # key (skip its del) yet still scrub the other key. One case per
                  # key so each `cfg.env ? KEY` guard is exercised independently.
                  nullPins1h =
                    (evalActivation {
                      phillipgreenii.programs.claude = {
                        enable = true;
                        settings = {
                          env.ENABLE_PROMPT_CACHING_1H = "keep";
                          promptCacheTtl = null;
                        };
                      };
                    }).home.activation.claude-settings;

                  nullPins5m =
                    (evalActivation {
                      phillipgreenii.programs.claude = {
                        enable = true;
                        settings = {
                          env.FORCE_PROMPT_CACHING_5M = "keep";
                          promptCacheTtl = null;
                        };
                      };
                    }).home.activation.claude-settings;

                  # A non-null state deletes the OPPOSITE key even when it is pinned
                  # via `env` — the dedicated option wins over env whenever it is set.
                  oppositePinned =
                    (evalActivation {
                      phillipgreenii.programs.claude = {
                        enable = true;
                        settings = {
                          env.FORCE_PROMPT_CACHING_5M = "keep";
                          promptCacheTtl = "1h";
                        };
                      };
                    }).home.activation.claude-settings;

                  hasSub = needle: haystack: lib.hasInfix needle haystack;

                  # Everything after the generic-env assignment; the dedicated
                  # assignment must appear here (i.e. later in the jq program).
                  afterGenericEnv = lib.last (lib.splitString ''.env["ENABLE_PROMPT_CACHING_1H"] = "bogus"'' ordered);
                in
                # "1h" forces the 1-hour TTL and removes the 5-minute override.
                assert hasSub ''.env.ENABLE_PROMPT_CACHING_1H = "1"'' oneHour;
                assert hasSub "del(.env.FORCE_PROMPT_CACHING_5M)" oneHour;
                assert !(hasSub ''FORCE_PROMPT_CACHING_5M = "1"'' oneHour);
                # "5m" is the inverse: force 5-minute, remove the 1-hour flag.
                assert hasSub ''.env.FORCE_PROMPT_CACHING_5M = "1"'' fiveMin;
                assert hasSub "del(.env.ENABLE_PROMPT_CACHING_1H)" fiveMin;
                assert !(hasSub ''ENABLE_PROMPT_CACHING_1H = "1"'' fiveMin);
                # null (default) scrubs both keys via del, but writes neither value.
                # (The bare key name now appears inside the del, so the negative
                # must match the full assignment substring, not the bare key.)
                assert hasSub "del(.env.ENABLE_PROMPT_CACHING_1H)" unset;
                assert hasSub "del(.env.FORCE_PROMPT_CACHING_5M)" unset;
                assert !(hasSub ''.env.ENABLE_PROMPT_CACHING_1H = "1"'' unset);
                assert !(hasSub ''.env.FORCE_PROMPT_CACHING_5M = "1"'' unset);
                # null preserves a cache key pinned via `env` (skips that del) while
                # still scrubbing the other key. The pin uses the bracket form
                # `.env["KEY"]` and the del the dot form `del(.env.KEY)`, so the two
                # substrings never cross-match.
                assert hasSub ''.env["ENABLE_PROMPT_CACHING_1H"] = "keep"'' nullPins1h;
                assert !(hasSub "del(.env.ENABLE_PROMPT_CACHING_1H)" nullPins1h);
                assert hasSub "del(.env.FORCE_PROMPT_CACHING_5M)" nullPins1h;
                assert hasSub ''.env["FORCE_PROMPT_CACHING_5M"] = "keep"'' nullPins5m;
                assert !(hasSub "del(.env.FORCE_PROMPT_CACHING_5M)" nullPins5m);
                assert hasSub "del(.env.ENABLE_PROMPT_CACHING_1H)" nullPins5m;
                # A non-null state still deletes the opposite key even when it is
                # pinned via `env` (dedicated option wins when set).
                assert hasSub ''.env["FORCE_PROMPT_CACHING_5M"] = "keep"'' oppositePinned;
                assert hasSub "del(.env.FORCE_PROMPT_CACHING_5M)" oppositePinned;
                assert hasSub ''.env.ENABLE_PROMPT_CACHING_1H = "1"'' oppositePinned;
                # Ordering guarantee: the dedicated option overrides a conflicting
                # generic `env` entry because its filter runs later in the chain.
                assert hasSub ''.env["ENABLE_PROMPT_CACHING_1H"] = "bogus"'' ordered;
                assert hasSub ''.env.ENABLE_PROMPT_CACHING_1H = "1"'' afterGenericEnv;
                assert hasSub "del(.env.FORCE_PROMPT_CACHING_5M)" afterGenericEnv;
                pkgs.runCommand "claude-settings-prompt-cache-ttl-ok" { } "touch $out";

              # Regression guard for pg2-a6y3: the sibling nullOr options that write
              # TOP-LEVEL settings keys are deliberate NO-OPs on null — a prior value is
              # left in place, NOT scrubbed (the opposite of promptCacheTtl). This pins
              # that decision so a future "consistency" refactor cannot silently add a
              # del-on-null. Positive controls prove a set value still emits its
              # assignment, so the negative assertions are not vacuous. `sandbox`'s null
              # behavior is intentionally NOT pinned here (a scrub-on-null was left open;
              # see the option description), so only its write path gets a positive control.
              test-claude-settings-nullor-noop =
                let
                  hmLib = lib // {
                    hm = (lib.hm or { }) // {
                      dag = (lib.hm.dag or { }) // {
                        entryAfter = _deps: text: text;
                      };
                    };
                  };
                  evalActivation =
                    cfg:
                    (lib.evalModules {
                      specialArgs = {
                        inherit pkgs inputs;
                        lib = hmLib;
                        mkBashBuildersFor =
                          p:
                          inputs.phillipgreenii-nix-base.lib.mkBashBuilders {
                            pkgs = p;
                            inherit self;
                            inherit (p) lib;
                          };
                      };
                      modules = [
                        ./home/programs/claude-settings/default.nix
                        (
                          { lib, ... }:
                          {
                            options = {
                              phillipgreenii.programs.claude.enable = lib.mkEnableOption "claude (stub)";
                              home.activation = lib.mkOption {
                                type = lib.types.attrsOf lib.types.anything;
                                default = { };
                              };
                            };
                          }
                        )
                        cfg
                      ];
                    }).config;

                  activationWith =
                    settings:
                    (evalActivation {
                      phillipgreenii.programs.claude = {
                        enable = true;
                        inherit settings;
                      };
                    }).home.activation.claude-settings;

                  hasSub = needle: haystack: lib.hasInfix needle haystack;

                  # Every sibling option left at its null default. Only noFlicker and the
                  # promptCacheTtl null-cleanup emit filters in this state.
                  allNull = activationWith { };

                  # Positive controls: a set value must emit its assignment.
                  themeSet = activationWith { theme = "dark"; };
                  statusLineSet = activationWith {
                    statusLine = {
                      type = "command";
                      command = "x";
                    };
                  };
                  thinkingSet = activationWith { showThinkingSummaries = true; };
                  coauthSet = activationWith { includeCoAuthoredBy = true; };
                  clearCtxSet = activationWith { showClearContextOnPlanAccept = true; };
                  sandboxSet = activationWith {
                    sandbox = {
                      enabled = true;
                    };
                  };
                  # sandboxEnabled is guarded by `sandbox == null`, so leave sandbox unset.
                  sandboxEnabledSet = activationWith { sandboxEnabled = true; };
                in
                # null ⇒ neither an assignment nor a del for these top-level keys. The del
                # forms are the real regression guard (a silent scrub would add them). The
                # assignment/del substrings are jq syntax, so they cannot collide with the
                # activation-script boilerplate.
                assert !(hasSub ".statusLine = " allNull);
                assert !(hasSub "del(.statusLine)" allNull);
                assert !(hasSub ".showClearContextOnPlanAccept = " allNull);
                assert !(hasSub "del(.showClearContextOnPlanAccept)" allNull);
                assert !(hasSub ".showThinkingSummaries = " allNull);
                assert !(hasSub "del(.showThinkingSummaries)" allNull);
                assert !(hasSub ".includeCoAuthoredBy = " allNull);
                assert !(hasSub "del(.includeCoAuthoredBy)" allNull);
                assert !(hasSub ".theme = " allNull);
                assert !(hasSub "del(.theme)" allNull);
                # Positive controls: a set value emits its assignment.
                assert hasSub ''.theme = "dark"'' themeSet;
                assert hasSub ".statusLine = " statusLineSet;
                assert hasSub ".showThinkingSummaries = true" thinkingSet;
                assert hasSub ".includeCoAuthoredBy = true" coauthSet;
                assert hasSub ".showClearContextOnPlanAccept = true" clearCtxSet;
                # The sandbox object writes `.sandbox = <json>`; the sandboxEnabled alias
                # writes the dotted `.sandbox.enabled` and NOT the object form. Matching
                # `.sandbox = ` (space-equals) vs `.sandbox.enabled` keeps the two from
                # cross-matching — the same defensive discipline the prompt-cache-ttl test
                # uses for `.env["KEY"]` (bracket) vs `del(.env.KEY)` (dot).
                assert hasSub ".sandbox = " sandboxSet;
                assert hasSub ".sandbox.enabled = true" sandboxEnabledSet;
                assert !(hasSub ".sandbox = " sandboxEnabledSet);
                pkgs.runCommand "claude-settings-nullor-noop-ok" { } "touch $out";

              # Regression guard for pg2-w6us.20: the daemon's OTel config.toml
              # must render on daemon.enable even when the TUI (enable/
              # claude.enable) is off, and must NOT render when nothing is
              # enabled. Pure module eval — no HM/NixOS harness, no package build.
              test-pa-monitor-config-gating =
                let
                  evalCfg =
                    cfg:
                    (lib.evalModules {
                      specialArgs = { inherit pkgs lib; };
                      modules = [
                        ./home/programs/pa-monitor/default.nix
                        (
                          { lib, ... }:
                          {
                            options = {
                              phillipgreenii.programs.claude.enable = lib.mkEnableOption "claude (stub for pa-monitor eval test)";
                              home.packages = lib.mkOption {
                                type = lib.types.listOf lib.types.anything;
                                default = [ ];
                              };
                              xdg.configFile = lib.mkOption {
                                type = lib.types.attrsOf lib.types.anything;
                                default = { };
                              };
                            };
                          }
                        )
                        cfg
                      ];
                    }).config;
                  hasConfig = c: c.xdg.configFile ? "pa-monitor/config.toml";
                  endpoint = {
                    otel.endpoint = "http://127.0.0.1:4317";
                  };
                  daemonOnly = evalCfg {
                    phillipgreenii.programs.pa-monitor = {
                      daemon.enable = true;
                      settings = endpoint;
                    };
                  };
                  tuiOnly = evalCfg {
                    phillipgreenii.programs.claude.enable = true;
                    phillipgreenii.programs.pa-monitor = {
                      enable = true;
                      settings = endpoint;
                    };
                  };
                  neither = evalCfg {
                    phillipgreenii.programs.pa-monitor.settings = endpoint;
                  };
                  bothEnabled = evalCfg {
                    phillipgreenii.programs.claude.enable = true;
                    phillipgreenii.programs.pa-monitor = {
                      enable = true;
                      daemon.enable = true;
                      settings = endpoint;
                    };
                  };
                in
                assert hasConfig daemonOnly; # the fix: daemon.enable alone ⇒ config rendered
                assert hasConfig tuiOnly; # TUI path unchanged ⇒ still rendered
                assert !(hasConfig neither); # nothing enabled ⇒ no file
                assert hasConfig bothEnabled; # both gates ⇒ still rendered
                pkgs.runCommand "pa-monitor-config-gating-ok" { } "touch $out";

              # Regression guard that the pa-monitor binary's version string is
              # actually stamped by the build-time ldflag (versionPath =
              # "main.version" in packages/pa-monitor/default.nix). Before that
              # fix, mkGoApp's default `-X main.Version=` targeted a symbol the
              # code does not declare (cmd/pa-monitor/main.go declares lowercase
              # `var version`), so the linker silently dropped it and every role
              # reported the `var version = "dev"` fallback. That made
              # versioncmp.Mismatch a permanent false-negative, disabling both
              # the stale-daemon warning and the client self-restart feature. The
              # stamped string is baseVersion "0.0.0" + an 8-char lowercase-hex
              # per-source content digest (mkSrcDigest, ADR 0006), i.e. exactly
              # `pa-monitor 0.0.0-XXXXXXXX`; the "dev" fallback fails the glob.
              # This build-time linker behavior is invisible to the Go unit tests
              # (main_test.go only asserts version != "", which "dev" passes), so
              # this check is the sole automated guard for it.
              test-pa-monitor-version-stamped = pkgs.runCommand "pa-monitor-version-stamped" { } ''
                v=$(${pkgs.pa-monitor}/bin/pa-monitor --version)
                case "$v" in
                  "pa-monitor 0.0.0-"????????) touch "$out" ;;
                  *)
                    echo "pa-monitor version not stamped (got: '$v', want 'pa-monitor 0.0.0-<8hex>')" >&2
                    exit 1
                    ;;
                esac
              '';

              test-ollama-wrapper =
                let
                  wrapper = import ./home/programs/ollama/wrapper.nix {
                    inherit pkgs lib;
                    # Stub: bats mocks the binary via OLLAMA_BIN. Using a failing stub
                    # strengthens the override contract — any regression where the wrapper
                    # bypasses OLLAMA_BIN trips this immediately.
                    ollamaPackage = pkgs.writeShellScriptBin "ollama" ''
                      echo "stub ollama: not for runtime use" >&2
                      exit 1
                    '';
                  };
                in
                checksHelpers.testBashScripts {
                  package = wrapper;
                  tests = ./home/programs/ollama/tests;
                  extraInputs = [ ];
                };

              # Test claude-status-line wrapper and part scripts (nerd-font OFF: text fallbacks).
              test-claude-status-line =
                let
                  slScripts = import ./home/programs/claude-status-line/scripts.nix {
                    inherit pkgs lib;
                    nerdFont = false;
                  };
                  wrapperScript = slScripts.mkWrapperScript {
                    parts = slScripts.defaultParts;
                    reserve = 20;
                  };
                in
                checksHelpers.testBashScripts {
                  package = pkgs.writeShellScriptBin "claude-status-line" ''
                    exec ${wrapperScript} "$@"
                  '';
                  tests = ./home/programs/claude-status-line;
                  extraInputs = [ ];
                };

              # Same bats suite, but built with nerd-font ON so the glyph literals are asserted.
              # Cannot reuse checksHelpers.testBashScripts (it can't inject an env var), so this
              # mirrors that helper and exports CLAUDE_SL_TEST_NERD_FONT=1 as the mode marker the
              # shared bats file branches on.
              test-claude-status-line-nerdfont =
                let
                  slScripts = import ./home/programs/claude-status-line/scripts.nix {
                    inherit pkgs lib;
                    nerdFont = true;
                  };
                  wrapperScript = slScripts.mkWrapperScript {
                    parts = slScripts.defaultParts;
                    reserve = 20;
                  };
                  package = pkgs.writeShellScriptBin "claude-status-line" ''
                    exec ${wrapperScript} "$@"
                  '';
                in
                pkgs.runCommand "test-bash-scripts"
                  {
                    nativeBuildInputs = [
                      pkgs.bats
                      pkgs.git
                      pkgs.which
                      package
                    ];
                  }
                  ''
                    export PATH="${package}/bin:$PATH"
                    export CLAUDE_SL_TEST_NERD_FONT=1
                    bats ${./home/programs/claude-status-line}
                    touch $out
                  '';

              test-claude-settings-replace = checksHelpers.testBashScripts {
                package = claudeSettingsScripts.replaceManagedKeys.script;
                tests = claudeSettingsTestSrc "test_replace.bats";
                extraInputs = [
                  pkgs.jq
                  pkgs.coreutils
                ];
              };

              test-claude-settings-install-plugin = checksHelpers.testBashScripts {
                package = claudeSettingsScripts.installPlugin.script;
                tests = claudeSettingsTestSrc "test_install_plugin.bats";
                extraInputs = [
                  pkgs.jq
                  pkgs.coreutils
                ];
              };

              test-claude-settings-register-marketplace = checksHelpers.testBashScripts {
                package = claudeSettingsScripts.registerMarketplace.script;
                tests = claudeSettingsTestSrc "test_register_marketplace.bats";
                extraInputs = [
                  pkgs.coreutils
                ];
              };

              # Validate claude-theme token map: parse as JSON and assert required keys.
              # Uses mock Catppuccin Mocha hex values; actual values come from
              # config.lib.stylix.colors at module evaluation time.
              test-claude-theme-json =
                let
                  mockColors = {
                    base00 = "1e1e2e";
                    base01 = "181825";
                    base02 = "313244";
                    base03 = "45475a";
                    base04 = "585b70";
                    base05 = "cdd6f4";
                    base06 = "f5e0dc";
                    base07 = "b4befe";
                    base08 = "f38ba8";
                    base09 = "fab387";
                    base0A = "f9e2af";
                    base0B = "a6e3a1";
                    base0C = "89dceb";
                    base0D = "89b4fa";
                    base0E = "cba6f7";
                    base0F = "f2cdcd";
                  };
                  tokenMap = import ./home/programs/claude-theme/colors.nix {
                    colors = mockColors;
                  };
                  themeFile = pkgs.writeText "test-stylix-theme.json" (
                    builtins.toJSON {
                      name = "Stylix";
                      base = "dark";
                      overrides = tokenMap;
                    }
                  );
                in
                pkgs.runCommand "check-claude-theme-json" { buildInputs = [ pkgs.jq ]; } ''
                  # Validate JSON is well-formed
                  ${pkgs.jq}/bin/jq empty < ${themeFile}

                  # Assert required semantic tokens are present
                  ${pkgs.jq}/bin/jq -e '
                    .overrides | (
                      has("claude") and
                      has("error") and
                      has("success") and
                      has("warning") and
                      has("text") and
                      has("background") and
                      has("diffAdded") and
                      has("diffRemoved") and
                      has("rate_limit_fill") and
                      has("clawd_body") and
                      has("red_FOR_SUBAGENTS_ONLY") and
                      has("autoAccept") and
                      has("rainbow_red")
                    )
                  ' < ${themeFile}

                  # Assert all values are hex color strings starting with #
                  ${pkgs.jq}/bin/jq -e '
                    .overrides | to_entries | all(.value | test("^#[0-9a-fA-F]{6}$"))
                  ' < ${themeFile}

                  # Assert token count is reasonable (at least 30)
                  count=$(${pkgs.jq}/bin/jq '.overrides | length' < ${themeFile})
                  [ "$count" -ge 30 ] || {
                    echo "Expected at least 30 tokens, got $count"
                    exit 1
                  }

                  touch $out
                '';

              # Validate tuicr theme token map: render via the same TOML generator
              # the module uses, plus a JSON view for jq to assert required keys,
              # hex-format values, and a sane token count.
              test-tuicr-theme =
                let
                  mockColors = {
                    base00 = "1e1e2e";
                    base01 = "181825";
                    base02 = "313244";
                    base03 = "45475a";
                    base04 = "585b70";
                    base05 = "cdd6f4";
                    base06 = "f5e0dc";
                    base07 = "b4befe";
                    base08 = "f38ba8";
                    base09 = "fab387";
                    base0A = "f9e2af";
                    base0B = "a6e3a1";
                    base0C = "89dceb";
                    base0D = "89b4fa";
                    base0E = "cba6f7";
                    base0F = "f2cdcd";
                  };
                  tokens = import ./home/programs/tuicr/theme.nix {
                    colors = mockColors;
                    inherit (pkgs) lib;
                  };
                  tomlFile = (pkgs.formats.toml { }).generate "test-tuicr-stylix.toml" tokens;
                  jsonFile = pkgs.writeText "test-tuicr-stylix.json" (builtins.toJSON tokens);
                in
                pkgs.runCommand "check-tuicr-theme" { buildInputs = [ pkgs.jq ]; } ''
                  # The generated TOML must be serializable and non-empty.
                  test -s ${tomlFile}

                  # Validate JSON view is well-formed
                  ${pkgs.jq}/bin/jq empty < ${jsonFile}

                  # Assert required tokens across each category are present
                  ${pkgs.jq}/bin/jq -e '
                    has("panel_bg") and
                    has("fg_primary") and
                    has("diff_add") and
                    has("diff_del") and
                    has("diff_add_bg") and
                    has("diff_del_bg") and
                    has("syntax_add_bg") and
                    has("file_added") and
                    has("comment_issue") and
                    has("border_focused") and
                    has("status_bar_bg") and
                    has("mode_bg") and
                    has("message_error_bg")
                  ' < ${jsonFile}

                  # Assert all values are hex color strings starting with #
                  ${pkgs.jq}/bin/jq -e '
                    to_entries | all(.value | test("^#[0-9a-fA-F]{6}$"))
                  ' < ${jsonFile}

                  # Assert the full token set is present (tuicr v0.17.1 = 41 tokens)
                  count=$(${pkgs.jq}/bin/jq 'length' < ${jsonFile})
                  [ "$count" -ge 41 ] || {
                    echo "Expected at least 41 tokens, got $count"
                    exit 1
                  }

                  touch $out
                '';
            }
            // (import ./packages/integrate-branch-support {
              inherit pkgs;
              bashBuilders = pkgs._agentSupportBashBuilders;
            }).checks
            # Nine offline golangci-lint gates, one per Go module (pg2-2cuzv):
            # <module>-golangci for each of the six Pattern-A modules plus the
            # three Pattern-B (local-replace) modules.
            // lib.listToAttrs (map (module: goLint { inherit module; }) simpleGoLintModules)
            // lib.listToAttrs patternBGoLints;

          packages = {
            # This repo's own Claude Code marketplace, bundled into the store with
            # content-derived per-plugin version stamping by repo-base's
            # mkClaudeMarketplace (the same builder pn-workspace-rules uses). The
            # committed `claude-marketplace/` tree holds the marketplace manifest +
            # per-plugin dirs (plugin.json + skills/commands/agents/hooks). Each
            # plugin's version is stamped `<declared>+<digest>`; pg-pr ships
            # defaultEnabled=false, the rest default on. Registered into the
            # claude-marketplaces consumer module via marketplaces.nixProvided below.
            phillipgreenii-nix-agent-support-marketplace = agentSupportMarketplace;

            # Re-export overlay-defined Go packages so `nix-update -F` (used in
            # update-deps.sh) can resolve them via flake.packages.<system>.
            # Without this, nix-update 1.14+ reports `pkg = null` (surfaced as
            # "expected a set but found null").
            inherit (pkgs)
              ccpool
              pr-pool
              pb
              claude-extended-tool-approver
              pa-monitor
              pa-monitor-decorator-scope
              pg-pr
              integrate-branch-support
              ;
            # pg-pr SOURCE as a realized store path, for cross-repo gomod2nix
            # Pattern-B consumers (bead pg2-wtjz). phillipg-nix-ziprecruiter's
            # modules/pg-pr-zr has a `replace => …/packages/pg-pr` in its go.mod;
            # a hermetic build there cannot see this sibling repo, so we hand it
            # the source as a store path it copies into its build sandbox. This
            # is the WHOLE pg-pr module tree (go.mod + go.sum + gomod2nix.toml +
            # cmd/internal/pkg) — NOT a built binary. ADR 0008 §Decision.4.
            pg-pr-src = pkgs.runCommand "pg-pr-src" { } ''
              mkdir -p $out
              cp -R ${
                lib.fileset.toSource {
                  root = ./packages/pg-pr;
                  fileset = lib.fileset.fromSource (lib.sources.cleanSource ./packages/pg-pr);
                }
              }/. $out/
            '';
            # fix-lint + install-pre-commit-hooks REMOVED — pre-commit module
            # auto-contributes both (bead pg2-7vhvn).
            # pa-monitor-codegen wraps the gen-proto.sh script with
            # protoc + plugins on PATH so `nix run .#pa-monitor-codegen`
            # works without relying on the user's devbox.
            pa-monitor-codegen = pkgs.writeShellApplication {
              name = "pa-monitor-codegen";
              runtimeInputs = [
                pkgs.protobuf
                pkgs.protoc-gen-go
                pkgs.protoc-gen-go-grpc
              ];
              text = ''
                cd "''${1:-packages/pa-monitor}"
                exec ./scripts/gen-proto.sh
              '';
            };
            # ccpool-contract runs the on-demand, build-tagged (//go:build contract)
            # Claude Code contract suite and prints the per-OUTCOME bucket tally via
            # contract/classify.jq. It drives the REAL claude binary (uses the user's
            # ambient $HOME/$PATH for OAuth, tmux, sqlite3), spends tokens (~8-12 min),
            # and is deliberately NOT a flake check / not in CI. See
            # packages/ccpool/contract/README.md.
            ccpool-contract = pkgs.writeShellApplication {
              name = "ccpool-contract";
              runtimeInputs = [
                pkgs.go
                pkgs.jq
                pkgs.coreutils
              ];
              text = ''
                cd "''${1:-packages/ccpool}"
                go test -tags contract -timeout=0 -p 1 -json ./cmd/ccpool/... \
                  | tee /tmp/ccpool-contract.json \
                  | jq -r -f contract/classify.jq \
                  | sort | uniq -c
              '';
            };
            # pb-contract runs pb's on-demand, build-tagged (//go:build contract)
            # suite that pins the REAL bd/git/pn surfaces (bd gate surface, the
            # co-location invariant, the multi-DB dedupe key, git patch-id
            # behaviour, pn info schema). It drives real bd + git (and optionally
            # pn) in an isolated temp HOME/XDG, and is deliberately NOT a flake
            # check / not in CI. See packages/pb/README.md.
            pb-contract = pkgs.writeShellApplication {
              name = "pb-contract";
              runtimeInputs = [
                pkgs.go
                pkgs.git
                (pkgs.llm-agentsPkgs.beads or llm-agents.packages.${pkgs.stdenv.hostPlatform.system}.beads)
              ];
              text = ''
                cd "''${1:-packages/pb}"
                go test -tags contract -timeout=0 -p 1 ./...
              '';
            };
          };

          # devShells.default is auto-contributed by flakeModules.devshell
          # (nixfmt/statix/deadnix/shellcheck + the pre-commit shellHook + the
          # phillipgreenii.devshell.extraInputs go above).
        };

      flake = {
        darwinModules.default = ./darwin;
        nixosModules.default = ./nixos;
        homeModules.default =
          { lib, pkgs, ... }:
          let
            # Thread the bash-builders factory + inputs to the ./home modules so
            # consumers (e.g. claude-settings) can build activation-lib and their
            # framework scripts WITHOUT any downstream flake having to provide
            # these args. Mirrors how ziprecruiter's modules receive them.
            mkBashBuildersFor =
              p:
              inputs.phillipgreenii-nix-base.lib.mkBashBuilders {
                pkgs = p;
                inherit self;
                inherit (p) lib;
              };
          in
          {
            imports = [ ./home ];
            # `_module.args` must sit under `config` here: this module also sets a
            # top-level `config.<...>` (nixProvided below), and the module system
            # forbids a bare top-level `_module` alongside an explicit `config`.
            config._module.args = { inherit mkBashBuildersFor inputs; };
            # Auto-register repo-base's nix-built Claude marketplace AND this repo's
            # own (consumer half of the pattern documented in repo-base
            # docs/claude-marketplaces.md).
            #
            # System-guarded: repo-base publishes its package only on x86_64-linux +
            # aarch64-darwin (agent-support builds 4 systems), AND a locked repo-base
            # rev may predate the package. The `? …` guards make a missing package a
            # graceful empty no-op instead of an eval error.
            config.phillipgreenii.programs.claude.marketplaces.nixProvided =
              let
                p = inputs.phillipgreenii-nix-base.packages.${pkgs.stdenv.hostPlatform.system} or { };
                own = self.packages.${pkgs.stdenv.hostPlatform.system} or { };
              in
              (lib.optional (p ? phillipg-nix-repo-base-marketplace) p.phillipg-nix-repo-base-marketplace)
              ++ (lib.optional (
                own ? phillipgreenii-nix-agent-support-marketplace
              ) own.phillipgreenii-nix-agent-support-marketplace);
          };
        # Shape-B wrapper: imports the producer's HM module and sets options
        # with this flake's self + name. Downstream consumers see the configured
        # module shape (no further options to set).
        homeModules.install-metadata =
          { ... }:
          {
            imports = [ inputs.phillipgreenii-nix-base.homeModules.install-metadata ];
            phillipgreenii.install-metadata = {
              flakeSelf = self;
              name = "phillipgreenii-nix-agent-support";
            };
          };
        # Export the package overlay WITH the gomod2nix layer folded in, so any
        # consumer applying overlays.default gets pkgs.buildGoApplication (required
        # by the Go packages built via mkGoApp, ADR 0008) without re-adding
        # gomod2nix themselves. Previously overlays.default carried only `overlay`,
        # forcing every consumer (e.g. ziprecruiter) to prepend gomod2nix's overlay
        # to satisfy this flake's own Go packages — a rediscovered burden in the
        # terminal repo (bead pg2-gkhli). gomod2nix MUST precede `overlay` so
        # buildGoApplication exists in `final` when the Go packages evaluate.
        overlays.default = nixpkgs.lib.composeManyExtensions [
          gomod2nix.overlays.default
          overlay
        ];
      };
    };
}
