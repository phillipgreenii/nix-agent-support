{
  lib,
  mkGoApp,
  makeWrapper,
  bd,
  git,
  wtdone,
}:

mkGoApp {
  pname = "pb";

  src = lib.cleanSource ./.;

  subPackages = [ "cmd/pb" ];

  # gomod2nix engine (ADR 0008, Case A): no local replace; third-party deps
  # pinned in gomod2nix.toml; buildGoApplication builds from src — no vendorHash.
  gomod2nixToml = ./gomod2nix.toml;

  # pb exports its version as `main.Version` (capitalised).
  versionPath = "main.Version";

  nativeBuildInputs = [ makeWrapper ];

  # Real-git unit tests run in the build sandbox; bd/pn tests t.Skip when absent.
  # wtdone: `pb drain reclaim` removes a clean worktree through it, and its
  # tests drive the real script against a real fixture repository.
  nativeCheckInputs = [
    git
    wtdone
  ];

  # Runtime PATH deps wrapped: bd (gates) + git (patch-id/history) + wtdone
  # (`pb drain reclaim`'s guarded worktree teardown). `pn` is NOT
  # wrapped — it is an ambient runtime dep (see Global Constraints; agent-support
  # is standalone/no-external-flake-deps so it cannot reference repo-base's pn).
  # pn is present on the apply-env PATH (spec Component 3) and on dev PATHs.
  postInstall = ''
    wrapProgram $out/bin/pb --prefix PATH : ${
      lib.makeBinPath [
        bd
        git
        wtdone
      ]
    }
  '';

  meta = {
    description = "phillip-beads: pn:applied gates + drain-loop helpers (consumes pn workspace info; pn required on PATH)";
    mainProgram = "pb";
  };
}
