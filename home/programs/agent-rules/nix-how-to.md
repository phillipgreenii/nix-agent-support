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

## Scoping a `prek`/`pre-commit` run to a commit RANGE, not just a file list

`--files <list>` (above) is right when you know exactly which files one change touched. It is the
wrong tool for checking everything a whole BRANCH touched across several commits — hand-listing
files across multiple commits is easy to get wrong (miss one, or include a file a later commit on
the branch reverted). For a commit-range check, use `prek`'s diff-expression form instead:

```bash
prek run --from-ref <base> --to-ref <tip>     # every file changed between the two refs
prek run --last-commit                        # shorthand for --from-ref HEAD~1 --to-ref HEAD
```

This still only touches files the range actually changed (same cost profile as `--files`, not
`--all-files`) — it just computes the file list from git instead of you enumerating it. This is
what `ff-merge-to-main`'s FF-1b step now runs automatically at land time, for every repo with a
`.pre-commit-config.yaml`: `prek run --from-ref <primary> --to-ref <branch>`, verifying the
branch's cumulative diff in one pass rather than trusting that each commit's own per-commit run
summed to the same thing. Reach for the same form yourself whenever you need to validate more
than one commit's combined changes ad hoc (e.g. after an interactive rebase, or before manually
handing a multi-commit branch off).

## `nix flake check` is a land-time gate, not a per-change gate

Reserve a full `nix flake check` for once before the branch lands (or the repo's own
CI/land-time mechanism), not after every individual edit or bead. Many `flake.nix` repos wire
`pre-commit-hooks.nix`'s hook set into a `checks.pre-commit` derivation, so `nix flake check`
re-runs the exact same hooks the commit's own `prek`/`pre-commit` run (see the `--all-files`
prohibition above) already ran on the staged files — paying for the whole hook set twice per
change, on top of whatever CI already re-checks on push. Running it once per branch, right
before landing, still satisfies the core rule's `nix flake check` MUST-pass obligation without
multiplying that cost by however many changes land on the branch.

**Every repo gets a prek-level land-time check; only two get the full flake check.**
`ff-merge-to-main`'s FF-1b step runs the commit-range `prek` check above for every repo it lands
that has a `.pre-commit-config.yaml` — that part is universal, not repo-scoped. FF-2a's full
`nix flake check`, by contrast, only runs for `nix-agent-support`/`phillipg-nix-ziprecruiter`
(the two repos with no external CI) — it skips every other repo. So do not assume a repo without
that FF-2a scoping has NO land-time gate at all: it still gets FF-1b's prek check; it just does
not get the heavier `checks.*` derivations FF-2a covers unless you run `nix flake check` yourself
before landing.
