{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "claude-extended-tool-approver";

  src = lib.cleanSource ./.;

  subPackages = [ "cmd/claude-extended-tool-approver" ];

  gomod2nixToml = ./gomod2nix.toml;

  # This package exports its version as `main.Version` (capitalised).
  versionPath = "main.Version";

  # tc-nz82c: the untagged Go test suite this package build's own check phase
  # runs (buildGoApplication's default doCheck, scoped to `subPackages` above)
  # sums to ~586s — 97.7% of `go test`'s default 600s (10m) timeout, since
  # several of these tests spawn the compiled binary as a subprocess. That
  # margin is thin enough that a Go-toolchain bump or ordinary builder load can
  # push it over 600s mid-run, killing the whole package build with
  # `panic: test timed out after 10m0s` and cascading to anything that
  # installs this package (e.g. homelab's `nixosConfigurations."monorepod"`) —
  # already observed once during a `/pn-workspace-update` relock (tc-fqu7's
  # recurrence).
  #
  # Considered: (1) `checkFlags = [ "-timeout=30m" ];` to just widen the
  # window, and (2) speeding up the suite. Chose instead to disable the
  # package-build check entirely: `flake.nix` already registers
  # `checks.claude-extended-tool-approver-go-tests` via
  # `pkgs._agentSupportGoBuilders.mkGoTest`, which independently runs the FULL
  # untagged suite (`go test ./...`, no `subPackages` scoping) as part of
  # `nix flake check` — a path never reached by a package or
  # nixosConfiguration build. That check was added earlier for an unrelated
  # reason (ceta rule/engine/patheval security tests) but already gives this
  # suite full coverage, so disabling `doCheck` here loses no coverage, only
  # moves WHERE the timeout risk lives — off the deploy/build path and onto
  # `nix flake check`, which is exactly where `-go-tests` checks are meant to
  # carry this cost (see the mkGoTest doc comment in
  # `phillipg-nix-repo-base`'s `lib/go-builders.nix`).
  doCheck = false;

  postInstall = ''
    # Generate shell completions
    mkdir -p $out/share/bash-completion/completions
    mkdir -p $out/share/zsh/site-functions
    mkdir -p $out/share/fish/vendor_completions.d
    $out/bin/claude-extended-tool-approver completion bash > $out/share/bash-completion/completions/claude-extended-tool-approver
    $out/bin/claude-extended-tool-approver completion zsh > $out/share/zsh/site-functions/_claude-extended-tool-approver
    $out/bin/claude-extended-tool-approver completion fish > $out/share/fish/vendor_completions.d/claude-extended-tool-approver.fish
  '';

  meta = {
    description = "Claude Code extended tool approval with rule-based permission evaluation and decision logging";
    mainProgram = "claude-extended-tool-approver";
  };
}
