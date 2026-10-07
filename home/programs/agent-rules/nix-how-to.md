---
name: nix-how-to
description: Nix build/check how-to detail — build-only validation forms, the `darwin-rebuild check` caveat, and the pre-commit `--all-files` prohibition — for changes touching `.nix`/`flake.nix` files.
paths: ["**/*.nix", "**/flake.nix"]
---

# Nix How-To

Moved out of the always-on core rules (tc-ql0o Stage D, 2026-08-26): this detail only matters
while actively working on `.nix`/`flake.nix` files, so it rides in a path-scoped rule instead of
every session unconditionally. The two completion-gate rules themselves (pre-commit hooks MUST
pass on changed files; what `flake.nix` does and does NOT require) stay in the core
`pgii-agent-rules.md` — they trigger on a repo PROPERTY, not on reading a `.nix` file (a Go-only
edit in a flake repo never reads one — the `pg2-3nb2t` class), so a file-glob trigger cannot carry
them. This file is the HOW, not the WHETHER.

## Build-only validation

For machine-config validation use the build-only `nix build .#darwinConfigurations.<host>.system`
(or `zn-self-build`) — `darwin-rebuild check` MUST NOT be used: on current nix-darwin it bails
immediately with "system activation must now be run as root" and does NO build/eval (observed
2026, nix-darwin 26.05).

## The `--all-files` prohibition

To validate a hook-governed change before committing, run
`pg-hooks run pre-commit <the changed files>` (scoped, fast; `git add` the files first) — the
**commit's own hook run is the real gate**, since a `git commit` fires the hooks on the staged
files (so `git add -A` first, or a generated change escapes the run). `pg-hooks` runs the
clone's per-clone hook bundle; only when
`pg-hooks` is absent (exit `127`) fall back to `prek run --files <the changed files>`. Probe
whether a repo has hooks with `pg-hooks status`, never `test -f .pre-commit-config.yaml` — a bundle
repo has no such file in its working tree and its hooks are still live. For autofix before
committing in a bundle repo, `git add` the files and then run `pg-hooks fix` (also `pre-commit-fix`;
it touches only staged files and exits `11` listing any file with both staged and unstaged
changes). Do **NOT** use `pg-hooks run pre-commit --all-files` (or `prek run --all-files`, or
`pre-commit run --all-files`) as a per-change completion gate: it re-runs every hook over the whole repo —
duplicating the commit run, forcing the slow always-on hooks (bats, nix, …) even for an unrelated
diff, and **false-blocking** a clean change on a pre-existing violation in a file it never touched.
Reserve `--all-files` for a deliberate full-repo sweep, not per-change validation.

## `pg-hooks status` state reference

Moved here from the always-on core. `pg-hooks status --porcelain || true` prints a `state=` line:
`present`, `stale`, `missing`, `broken`, `unreachable` or `relocated`. `status` exits non-zero for
most non-`present` states by design, so read the state, not the exit code. Never probe with bare
`ls` (it exits non-zero on a missing file, which is itself a failed tool call).

- `present`/`stale` (bundle repo): `git add` first, then `pg-hooks fix` (also `pre-commit-fix`),
  which applies the repo's fixers to staged files only and restages them; it exits `11` after
  listing files skipped because they have both staged and unstaged changes. Before staging, run
  the project formatter in write mode (`nix fmt -- <files>`, `gofumpt -w`, `prettier --write`) to
  avoid a failed-commit + restage round trip.
- `missing`/`broken`/`unreachable`/`relocated`: the commit-time stubs print one `pg-hooks:` notice
  and exit 0, so the commit gate is NOT in force; say so in the report. The notice names the exact
  rebuild command; a rebuild is a nix build, so run it through `bgrun` and check with `bgcheck`.
  Never link, copy or regenerate a hook config to make hooks run.
- Exit `127` or no `state=` line: `pg-hooks` is not installed; report
  `pg-hooks not installed on this machine; ask the operator to run pn workspace apply` and fall back
  to `test -f .pre-commit-config.yaml && echo yes || echo no`.

```text
before committing:  git add <files>; pg-hooks fix; git commit
before landing:     pg-hooks run pre-land
diagnose:           pg-hooks status        (full reference: docs/hooks.md in phillipg-nix-repo-base)
```

## Scoping a `prek`/`pre-commit` run to a commit RANGE, not just a file list

`--files <list>` (above) is right when you know exactly which files one change touched. It is the
wrong tool for checking everything a whole BRANCH touched across several commits — hand-listing
files across multiple commits is easy to get wrong (miss one, or include a file a later commit on
the branch reverted). For a commit-range check, use the branch-diff stage instead:

```bash
pg-hooks run pre-land            # the pre-commit hooks over <primary>...HEAD (the checked-out commit)
```

(`pg-hooks run pre-land [<ref>]` refuses with exit `2` unless `<ref>` is the commit checked out in
the current worktree, because the hooks read working-tree files.) In a repo where `pg-hooks` is
absent, `prek`'s own diff-expression form is the fallback:

