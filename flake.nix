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
        nixpkgs-unstable.follows = "nixpkgs-unstable";
        llm-agents.follows = "llm-agents";
        nix-vscode-extensions.follows = "nix-vscode-extensions";
        flake-utils.follows = "flake-utils";
        git-hooks.follows = "git-hooks";
        treefmt-nix.follows = "treefmt-nix";
      };
    };
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
    {
      self,
      nixpkgs,
      llm-agents,
      phillipgreenii-nix-overlay,
      phillipgreenii-nix-base,
      flake-utils,
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
        in
        {
          # packages added in later tasks
          _agentSupportBashBuilders = bashBuilders; # expose for modules
          inherit (llm-agents.packages.${final.stdenv.hostPlatform.system}) ccusage;
          bash-scripting = final.callPackage ./packages/bash-scripting { };
          pg-pr = final.callPackage ./packages/pg-pr {
            version = phillipgreenii-nix-base.lib.mkVersion self;
          };
          pg-pr-plugin = final.callPackage ./packages/pg-pr-plugin { };
          claude-extended-tool-approver = final.callPackage ./packages/claude-extended-tool-approver {
            version = phillipgreenii-nix-base.lib.mkVersion self;
          };
          pa-monitor = final.callPackage ./packages/pa-monitor {
            version = phillipgreenii-nix-base.lib.mkVersion self;
          };
          pa-monitor-decorator-gc = final.callPackage ./packages/pa-monitor-decorator-gc {
            version = phillipgreenii-nix-base.lib.mkVersion self;
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
        };

      systemOutputs = flake-utils.lib.eachDefaultSystem (
        system:
        let
          pkgs = import nixpkgs {
            inherit system;
            overlays = [
              phillipgreenii-nix-overlay.overlays.default
              llmAgentsCcusageOverlay
              overlay
            ];
          };
          inherit (pkgs) lib;

          checks-lib = phillipgreenii-nix-base.lib.mkChecks pkgs;
          treefmtEval = phillipgreenii-nix-base.lib.mkTreefmtConfig { inherit pkgs; };
          pre-commit = phillipgreenii-nix-base.lib.mkPreCommitHooks {
            inherit system;
            src = ./.;
            treefmtWrapper = treefmtEval.config.build.wrapper;
            extraHooks = {
              gofmt = {
                enable = true;
                files = "^packages/pg-pr/.*\\.go$";
              };
              golangci-lint = {
                enable = true;
                files = "^packages/pg-pr/.*\\.go$";
                # Default hook runs `golangci-lint run ./<dir>` from the repo root,
                # which fails for monorepo modules (no enclosing go.mod). Override
                # entry to chdir into the pg-pr module first.
                entry = toString (
                  pkgs.writeShellScript "precommit-golangci-lint-pg-pr" ''
                    set -e
                    cd packages/pg-pr
                    ${pkgs.golangci-lint}/bin/golangci-lint run ./...
                  ''
                );
                pass_filenames = false;
              };
            };
          };
        in
        {
          formatter = treefmtEval.config.build.wrapper;

          checks = {
            formatting = treefmtEval.config.build.check self;
            linting = checks-lib.linting ./.;
            test-update-locks-lib = checks-lib.testUpdateLocksLib { };

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
              checks-lib.testBashScripts {
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
              checks-lib.testBashScripts {
                package = pkgs.writeShellScriptBin "claude-status-line" ''
                  exec ${wrapperScript} "$@"
                '';
                tests = ./home/programs/claude-status-line;
                extraInputs = [ ];
              };

            test-claude-settings-replace = checks-lib.testBashScripts {
              package = pkgs.writeShellApplication {
                name = "claude-settings-replace-managed-keys";
                runtimeInputs = [
                  pkgs.jq
                  pkgs.coreutils
                ];
                text = builtins.readFile ./home/programs/claude-settings/replace-managed-keys.sh;
              };
              tests = ./home/programs/claude-settings/tests;
              extraInputs = [
                pkgs.jq
                pkgs.coreutils
              ];
            };

            test-claude-settings-install-plugin = checks-lib.testBashScripts {
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

            test-pgii-packs-activation = checks-lib.testBashScripts {
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
              test -f "$pack/orders/pr-watcher.toml"                      || { echo "missing pr-watcher order"; exit 1; }
              test -f "$pack/orders/wake-on-work.toml"                    || { echo "missing wake-on-work order"; exit 1; }
              test -x "$pack/scripts/pr-watcher.sh"                       || { echo "pr-watcher.sh not exec"; exit 1; }
              test -x "$pack/scripts/wake-on-work.sh"                     || { echo "wake-on-work.sh not exec"; exit 1; }
              for d in check-pr-watcher-recent-runs \
                       check-pr-agent-woke-no-progress \
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
          };

          packages = {
            # Re-export overlay-defined Go packages so `nix-update -F` (used in
            # update-deps.sh) can resolve them via flake.packages.<system>.
            # Without this, nix-update 1.14+ reports `pkg = null` (surfaced as
            # "expected a set but found null").
            inherit (pkgs)
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
              ;
            fix-lint = pkgs.writeShellScriptBin "fix-lint" ''
              ${lib.getExe pkgs.statix} fix ${./.}
            '';
            install-pre-commit-hooks = pkgs.writeShellScriptBin "install-pre-commit-hooks" ''
              ${pre-commit.shellHook}
              echo "Pre-commit hooks installed successfully!"
            '';
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
          };

          devShells.default = phillipgreenii-nix-base.lib.mkDevShell {
            inherit pkgs;
            pre-commit-shellHook = pre-commit.shellHook;
            extraInputs = [ pkgs.go ];
          };
        }
      );
    in
    systemOutputs
    // {
      darwinModules.default = ./darwin;
      nixosModules.default = ./nixos;
      homeModules.default =
        { lib, ... }:
        {
          imports = [ ./home ];
          config.phillipgreenii.programs.claude.plugins.local.version = lib.mkDefault self.lib.pluginVersion;
        };
      homeModules.install-metadata = phillipgreenii-nix-base.lib.mkInstallMetadata {
        flakeSelf = self;
        name = "phillipgreenii-nix-agent-support";
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
}
