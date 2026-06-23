# Nix cross-repo build of `pg-pr-zr` (gomod2nix Pattern B) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **This plan is NIX-heavy and spans TWO git repos.** Every task names its repo. There is no Go-TDD red/green loop for production code (the Go is already written); the "tests" are `nix build .#<pkg>`, the binaries' own `go test` run under nix (`buildGoApplication` runs `doCheck` by default), and `nix flake check` — per repo. Each task lists the exact command and the expected result.

**Bead:** `pg2-wtjz` (P3, labels `go`/`nix`/`tech-debt`) — discovered from `pg2-gjzz`; cross-repo follow-up named in `phillipg-nix-repo-base` ADR 0008 §Decision.4.

**Goal:** Build `pg-pr-cicd-captains-log` and `pg-pr-issues-jira-zr` entirely inside nix (drop the out-of-band `go build` and the user-built `realBinary` requirement) by having `phillipgreenii-nix-agent-support` expose `packages/pg-pr`'s **source** as a flake output, and having `phillipg-nix-ziprecruiter` consume that source into a gomod2nix Pattern-B build sandbox.

**Architecture:** ADR 0008 Pattern B resolves a `replace => ../sibling` natively: `buildGoApplication` reads the `replace` directive from `go.mod` and runs `ln -s ${pwd}/<replace.path> vendor/<importpath>` (gomod2nix `builder/default.nix:127`), so the replace target must physically exist inside the build sandbox at exactly `${pwd}/<replace.path>`. For an **in-repo** sibling that is a `lib.fileset.toSource` union rooted at the common parent. For a **cross-repo** sibling we (1) export the producing repo's source as a store path, and (2) in the consumer, reshape the `replace` to a shallow sibling path and build a synthetic parent dir (via `runCommand`/`linkFarm`) that places the consumer module and the imported pg-pr source as siblings, then point `mkGoApp` at it with `modRoot` set to the module subdir. There is no `vendorHash` anywhere in this family (ADR 0008), so pg-pr source edits never need a hash bump — the symlinked sibling is read live.

**Tech Stack:** Nix (flakes, flake-parts, `lib.fileset`, `lib.fileset.toSource`), gomod2nix `buildGoApplication` via `phillipg-nix-repo-base` `mkGoApp` (`lib/go-builders.nix`), Go 1.25. Two repos:
- **Producer:** `phillipgreenii-nix-agent-support` (`packages/pg-pr`) — branch **`expose-pg-pr-source`**.
- **Consumer:** `phillipg-nix-ziprecruiter` (`modules/pg-pr-zr`) — branch **`pg-pr-zr-nix-build`**.

The consumer already wires the producer as flake input `phillipgreenii-agent-support` (`flake.nix:56-73`) and applies its overlay (`machines/default.nix:165`) plus the `gomod2nix` overlay (`machines/default.nix:163`). `pkgs.buildGoApplication` is therefore already in scope on the consumer's `perSystem` pkgs.

---

## File Structure / decomposition

**Producer (`phillipgreenii-nix-agent-support`):**
- `flake.nix` outputs `packages.<system>` set (`~:736-800`) — add a `pg-pr-src` attr: a store-path derivation of `packages/pg-pr`'s full source (Go + `go.mod` + `go.sum` + `gomod2nix.toml`). This is the only producer-side change. It is additive (no existing output changes).

