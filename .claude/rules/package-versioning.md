---
name: package-versioning
description: Per-source-digest versioning for custom Bash/Python/Go packages, the gomod2nix engine, and the whole-module Go test gate.
paths: ["packages/**", "**/gomod2nix.toml"]
---

# Versioning of Custom Packages

Moved out of the repo's always-on `CLAUDE.md` (tc-ql0o Stage D, 2026-08-26): this detail only
matters while working inside `packages/**`.

Custom artifacts (Bash, Python, Go) version from a **per-source content digest**, never the repo
git rev. The `--version` string is `YY.MM.DD.SSSSS+<srcDigest>` (build-time date + an 8-char digest
of the artifact's own source). It changes iff that artifact's source changes (committed or dirty);
an unrelated commit elsewhere in the repo leaves it cached. As of `phillipg-nix-repo-base` ADR 0011,
the per-source digest now ALSO appears in the derivation `version` for Bash and Python artifacts
(matching Go), so it shows up in `nvd` / "Package changes" output. The helpers (`mkSrcDigest`,
`mkBashScript`/`mkBashBuilders`, `mkGoApp`/`mkGoBinary`, `mkPythonPackage`) do this for you — do
**not** thread a repo `gitHash` into a package build (that rebuilds every stamped artifact on every
commit). The repo git rev belongs only in the repo-meta install-metadata module. Third-party deps
bump only via `update-locks.sh`. Authority: `phillipg-nix-repo-base` ADR 0006; see also the
`bash-scripting` skill's "Help and Version" section.

**Go packages** (`mkGoApp`/`mkGoBinary`) use the **gomod2nix engine** — pass
`gomod2nixToml = ./gomod2nix.toml;`, commit that toml beside `go.mod`, and refresh deps with
`go mod tidy && nix run github:nix-community/gomod2nix -- generate` (NOT `nix-update`; there is no
`vendorHash` for this family). A local `replace => ../sibling` (e.g. `../claude-transcript`) is
resolved natively — use the rooted-fileset + `modRoot` form (Pattern B). Authority and the full
A/B pattern: `phillipg-nix-repo-base` ADR 0008 and its `CLAUDE.md` "Go packages" section. Do not
reintroduce `vendorHash`/`buildGoModule`/`localReplaceModules` for these packages.

**Go test gate**: a Go package with `subPackages` set means `nix build .#<pkg>` compiles only
`cmd/` — packages outside `cmd/` are never compiled and their tests never run, so a green package
build is NOT a whole-module test gate (proven 2026-08-12, bead `pg2-3nb2t`: `nix build .#pg-pr`
exited 0 while `checks.pg-pr-go-tests` had been red for a week). The whole-module gate is
`nix build .#checks.<system>.<pkg>-go-tests`, or the full `nix flake check` — which builds
`checks.*` but NOT `packages.*`.
