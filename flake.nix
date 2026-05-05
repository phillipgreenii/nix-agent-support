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
      phillipgreenii-nix-overlay,
      phillipgreenii-nix-base,
      flake-utils,
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
        in
        {
          # packages added in later tasks
          _agentSupportBashBuilders = bashBuilders; # expose for modules
        };

      systemOutputs = flake-utils.lib.eachDefaultSystem (
        system:
        let
          pkgs = import nixpkgs {
            inherit system;
            overlays = [
              phillipgreenii-nix-overlay.overlays.default
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
            # Per-module tests added as modules are migrated
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
