---
name: nix-how-to
description: Nix build/check how-to detail — build-only validation forms, the `darwin-rebuild check` caveat, and the pre-commit `--all-files` prohibition — for changes touching `.nix`/`flake.nix` files.
paths: ["**/*.nix", "**/flake.nix"]
---

# Nix How-To

Moved out of the always-on core rules (tc-ql0o Stage D, 2026-08-26): this detail only matters
while actively working on `.nix`/`flake.nix` files, so it rides in a path-scoped rule instead of
every session unconditionally. The two completion-gate OBLIGATIONS themselves (pre-commit hooks
MUST pass on changed files; `nix flake check` MUST pass when `flake.nix` exists) stay in the core
`pgii-agent-rules.md` — they trigger on a repo PROPERTY, not on reading a `.nix` file (a Go-only
edit in a flake repo never reads one — the `pg2-3nb2t` class), so a file-glob trigger cannot carry
them. This file is the HOW, not the WHETHER.

## Build-only validation

For machine-config validation use the build-only `nix build .#darwinConfigurations.<host>.system`
(or `zn-self-build`) — `darwin-rebuild check` MUST NOT be used: on current nix-darwin it bails
immediately with "system activation must now be run as root" and does NO build/eval (observed
2026, nix-darwin 26.05).

## The `--all-files` prohibition

To validate a `.pre-commit-config.yaml`-governed change before committing, run
`prek run --files <the changed files>` (scoped, fast) — the **commit's own hook run is the real
gate**, since a `git commit` fires `prek`/`pre-commit` on the staged files (so `git add -A` first,
or a generated change escapes the run). Do **NOT** use `prek`/`pre-commit run --all-files` as a
per-change completion gate: it re-runs every hook over the whole repo — duplicating the commit
run, forcing the slow always-on hooks (bats, nix, …) even for an unrelated diff, and
**false-blocking** a clean change on a pre-existing violation in a file it never touched. Reserve
`--all-files` for a deliberate full-repo sweep, not per-change validation.

## `nix flake check` is a land-time gate, not a per-change gate

Reserve a full `nix flake check` for once before the branch lands (or the repo's own
CI/land-time mechanism), not after every individual edit or bead. Many `flake.nix` repos wire
`pre-commit-hooks.nix`'s hook set into a `checks.pre-commit` derivation, so `nix flake check`
re-runs the exact same hooks the commit's own `prek`/`pre-commit` run (see the `--all-files`
prohibition above) already ran on the staged files — paying for the whole hook set twice per
change, on top of whatever CI already re-checks on push. Running it once per branch, right
before landing, still satisfies the core rule's `nix flake check` MUST-pass obligation without
multiplying that cost by however many changes land on the branch.
