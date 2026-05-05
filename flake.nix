{
  description = "Agent and AI tooling for macOS and Linux (nix-darwin + NixOS)";

  nixConfig = {
    extra-substituters = [ "https://cache.numtide.com" ];
    extra-trusted-public-keys = [
      "niks3.numtide.com-1:DTx8wZduET09hRmMtKdQDxNNthLQETkc/yaX7M4qK0g="
    ];
  };

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-25.11-darwin";
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
    nix-darwin.url = "github:LnL7/nix-darwin/nix-darwin-25.11";
    nix-darwin.inputs.nixpkgs.follows = "nixpkgs";
    home-manager.url = "github:nix-community/home-manager/release-25.11";
    home-manager.inputs.nixpkgs.follows = "nixpkgs";
    nix-vscode-extensions.url = "github:nix-community/nix-vscode-extensions";
    nix-vscode-extensions.inputs.nixpkgs.follows = "nixpkgs";
    flake-utils.url = "github:numtide/flake-utils";
    git-hooks.url = "github:cachix/git-hooks.nix";
    git-hooks.inputs.nixpkgs.follows = "nixpkgs";
    treefmt-nix.url = "github:numtide/treefmt-nix";
    treefmt-nix.inputs.nixpkgs.follows = "nixpkgs";
    stylix = {
      url = "github:danth/stylix/release-25.11";
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
      # Injects ccusage from llm-agents into pkgs so claude-agents-tui can callPackage it.
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
          gitHash = phillipgreenii-nix-base.lib.mkGitHash (self.rev or self.dirtyRev or null);
        in
        {
          # packages added in later tasks
          _agentSupportBashBuilders = bashBuilders; # expose for modules
          bash-scripting = final.callPackage ./packages/bash-scripting { };
          my-code-review-support-cli = final.callPackage ./packages/my-code-review-support-cli {
            inherit gitHash;
          };
          my-code-review-support = final.callPackage ./packages/my-code-review-support { };
          claude-extended-tool-approver = final.callPackage ./packages/claude-extended-tool-approver { };
          claude-agents-tui = final.callPackage ./packages/claude-agents-tui { };
          gh-prreview = final.callPackage ./packages/gh-prreview { inherit gitHash; };
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
                inherit (final) claude-agents-tui;
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
            fix-lint = pkgs.writeShellScriptBin "fix-lint" ''
              ${lib.getExe pkgs.statix} fix ${./.}
            '';
            install-pre-commit-hooks = pkgs.writeShellScriptBin "install-pre-commit-hooks" ''
              ${pre-commit.shellHook}
              echo "Pre-commit hooks installed successfully!"
            '';
            update-locks = pkgs.writeShellApplication {
              name = "update-locks";
              runtimeInputs = [
                pkgs.nix
                pkgs.git
                pkgs.coreutils
              ];
              text = ''
                # shellcheck source=/dev/null
                source "${phillipgreenii-nix-base}/lib/scripts/update-locks-lib.bash"
                ul_setup "phillipgreenii-nix-agent-support" "$PWD"

                case "''${1:-}" in
                --ci) export UL_CI_MODE=true ;;
                "") ;;
                *) echo "Unknown argument: $1" >&2; exit 1 ;;
                esac

                ul_run_step "nix-flake-update" \
                  "update-locks: update nix flake.lock" \
                  nix flake update

                ul_run_step "update-deps-my-code-review-support-cli" \
                  "update-locks: update my-code-review-support-cli Go deps" \
                  bash -c 'cd packages/my-code-review-support-cli && go get -u ./... && go mod tidy'

                ul_run_step "update-deps-claude-extended-tool-approver" \
                  "update-locks: update claude-extended-tool-approver Go deps" \
                  bash -c 'cd packages/claude-extended-tool-approver && go get -u ./... && go mod tidy'

                ul_finalize
              '';
            };
          };

          devShells.default = phillipgreenii-nix-base.lib.mkDevShell {
            inherit pkgs;
            pre-commit-shellHook = pre-commit.shellHook;
          };
        }
      );
    in
    systemOutputs
    // {
      darwinModules.default = ./darwin;
      nixosModules.default = ./nixos;
      homeModules.default = ./home;
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