**Consumer (`phillipg-nix-ziprecruiter`):**
- `modules/pg-pr-zr/go.mod` — reshape the `replace` from the deep `../../../phillipgreenii-nix-agent-support/packages/pg-pr` to a shallow `./pg-pr-src` (a path the synthetic sandbox parent will contain).
- `modules/pg-pr-zr/go.sum` — **NEW (must be generated & committed).** gomod2nix copies `go.sum` from `pwd` (`builder/default.nix:389`); the module currently has none.
- `modules/pg-pr-zr/gomod2nix.toml` — **NEW (must be generated & committed).** Third-party deps come transitively through pg-pr; the toml lists them (pg-pr itself is absent — it is the local replace).
- `modules/pg-pr-zr/build.nix` — **NEW.** A `callPackage`-able derivation factory that (a) assembles the synthetic sandbox parent (consumer module + imported pg-pr source as siblings), (b) calls `mkGoApp` with `modRoot`, and returns the binary package. One file, one responsibility: the cross-repo Pattern-B build.
- `flake.nix` `perSystem.packages` (`~:435-459`) — expose `pg-pr-cicd-captains-log` and `pg-pr-issues-jira-zr` so `nix build .#<bin>` and `nix flake check` build them.
- `flake.nix` `perSystem.checks` (`~:368`) — add a check that builds both binaries (so `nix flake check` exercises the cross-repo build + the binaries' `go test`).
- `modules/pg-pr-zr/default.nix` — switch `jiraWrapper`'s `realBinary` default to the nix-built binary store path; drop the "build it yourself" error branch and the out-of-band-build language in option docs/header.
- `modules/pg-pr-zr/pg-pr-cicd-captains-log` (untracked, gitignored stray binary) — delete from the working tree.

---

## BLOCKING UNKNOWNS — resolve before/while executing (do not guess)

1. **Cleanest flake-output shape for pg-pr source (Task 1).** This plan uses a **derivation that copies a `lib.fileset.toSource` of `packages/pg-pr`** exposed as `packages.<system>.pg-pr-src` (a realized store path the consumer reads via `inputs.phillipgreenii-agent-support.packages.<system>.pg-pr-src`). Alternative shapes the executor may prefer if Task 1's verify reveals friction: expose the *unrealized* fileset via a `flake.lib.pgPrSource` attr (avoids a build, but the consumer must `import` it under the producer flake's `self` — messier across the input boundary); or add a `legacyPackages` attr. **Recommendation: the realized `packages.pg-pr-src` derivation** — it crosses the flake-input boundary as a plain store path (no `self`-threading), and gomod2nix's symlink only needs a directory tree, not a buildable Go module. Confirm at Task 1 Step 4 that the store path contains `go.mod`, `go.sum`, `gomod2nix.toml`, `cmd/`, `internal/`, `pkg/`.
2. **Synthetic-parent assembly mechanism (Task 5).** The deep replace `../../../.../pg-pr` cannot be reproduced by a single `lib.fileset.toSource` because the consumer module lives at `modules/pg-pr-zr` and pg-pr lives in a *different repo* — there is no common on-disk parent inside one store tree. This plan **reshapes the replace to `./pg-pr-src`** and builds the parent with `pkgs.runCommand` (copy the consumer module subtree + symlink/copy the imported `pg-pr-src` as a sibling), then sets `mkGoApp { src = <that parent>; modRoot = "pg-pr-zr"; }`. Open sub-question for the executor to verify empirically at Task 6: whether gomod2nix's `pwd + "/${value.path}"` symlink tolerates the imported source being a **symlink** vs a **real copy** inside the sandbox — if the symlink dangles (store-path indirection), switch the `runCommand` from `ln -s` to `cp -R --no-preserve=mode`. Pattern B in-repo uses a real fileset copy, so **default to `cp -R`**; only try a symlink if copy is too slow.
3. **Flake-input wiring agent-support↔ziprecruiter.** Already present — the consumer's `flake.nix:56` declares `phillipgreenii-agent-support` and `machines/default.nix:165` applies its overlay. **The only new wiring is reading `inputs.phillipgreenii-agent-support.packages.${system}.pg-pr-src`** in the consumer `perSystem` (Tasks 5-6). No new input needed. **Caveat the executor must confirm:** after the producer change lands, the consumer's `flake.lock` pin of `phillipgreenii-agent-support` must be updated (`nix flake update phillipgreenii-agent-support`, or a local `--override-input` during dev) or the new `pg-pr-src` output won't be visible. This is the one cross-repo ordering hazard: **producer must land (or be override-input'd) before the consumer build can see `pg-pr-src`.**

---

## PART A — Producer: `phillipgreenii-nix-agent-support` (branch `expose-pg-pr-source`)

### Task 1: Expose `packages/pg-pr` source as a flake output `pg-pr-src`

**Repo:** `phillipgreenii-nix-agent-support`

**Files:**
- Modify: `flake.nix` — the `perSystem` `packages` set (the `inherit (pkgs) … pg-pr …;` block ends at `flake.nix:759`; add the new attr right after the closing `;` of that `inherit`, near line 760).

