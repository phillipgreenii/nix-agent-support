{
  lib,
  mkGoApp,
  makeWrapper,
  # External runtime tools pg-review and its providers exec by bare name.
  pg-ccaudit,
  ccpool,
}:

mkGoApp {
  pname = "pg-review";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at this
  # package dir, with go.mod and the committed gomod2nix.toml side by side. No
  # local `replace`, so no modRoot and no parent-rooted fileset.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  # Build every directory under cmd/, so later packets add binaries (the
  # providers) without editing this file. Each declares `var Version = "dev"`,
  # the mkGoApp default ldflag target.
  subPackages = map (d: "cmd/${d}") (
    builtins.attrNames (lib.filterAttrs (_: t: t == "directory") (builtins.readDir ./cmd))
  );

  # The whole-module Go test gate is checks.<system>.pg-review-go-tests.

  # Bare-name execs of external tools, audited in pg-review, its three providers
  # and the ccpool analyst path:
  #   pg-ccaudit  provider/ccaudit, provider/configinventory, doctor  (wrapped here)
  #   ccpool      internal/ccpool (analyst), metrics/push, doctor     (wrapped here)
  #   pg-review-provider-*  builtin provider commands, from this package's own
  #               bin dir (prefixed here so they resolve under any env)
  #   git, ps     runner/postcheck, runner/lock: base system tools
  #   bd          ledger outbox (configurable tracker command) and
  #   claude-extended-tool-approver  (doctor hook check only): not packages of
  #               this flake, still resolved through the caller's PATH.
  # The strict base env of pg-router role payloads carries no per-user profile,
  # so pg-review carries its own runtime tools (the same way the
  # pg-review-router-adapter wraps pg-review). `--prefix` keeps the inherited
  # PATH. Guarded by `test-pg-review-resolves-runtime-tools`.
  nativeBuildInputs = [ makeWrapper ];
  postInstall = ''
    wrapProgram $out/bin/pg-review --prefix PATH : ${
      lib.makeBinPath [
        (placeholder "out")
        pg-ccaudit
        ccpool
      ]
    }
  '';

  meta = {
    description = "Self-review: find, rule on and track improvements to how agents work";
    mainProgram = "pg-review";
  };
}
