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
`packages/<pkg>/update-deps.sh` (= `go mod tidy && nix run github:nix-community/gomod2nix/<locked rev> -- generate --with-deps`;
NOT `nix-update`; there is no `vendorHash` for this family). `--with-deps` is REQUIRED
(`phillipg-nix-repo-base` ADR 0031): it writes the `cachePackages` list that primes the Go build
cache, and a plain `generate` silently removes it. Every `packages/*/gomod2nix.toml` module has an
`update-deps.sh` wrapper. `update-locks.sh` schedules a refresh (`go get -u` + `update-deps.sh`)
only for `GO_GET_U_MODULES`; the other modules change only when their own code or `go.mod` does, so
they get no scheduled step and the `go-deps-wired` flake check instead fails when one has no wrapper
or a stale `gomod2nix.toml` (run `packages/<pkg>/update-deps.sh` to fix it). A local `replace => ../sibling` (e.g. `../claude-transcript`) is
resolved natively — use the rooted-fileset + `modRoot` form (Pattern B). Authority and the full
A/B pattern: `phillipg-nix-repo-base` ADR 0008 and its `CLAUDE.md` "Go packages" section. Do not
reintroduce `vendorHash`/`buildGoModule`/`localReplaceModules` for these packages.

**Regenerate with `GOWORK=off`, and the toml scope check.** The root `go.work` is for the editor and
`go-work-vet` only. `update-gomod2nix-deps.sh` and `update-locks.sh` export `GOWORK=off`: under the
workspace, `gomod2nix generate --with-deps` walks every module's `all` scope and writes other modules'
packages into `cachePackages`, which breaks the `go-cache-env` build ("import lookup disabled by
-mod=vendor"; this happened on 2026-11-18). `scripts/check-gomod2nix-cache-scope.sh` enforces the
result: a first-party `cachePackages` entry MUST lie in a module that `go.mod` replaces with a local
path (the builder drops only those, never the module's own packages), every `[mod]` entry MUST
be in `go.sum`, and its version MUST be one `go.sum` hashes and equal the `go.mod` requirement (the
staleness rule). The updater runs it for the regenerated module, library modules included, and exits
3 before `update-locks.sh` can commit; the `go-deps-wired` check runs it over every module. Tests:
`tests/check-go-deps-wired.bats`, `tests/update-gomod2nix-deps-build.bats`.

**The go directive tracks this repo's locked nixpkgs and relies on a same-time relock of each consumer.**
`go-deps-wired` judges every `packages/*/go.mod` directive against `pkgs.go.version` of THIS repo's
own locked nixpkgs. A consumer that makes this repo follow its own nixpkgs (the terminal
`nix-ziprecruiter` does) builds the modules with ITS Go, which this repo cannot see: when that
nixpkgs lags, every directive exceeds the toolchain and the build fails with `go: download go1.N.M
... toolchain not available` (happened on 2026-11-19 after the 2026-11-18 directive bump to Go
1.26.10, against a consumer locked to Go 1.26.8). This is intentional and unchecked here: a
nixpkgs bump in this repo that raises the directive MUST be followed, before the consumer builds,
by relocking the consumer's `nixpkgs` input to a revision whose Go is at least the directive
(operator-run `pn workspace update`). No check can enforce it from inside this repo, because a
consumer's nixpkgs is not an input of this flake; recover by relocking the consumer, not by
lowering the directive.

`mkGoApp` (and so `mkGoBinary`) excludes every `*_test.go` file and every `testdata/` directory from
`src`, and therefore from the version digest and the build input, unless `doCheck = true`
(`phillipg-nix-repo-base` ADR 0006 amendment 2026-11-10). A test-only edit changes no derivation,
including those of packages that consume the module through a local `replace`. Production (non-test)
code MUST NOT depend on tests or `testdata/` (a non-test `//go:embed testdata/...` fails the build
with "no matching files"; such a package MUST keep test-support packages out of its `subPackages`
closure). `doCheck = true` opts out of the filter. Renaming `testdata` to dodge the filter is not
allowed (operator ruling, Phillip, 2026-11-10).

**Cross-module consistency of local replaces** (bead `pg2-cahkj`): `go-deps-wired` checks wiring,
not that sibling modules still build against each other. A dependency bump synced into one module
(e.g. `pg-connector`) leaves every module that consumes it through `replace => ../<module>`
(`pg-desk`, `work-report`, ...) unbuildable, and a hook that only sees the changed module's diff
misses it. The `go-local-replace-build` hook (commit time and `pg-hooks run pre-land`) therefore
runs `go build ./...` in every `packages/*` module with a `=> ../<changed>` replace, but only when a
`packages/<module>/go.mod` or `go.sum` is in the diff. It is a path-scoped hook rather than a nix
check because a nix check is not a land-time gate in this repo; the logic is
`scripts/check-go-local-replace-build.sh`, guarded by `tests/go-local-replace-build.bats` (real
`go`, hermetic fixture). Adding a new local replace needs no wiring: dependents are discovered from
the `go.mod` files.

**Go test gate**: package builds no longer run tests at all — `mkGoApp`/`mkGoBinary` default
`doCheck = false` (operator ruling 2026-10-01, bead `pg2-pla9d.2`), so a green `nix build .#<pkg>`
proves only that the binary compiles, never that any test passes. (Even before that, a package
with `subPackages` set compiled and tested only `cmd/` — proven 2026-08-12, bead `pg2-3nb2t`:
`nix build .#<pkg>` exited 0 while `checks.<pkg>-go-tests` had been red for a week, on a package since removed.) The Go test
gate is the module's `mkGoTest` check: `nix build .#checks.<system>.<pkg>-go-tests` (a full
`nix flake check` would also build it — it builds `checks.*` but NOT `packages.*` — but is NOT a
per-change or land-time gate; build the targeted check). Every Go module with tests MUST have a
`*-go-tests` check in `flake.nix`; a new module without one has no nix-side test coverage. Opting a
package back into `doCheck = true` MUST carry a comment stating why its `*-go-tests` check is not
enough.

**pg-connector unit tier and integration tier** (operator ruling 2026-10-26, bead `pg2-bkwi41`):
`checks.<system>.pg-connector-go-tests`, the commit-time `run-unit-tests` hook and
`pg-hooks run pre-land` all run a plain `go test ./...` (no tags): the UNIT tier. A test that starts
the daemon (the `servetest` harness), builds or runs a binary as a subprocess, or needs a real
external tool (`bd`, `dolt`, `tmux`, `ccpool`) sits in a file tagged `//go:build integration`, which
that run does not compile. The integration tier includes the Linux cross-compile gate
(`cmd/pg-connector/crosscompile_test.go`: `GOOS=linux CGO_ENABLED=0 go vet ./...` plus `go test -c`
for every package, so a `_linux.go` file or `_linux_test.go` that stops compiling fails the gate on a
darwin host; Linux-only tests are only compiled there, pg2-opk5g first runs them) and the `env -i`
independence tests (`cmd/pg-connector-github/envindependence_test.go`). It runs on demand, not on
commit or land: `nix build .#integration-checks.pg-connector-integration-tests` (exactly the tagged
tests, under `-race`, 60m), or `go test -tags integration ./...` from `packages/pg-connector`. Run it
when the daemon, the umbrella or a connector's wire contract changes (and before relying on the
Linux cross-compile gate). It MUST NOT be wired into any hook, `run-unit-tests` or pre-land gate, and
a test MUST NOT move back into the unit tier by dropping its tag or by raising the unit timeout.
Classification is per test (a pure-logic test stays untagged); the regression guards are
`TestUnitTierHasNoIntegrationTests` (unit tier, `cmd/pg-connector/unit_tier_guard_test.go`; fails when an untagged test file matches the criteria; the whole-module scan is made cheap by skipping the parse of files whose text cannot carry a finding and parsing the rest in parallel, so it runs at commit and land time like `TestGHExecChokePoint` and `TestBackendFilesetCoversImports`)
and `checks.<system>.pg-connector-integration-tests-gating` (real `go`: the tag splits every
integration package and both builds vet). Details: `packages/pg-connector/README.md`, "Integration
tier (on demand)". The `contract` tag (real `bd`, real credentials) is a separate on-demand tier.

**pg-connector second-registration tier** (on demand, not a gate): the whole-binary re-run of
`engine`, `enginetest`, `query`, `feed` and `feedop` over a second set of invented names
(`TestWholeSetOverASecondRegistration`) sits behind `//go:build secondregistration`, so the unit tier
runs those packages over the first registration only. Run it with
`nix build .#checks.<system>.pg-connector-second-registration`; it MUST NOT be wired into any hook,
`run-unit-tests` or pre-land gate.

**pg-connector load test (on demand, not a gate)**: `cmd/pg-connector-github/internal/loadtest` is
excluded from `go test ./...` and from `pg-connector-go-tests` (operator ruling 2026-10-25, bead
`pg2-7r19vr`): its `TestMain` skips the whole package unless `PG_CONNECTOR_LOAD_TEST=1`, so a test
file added to it later is covered automatically. Run it by hand when the scheduler, governor or
change engine changes: `nix build .#integration-checks.pg-connector-load-test` (sets the variable, runs
only that package under `-race` with a 60m timeout, fails if a scenario fails). That check MUST NOT be
wired into any hook, `run-unit-tests` or pre-land gate. `pg-connector-load-test-gating` is its cheap
regression guard.