- [ ] **Step 1: Create the branch**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git checkout -b expose-pg-pr-source
```

- [ ] **Step 2: Add the `pg-pr-src` package output**

In `flake.nix`, inside `perSystem … packages = { … }`, immediately after the existing
`inherit (pkgs) … pg-pr … ;` block (the `;` is at line 759) and before `fix-lint =` (line 760),
insert:

```nix
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
```

Notes for the executor:
- `lib` is already in scope in this `perSystem` (it is used elsewhere in `packages`, e.g. `fix-lint` at line 761 uses `lib.getExe`). If a `lib` arg is missing from the `perSystem` function head, add it.
- `lib.sources.cleanSource` drops `.git`/result symlinks but **keeps** `go.mod`/`go.sum`/`gomod2nix.toml` and all `.go` files. Do **not** use a `*.go`-only filter — gomod2nix needs the lockfiles too.
- The `cp -R …/. $out/` (trailing `/.`) copies the *contents* so `$out/go.mod` exists (not `$out/pg-pr/go.mod`).

- [ ] **Step 3: Build the new output**

Run:
```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
nix build .#pg-pr-src --print-out-paths
```
Expected: succeeds; prints a `/nix/store/...-pg-pr-src` path.

- [ ] **Step 4: Verify the store path has the files the consumer needs**

Run:
```bash
out=$(nix build .#pg-pr-src --no-link --print-out-paths)
ls "$out"/go.mod "$out"/go.sum "$out"/gomod2nix.toml
ls "$out"/cmd "$out"/internal "$out"/pkg
```
Expected: all five top-level files/dirs listed with no "No such file" error. (Resolves BLOCKING UNKNOWN #1.)

- [ ] **Step 5: `nix flake check` (producer)**

Run:
```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
nix flake check
```
Expected: PASS (the change is purely additive; no existing check references `pg-pr-src`).

- [ ] **Step 6: Pre-commit + commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
nix fmt
prek run --all-files || pre-commit run --all-files
git add flake.nix
git commit -m "feat(pg-pr): expose pg-pr source as flake output pg-pr-src for cross-repo gomod2nix consumers (pg2-wtjz)"
```
Expected: hooks PASS; commit created on `expose-pg-pr-source`.

---

## PART B — Consumer: `phillipg-nix-ziprecruiter` (branch `pg-pr-zr-nix-build`)

> **Ordering gate (BLOCKING UNKNOWN #3):** Part B builds against the producer's new `pg-pr-src` output. Before Task 6's first build, either:
> - merge/push `expose-pg-pr-source` and run `nix flake update phillipgreenii-agent-support` in the consumer, **or**
> - dev locally with `--override-input phillipgreenii-agent-support git+file:///Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support` appended to each `nix build`/`nix flake check`.
>
> Tasks 2-5 do not need the producer (they only touch consumer files), so they can proceed in parallel with Part A; only Task 6 onward needs `pg-pr-src` visible.

### Task 2: Create the branch and remove the stray pre-built binary

**Repo:** `phillipg-nix-ziprecruiter`

**Files:**
- Delete (working tree only; already gitignored): `modules/pg-pr-zr/pg-pr-cicd-captains-log`

- [ ] **Step 1: Branch**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
git checkout -b pg-pr-zr-nix-build
```

- [ ] **Step 2: Delete the stray out-of-band binary**

```bash
rm -f /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr/pg-pr-cicd-captains-log
```
(It is listed in `modules/pg-pr-zr/.gitignore` and is NOT git-tracked — `git ls-files` does not show it — so this only cleans the working tree. No commit needed for the deletion itself.)

- [ ] **Step 3: Confirm clean**

Run:
```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
git status --porcelain modules/pg-pr-zr/
```
Expected: empty (the deleted file was untracked, so it does not appear).

---

### Task 3: Reshape the `replace` directive to a shallow sibling path

**Repo:** `phillipg-nix-ziprecruiter`

**Why:** gomod2nix symlinks the replace target at `${pwd}/<replace.path>` (`builder/default.nix:127`). The current `../../../phillipgreenii-nix-agent-support/packages/pg-pr` would require the sandbox to reproduce a 3-levels-up-then-down tree from another repo — impossible inside one store copy. A shallow `./pg-pr-src` lets the synthetic sandbox parent (Task 5) place the imported source as a direct child of the module dir.

**Files:**
- Modify: `modules/pg-pr-zr/go.mod:11`

- [ ] **Step 1: Edit the replace path**

In `modules/pg-pr-zr/go.mod`, change the `replace` line and its comment. Current (lines 7-11):

```
// pg-pr is published from a sibling workspace flake; until a tagged release is
// available we resolve it via a relative-path replace. The agent-support repo
// is expected to live at ../../../phillipgreenii-nix-agent-support relative to
// this go.mod (i.e. workspace-root/phillipgreenii-nix-agent-support).
replace github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr => ../../../phillipgreenii-nix-agent-support/packages/pg-pr
```

Replace with:

```
// pg-pr is published from a sibling workspace flake. The replace target is a
// shallow `./pg-pr-src` sibling that both the local dev checkout and the nix
// build sandbox provide:
//   - dev:   a `pg-pr-src` symlink (Task 4) -> ../../../phillipgreenii-nix-agent-support/packages/pg-pr
//   - nix:   modules/pg-pr-zr/build.nix copies the agent-support `pg-pr-src`
//            flake output into the sandbox at this path (bead pg2-wtjz, ADR 0008 Pattern B).
replace github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr => ./pg-pr-src
```

- [ ] **Step 2: Verify go.mod still parses**

Run (uses the dev symlink created next — so this step is sequenced AFTER Task 4 in practice; if running standalone now, expect a "missing pg-pr-src" error which Task 4 fixes):

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr
go mod verify 2>&1 | head
```
Expected after Task 4: no parse error about the replace directive.

---

### Task 4: Provide a dev-time `pg-pr-src` symlink + generate `go.sum` and `gomod2nix.toml`

**Repo:** `phillipg-nix-ziprecruiter`

**Why:** (a) Local `go build`/`go test`/golangci-lint and `gomod2nix generate` all need the replace target to resolve on disk; a git-ignored `pg-pr-src` symlink to the sibling checkout provides it without committing the source. (b) gomod2nix copies `go.sum` from `pwd` (`builder/default.nix:389`) and reads `gomod2nix.toml` — the module currently has **neither**; both must be generated and **committed** (an untracked toml is invisible to flake builds — ADR 0008 rough edge).

**Files:**
- Create (symlink, gitignored): `modules/pg-pr-zr/pg-pr-src` → `../../../phillipgreenii-nix-agent-support/packages/pg-pr`
- Modify: `modules/pg-pr-zr/.gitignore` (add `pg-pr-src` symlink; the source must NOT be committed into the consumer repo)
- Create (committed): `modules/pg-pr-zr/go.sum`
- Create (committed): `modules/pg-pr-zr/gomod2nix.toml`

- [ ] **Step 1: Create the dev symlink to the sibling pg-pr checkout**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr
ln -sfn ../../../phillipgreenii-nix-agent-support/packages/pg-pr pg-pr-src
ls -l pg-pr-src   # should show the symlink resolving to the sibling pg-pr dir
test -f pg-pr-src/go.mod && echo "OK: pg-pr-src resolves"
```
Expected: `OK: pg-pr-src resolves`.

- [ ] **Step 2: Ignore the dev symlink (do NOT vendor agent-support source into this repo)**

In `modules/pg-pr-zr/.gitignore`, append below the existing two entries:

```
# Dev-time symlink to the sibling agent-support pg-pr checkout, used to resolve
# the `replace => ./pg-pr-src` directive for local go build / go test /
# gomod2nix generate. The nix build provides this path from the agent-support
# `pg-pr-src` flake output instead (build.nix); never commit the source here.
/pg-pr-src
```

- [ ] **Step 3: Generate `go.sum`**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr
go mod tidy
```
Expected: creates/updates `go.sum` with pg-pr's transitive third-party deps (prometheus, otel, cobra, modernc sqlite, …). `go.mod`'s single `require` of pg-pr stays; the replace makes its deps transitive.

- [ ] **Step 4: Generate `gomod2nix.toml`**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr
nix run github:nix-community/gomod2nix -- generate
```
Expected: writes `modules/pg-pr-zr/gomod2nix.toml` (schema 3) listing the transitive third-party deps. `pg-pr` itself is **absent** (it is the local replace, resolved by symlink — same as ccpool's toml omits `claude-transcript`). Confirm:
```bash
grep -c "phillipgreenii-nix-agent-support/packages/pg-pr" gomod2nix.toml
```
Expected: `0`.

- [ ] **Step 5: Local sanity — the binaries build & their tests pass out-of-nix**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr
go build ./cmd/... && go test ./...
```
Expected: build succeeds; tests pass (this confirms `go.sum`/replace are correct before involving nix). Remove any binaries `go build` dropped in the cwd afterward:
```bash
rm -f pg-pr-cicd-captains-log pg-pr-issues-jira-zr
```

- [ ] **Step 6: Stage the committed lockfiles (NOT the symlink or binaries)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
git add modules/pg-pr-zr/go.mod modules/pg-pr-zr/go.sum modules/pg-pr-zr/gomod2nix.toml modules/pg-pr-zr/.gitignore
git status --porcelain modules/pg-pr-zr/   # verify pg-pr-src symlink is NOT staged
```
Expected: `go.mod`, `go.sum`, `gomod2nix.toml`, `.gitignore` staged; `pg-pr-src` absent from the list (gitignored).

- [ ] **Step 7: Commit the lockfiles + replace reshape**

```bash
git commit -m "build(pg-pr-zr): reshape replace to ./pg-pr-src, commit go.sum + gomod2nix.toml (pg2-wtjz)"
```

---

### Task 5: Add `modules/pg-pr-zr/build.nix` — the cross-repo Pattern-B build

**Repo:** `phillipg-nix-ziprecruiter`

**What it does:** assembles a synthetic sandbox parent containing the consumer module (`pg-pr-zr/…`) and the imported `pg-pr-src` as a **sibling at `pg-pr-zr/pg-pr-src`** (so the `replace => ./pg-pr-src` resolves relative to `pwd = <parent>/pg-pr-zr`), then calls `mkGoApp` with `modRoot = "pg-pr-zr"`. Returns one package exposing **both** binaries.

**Files:**
- Create: `modules/pg-pr-zr/build.nix`

- [ ] **Step 1: Write `build.nix`**

Create `modules/pg-pr-zr/build.nix`:

```nix
# Cross-repo gomod2nix Pattern-B build for the pg-pr-zr extension binaries
# (bead pg2-wtjz; phillipg-nix-repo-base ADR 0008 §Decision.4).
#
# modules/pg-pr-zr/go.mod has `replace …/packages/pg-pr => ./pg-pr-src`. A
# hermetic nix build cannot see the sibling agent-support repo, so we copy that
# repo's `pg-pr-src` flake output (its pg-pr source tree) INTO the build sandbox
# as `pg-pr-zr/pg-pr-src`, making the shallow replace resolve. gomod2nix then
# symlinks it natively (ln -s ${pwd}/pg-pr-src vendor/<importpath>); no
# vendorHash, no overlay — pg-pr edits are read live (ADR 0008 Pattern B).
{
  lib,
  pkgs,
  mkGoApp,
  # The agent-support pg-pr source, passed by the flake as
  # inputs.phillipgreenii-agent-support.packages.<system>.pg-pr-src.
  pgPrSrc,
}:
let
  moduleSrc = lib.fileset.toSource {
    root = ./.;
    # Exclude the dev symlink + any stray built binaries from the module copy;
    # we splice pgPrSrc in below from the trusted flake output.
    fileset = lib.fileset.difference ./. (
      lib.fileset.unions [
        (lib.fileset.maybeMissing ./pg-pr-src)
        (lib.fileset.maybeMissing ./pg-pr-cicd-captains-log)
        (lib.fileset.maybeMissing ./pg-pr-issues-jira-zr)
      ]
    );
  };

  # Synthetic parent: <root>/pg-pr-zr is the module; <root>/pg-pr-zr/pg-pr-src
  # is the imported source. mkGoApp sets pwd = <root>/pg-pr-zr, so the
  # `./pg-pr-src` replace resolves to the copied source.
  sandboxRoot = pkgs.runCommand "pg-pr-zr-sandbox" { } ''
    mkdir -p $out/pg-pr-zr
    cp -R ${moduleSrc}/. $out/pg-pr-zr/
    chmod -R u+w $out/pg-pr-zr
    # Real copy (NOT a symlink): gomod2nix's `ln -s ${"\${pwd}"}/pg-pr-src` must
    # land on a real directory inside the sandbox, and pg-pr is read live from
    # this copy. (If a future change makes this too slow, BLOCKING UNKNOWN #2
    # permits switching to a symlink and re-verifying.)
    cp -R ${pgPrSrc} $out/pg-pr-zr/pg-pr-src
    chmod -R u+w $out/pg-pr-zr/pg-pr-src
  '';
in
mkGoApp {
  pname = "pg-pr-zr";
  src = sandboxRoot;
  modRoot = "pg-pr-zr";
  subPackages = [
    "cmd/pg-pr-cicd-captains-log"
    "cmd/pg-pr-issues-jira-zr"
  ];
  gomod2nixToml = ./gomod2nix.toml;
  meta = with lib; {
    description = "ZR pg-pr extension binaries (cicd captains-log + jira issues)";
    platforms = platforms.all;
  };
}
```

Notes for the executor:
- `gomod2nixToml = ./gomod2nix.toml;` — `mkGoApp` only asserts it is non-null and derives the real path from `pwd` (`go-builders.nix:199,205`), so this points at the committed toml.
- `modRoot = "pg-pr-zr"` makes `mkGoApp` set `pwd = sandboxRoot + "/pg-pr-zr"` (`go-builders.nix:186`).
- `subPackages` builds both `cmd/` mains into one `$out/bin`.
- `lib.fileset.maybeMissing` guards the excludes so the build works whether or not the dev symlink/stray binaries exist on disk.

---

### Task 6: Wire `build.nix` into the consumer flake (packages + check)

**Repo:** `phillipg-nix-ziprecruiter`

**Files:**
- Modify: `flake.nix` `perSystem` — `packages` (`~:435`) and `checks` (`~:368`)

- [ ] **Step 1: Ensure the producer source is visible (BLOCKING UNKNOWN #3)**

Either update the lock (after Part A is pushed):
```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
nix flake update phillipgreenii-agent-support
```
…or, for local-only dev against the uncommitted producer branch, append to every `nix build`/`nix flake check` below:
```
--override-input phillipgreenii-agent-support git+file:///Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
```
Confirm the output exists:
```bash
nix eval --raw .#packages.aarch64-darwin.pg-pr-cicd-captains-log.name 2>/dev/null \
  || nix eval .#inputs 2>/dev/null >/dev/null; \
nix eval --raw "$(printf '%s' 'inputs.phillipgreenii-agent-support')" 2>/dev/null || true
# Direct check that pg-pr-src is reachable through the input:
nix build '.#packages.aarch64-darwin.pg-pr-zr' --no-link 2>&1 | head   # will fail until Step 2 adds it; this is just a reachability smoke later
```
(Definitive reachability is Step 3's build.)

- [ ] **Step 2: Add the build to `perSystem.packages`**

In `flake.nix`, inside `perSystem … packages = { … }` (the set that starts at line 435 with `fix-lint`), add — using the perSystem `system` and the agent-support input. Place after the `test-colima = tests.colima;` line (the last package, line 458) and before the closing `};`:

```nix
            # pg-pr-zr extension binaries built INSIDE nix via cross-repo
            # gomod2nix Pattern B (bead pg2-wtjz). The agent-support flake
            # exposes pg-pr source as `pg-pr-src`; build.nix copies it into the
            # sandbox so the `replace => ./pg-pr-src` resolves.
            pg-pr-zr = pkgs.callPackage ./modules/pg-pr-zr/build.nix {
              # mkGoApp comes from repo-base's go-builders; agent-support's
              # overlay does not re-export it, so build it from the same lib the
              # overlay uses. nix-base.lib.mkGoBuilders needs a gomod2nix-overlaid
              # pkgs (buildGoApplication) — the perSystem `pkgs` already has it
              # (machines/default.nix applies gomod2nix.overlays.default; the
              # perSystem pkgs is the flake-parts default which the imported
              # overlay flakeModules feed). If buildGoApplication is missing on
              # `pkgs` here, see Step 2a.
              mkGoApp =
                (inputs.phillipgreenii-nix-base.lib.mkGoBuilders {
                  inherit pkgs;
                  inherit (pkgs) lib;
                  self = inputs.phillipgreenii-agent-support;
                }).mkGoApp;
              pgPrSrc = inputs.phillipgreenii-agent-support.packages.${system}.pg-pr-src;
            };

            # Convenience aliases so `nix build .#pg-pr-cicd-captains-log` works
            # (both binaries live in the single pg-pr-zr derivation's bin/).
            pg-pr-cicd-captains-log = config.packages.pg-pr-zr;
            pg-pr-issues-jira-zr = config.packages.pg-pr-zr;
```

> **Step 2a (only if `nix build` errors that `buildGoApplication` is missing):** the perSystem `pkgs` may be the plain flake-parts pkgs without the gomod2nix overlay (the overlay is applied in `machines/default.nix`, not necessarily on the perSystem pkgs). If so, build a local overlaid pkgs in the `perSystem` `let` (mirror the existing `pkgsWithSupportApps` at `flake.nix:355`), adding `inputs.gomod2nix.overlays.default` to its `overlays` list, and pass that as `pkgs` to both `mkGoBuilders` and `callPackage`. Confirm with: `nix eval .#packages.aarch64-darwin.pg-pr-zr.drvPath`.

> **Step 2b (config reference):** `config.packages.pg-pr-zr` requires `config` in the `perSystem` head. If `config` is not already an arg (the head is `{ pkgs, system, checksHelpers, ... }` at `flake.nix:340`), add `config` to it. Alternatively, define a `let pgPrZr = pkgs.callPackage …;` once in the `perSystem` `let` block and reference `pgPrZr` from all three package attrs + the check (avoids `config`). **Recommended: the `let pgPrZr = …` form** — simpler and avoids the `config` self-reference.

- [ ] **Step 3: Build both binaries via nix (the core acceptance)**

Run (add `--override-input …` per Step 1 if not locked):
```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
nix build .#pg-pr-zr --print-out-paths
ls "$(nix build .#pg-pr-zr --no-link --print-out-paths)"/bin
```
Expected: build succeeds; `bin/` lists **both** `pg-pr-cicd-captains-log` and `pg-pr-issues-jira-zr`. (`buildGoApplication` ran the binaries' `go test` as `doCheck` during this build — a build pass means the unit tests passed too. Resolves BLOCKING UNKNOWN #2: if the replace symlink dangled, the build would fail here with an unresolved-import error; if so switch `build.nix`'s `cp -R` per the note.)

- [ ] **Step 4: Add a flake check that builds the binaries**

In `flake.nix` `perSystem … checks = { … }` (starts at `flake.nix:368`), add (using `pgPrZr` from the `let`, per Step 2b recommendation):

```nix
            # Builds the pg-pr-zr extension binaries via cross-repo gomod2nix
            # Pattern B and asserts both land on disk (bead pg2-wtjz). The
            # buildGoApplication doCheck already ran the binaries' go tests.
            pg-pr-zr-build = pkgs.runCommand "check-pg-pr-zr-build" { } ''
              test -x ${pgPrZr}/bin/pg-pr-cicd-captains-log || { echo "missing cicd binary"; exit 1; }
              test -x ${pgPrZr}/bin/pg-pr-issues-jira-zr    || { echo "missing jira binary"; exit 1; }
              touch $out
            '';
```

- [ ] **Step 5: `nix flake check` (consumer)**

Run (with `--override-input …` if not locked):
```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
nix flake check
```
Expected: PASS, including the new `pg-pr-zr-build` check. (This is one of the two acceptance gates: `nix flake check` passes in the consumer repo.)

- [ ] **Step 6: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
nix fmt
git add modules/pg-pr-zr/build.nix flake.nix
# include flake.lock ONLY if Step 1 updated it:
git add flake.lock 2>/dev/null || true
git commit -m "feat(pg-pr-zr): build extension binaries in nix via cross-repo gomod2nix Pattern B (pg2-wtjz)"
```

---

### Task 7: Point the jira wrapper at the nix-built binary; drop the user-build requirement

**Repo:** `phillipg-nix-ziprecruiter`

**Scope (per bead AC):** the secret-injection wrapper **stays**, but `realBinary` must default to the nix-built binary and the "build it yourself" failure branch + out-of-band-build docs go away.

**Files:**
- Modify: `modules/pg-pr-zr/default.nix` — header comment (`:22-35`), `jiraWrapper` text (`:94-99`), the `realBinary` option (`:213-226`), and import the build so the module can reference the binary.

- [ ] **Step 1: Make the module aware of the nix-built binary**

The module is a home/darwin module evaluated with `{ config, lib, pkgs, ... }`. `pkgs` here is the
machine's overlaid pkgs (from `machines/default.nix`, which already applies `gomod2nix` +
agent-support overlays). Add a `let` binding that calls `build.nix` with the same wiring used in
`flake.nix` Task 6. In `modules/pg-pr-zr/default.nix`, inside the existing `let … in` (after
`packSrc = ./pack-src;` at line 47), add:

```nix
  # The pg-pr-zr extension binaries, built in nix (bead pg2-wtjz). Replaces the
  # Phase-4 out-of-band `go build`. Needs the agent-support pg-pr source; the
  # module receives it via specialArgs/_module.args `pgPrSrc` (see Step 1a).
  pgPrZrBins = pkgs.callPackage ./build.nix {
    mkGoApp = pgPrZrMkGoApp;
    pgPrSrc = pgPrZrPgPrSrc;
  };
```

> **Step 1a — passing `pgPrSrc`/`mkGoApp` into a home/darwin module.** A home-manager/darwin
> module cannot read flake `inputs` directly. Provide them via `_module.args` from where the module
> is imported. **Find the import site first** — note the module is currently NOT imported anywhere
> (confirmed: only the pre-commit Go hooks reference `modules/pg-pr-zr`; `grep -rn pgPrZr home
> machines darwin` finds no import). So Task 7 has a prerequisite: **decide and implement where
> `modules/pg-pr-zr/default.nix` gets imported** (the machine config under
> `machines/phillipg-mbp-02/`), and at that import add:
> ```nix
> _module.args.pgPrZrMkGoApp =
>   (inputs.phillipgreenii-nix-base.lib.mkGoBuilders {
>     inherit pkgs; inherit (pkgs) lib; self = inputs.phillipgreenii-agent-support;
>   }).mkGoApp;
> _module.args.pgPrZrPgPrSrc =
>   inputs.phillipgreenii-agent-support.packages.${pkgs.stdenv.hostPlatform.system}.pg-pr-src;
> ```
> and add `pgPrZrMkGoApp` + `pgPrZrPgPrSrc` to the module head args.
> **If the module is intentionally never imported into a machine yet** (it may be dormant pending
> the gascity pack wiring), then Task 7 is reduced to: set the `realBinary` *option default* to a
> path string and update the docs/wrapper text only (Steps 2-4), and the live binary reference
> (Step 1/1a) is deferred to whenever the module is first imported. **Confirm with the user which
> applies** — this is the one place the bead's "module installs the nix-built binary" AC depends on
> an import that does not yet exist. (See BLOCKING UNKNOWN note at end.)

- [ ] **Step 2: Default `realBinary` to the nix-built path (option)**

In the `realBinary` option (`:213-226`), change `default` and `description`:

```nix
      realBinary = mkOption {
        type = types.str;
        default = "${pgPrZrBins}/bin/pg-pr-issues-jira-zr";
        defaultText = lib.literalExpression "\"\${pgPrZrBins}/bin/pg-pr-issues-jira-zr\"";
        example = "/Users/phillipg/go/bin/pg-pr-issues-jira-zr";
        description = ''
          Absolute path to the real `pg-pr-issues-jira-zr` binary the wrapper
          exec's into. Defaults to the nix-built binary (bead pg2-wtjz: built
          in-tree via cross-repo gomod2nix Pattern B — no manual `go build`).
          Override only to point at a hand-built binary.
        '';
      };
