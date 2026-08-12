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
        final: prev:
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
          # `pnwf` is built in repo-base (modules/pnwf), not here. Thread it in from
          # repo-base's packages, system-guarded: repo-base publishes only x86_64-linux +
          # aarch64-darwin (this repo builds 4 systems), and a locked repo-base rev may
          # predate the package. The `? pnwf` guard below makes a missing package a
          # graceful no-op (attr absent) instead of an eval error (pg2-xs5cj C1).
          # NB: use `prev` (the input pkgs), never `final`, to derive the system and the
          # attr — deriving the overlay's OUTPUT SHAPE from `final` is a fixpoint cycle.
          basePkgs = phillipgreenii-nix-base.packages.${prev.stdenv.hostPlatform.system} or { };
        in
        {
          # packages added in later tasks
          _agentSupportBashBuilders = bashBuilders; # expose for modules
          _agentSupportPythonBuilders = pythonBuilders; # expose for modules
          _agentSupportGoBuilders = goBuilders; # expose for checks (mirrors bash/python)
          # codeburn: first npm package here — prebuilt-dist repackaging of a published CLI
          # (buildNpmPackage + importNpmLock). Needs no builder args; callPackage supplies
          # buildNpmPackage/fetchurl/importNpmLock/nodejs_22 from the overlaid pkgs.
          codeburn = final.callPackage ./packages/codeburn { };
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
        }
        // prev.lib.optionalAttrs (basePkgs ? pnwf) { inherit (basePkgs) pnwf; };

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

      # The behavior-docs conformance gates run ON THE WAY IN, not only under a
      # full `nix flake check` (bead pg2-wr6lm.4, plan item 6). A gate that fires
      # only on a flake check does not hold the line while a work stream is
      # actively editing the sets it governs, which is exactly when it is needed.
      #
      # WHY pre-commit AND NOT pre-push/CI. Measured on this repo, 2026-07-31:
      # the runner takes 7.3s over both real sets (self-checks 1.6s + 2.2s,
      # trace-extract 0.7s + 0.8s, impl-traces 0.6s, the three inter scripts
      # ~0.6s together). 7.3s on EVERY commit would not be acceptable, so the
      # hook carries a `files` filter and fires only when something it actually
      # governs is staged: a behavior-docs set, an evaluator script, the runner,
      # or the recorded baseline. On every other commit it does not run at all.
      # `always_run = false` plus that filter is what makes pre-commit the right
      # stage rather than pre-push.
      #
      # `pass_filenames = false`: the evaluators take SET DIRECTORIES, not files.
      # The runner resolves everything under `$PWD` — the working tree — so the
      # hook checks what is about to be committed, while the flake check resolves
      # the same runner against the store copy of the flake source.
      phillipgreenii.pre-commit.extraHooks = pkgs: {
        behavior-docs-real-corpus = {
          enable = true;
          name = "behavior-docs-real-corpus";
          description = "run the intra/inter/impl conformance evaluators over the real in-repo behavior-docs sets";
          entry = "${
            pkgs.writeShellApplication {
              name = "behavior-docs-real-corpus-hook";
              runtimeInputs = [
                pkgs.bash
                pkgs.gawk
                pkgs.gnugrep
                pkgs.gnused
                pkgs.coreutils
                pkgs.findutils
              ];
              text = ''
                exec bash "$PWD/tests/behavior-docs-real-corpus.sh" "$PWD"
              '';
            }
          }/bin/behavior-docs-real-corpus-hook";
          language = "system";
          pass_filenames = false;
          always_run = false;
          files = "^(behavior-docs/docs/behavior/|packages/pr-pool/docs/behavior/|packages/pr-pool/.*\\.go$|claude-marketplace/behavior-docs-conformance/|tests/behavior-docs-real-corpus)";
        };
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
          # go: Go packages / pre-commit Go hooks. nodejs: regenerate codeburn's
          # package-lock.json shim on a version bump (`npm install --package-lock-only`).
          phillipgreenii.devshell.extraInputs = [
            pkgs.go
            pkgs.nodejs
          ];

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

              # Schema-agreement gate for the review-input shape (bead pg2-cns7a
              # AC2). internal/reviewinput is the authoritative schema and its Go
              # tests pin it, but the pg-pr reviewer AGENT assets are markdown
              # OUTSIDE the Go module's src (pg-pr-go-tests only sees
              # ./packages/pg-pr), so nothing else can catch the assets drifting
              # away from the verb — which is exactly how the original bug stayed
              # invisible. Feed every documented JSON example from the three
              # reviewer assets to the BUILT pg-pr and assert each finding
              # round-trips into the staged draft with a non-empty body and its
              # documented path/line. Red on the pre-fix tree (bodies decode
              # empty), and red on a drifted asset key, a blank-body example, or a
              # severity that is not one of the three literal values.
              test-pg-pr-review-input-assets =
                pkgs.runCommand "test-pg-pr-review-input-assets"
                  {
                    nativeBuildInputs = [
                      pkgs.pg-pr
                      pkgs.jq
                    ];
                  }
                  ''
                    export HOME="$PWD"
                    export PG_PR_STATE_HOME="$PWD/state"
                    pr=0
                    verified=0

                    for asset in \
                      ${./claude-marketplace/pg-pr/agents/pg-pr-review-code-changes.md} \
                      ${./claude-marketplace/pg-pr/agents/pg-pr-review-pr-structure.md} \
                      ${./claude-marketplace/pg-pr/agents/pg-pr-review-jira-alignment.md}; do
                      name="$(basename "$asset")"
                      rm -f block-*.json

                      # Split out every fenced ```json example (fences may be
                      # list-indented; a ```bash fence never matches the opener).
                      awk '
                        /^[ \t]*```json[ \t]*$/ { n++; f = "block-" n ".json"; inb = 1; next }
                        /^[ \t]*```[ \t]*$/     { inb = 0; next }
                        inb                     { print > f }
                      ' "$asset"

                      test -e block-1.json || {
                        echo "FAIL: $name documents no fenced json example" >&2
                        exit 1
                      }

                      for block in block-*.json; do
                        jq -e . "$block" >/dev/null || {
                          echo "FAIL: $name: $block is not valid JSON" >&2
                          exit 1
                        }
                        # Only the review-payload examples are in scope; a
                        # subagent envelope without `comments` is not one.
                        jq -e 'has("comments")' "$block" >/dev/null || continue

                        pr=$((pr + 1))
                        if ! jq -c '{comments: .comments}' "$block" |
                          pg-pr review draft "$pr" --repo test/repo >/dev/null; then
                          echo "FAIL: $name: $block is REJECTED by 'pg-pr review draft'" >&2
                          echo "      the asset and internal/reviewinput disagree on the schema" >&2
                          exit 1
                        fi

                        draft="$PG_PR_STATE_HOME/reviews/test-repo-$pr.json"
                        # A "lines" array anchors the comment at its LAST line
                        # (its first becomes start_line), so the documented anchor
                        # is max(.lines), not .lines[0] (pg2-3c8mo).
                        want="$(jq -cS '[.comments[]? | {path: (.path // ""), line: ((.line // (if (.lines | type) == "array" then (.lines | max) else null end)) // 0), start_line: (.start_line // (if (.lines | type) == "array" and ((.lines | max) != (.lines | min)) then (.lines | min) else null end) // 0)}]' "$block")"
                        got="$(jq -cS '[.comments[]? | {path: (.path // ""), line: (.line // 0), start_line: (.start_line // 0)}]' "$draft")"
                        if [ "$want" != "$got" ]; then
                          echo "FAIL: $name: $block path/line/start_line did not round-trip" >&2
                          echo "      documented: $want" >&2
                          echo "      staged:     $got" >&2
                          exit 1
                        fi

                        if ! jq -e 'all(.comments[]?; ((.body // "") | length) > 0)' "$draft" >/dev/null; then
                          echo "FAIL: $name: $block staged a comment with an EMPTY body" >&2
                          echo "      (the pg2-cns7a defect: the finding text was dropped)" >&2
                          exit 1
                        fi
                        verified=$((verified + $(jq '.comments | length' "$block")))
                      done
                    done

                    # Guard against the gate passing because it extracted nothing:
                    # one finding per reviewer asset is the documented minimum.
                    if [ "$verified" -lt 3 ]; then
                      echo "FAIL: only $verified documented comment(s) verified, expected >= 3" >&2
                      exit 1
                    fi
                    echo "ok: $verified documented agent finding(s) round-trip through 'pg-pr review draft'"
                    touch $out
                  '';

              # Durable-citation guard for the ccpool surface OUTSIDE the Go
              # module (bead pg2-qkk8n, widening pg2-oxrha's guard).
              #
              # packages/ccpool/cmd/ccpool/spec_citations_test.go bans the section
              # sign across the ccpool Go MODULE, but it walks up to go.mod and the
              # module's nix src is rooted at ./packages (Pattern B) — so ccpool's
              # nix modules under home/, darwin/, and nixos/ are outside BOTH that
              # walk and the build sandbox tree, and are unreachable from Go. Same
              # structural gap as test-pg-pr-review-input-assets above; this check
              # closes it from the repo root. It found and retired
              # home/programs/ccpool/default.nix's `spec <sign>8.1.1/<sign>14 step 6`.
              #
              # Scope is DELIBERATELY the ccpool surface, not the repo: repo-wide
              # the glyph appears 534 times across 146 files (ADR prose, historical
              # docs/superpowers/plans, other packages' own in-repo citations),
              # nearly all legitimate, so a repo-wide ban would need an allowlist
              # longer than the rule. See CLAUDE.md "Citation conventions".
              #
              # A NEW ccpool-surface directory MUST be added to `surface` below.
              test-ccpool-surface-spec-citations =
                let
                  surface = lib.fileset.toSource {
                    root = ./.;
                    fileset = lib.fileset.unions [
                      ./home/programs/ccpool
                      ./darwin/modules/ccpool
                      ./nixos/modules/ccpool
                    ];
                  };
                in
                pkgs.runCommand "test-ccpool-surface-spec-citations" { } ''
                  # Build the glyph from its UTF-8 bytes so THIS file never
                  # contains the character it forbids (the discipline the Go guard
                  # gets from `string(rune(0x00a7))`), and so the match is
                  # locale-independent in the sandbox (no UTF-8 locale there).
                  sign="$(printf '\302\247')"

                  # Liveness self-check, run FIRST: the expected violation count is
                  # zero, so "found nothing" cannot double as proof the scan ran.
                  # Assert the files this invariant exists for are really present —
                  # a rename that moved one out from under the fileset must FAIL
                  # loudly, not silently reduce coverage to nothing.
                  for want in \
                    home/programs/ccpool/default.nix \
                    darwin/modules/ccpool/default.nix \
                    nixos/modules/ccpool/default.nix; do
                    if [ ! -f "${surface}/$want" ]; then
                      echo "FAIL: guard never scanned $want — it was renamed or moved" >&2
                      echo "      out from under this check; update the fileset in flake.nix" >&2
                      exit 1
                    fi
                  done

                  scanned="$(find ${surface} -type f | wc -l)"
                  if [ "$scanned" -lt 3 ]; then
                    echo "FAIL: guard scanned only $scanned file(s); the ccpool surface" >&2
                    echo "      outside the Go module has at least 3" >&2
                    exit 1
                  fi

                  hits="$(cd ${surface} && grep -rn -- "$sign" . || true)"
                  if [ -n "$hits" ]; then
                    echo "FAIL: forbidden section-sign citation on the ccpool surface:" >&2
                    echo "$hits" >&2
                    echo "ccpool's design specs live OUTSIDE this repo (the pn-workspace root's" >&2
                    echo "docs/superpowers/specs/), are used once and abandoned, and are not a" >&2
                    echo "durable record — so a section-number reference into them dangles." >&2
                    echo "State the rule in the comment itself, or cite a durable in-repo owner" >&2
                    echo "by number and PROSE heading name (e.g. \"ADR 0038's Context\")." >&2
                    exit 1
                  fi

                  echo "ok: $scanned file(s) scanned on the ccpool surface outside the Go module; no section-sign citations"
                  touch $out
                '';

              # ── Full-module Go test gates (bead pg2-adhga; converged onto the
              # fleet builder by bead pg2-spwj9) ────────────────────────────────
              #
              # Why these checks exist at all: the package builds pin
              # `subPackages = [ "cmd/<name>" ]`, and the gomod2nix builder's
              # goCheckHook scopes `go test` to `$subPackages` when set — so the
              # shipped-binary build (and thus `nix flake check` via that build)
              # only tests `cmd/`, leaving every `internal/`+`pkg/` suite ungated
              # (ceta's rule tests, pg-pr's sync/store/auth seams, …). The
              # shipped-binary builds keep `subPackages` (stay scoped); only these
              # gates pay the test cost, and only under `nix flake check`, never a
              # system build.
              #
              # Why they call base's `goBuilders.mkGoTest`: it is the fleet's ONE
              # builder for this job (`phillipg-nix-repo-base` ADR 0021). It never
              # sets `subPackages` — that is its defining property, not an
              # accident of the call site — and it runs `go test ./...` from the
              # module root in buildPhase, so full-module coverage no longer
              # depends on the caller remembering to OMIT an attribute. It also
              # runs `go vet` (the `go test` default), which the previous
              # mkGoApp-without-subPackages fallback disabled via gomod2nix's
              # `-vet=off` check hook — so these gates are now strictly stricter
              # than before. Test-time tools move from `nativeCheckInputs` (the
              # mkGoApp check-phase attribute) to `testDeps`, mkGoTest's own
              # attribute, which it places on nativeBuildInputs so the tool is on
              # PATH during the buildPhase test run. The rename is not optional:
              # mkGoTest's argument set is CLOSED (no `...`), so a leftover
              # `nativeCheckInputs` is a hard eval error, not a silently dropped
              # dependency.
              #
              # ceta — the finding's primary motivation: internal rule / engine /
              # patheval security tests. git on PATH for the primary-commit
              # resolver's real-git contract test (builds fixtures only; the
              # resolver itself is filesystem-only, never a git subprocess).
              claude-extended-tool-approver-go-tests = pkgs._agentSupportGoBuilders.mkGoTest {
                pname = "claude-extended-tool-approver-go-tests";
                src = lib.cleanSource ./packages/claude-extended-tool-approver; # matches default.nix
                gomod2nixToml = ./packages/claude-extended-tool-approver/gomod2nix.toml;
                testDeps = [ pkgs.git ];
              };

              # pb — 10 internal suites (gate ×4, bd, pn, patchid, discover,
              # duration, run). git on PATH for the real-git unit tests; bd/pn
              # tests t.Skip when their tool is absent (matches pb/default.nix
              # nativeCheckInputs). contract/smoke-tagged files stay off by default.
              pb-go-tests = pkgs._agentSupportGoBuilders.mkGoTest {
                pname = "pb-go-tests";
                src = lib.cleanSource ./packages/pb; # matches default.nix
                gomod2nixToml = ./packages/pb/gomod2nix.toml;
                testDeps = [ pkgs.git ];
              };

              # pg-pr — 100+ internal/pkg suites incl. sync/store/auth security
              # seams. exec is temp-repo git (git on PATH) + in-process httptest
              # (loopback, sandbox-ok); the github.com URLs in the fixtures are
              # struct data, not live calls.
              pg-pr-go-tests = pkgs._agentSupportGoBuilders.mkGoTest {
                pname = "pg-pr-go-tests";
                src = ./packages/pg-pr; # matches default.nix (raw ./., no cleanSource)
                gomod2nixToml = ./packages/pg-pr/gomod2nix.toml;
                testDeps = [ pkgs.git ];
              };

              # pa-monitor — the largest suite (bead pg2-ymi3l, fast-follow to
              # pg2-adhga / ADR 0021). Pattern-B module (local replace
              # ../claude-transcript), so root the fileset at packages/ and pass
              # modRoot, mirroring the pa-monitor goLint + default.nix. Uses base
              # `mkGoTest`, as every full-module Go test gate in this flake now
              # does (bead pg2-spwj9 retired the last mkGoApp-without-subPackages
              # fallbacks above). Sandbox-hostile tests are
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

              # pr-pool — full internal/* suite PLUS a per-package STATEMENT-
              # coverage gate (bead pg2-hvlyj.19, plan item 5.7 / FIX 2). This is
              # the enforcer the Cluster-5 Go items (.13/.16/.17/.18) cite by
              # name. It EXTENDS the base `mkGoTest` builder (ADR 0021's preferred
              # builder, same as pa-monitor-go-tests): mkGoTest runs
              # `go test -coverprofile=cover.out -covermode=set ./...` from the
              # module root (its vendor/goConfigHook setup is reused verbatim),
              # then an overridden postBuild runs the committed coverage-gate
              # script against the committed thresholds table and FAILs the build
              # if any gated package is below its pinned bar. Pattern-B module
              # (local replace ../ccpool, ../claude-transcript) so root the
              # fileset at packages/ and pass modRoot, mirroring the pr-pool
              # goLint + default.nix (docs excluded — behavior docs, not build
              # inputs). The thresholds start empty and each Go bead activates its
              # line as its package lands (the gate is wired first, extended after).
              pr-pool-go-tests =
                (pkgs._agentSupportGoBuilders.mkGoTest {
                  pname = "pr-pool-go-tests";
                  src = lib.fileset.toSource {
                    root = ./packages;
                    fileset = lib.fileset.unions [
                      (lib.fileset.difference ./packages/pr-pool ./packages/pr-pool/docs)
                      ./packages/ccpool
                      ./packages/claude-transcript
                    ];
                  };
                  modRoot = "pr-pool";
                  gomod2nixToml = ./packages/pr-pool/gomod2nix.toml;
                  testFlags = [
                    "-coverprofile=cover.out"
                    "-covermode=set"
                  ];
                }).overrideAttrs
                  (old: {
                    # mkGoTest's buildPhase ends with `runHook postBuild`; run the
                    # gate there, in the module cwd where `go test ./...` wrote
                    # cover.out and where tests/ (script + thresholds) lives.
                    postBuild = (old.postBuild or "") + ''
                      echo "=== pr-pool per-package statement-coverage gate (bead pg2-hvlyj.19) ==="
                      bash tests/coverage-gate.sh cover.out tests/coverage-thresholds.txt
                    '';
                  });

              # Red/green meta-test for the coverage gate (bead pg2-hvlyj.19
              # acceptance): a synthetic profile deliberately BELOW its bar makes
              # the gate FAIL, at/above PASSES, and a gated-but-absent package
              # FAILS — proving the gate actually blocks, not merely measures.
              test-pr-pool-coverage-gate =
                let
                  gate = ./packages/pr-pool/tests/coverage-gate.sh;
                  # queue = 3/4 stmts = 75%; msgschema = 9/10 = 90%.
                  profile = pkgs.writeText "synthetic.cover" ''
                    mode: set
                    ex/internal/queue/a.go:1.1,2.2 2 1
                    ex/internal/queue/a.go:3.1,4.2 1 1
                    ex/internal/queue/b.go:1.1,2.2 1 0
                    ex/internal/msgschema/s.go:1.1,2.2 9 1
                    ex/internal/msgschema/s.go:3.1,4.2 1 0
                    ex/internal/vaXb/c.go:1.1,2.2 5 1
                  '';
                  belowBar = pkgs.writeText "below.txt" "internal/queue 90\n";
                  atBar = pkgs.writeText "at.txt" "internal/queue 70\ninternal/msgschema 85\n";
                  absent = pkgs.writeText "absent.txt" "internal/doesnotexist 80\n";
                  # A suffix with a regex metachar ('.') must be matched LITERALLY;
                  # it must NOT over-match `ex/internal/vaXb` (bead pg2-vybrv #9).
                  metachar = pkgs.writeText "metachar.txt" "internal/va.b 90\n";
                in
                pkgs.runCommand "test-pr-pool-coverage-gate" { nativeBuildInputs = [ pkgs.bash ]; } ''
                  fail() { echo "META-TEST FAIL: $1" >&2; exit 1; }

                  # BELOW threshold must be rejected (non-zero).
                  if bash ${gate} ${profile} ${belowBar} >/dev/null 2>&1; then
                    fail "gate passed a below-threshold package (should block)"
                  fi

                  # AT/ABOVE threshold must pass (zero).
                  bash ${gate} ${profile} ${atBar} >/dev/null 2>&1 \
                    || fail "gate blocked an at/above-threshold set (should pass)"

                  # A gated-but-absent package must be rejected (non-zero).
                  if bash ${gate} ${profile} ${absent} >/dev/null 2>&1; then
                    fail "gate passed a gated-but-absent package (should block)"
                  fi

                  # A threshold suffix with a regex metachar ('.') must be matched
                  # LITERALLY: it must NOT over-match a package differing only at
                  # that position (ex/internal/vaXb), so the gated suffix is
                  # literally absent and the gate must FAIL (bead pg2-vybrv #9).
                  if bash ${gate} ${profile} ${metachar} >/dev/null 2>&1; then
                    fail "gate passed a suffix whose regex metachar over-matched a different package (should block as absent)"
                  fi

                  echo "coverage-gate meta-test: block/pass/absent/metachar all correct"
                  touch $out
                '';

              # INTRA-evaluator mechanical coverage (bead pg2-hvlyj.14, plan
              # item 5.2): drive the behavior-docs-intra-conformance skill's
              # self-checks.sh over inline-status / floor-leakage FAIL & PASS
              # fixtures and assert it flags / stays clean, plus trace-extract.sh
              # (INV-22 traceability) and capture-prefix-snapshots.sh. bc for the
              # mermaid fence-count section; git for the capture test's synthetic
              # throwaway repo.
              # The check is named for the CONCERN it evaluates (intra), never for
              # a version number: `v2` read as a release of one evaluator when it
              # actually named one of three parallel evaluators (intra / inter /
              # impl), which is why the whole family was renamed.
              # Not checksHelpers.testBashScripts: that helper cannot inject an env
              # var, and the suite also drives the shipped corpus/intra fixtures
              # (bead pg2-vybrv #5) via CORPUS_INTRA_DIR so the durable corpus
              # cannot rot while the gate stays green. Mirrors that helper otherwise.
              test-behavior-docs-intra-conformance =
                let
                  selfChecks = pkgs.writeShellScriptBin "self-checks" ''
                    exec ${./claude-marketplace/behavior-docs-conformance/skills/behavior-docs-intra-conformance/scripts/self-checks.sh} "$@"
                  '';
                  traceExtract = pkgs.writeShellScriptBin "trace-extract" ''
                    exec ${./claude-marketplace/behavior-docs-conformance/skills/behavior-docs-intra-conformance/scripts/trace-extract.sh} "$@"
                  '';
                  capturePrefix = pkgs.writeShellScriptBin "capture-prefix-snapshots" ''
                    exec ${./claude-marketplace/behavior-docs-conformance/skills/behavior-docs-intra-conformance/scripts/capture-prefix-snapshots.sh} "$@"
                  '';
                in
                pkgs.runCommand "test-behavior-docs-intra-conformance"
                  {
                    nativeBuildInputs = [
                      pkgs.bats
                      pkgs.git
                      pkgs.which
                      pkgs.bc
                      pkgs.gnutar
                      selfChecks
                      traceExtract
                      capturePrefix
                    ];
                  }
                  ''
                    export PATH="${selfChecks}/bin:${traceExtract}/bin:${capturePrefix}/bin:$PATH"
                    export CORPUS_INTRA_DIR="${./claude-marketplace/behavior-docs-conformance/skills/behavior-docs-intra-conformance/corpus/intra}"
                    bats ${./tests/behavior-docs-intra-conformance.bats}
                    touch $out
                  '';

              # INTER-evaluator mechanical coverage (bead pg2-hvlyj.15, plan
              # item 5.3): drive the behavior-docs-inter-conformance skill's
              # resolve-imports.sh over a shared owner set and per-seam-check-type
              # implementer fixtures (aligned/stale-name/divergence/external) and
              # assert the classification + exit code, plus reconcile-imports.sh
              # (the BIDIRECTIONAL imports reconciler) and name-collisions.sh.
              # Named for the CONCERN, not a version — see the intra check above.
              # Not checksHelpers.testBashScripts: that helper cannot inject an env
              # var, and the suite also drives the shipped corpus/inter fixtures
              # (bead pg2-vybrv #5) via CORPUS_INTER_DIR so the durable corpus
              # cannot rot while the gate stays green. Mirrors that helper otherwise.
              test-behavior-docs-inter-conformance =
                let
                  resolveImports = pkgs.writeShellScriptBin "resolve-imports" ''
                    exec ${./claude-marketplace/behavior-docs-conformance/skills/behavior-docs-inter-conformance/scripts/resolve-imports.sh} "$@"
                  '';
                  reconcileImports = pkgs.writeShellScriptBin "reconcile-imports" ''
                    exec ${./claude-marketplace/behavior-docs-conformance/skills/behavior-docs-inter-conformance/scripts/reconcile-imports.sh} "$@"
                  '';
                  nameCollisions = pkgs.writeShellScriptBin "name-collisions" ''
                    exec ${./claude-marketplace/behavior-docs-conformance/skills/behavior-docs-inter-conformance/scripts/name-collisions.sh} "$@"
                  '';
                in
                pkgs.runCommand "test-behavior-docs-inter-conformance"
                  {
                    nativeBuildInputs = [
                      pkgs.bats
                      pkgs.git
                      pkgs.which
                      pkgs.gawk
                      resolveImports
                      reconcileImports
                      nameCollisions
                    ];
                  }
                  ''
                    export PATH="${resolveImports}/bin:${reconcileImports}/bin:${nameCollisions}/bin:$PATH"
                    export CORPUS_INTER_DIR="${./claude-marketplace/behavior-docs-conformance/skills/behavior-docs-inter-conformance/corpus/inter}"
                    bats ${./tests/behavior-docs-inter-conformance.bats}
                    touch $out
                  '';

              # IMPL-evaluator mechanical coverage — the third parallel evaluator
              # (implementation vs. its OWN behavior docs). Drives impl-traces.sh
              # over corpus/impl fixtures: a citation that resolves to a
              # definition, one that resolves only through the imports table, one
              # framed as historical, and one that resolves to nothing (FAIL).
              test-behavior-docs-impl-conformance =
                let
                  implTraces = pkgs.writeShellScriptBin "impl-traces" ''
                    exec ${./claude-marketplace/behavior-docs-conformance/skills/behavior-docs-impl-conformance/scripts/impl-traces.sh} "$@"
                  '';
                in
                pkgs.runCommand "test-behavior-docs-impl-conformance"
                  {
                    nativeBuildInputs = [
                      pkgs.bats
                      pkgs.git
                      pkgs.which
                      pkgs.gawk
                      implTraces
                    ];
                  }
                  ''
                    export PATH="${implTraces}/bin:$PATH"
                    export CORPUS_IMPL_DIR="${./claude-marketplace/behavior-docs-conformance/skills/behavior-docs-impl-conformance/corpus/impl}"
                    bats ${./tests/behavior-docs-impl-conformance.bats}
                    touch $out
                  '';

              # REAL-CORPUS gate (plan item 6 / WS-6 item 3) — the highest-value
              # one. Every check above runs over FIXTURES: they prove each
              # evaluator CAN see a defect, and prove nothing about the docs that
              # actually ship. Two known defects reached main precisely because no
              # gate ever read a real set. This check runs all three evaluators
              # over every in-repo behavior-docs set and the real method->pr-pool
              # seam, so a violation in shipped docs fails the build.
              #
              # `${./.}` is the flake source, already realised in the store — so
              # the sets, the evaluator scripts and the runner all come from ONE
              # consistent tree. The identical runner backs the pre-commit hook,
              # invoked with the WORKING TREE as its root instead.
              #
              # The ZR deployment set (phillipg-nix-ziprecruiter) is the third real
              # set and is deliberately NOT here: it lives in another repo, so it
              # is absent from this flake's source and unreachable from the build
              # sandbox. Its seams are checked by running the runner against a
              # workspace checkout, never by this gate.
              test-behavior-docs-real-corpus =
                pkgs.runCommand "test-behavior-docs-real-corpus"
                  {
                    nativeBuildInputs = [
                      pkgs.bash
                      pkgs.gawk
                      pkgs.gnugrep
                      pkgs.gnused
                      pkgs.coreutils
                      pkgs.findutils
                    ];
                  }
                  ''
                    bash ${./tests/behavior-docs-real-corpus.sh} ${./.}
                    touch $out
                  '';

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
                              phillipgreenii.programs.claude-code.enable = lib.mkEnableOption "claude (stub)";
                              phillipgreenii.programs.claude-code.settings = {
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
                    phillipgreenii.programs.claude-code = {
                      enable = true;
                      marketplaces.nixProvided = [ mockMarketplace ];
                    };
                  };
                  baseSettings = base.phillipgreenii.programs.claude-code.settings;

                  # Per-plugin override flips on-plugin off.
                  overridden = evalCfg {
                    phillipgreenii.programs.claude-code = {
                      enable = true;
                      marketplaces.nixProvided = [ mockMarketplace ];
                      marketplaces.overrides."on-plugin@mock-repo-marketplace-local" = false;
                    };
                  };

                  # Per-marketplace disable removes all keys.
                  disabled = evalCfg {
                    phillipgreenii.programs.claude-code = {
                      enable = true;
                      marketplaces.nixProvided = [ mockMarketplace ];
                      marketplaces.enabled."mock-repo-marketplace-local" = false;
                    };
                  };
                  disabledSettings = disabled.phillipgreenii.programs.claude-code.settings;
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
                  overridden.phillipgreenii.programs.claude-code.settings.enabledPlugins."on-plugin@mock-repo-marketplace-local"
                  == false;
                # Per-marketplace disable removes ALL keys (settings + symlink).
                assert disabledSettings.extraKnownMarketplaces == { };
                assert disabledSettings.enabledPlugins == { };
                assert disabledSettings.plugins == [ ];
                assert disabled.home.file == { };
                pkgs.runCommand "claude-marketplaces-ok" { } "touch $out";

              # Durable eval test (pg2-sij2i) for the wayfinder/beads MATCHED PAIR: the
              # `wayfinder-beads` skill in this repo's marketplace exists only to bind
              # `/wayfinder` (from the third-party `mattpocock-skills` plugin) onto beads.
              # Ship one without the other and it fails SILENTLY in the worse direction —
              # `/wayfinder` takes its local-markdown fallback and writes planning state to
              # `.scratch/` files. Same drift-guard rationale as
              # test-integrate-branch-support-enable-default below.
              test-wayfinder-beads-pairing =
                let
                  ownPkgs = self.packages.${pkgs.stdenv.hostPlatform.system} or { };
                  mkt = ownPkgs.phillipgreenii-nix-agent-support-marketplace or null;

                  evalThirdparty =
                    cfg:
                    (lib.evalModules {
                      specialArgs = { inherit pkgs lib; };
                      modules = [
                        ./home/programs/pgii-claude-plugins/default.nix
                        (
                          { lib, ... }:
                          {
                            # Stubs for the surface this module reads/writes; the real
                            # options live in claude-settings / home-manager.
                            options = {
                              home.packages = lib.mkOption {
                                type = lib.types.listOf lib.types.package;
                                default = [ ];
                              };
                              phillipgreenii.programs.claude-code.enable = lib.mkEnableOption "claude (stub)";
                              phillipgreenii.programs.claude-code.settings = {
                                extraKnownMarketplaces = lib.mkOption {
                                  type = lib.types.attrs;
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
                            };
                          }
                        )
                        cfg
                      ];
                    }).config;

                  # This repo's half, as homeModules.default declares it.
                  ours = evalThirdparty {
                    phillipgreenii.programs.claude-code = {
                      enable = true;
                      plugins.thirdparty.officialPlugins = [ "mattpocock-skills" ];
                    };
                  };
                  # Plus a consumer declaring its OWN list, to prove `listOf` concatenates
                  # rather than one definition overriding the other.
                  withConsumer = evalThirdparty {
                    phillipgreenii.programs.claude-code = {
                      enable = true;
                      plugins.thirdparty.officialPlugins = [
                        "mattpocock-skills"
                        "skill-creator"
                      ];
                    };
                  };
                  key = "mattpocock-skills@claude-plugins-official";
                in
                # The plugin half: declared, enabled, and in the install list.
                assert ours.phillipgreenii.programs.claude-code.settings.enabledPlugins.${key} == true;
                assert lib.elem key ours.phillipgreenii.programs.claude-code.settings.plugins;
                # A consumer's own entries survive alongside ours (concatenation).
                assert lib.elem "skill-creator@claude-plugins-official"
                  withConsumer.phillipgreenii.programs.claude-code.settings.plugins;
                assert lib.elem key withConsumer.phillipgreenii.programs.claude-code.settings.plugins;
                # The skill half: this repo's marketplace must actually carry
                # `wayfinder-beads`, enabled by default, or the binding never loads.
                assert mkt != null;
                assert lib.any (p: p.name == "wayfinder-beads" && p.defaultEnabled) mkt.passthru.plugins;
                pkgs.runCommand "wayfinder-beads-pairing-ok" { } "touch $out";

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
                            # real options live in claude-code / claude-marketplaces /
                            # home-manager, not pulled in here.
                            options = {
                              phillipgreenii.programs.claude-code.enable = lib.mkEnableOption "claude (stub)";
                              phillipgreenii.programs.claude-code.marketplaces = {
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
                    phillipgreenii.programs.claude-code = {
                      enable = true;
                      marketplaces.nixProvided = [ mockMarketplace ];
                    };
                  };
                  # plugin explicitly overridden off => off, even with claude on.
                  overriddenOff = evalEnable {
                    phillipgreenii.programs.claude-code = {
                      enable = true;
                      marketplaces.nixProvided = [ mockMarketplace ];
                      marketplaces.overrides."integrate-branch@mock-agent-support-marketplace-local" = false;
                    };
                  };
                  # claude disabled => off, even though plugin metadata is present.
                  claudeOff = evalEnable {
                    phillipgreenii.programs.claude-code = {
                      enable = false;
                      marketplaces.nixProvided = [ mockMarketplace ];
                    };
                  };
                  # explicit opt-in still wins upward.
                  explicitOn = evalEnable {
                    phillipgreenii.programs.claude-code.enable = false;
                    phillipgreenii.programs.integrate-branch-support.enable = true;
                  };
                in
                assert onDefault == true;
                assert overriddenOff == false;
                assert claudeOff == false;
                assert explicitOn == true;
                pkgs.runCommand "integrate-branch-support-enable-default-ok" { } "touch $out";

              # Durable eval test (pg2-xs5cj) for the pnwf enable DEFAULT: the pnwf CLI
              # (the deterministic helper the pn-workspace-rules plugin's workforest
              # stage-skills + /pn-workspace-sync invoke as a bare PATH command) must ship
              # exactly when the pn-workspace-rules PLUGIN is enabled, so skills + helper
              # can't drift apart. Mirrors test-integrate-branch-support-enable-default;
              # reads ONLY the resolved `enable` so cfg.package (pkgs.pnwf) is never forced
              # — which is why this test needs no repo-base package present.
              test-pnwf-enable-default =
                let
                  mockMarketplace = pkgs.runCommand "mock-pnwf-marketplace" {
                    passthru = {
                      marketplaceName = "mock-pnwf-marketplace-local";
                      plugins = [
                        {
                          name = "pn-workspace-rules";
                          version = "0.1.0+aaaaaaaa";
                          key = "pn-workspace-rules@mock-pnwf-marketplace-local";
                          defaultEnabled = true;
                        }
                      ];
                    };
                  } "mkdir -p $out/.claude-plugin; echo '{}' > $out/.claude-plugin/marketplace.json";

                  evalEnable =
                    cfg:
                    (lib.evalModules {
                      # Inject a stub `pnwf` so the module's `pkgs ? pnwf` availability
                      # term is satisfied — this test exercises the PLUGIN-ENABLED
                      # resolution logic, not the package-availability guard (the check's
                      # real pkgs may lack pnwf when the locked repo-base predates it).
                      specialArgs = {
                        pkgs = pkgs // {
                          pnwf = pkgs.hello;
                        };
                        inherit lib;
                      };
                      modules = [
                        ./home/programs/pnwf/default.nix
                        (
                          { lib, ... }:
                          {
                            # Stubs for the config surface the module reads/writes; the
                            # real options live in claude-code / claude-marketplaces /
                            # home-manager, not pulled in here.
                            options = {
                              phillipgreenii.programs.claude-code.enable = lib.mkEnableOption "claude (stub)";
                              phillipgreenii.programs.claude-code.marketplaces = {
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
                    }).config.phillipgreenii.programs.pnwf.enable;

                  # claude on + pn-workspace-rules plugin present (defaultEnabled) => on.
                  onDefault = evalEnable {
                    phillipgreenii.programs.claude-code = {
                      enable = true;
                      marketplaces.nixProvided = [ mockMarketplace ];
                    };
                  };
                  # plugin explicitly overridden off => off, even with claude on.
                  overriddenOff = evalEnable {
                    phillipgreenii.programs.claude-code = {
                      enable = true;
                      marketplaces.nixProvided = [ mockMarketplace ];
                      marketplaces.overrides."pn-workspace-rules@mock-pnwf-marketplace-local" = false;
                    };
                  };
                  # claude disabled => off, even though plugin metadata is present.
                  claudeOff = evalEnable {
                    phillipgreenii.programs.claude-code = {
                      enable = false;
                      marketplaces.nixProvided = [ mockMarketplace ];
                    };
                  };
                  # explicit opt-in still wins upward.
                  explicitOn = evalEnable {
                    phillipgreenii.programs.claude-code.enable = false;
                    phillipgreenii.programs.pnwf.enable = true;
                  };
                in
                assert onDefault == true;
                assert overriddenOff == false;
                assert claudeOff == false;
                assert explicitOn == true;
                pkgs.runCommand "pnwf-enable-default-ok" { } "touch $out";

              # Durable eval test (pg2-t76k8) for the CETA base read-only roots:
              # when claude-code + ceta are enabled, the module must contribute the
              # human-named home inspection roots to extraReadOnlyRoots (which the
              # module exports as CETA_EXTRA_READONLY_ROOTS), consumer additions must
              # list-merge on top, and a disabled module must contribute nothing.
              # Reads ONLY the resolved option so cfg.package / home.packages are
              # never forced (no ceta package needed in the check's pkgs).
              test-ceta-extra-readonly-roots =
                let
                  evalRoots =
                    cfg:
                    (lib.evalModules {
                      specialArgs = { inherit pkgs lib; };
                      modules = [
                        ./home/programs/claude-extended-tool-approver/default.nix
                        (
                          { lib, ... }:
                          {
                            # Stubs for the config surface the module reads/writes;
                            # the real options live in claude-code / home-manager.
                            options = {
                              phillipgreenii.programs.claude-code.enable = lib.mkEnableOption "claude (stub)";
                              home.homeDirectory = lib.mkOption {
                                type = lib.types.str;
                                default = "/home/test";
                              };
                              home.packages = lib.mkOption {
                                type = lib.types.listOf lib.types.package;
                                default = [ ];
                              };
                            };
                          }
                        )
                        cfg
                      ];
                    }).config.phillipgreenii.programs.claude-extended-tool-approver.extraReadOnlyRoots;

                  base = [
                    "/home/test/.beads"
                    "/home/test/.zshrc"
                    "/home/test/.zshenv"
                    "/home/test/.zprofile"
                    "/home/test/.profile"
                    "/home/test/.local/bin"
                    "/home/test/.local/state"
                  ];

                  # claude + ceta enabled => exactly the base inspection roots.
                  enabled = evalRoots {
                    phillipgreenii.programs.claude-code.enable = true;
                    phillipgreenii.programs.claude-extended-tool-approver.enable = true;
                  };
                  # A consumer/machine addition list-merges on top of the base set.
                  withConsumerExtra = evalRoots {
                    phillipgreenii.programs.claude-code.enable = true;
                    phillipgreenii.programs.claude-extended-tool-approver = {
                      enable = true;
                      extraReadOnlyRoots = [ "/org/extra" ];
                    };
                  };
                  # claude disabled => the guarded config is inactive, so the module
                  # contributes nothing (option stays at its empty default).
                  disabled = evalRoots {
                    phillipgreenii.programs.claude-code.enable = false;
                    phillipgreenii.programs.claude-extended-tool-approver.enable = true;
                  };
                in
                assert enabled == base;
                assert lib.length withConsumerExtra == lib.length base + 1;
                assert lib.all (r: lib.elem r withConsumerExtra) (base ++ [ "/org/extra" ]);
                assert disabled == [ ];
                pkgs.runCommand "ceta-extra-readonly-roots-ok" { } "touch $out";

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
                              phillipgreenii.programs.claude-code.enable = lib.mkEnableOption "claude (stub)";
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
                      phillipgreenii.programs.claude-code = {
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
                      phillipgreenii.programs.claude-code = {
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
                              phillipgreenii.programs.claude-code.enable = lib.mkEnableOption "claude (stub)";
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
                      phillipgreenii.programs.claude-code = {
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
                      phillipgreenii.programs.claude-code = {
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
                      phillipgreenii.programs.claude-code = {
                        enable = true;
                        settings = {
                          env.ENABLE_PROMPT_CACHING_1H = "keep";
                          promptCacheTtl = null;
                        };
                      };
                    }).home.activation.claude-settings;

                  nullPins5m =
                    (evalActivation {
                      phillipgreenii.programs.claude-code = {
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
                      phillipgreenii.programs.claude-code = {
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
                              phillipgreenii.programs.claude-code.enable = lib.mkEnableOption "claude (stub)";
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
                      phillipgreenii.programs.claude-code = {
                        enable = true;
                        inherit settings;
                      };
                    }).home.activation.claude-settings;

                  hasSub = needle: haystack: lib.hasInfix needle haystack;

                  # Every option left at its default. NOTE this is no longer an
                  # "everything null" state: cleanupPeriodDays defaults to 365 and so
                  # emits its assignment here (asserted below — that is the fleet-wide
                  # retention guarantee). noFlicker and the promptCacheTtl null-cleanup
                  # are the only other filters emitted.
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

                  # cleanupPeriodDays inverts the sibling convention: non-null DEFAULT
                  # (so every machine retains history), with null as the opt-out no-op
                  # (pg2-3sca9). Both halves are pinned so a future "make it consistent
                  # with its siblings" refactor cannot silently restore the 30-day sweep.
                  cleanupNull = activationWith { cleanupPeriodDays = null; };
                  cleanupSet = activationWith { cleanupPeriodDays = 180; };
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
                # cleanupPeriodDays: the DEFAULT must write 365 with no per-machine
                # wiring — this is the assertion that keeps the fleet off Claude Code's
                # 30-day transcript sweep, so it is the one to read first if it fails.
                assert hasSub ".cleanupPeriodDays = 365" allNull;
                # An explicit value overrides the default.
                assert hasSub ".cleanupPeriodDays = 180" cleanupSet;
                assert !(hasSub ".cleanupPeriodDays = 365" cleanupSet);
                # Explicit null is the sibling-style no-op: neither an assignment nor a
                # del, so a hand-set value survives. Matching `.cleanupPeriodDays = `
                # (space-equals) keeps this from cross-matching the del form.
                assert !(hasSub ".cleanupPeriodDays = " cleanupNull);
                assert !(hasSub "del(.cleanupPeriodDays)" cleanupNull);
                pkgs.runCommand "claude-settings-nullor-noop-ok" { } "touch $out";

              # Plan 5: agent-tooling capability/bundle -> feature-flag wiring
              # (claude-code binary+ceta default-on, ccusage off, perles human-only,
              # bundle enable + child veto). Pure eval; supplies the framework +
              # mkCapability/mkBundle from nix-repo-base (override nix-base locally
              # to exercise the unmerged framework).
              capabilities-wiring-eval =
                let
                  r = import ./tests/capabilities-eval.nix {
                    inherit lib;
                    inherit (phillipgreenii-nix-base.lib) mkCapability mkBundle;
                    framework = phillipgreenii-nix-base.homeModules.capability-framework;
                  };
                  failures = builtins.attrNames (lib.filterAttrs (_: v: v == false) (removeAttrs r [ "allPass" ]));
                in
                pkgs.runCommand "capabilities-wiring-eval" { } (
                  if r.allPass then "touch $out" else throw "capabilities wiring failed: ${toString failures}"
                );

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
                              phillipgreenii.programs.claude-code.enable = lib.mkEnableOption "claude (stub for pa-monitor eval test)";
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
                    phillipgreenii.programs.claude-code.enable = true;
                    phillipgreenii.programs.pa-monitor = {
                      enable = true;
                      settings = endpoint;
                    };
                  };
                  neither = evalCfg {
                    phillipgreenii.programs.pa-monitor.settings = endpoint;
                  };
                  bothEnabled = evalCfg {
                    phillipgreenii.programs.claude-code.enable = true;
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

              # codeburn is a prebuilt-dist repackaging (packages/codeburn) — the runtime deps
              # are supplied by a synthetic shim, so a green build does NOT prove the CLI
              # actually resolves them. Executing `--version` exercises the shebang, the node
              # module resolution of every externalized dep (dist/main.js imports them), and the
              # TS type-stripping of the cli entry — the same failure the spike caught. HOME is
              # isolated so no ambient config/cache is read.
              test-codeburn-version = pkgs.runCommand "codeburn-version" { } ''
                export HOME="$TMPDIR"
                v=$(${pkgs.codeburn}/bin/codeburn --version)
                if [ "$v" = "0.9.19" ]; then
                  touch "$out"
                else
                  echo "codeburn --version mismatch (got: '$v', want '0.9.19')" >&2
                  exit 1
                fi
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

              # Regression guard for pg2-ly6a6: `claude plugin install` / `plugin
              # marketplace update` clone a url/github-source plugin by shelling out
              # to `git` BY NAME, but home-manager REPLACES PATH during activation
              # with a set that has no git — and on darwin git is home-manager
              # provided, so there is no /run/current-system/sw/bin/git fallback
              # either. The observed failure was `Failed to clone repository:` with
              # an EMPTY reason, because there was no git stderr to quote.
              #
              # This asserts git is REACHABLE, not merely declared: it RUNS the
              # wrapped script's own PATH setup in an `env -i` shell and resolves
              # `git` through it, so a future edit that drops pkgs.git from
              # runtimeDeps fails here rather than at someone's next apply.
              test-claude-settings-install-plugin-has-git =
                pkgs.runCommand "claude-settings-install-plugin-has-git-ok"
                  {
                    installPlugin = claudeSettingsScripts.installPlugin.script;
                    gitBin = "${pkgs.git}/bin";
                  }
                  ''
                    set -euo pipefail
                    wrapper="$installPlugin/bin/claude-settings-install-plugin"
                    test -f "$wrapper" || { echo "FAIL: wrapper not found at $wrapper"; exit 1; }

                    # The wrapper is: shebang, then one PATH-extending block per
                    # runtimeDep, then a final `exec` of the real script. Take
                    # EVERYTHING between the shebang and that `exec` — the store-path
                    # lines are INDENTED inside `if` blocks, so a `^PATH=` filter would
                    # silently drop exactly the lines under test and make this vacuous.
                    prologue="$(${pkgs.gawk}/bin/awk '/^exec /{exit} NR>1{print}' "$wrapper")"
                    test -n "$prologue" || { echo "FAIL: no PATH prologue in wrapper"; exit 1; }

                    # Run that prologue under an EMPTY environment over a sentinel PATH,
                    # mirroring activation (PATH replaced wholesale, no git), and ask
                    # whether git resolves. Behavioural, not a grep of the closure.
                    probe() {
                      ${pkgs.coreutils}/bin/env -i ${pkgs.bash}/bin/bash -c "
                        PATH=/nonexistent-activation-path
                        $prologue
                        command -v $1 || true
                      "
                    }

                    if [ -z "$(probe git)" ]; then
                      echo "FAIL: git does NOT resolve from the install-plugin wrapper PATH."
                      echo "      \`claude plugin install\` will fail as 'Failed to clone repository:'"
                      echo "      with an EMPTY reason. Re-add pkgs.git to installPlugin runtimeDeps."
                      echo "      store paths present in the wrapper were:"
                      ${pkgs.gnugrep}/bin/grep -oE "/nix/store/[a-z0-9]{32}-[^'\"]*/bin" "$wrapper" | sort -u
                      exit 1
                    fi

                    # Negative control: an undeclared tool MUST NOT resolve, else the
                    # assertion above would pass no matter what runtimeDeps contained.
                    if [ -n "$(probe definitely-not-a-real-tool)" ]; then
                      echo "FAIL: negative control resolved; this check is vacuous."
                      exit 1
                    fi

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
            # codeburn is a manual-bump npm package (not Go/nix-update); re-exported so
            # `nix build .#codeburn` resolves it via flake.packages.<system>.
            inherit (pkgs) codeburn;
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
        homeModules = {
          default =
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
              # Everything this module contributes sits under ONE `config` attrset. The
              # module system forbids a bare top-level `_module` alongside an explicit
              # `config`, so `_module.args` has to live under `config` too — and statix
              # rejects splitting `config` across several `config.<path> =` assignments,
              # so they are collapsed here rather than written one per concern.
              config = {
                _module.args = { inherit mkBashBuildersFor inputs; };
                phillipgreenii.programs.claude-code = {
                  # Auto-register repo-base's nix-built Claude marketplace AND this repo's
                  # own (consumer half of the pattern documented in repo-base
                  # docs/claude-marketplaces.md).
                  #
                  # System-guarded: repo-base publishes its package only on x86_64-linux +
                  # aarch64-darwin (agent-support builds 4 systems), AND a locked repo-base
                  # rev may predate the package. The `? …` guards make a missing package a
                  # graceful empty no-op instead of an eval error.
                  marketplaces.nixProvided =
                    let
                      p = inputs.phillipgreenii-nix-base.packages.${pkgs.stdenv.hostPlatform.system} or { };
                      own = self.packages.${pkgs.stdenv.hostPlatform.system} or { };
                    in
                    (lib.optional (p ? phillipg-nix-repo-base-marketplace) p.phillipg-nix-repo-base-marketplace)
                    ++ (lib.optional (
                      own ? phillipgreenii-nix-agent-support-marketplace
                    ) own.phillipgreenii-nix-agent-support-marketplace);

                  # `mattpocock-skills` is declared HERE, not in a machine flake, because
                  # it is one half of a MATCHED PAIR with this repo's `wayfinder-beads`
                  # plugin: that skill's entire purpose is to bind `/wayfinder` (and
                  # `/triage`, `/to-tickets`, `/to-spec`) onto beads, so shipping the skill
                  # to a machine without the plugin ships a binding for something absent,
                  # and shipping the plugin without the skill lets `/wayfinder` take its
                  # SILENT local-markdown fallback. Declaring both in this module is what
                  # makes them arrive together on every consumer, rather than only wherever
                  # a machine flake remembered to list the plugin. Guarded by
                  # `checks.<system>.test-wayfinder-beads-pairing`. See bead pg2-sij2i.
                  #
                  # The option is `listOf str`, so this concatenates with any consumer's
                  # own `officialPlugins` rather than overriding it — a machine may still
                  # add its own without touching this.
                  plugins.thirdparty.officialPlugins = [ "mattpocock-skills" ];
                };
              };
            };
          # Shape-B wrapper: imports the producer's HM module and sets options
          # with this flake's self + name. Downstream consumers see the configured
          # module shape (no further options to set).
          install-metadata =
            { ... }:
            {
              imports = [ inputs.phillipgreenii-nix-base.homeModules.install-metadata ];
              phillipgreenii.install-metadata = {
                flakeSelf = self;
                name = "phillipgreenii-nix-agent-support";
              };
            };
          # Light capability model (Plan 5): the agent-tooling capabilities + bundles.
          # Self-contained — bundles in the capability-framework (account.* + bundles.*
          # namespaces), so a consumer imports this ONE module alongside
          # homeModules.default (the feature modules the capabilities enable) to opt
          # accounts into agent tooling via phillipgreenii.{capabilities,bundles}.*.
          capabilities = {
            imports = [
              inputs.phillipgreenii-nix-base.homeModules.capability-framework
              (import ./home/capabilities {
                inherit (inputs.phillipgreenii-nix-base.lib) mkCapability mkBundle;
              })
            ];
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