```bash
prek run --from-ref <base> --to-ref <tip>     # every file changed between the two refs
prek run --last-commit                        # shorthand for --from-ref HEAD~1 --to-ref HEAD
```

This still only touches files the range actually changed (same cost profile as `--files`, not
`--all-files`) — it just computes the file list from git instead of you enumerating it. This is
what `ff-merge-to-main`'s FF-1b step runs automatically at land time, for every repo it lands
(`integrate-branch-support --prek-branch-diff`, which delegates to `pg-hooks run pre-land`),
verifying the branch's cumulative diff in one pass rather than trusting that each commit's own
per-commit run summed to the same thing. Reach for the same form yourself whenever you need to
validate more than one commit's combined changes ad hoc (e.g. after an interactive rebase, or
before manually handing a multi-commit branch off).

## A full `nix flake check` is NOT a per-change or land-time gate

Operator ruling (Phillip, 2026-10-01): a full `nix flake check` is NOT a land-time gate in any
repo, and not a per-change gate either. This overrides any older text — a cached copy of a skill,
a memory file, a repo doc — that tells an agent to run a full flake check at land or before
committing. The gates are:

1. **The commit's own hook run** — the hooks on the staged files (see the `--all-files`
   prohibition above), including the commit-time `run-unit-tests` hook (`pg-test-runner`, touched
   projects only).
2. **At land, `ff-merge-to-main`'s FF-1b** — `integrate-branch-support --prek-branch-diff`, which
   runs `pg-hooks run pre-land` (the commit-range check above) over the whole branch diff, for
   every repo it lands. `pg-hooks` resolves the clone's hook bundle itself. Two cases skip the check with a notice
   instead of failing the land, and FF-1b records the notice line verbatim in its outcome report:
   - No hook bundle (`pg-hooks` exit `13`): the one `pg-hooks:` notice line,
     for example `pg-hooks: no hook bundle for <repo>; pre-land hooks not run. Fix: (cd <canonical> && nix run .#install-pre-commit-hooks)`.
   - `pg-hooks` not installed (exit `127`): `pg-hooks not installed on this machine; ask the operator to run pn workspace apply`.

   Never link, copy, or regenerate a config, and never build a bundle, to make the check run. A
   hook failure (exit `10`) or any other non-zero exit halts the land as
   `stopped:precommit-branch-diff-failed`.

Beyond those, an agent MAY — and SHOULD when it touched shared infrastructure (a builder, a flake
module, a shared library) — build the targeted checks relevant to its change, in the background
(`nix build .#checks.<system>.<name> -L`, with an explicit long timeout or `run_in_background`).
Why not the whole thing: many `flake.nix` repos wire `pre-commit-hooks.nix`'s hook set into a
`checks.pre-commit` derivation, so a full `nix flake check` re-runs the hooks the commit already
ran, and several concurrent sessions each running one saturated the machine.

Repos with cloud CI (`phillipgreenii-nix-support-apps`, `phillipgreenii-nix-personal`) keep CI as
the whole-repo gate. Accepted interim risk: `phillipgreenii-nix-agent-support` and
`phillipg-nix-ziprecruiter` have no CI, so their only automatic test runners are the commit-time
`run-unit-tests` hook plus FF-1b's `pg-hooks run pre-land` over the branch diff — the whole-repo `checks.*`
derivations (`checks.pre-commit`, every `*-go-tests`, golangci lint, spec-drift) run only when an
agent builds them. If that lets problems through, that is the signal to bring CI back.

## A repo's own `check.sh`/convenience wrapper is not equivalent to the `checks.*` derivations

If a repo ships its own `./check.sh` (or similar) convenience script, a clean run of it does NOT
prove the checks relevant to your change build. A `check.sh` that passes `--no-build` skips
derivation builds entirely, and — like the commit-time pre-commit hooks — it only lints
the files it targets, not the whole repo. A gate can be fully green on `check.sh` and `pre-commit`
while a repo-wide lint or a consumer-input-alignment derivation the narrower script never runs
still fails. When you need confidence beyond the commit hooks, build the actual
`checks.<system>.<name>` derivations relevant to your change — `check.sh` alone is not a
substitute, however convenient.

## `end-of-file-fixer` can still modify a file at commit time after a clean `pre-commit run`

Pre-commit's `end-of-file-fixer` hook (auto-adds a trailing newline to Markdown files) can fire
and modify a file during `git commit` even when a prior `pre-commit run` on that same file was
clean — the commit-time hook run may use a different file-discovery path than a manual `run`.
Symptom: `pre-commit run` shows green, `git commit` re-runs the hooks, `end-of-file-fixer`
modifies the file, and the commit aborts needing a re-stage. Don't be surprised by this —
workaround: stage the change, run pre-commit, re-stage anything it modified, then commit.