```

> If Step 1a determined the module is dormant (not imported), keep `default` as a path STRING you
> can still build standalone, e.g. set it from the flake instead, or leave the option but document
> that the flake package `.#pg-pr-zr` is the source of the binary. Prefer the live `${pgPrZrBins}`
> default once an import exists.

- [ ] **Step 3: Drop the "build it yourself" failure branch in the wrapper**

In `jiraWrapper.text` (`:94-99`), replace the user-build error block:

```bash
      real_bin=${lib.escapeShellArg cfg.jira.realBinary}
      if [ ! -x "$real_bin" ]; then
        echo "pg-pr-issues-jira-zr (wrapper): real binary not found at $real_bin" >&2
        echo "Build it with:  cd modules/pg-pr-zr && go build -o \"$real_bin\" ./cmd/pg-pr-issues-jira-zr" >&2
        exit 127
      fi
```

with:

```bash
      real_bin=${lib.escapeShellArg cfg.jira.realBinary}
      if [ ! -x "$real_bin" ]; then
        echo "pg-pr-issues-jira-zr (wrapper): real binary not found at $real_bin" >&2
        echo "Expected the nix-built binary (.#pg-pr-zr); rebuild your config." >&2
        exit 127
      fi
```

- [ ] **Step 4: Update the module header + the jiraWrapper comment**

