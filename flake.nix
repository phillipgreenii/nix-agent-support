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
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/master";
    llm-agents.url = "github:numtide/llm-agents.nix";
    phillipgreenii-nix-overlay = {
      url = "github:phillipgreenii/nix-overlay";
      inputs.nixpkgs.follows = "nixpkgs";
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
    nix-vscode-extensions.url = "github:nix-community/nix-vscode-extensions";
    nix-vscode-extensions.inputs.nixpkgs.follows = "nixpkgs";
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
      # Injects ccusage from llm-agents into pkgs so pa-monitor can callPackage it.
      llmAgentsCcusageOverlay = final: _prev: {
        inherit (llm-agents.packages.${final.stdenv.hostPlatform.system}) ccusage;
      };

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
          pythonBuilders = import ./lib/python-package.nix {
            pkgs = final;
            inherit (final) lib;
            inherit (phillipgreenii-nix-base.lib) mkSrcDigest;
          };
        in
        {
          # packages added in later tasks
          _agentSupportBashBuilders = bashBuilders; # expose for modules
          _agentSupportPythonBuilders = pythonBuilders; # expose for modules
          inherit (llm-agents.packages.${final.stdenv.hostPlatform.system}) ccusage;
          bash-scripting = final.callPackage ./packages/bash-scripting { };
          pg-pr = final.callPackage ./packages/pg-pr {
            inherit (goBuilders) mkGoApp;
          };
          pg-pr-plugin = final.callPackage ./packages/pg-pr-plugin { };
          claude-extended-tool-approver = final.callPackage ./packages/claude-extended-tool-approver {
            inherit (goBuilders) mkGoApp;
          };
          ccpool = final.callPackage ./packages/ccpool {
            inherit (goBuilders) mkGoApp;
          };
          pr-pool = final.callPackage ./packages/pr-pool {
            inherit (goBuilders) mkGoApp;
            # No top-level bd/beads overlay attr — source it like gascity (flake.nix:181).
            bd = final.llm-agentsPkgs.beads or llm-agents.packages.${final.stdenv.hostPlatform.system}.beads;
          };
          pa-monitor = final.callPackage ./packages/pa-monitor {
            inherit (goBuilders) mkGoApp;
          };
          pa-monitor-decorator-gc = final.callPackage ./packages/pa-monitor-decorator-gc {
            inherit (goBuilders) mkGoApp;
          };
          pgii-pack-test-fixture = final.callPackage ./packages/pgii-pack-test-fixture { };
          pgii-pack-pr-support = final.callPackage ./packages/pgii-pack-pr-support { };
          pgii-pack-dolt-hacks = final.callPackage ./packages/pgii-pack-dolt-hacks { };
          pgii-pack-workers = final.callPackage ./packages/pgii-pack-workers { };
          pgii-pack-gastown = final.callPackage ./packages/pgii-pack-gastown { };
          pgii-pack-foremen = final.callPackage ./packages/pgii-pack-foremen { };
          goccc = final.callPackage ./packages/goccc { };
          toktrack = final.callPackage ./packages/toktrack { };
          claude-activity =
            let
              result = import ./packages/claude-activity {
                pkgs = final;
                inherit bashBuilders;
              };
            in
            final.symlinkJoin {
              name = "claude-activity";
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
              name = "agent-activity";
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
              name = "wait-for-agents";
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
              name = "git-tools";
              paths = result.packages;
            };
          pw-reset-agents = final.callPackage ./packages/pw-reset-agents { };
          pw-agent-activity = final.callPackage ./packages/pw-agent-activity { };
          # Single source of truth for the gas city package across the
          # workspace. ziprecruiter consumes this via overlays.default and no
          # longer defines its own pkgs/gascity.
          #
          # dolt is threaded from final.unstable so consumers that pin
          # unstable.dolt get that version (ziprecruiter pins the official dolt
          # 2.1.1 release); standalone agent-support resolves final.unstable
          # from the systemOutputs overlay below (nixpkgs-unstable).
          #
          # beads (bd) is bundled into the gc wrapper because the supervisor
          # runs under launchd with a minimal PATH and otherwise can't find bd.
          # Route it through final.llm-agentsPkgs.beads so consumers pin it
          # (ziprecruiter pins beads v1.0.4 there); fall back to the llm-agents
          # input for standalone agent-support builds where that attr is absent.
          gascity = final.callPackage ./packages/gascity {
            inherit (final.unstable) dolt;
            beads = final.llm-agentsPkgs.beads or llm-agents.packages.${final.stdenv.hostPlatform.system}.beads;
          };
          gc-bd-import-breaker =
            let
              result = import ./packages/gc-dolt-maintenance {
                pkgs = final;
                inherit bashBuilders;
                inherit (final) gascity;
              };
            in
            final.symlinkJoin {
              name = "gc-bd-import-breaker";
              paths = result.gc-bd-import-breaker.packages;
            };
          gc-dolt-maintenance =
            let
              result = import ./packages/gc-dolt-maintenance {
                pkgs = final;
                inherit bashBuilders;
                inherit (final) gascity;
              };
            in
            final.symlinkJoin {
              name = "gc-dolt-maintenance";
              paths = result.gc-dolt-maintenance.packages;
            };
        };

      # Fixed pkgs used ONLY to build the pre-commit hook entries below. The
      # `phillipgreenii.pre-commit.extraHooks` option is top-level (system-agnostic),
      # so it cannot close over perSystem `pkgs`; pin the hook tooling to
      # aarch64-darwin, the primary dev host where these hooks run at commit time.
      hookSystem = "aarch64-darwin";
      hookPkgs = import nixpkgs { system = hookSystem; };

      # Custom pre-commit hooks merged into the base set (which already provides
      # treefmt, statix, deadnix, shellcheck --severity=warning, trailing-whitespace,
      # end-of-file-fixer, check-merge-conflicts, check-case-conflicts). Those are
      # DROPPED here as redundant.
      extraHooks = {
        gofmt = {
          enable = true;
          name = "gofmt (pg-pr/pr-pool)";
          entry = "${hookPkgs.go}/bin/gofmt -l -w";
          files = "^packages/(pg-pr|pr-pool)/.*\\.go$";
          types_or = [ "go" ];
        };
        golangci-lint = {
          enable = true;
          name = "golangci-lint (pg-pr)";
          # Default hook runs `golangci-lint run ./<dir>` from the repo root,
          # which fails for monorepo modules (no enclosing go.mod). Override
          # entry to chdir into the pg-pr module first.
          entry = toString (
            hookPkgs.writeShellScript "precommit-golangci-lint-pg-pr" ''
              set -e
              # golangci-lint shells out to `go`; put it on PATH.
              export PATH="${hookPkgs.go}/bin:$PATH"
              # The auto checks.pre-commit runs this hook inside a pure nix build
              # sandbox (NIX_BUILD_TOP set, HOME=/homeless-shelter, no network).
              # golangci-lint needs to download pg-pr's external module deps,
              # which the sandbox cannot do. Skip there; lint normally on a dev
              # machine. Preserves the pre-migration behaviour, where this hook
              # only ran at commit time (it never ran in flake check before).
              if [ -n "''${NIX_BUILD_TOP:-}" ]; then
                echo "golangci-lint (pg-pr): skipped — nix build sandbox (no network for go module download)"
                exit 0
              fi
              cd packages/pg-pr
              ${hookPkgs.golangci-lint}/bin/golangci-lint run ./...
            ''
          );
          files = "^packages/pg-pr/.*\\.go$";
          pass_filenames = false;
        };
        golangci-lint-pr-pool = {
          enable = true;
          name = "golangci-lint (pr-pool)";
          entry = toString (
            hookPkgs.writeShellScript "precommit-golangci-lint-pr-pool" ''
              set -e
              # golangci-lint shells out to `go`; put it on PATH.
              export PATH="${hookPkgs.go}/bin:$PATH"
              # Skip inside the pure nix build sandbox (the auto checks.pre-commit):
              # pr-pool has external deps + a local replace to ../claude-transcript
              # that golangci-lint cannot resolve offline. Lint normally on a dev
              # machine. Mirrors the pg-pr hook guard above.
              if [ -n "''${NIX_BUILD_TOP:-}" ]; then
                echo "golangci-lint (pr-pool): skipped — nix build sandbox (no network for go module download)"
                exit 0
              fi
              cd packages/pr-pool
              ${hookPkgs.golangci-lint}/bin/golangci-lint run ./...
            ''
          );
          files = "^packages/pr-pool/.*\\.go$";
          pass_filenames = false;
        };
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

      phillipgreenii = {
        # Custom hooks merged into the base pre-commit set.
        pre-commit.extraHooks = extraHooks;
      };

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
              # exported overlay's `final.unstable.dolt` (used by gascity)
              # resolves here too. Deliberately NOT part of overlays.default,
              # so it never clobbers a consumer's own `unstable` — e.g.
              # ziprecruiter extends unstable.dolt to the official 2.1.1 release.
              (_final: _prev: {
                unstable = import nixpkgs-unstable {
                  inherit system;
                  config.allowUnfree = true;
                };
              })
              llmAgentsCcusageOverlay
              overlay
            ];
          };
          inherit (pkgs) lib;
        in
        {
          # The perSystem pkgs carries the full agent-support overlay stack
          # (gomod2nix + nix-overlay + unstable + llm-agents ccusage + this
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

          checks = {
            test-update-locks-lib = checksHelpers.testUpdateLocksLib { };

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

            # Test claude-status-line wrapper and part scripts
            test-claude-status-line =
              let
                slScripts = import ./home/programs/claude-status-line/scripts.nix {
                  inherit pkgs lib;
                };
                wrapperScript = slScripts.mkWrapperScript slScripts.defaultParts;
              in
              checksHelpers.testBashScripts {
                package = pkgs.writeShellScriptBin "claude-status-line" ''
                  exec ${wrapperScript} "$@"
                '';
                tests = ./home/programs/claude-status-line;
                extraInputs = [ ];
              };

            test-claude-settings-replace = checksHelpers.testBashScripts {
              package = pkgs.writeShellApplication {
                name = "claude-settings-replace-managed-keys";
                runtimeInputs = [
                  pkgs.jq
                  pkgs.coreutils
                ];
                text = builtins.readFile ./home/programs/claude-settings/replace-managed-keys.sh;
              };
              tests = ./home/programs/claude-settings/tests/test_replace.bats;
              extraInputs = [
                pkgs.jq
                pkgs.coreutils
              ];
            };

            test-claude-settings-install-plugin = checksHelpers.testBashScripts {
              package = pkgs.writeShellApplication {
                name = "claude-settings-install-plugin";
                runtimeInputs = [
                  pkgs.jq
                  pkgs.coreutils
                ];
                text = builtins.readFile ./home/programs/claude-settings/install-plugin.sh;
              };
              tests = ./home/programs/claude-settings/tests/test_install_plugin.bats;
              extraInputs = [
                pkgs.jq
                pkgs.coreutils
              ];
            };

            test-pgii-packs-activation = checksHelpers.testBashScripts {
              package = pkgs.writeShellApplication {
                name = "pgii-packs-activation";
                runtimeInputs = [
                  pkgs.bash
                  pkgs.coreutils
                  pkgs.jq
                  pkgs.gnugrep
                  pkgs.gawk
                  pkgs.gnused
                ];
                text = builtins.readFile ./home/programs/pgii-packs/activation.sh;
              };
              tests = ./home/programs/pgii-packs/tests;
              extraInputs = [
                pkgs.jq
                pkgs.coreutils
                pkgs.gnugrep
                pkgs.gawk
                pkgs.gnused
              ];
            };

            check-pgii-pack-pr-support-layout = pkgs.runCommand "check-pgii-pack-pr-support-layout" { } ''
              pack=${pkgs.pgii-pack-pr-support}
              test -f "$pack/pack.toml"                                   || { echo "missing pack.toml"; exit 1; }
              test -f "$pack/.pack-meta.json"                             || { echo "missing .pack-meta.json"; exit 1; }
              test -f "$pack/agents/pr-reviewer/agent.toml"               || { echo "missing pr-reviewer"; exit 1; }
              test -f "$pack/agents/pr-self-fixer/agent.toml"             || { echo "missing pr-self-fixer"; exit 1; }
              test -f "$pack/agents/pr-triage/agent.toml"                 || { echo "missing pr-triage"; exit 1; }
              # pr-watcher order + script and the check-pr-watcher-recent-runs
              # doctor check were removed 2026-06-08: the standalone
              # org.nixos.pg-pr-sync launchd daemon owns PR sync now, so the
              # gas-city order is gone. No longer asserted here.
              for d in check-pr-agent-woke-no-progress \
                       check-pr-feedback-backlog \
                       check-pr-feedback-throughput \
                       check-pr-orphan-beads \
                       check-hack-1-still-needed; do
                test -f "$pack/doctor/$d/doctor.toml" || { echo "missing doctor/$d/doctor.toml"; exit 1; }
                test -x "$pack/doctor/$d/run.sh"      || { echo "doctor/$d/run.sh not exec"; exit 1; }
              done
              ! find "$pack" -name "*.template" | grep -q . || { echo "stale .template files in pack"; exit 1; }
              ! grep -rnE 'zr\.pr-' "$pack/doctor" >/dev/null 2>&1 || { echo "stale zr.pr- refs in doctor/"; exit 1; }
              touch $out
            '';

            check-pgii-pack-dolt-hacks-layout = pkgs.runCommand "check-pgii-pack-dolt-hacks-layout" { } ''
              pack=${pkgs.pgii-pack-dolt-hacks}
              test -f "$pack/pack.toml"                                   || { echo "missing pack.toml"; exit 1; }
              test -f "$pack/.pack-meta.json"                             || { echo "missing .pack-meta.json"; exit 1; }
              test -d "$pack/formulas"                                    || { echo "missing formulas/"; exit 1; }
              for o in hack-archive-and-compact \
                       hack-autoclose-completed-mols \
                       hack-daily-summary \
                       hack-message-forwarder \
                       hack-mol-dog-jsonl \
                       hack-order-override-watchdog \
                       hack-stale-lock-sweeper; do
                test -f "$pack/orders/$o.toml" || { echo "missing orders/$o.toml"; exit 1; }
                test -x "$pack/scripts/$o.sh"  || { echo "scripts/$o.sh not exec"; exit 1; }
                grep -q "$pack/scripts/$o.sh" "$pack/orders/$o.toml" || { echo "orders/$o.toml exec line not substituted"; exit 1; }
              done
              test -f "$pack/scripts/hack-archive-and-compact.RUNBOOK.md" || { echo "missing RUNBOOK"; exit 1; }
              for d in check-formulas-dir \
                       check-hack-2-still-needed \
                       check-hack-10-still-needed \
                       check-hack-11-still-needed; do
                test -f "$pack/doctor/$d/doctor.toml" || { echo "missing doctor/$d/doctor.toml"; exit 1; }
                test -x "$pack/doctor/$d/run.sh"      || { echo "doctor/$d/run.sh not exec"; exit 1; }
              done
              test -f "$pack/scripts/tests/hack-daily-summary.bats" || { echo "missing bats"; exit 1; }
              test -d "$pack/scripts/tests/fixtures"                || { echo "missing fixtures/"; exit 1; }
              ! find "$pack" -name "*.template" | grep -q . || { echo "stale .template files in pack"; exit 1; }
              ! grep -rnE '/Users/phillipg/gc/assets/imports' "$pack" >/dev/null 2>&1 || { echo "stale legacy assets paths in pack"; exit 1; }
              touch $out
            '';

            check-pgii-pack-workers-layout =
              pkgs.runCommand "check-pgii-pack-workers-layout" { nativeBuildInputs = [ pkgs.jq ]; }
                ''
                  pack=${pkgs.pgii-pack-workers}
                  test -f "$pack/pack.toml"                                    || { echo "missing pack.toml"; exit 1; }
                  test -f "$pack/.pack-meta.json"                              || { echo "missing .pack-meta.json"; exit 1; }
                  test "$(jq -r .scope "$pack/.pack-meta.json")" = "rig"       || { echo ".pack-meta.json scope != rig"; exit 1; }
                  test -f "$pack/agents/worker/agent.toml"                     || { echo "missing agent.toml"; exit 1; }
                  test -f "$pack/agents/worker/prompt.template.md"             || { echo "missing prompt.template.md"; exit 1; }
                  test -x "$pack/agents/worker/scripts/bash-env.sh"            || { echo "bash-env.sh not exec"; exit 1; }
                  grep -qE '\{\{[[:space:]]*\.Rig(Name|Root|)[[:space:]]*\}\}' "$pack/agents/worker/prompt.template.md" || { echo "go-template markers stripped"; exit 1; }
                  grep -qE 'BASH_ENV = "/nix/store/[^/]+-pgii-workers-' "$pack/agents/worker/agent.toml" || { echo "PACK_ROOT not substituted in agent.toml"; exit 1; }
                  ! find "$pack" -name "*.template" -not -name "prompt.template.md" | grep -q . || { echo "stale envsubst .template files"; exit 1; }
                  ! grep -rnE '/Users/phillipg/gc/assets/imports' "$pack" >/dev/null 2>&1 || { echo "stale legacy assets paths"; exit 1; }
                  touch $out
                '';

            check-pgii-pack-gastown-layout =
              pkgs.runCommand "check-pgii-pack-gastown-layout" { nativeBuildInputs = [ pkgs.jq ]; }
                ''
                  pack=${pkgs.pgii-pack-gastown}
                  test -f "$pack/pack.toml"                                    || { echo "missing pack.toml"; exit 1; }
                  test -f "$pack/.pack-meta.json"                              || { echo "missing .pack-meta.json"; exit 1; }
                  test "$(jq -r .scope "$pack/.pack-meta.json")" = "city"      || { echo ".pack-meta.json scope != city"; exit 1; }

                  # Mayor: prompt.md only, NO agent.toml (intentional).
                  test -f "$pack/agents/mayor/prompt.md"                       || { echo "missing mayor/prompt.md"; exit 1; }
                  ! test -f "$pack/agents/mayor/agent.toml"                    || { echo "unexpected mayor/agent.toml (legacy has none)"; exit 1; }

                  # Deacon, operator: standard agent.toml + prompt
                  for a in deacon operator; do
                    test -f "$pack/agents/$a/agent.toml"                       || { echo "missing $a/agent.toml"; exit 1; }
                  done
                  test -f "$pack/agents/deacon/prompt.template.md"             || { echo "missing deacon/prompt.template.md"; exit 1; }
                  test -f "$pack/agents/operator/prompt.md"                    || { echo "missing operator/prompt.md"; exit 1; }

                  # Legacy foreman was retired 2026-05-29 (triage-extension
                  # Phase 11). Verify it's gone — its responsibilities are
                  # now in pgii-pack-foremen's three category foremen.
                  ! test -e "$pack/agents/foreman"                             || { echo "legacy agents/foreman not removed"; exit 1; }

                  # Formula + 3 doctor checks
                  test -f "$pack/formulas/mol-deacon-patrol.toml"              || { echo "missing mol-deacon-patrol formula"; exit 1; }
                  for d in check-gastown-divergence check-misplaced-beads check-stale-beads; do
                    test -f "$pack/doctor/$d/doctor.toml"                      || { echo "missing doctor/$d/doctor.toml"; exit 1; }
                    test -x "$pack/doctor/$d/run.sh"                           || { echo "doctor/$d/run.sh not exec"; exit 1; }
                  done

                  # No leftover envsubst .template files (excluding go-template *.template.md)
                  ! find "$pack" -name "*.template" -not -name "*.template.md" | grep -q . \
                    || { echo "stale envsubst .template files"; exit 1; }

                  # No stale legacy assets/imports paths
                  ! grep -rnE '/Users/phillipg/gc/assets/imports' "$pack" >/dev/null 2>&1 \
                    || { echo "stale legacy assets paths"; exit 1; }
                  touch $out
                '';

            test-pgii-pack-dolt-hacks-bats =
              pkgs.runCommand "test-pgii-pack-dolt-hacks-bats"
                {
                  nativeBuildInputs = [
                    pkgs.bats
                    pkgs.bash
                    pkgs.jq
                  ];
                }
                ''
                  pack=${pkgs.pgii-pack-dolt-hacks}
                  bats "$pack/scripts/tests/hack-daily-summary.bats"
                  touch $out
                '';

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
          // (import ./packages/gc-dolt-maintenance {
            inherit pkgs;
            bashBuilders = pkgs._agentSupportBashBuilders;
            inherit (pkgs) gascity;
          }).checks;

          packages = {
            # Re-export overlay-defined Go packages so `nix-update -F` (used in
            # update-deps.sh) can resolve them via flake.packages.<system>.
            # Without this, nix-update 1.14+ reports `pkg = null` (surfaced as
            # "expected a set but found null").
            inherit (pkgs)
              ccpool
              pr-pool
              claude-extended-tool-approver
              pa-monitor
              pa-monitor-decorator-gc
              pgii-pack-test-fixture
              pgii-pack-pr-support
              pgii-pack-dolt-hacks
              pgii-pack-workers
              pgii-pack-gastown
              pgii-pack-foremen
              pg-pr
              goccc
              toktrack
              gc-bd-import-breaker
              gc-dolt-maintenance
              gascity
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
            fix-lint = pkgs.writeShellScriptBin "fix-lint" ''
              ${lib.getExe pkgs.statix} fix ${./.}
            '';
            # install-pre-commit-hooks REMOVED — pre-commit module auto-contributes it.
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
          {
            imports = [ ./home ];
            config.phillipgreenii.programs.claude.plugins.local.version = lib.mkDefault self.lib.pluginVersion;
            # Auto-register repo-base's nix-built Claude marketplace (consumer half
            # of the pattern documented in repo-base docs/claude-marketplaces.md).
            #
            # System-guarded: repo-base publishes the package only on x86_64-linux +
            # aarch64-darwin (agent-support builds 4 systems), AND the currently-locked
            # repo-base rev predates the package. The `p ? …` guard makes both cases a
            # graceful empty no-op instead of an eval error.
            config.phillipgreenii.programs.claude.marketplaces.nixProvided =
              let
                p = inputs.phillipgreenii-nix-base.packages.${pkgs.stdenv.hostPlatform.system} or { };
              in
              lib.optional (p ? phillipg-nix-repo-base-marketplace) p.phillipg-nix-repo-base-marketplace;
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
        overlays.default = overlay;
        lib = {
          pluginVersion =
            let
              ts = self.lastModifiedDate or "19700101000000";
              year = builtins.substring 0 4 ts;
              rest = builtins.substring 4 10 ts;
            in
            "0.${year}.${rest}";
        };
      };
    };
}