In the header (`:22-35`), delete the "Build the Go extension binaries … inside nix … build the
binaries with `go build ./cmd/...` … place them on PATH manually" deferral bullet and replace with
a line noting the binaries are now built in nix (bead pg2-wtjz). In the `jiraWrapper` block comment
(`:50-66`), remove "currently built out-of-band (`go build ./cmd/...`) … Until that's resolved" and
state the wrapper now exec's the nix-built binary, keeping the runtime secret injection.

- [ ] **Step 5: Build/eval the module-bearing config**

Run (whichever applies; if the module is imported into the machine):
```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
nix flake check
darwin-rebuild check --flake .   # per consumer CLAUDE.md "before complete"
```
Expected: PASS. If the module is dormant (not imported), `nix flake check` + the `.#pg-pr-zr` build
(Task 6) is the gate, and `darwin-rebuild check` is unaffected.

- [ ] **Step 6: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
nix fmt
git add modules/pg-pr-zr/default.nix
# plus the machine import file if Step 1a added one:
git add machines/phillipg-mbp-02/default.nix 2>/dev/null || true
git commit -m "feat(pg-pr-zr): wrapper exec's nix-built jira binary; drop out-of-band build requirement (pg2-wtjz)"
```

---

### Task 8: Full cross-repo verification + bead close

**Repos:** both

- [ ] **Step 1: Producer full check**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
nix build .#pg-pr-src --no-link && nix flake check
```
Expected: both PASS.

- [ ] **Step 2: Consumer full check (binaries build + tests + flake check)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
out=$(nix build .#pg-pr-zr --no-link --print-out-paths)
test -x "$out/bin/pg-pr-cicd-captains-log" && test -x "$out/bin/pg-pr-issues-jira-zr" && echo "BOTH BINARIES OK"
nix flake check
```
Expected: `BOTH BINARIES OK`; `nix flake check` PASS. (Both acceptance gates green.)

- [ ] **Step 3: Confirm the no-vendorHash-bump property**

Edit any pg-pr source file in the producer (e.g. add a comment to `packages/pg-pr/internal/output/`'s
a Go file), rebuild the consumer binary, and confirm it recompiles **without** any hash edit:
```bash
# producer (dirty tree is fine):
echo "// pg2-wtjz live-edit probe" >> /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output/$(ls /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output | grep '\.go$' | head -1)
# consumer rebuild (override-input picks up the dirty producer):
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
nix build .#pg-pr-zr --no-link \
  --override-input phillipgreenii-agent-support git+file:///Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
```
Expected: rebuild succeeds with NO `gomod2nix.toml`/hash change required (gomod2nix has no vendorHash; pg-pr is read live). Then revert the probe edit:
```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support && git checkout -- packages/pg-pr/internal/output
```

- [ ] **Step 4: Close the bead**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
bd update pg2-wtjz --claim
bd comment pg2-wtjz "Done: agent-support exposes packages/pg-pr source as flake output .#pg-pr-src; ziprecruiter modules/pg-pr-zr builds both extension binaries (pg-pr-cicd-captains-log, pg-pr-issues-jira-zr) in nix via cross-repo gomod2nix Pattern B (build.nix: reshaped replace => ./pg-pr-src, synthetic sandbox parent, modRoot=pg-pr-zr). Committed go.sum + gomod2nix.toml. jira wrapper now exec's the nix-built binary (realBinary defaults to the store path); out-of-band go build dropped. Verified: both nix builds, both repos' nix flake check, and a pg-pr live-edit recompile with no vendorHash bump."
bd close pg2-wtjz
```

---

## Self-Review

**1. Spec coverage (bead AC):**
- "`pg-pr-cicd-captains-log` and `pg-pr-issues-jira-zr` build via nix (no out-of-band `go build`)" → Tasks 5-6 (`build.nix` + flake `packages`), verified Task 6 Step 3 / Task 8 Step 2. Out-of-band build removed: stray binary deleted (Task 2), wrapper error branch + docs dropped (Task 7).
- "jira module installs the nix-built binary (wrapper can remain, `realBinary` no longer requires a manual build)" → Task 7 (wrapper stays; `realBinary` defaults to `${pgPrZrBins}/bin/...`).
- "pg-pr source edit needs no `vendorHash` bump" → confirmed structurally (gomod2nix has no vendorHash; ADR 0008) and live-verified Task 8 Step 3.
- "agent-support exposes pg-pr source first" → Task 1 (`packages.pg-pr-src`).
- Acceptance "`nix flake check` passes in BOTH repos" → Task 1 Step 5 (producer), Task 6 Step 5 + Task 8 (consumer).

**2. Placeholder scan:** No TBD/TODO. Every nix snippet is concrete (real attr names, real file:line targets, real `replace`/`modRoot`/`subPackages`). The two genuine unknowns (#2 copy-vs-symlink, #3 input visibility) are called out with the exact fallback command rather than left vague. The one true open decision — *where the dormant module gets imported* — is flagged explicitly in Task 7 Step 1a as requiring a user decision, not hand-waved.

**3. Type/name consistency:** `pg-pr-src` (producer flake output) is consumed verbatim as `inputs.phillipgreenii-agent-support.packages.${system}.pg-pr-src` (consumer) and as the `pgPrSrc` arg of `build.nix`. `modRoot = "pg-pr-zr"` matches the synthetic-parent child dir name `$out/pg-pr-zr` and the `pwd = sandboxRoot + "/pg-pr-zr"` mkGoApp computes. The `replace => ./pg-pr-src` path matches the sandbox child `pg-pr-zr/pg-pr-src` and the dev symlink name. `gomod2nixToml = ./gomod2nix.toml;` matches the committed file from Task 4. `mkGoApp` is sourced identically (`mkGoBuilders … .mkGoApp`) in Task 6 and Task 7.

**4. Cross-repo gomod2nix mechanics check:** Verified against gomod2nix `builder/default.nix` — local replace = `ln -s ${pwd + "/" + value.path} vendor/<name>` (line 127), and `go.sum` is copied from `pwd` (line 389). Both requirements are satisfied: the `./pg-pr-src` replace target is a real dir inside the sandbox at `pwd`, and Task 4 generates+commits `go.sum`. The ADR 0008 "cross-repo replaces don't work (#101)" rough edge is precisely what Task 1+5 work around (bring the source into the sandbox), as the ADR itself prescribes for `pg2-wtjz`.
