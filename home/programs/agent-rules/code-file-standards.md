---
name: code-file-standards
description: Exit-code conventions, unit-test isolation, and structured-data-file tooling for source/config files.
paths:
  [
    "**/*.sh",
    "**/*.bash",
    "**/*.bats",
    "**/*.go",
    "**/*.py",
    "**/*.rs",
    "**/*.js",
    "**/*.ts",
    "**/*.json",
    "**/*.yaml",
    "**/*.yml",
    "**/*.toml",
  ]
---

# Code File Standards

Moved out of the always-on core rules (tc-ql0o Stage D, 2026-08-26): each of these three packs is
scoped to a file type, so it now rides in only when a matching file is read instead of every
session unconditionally.

## Exit Codes

In ANY language, exit code 1 is the conventional general/catch-all error and MUST NOT be given a
specific branchable meaning. If an exit code must carry a specific meaning (so callers/scripts can
branch on it), it MUST be a distinct value >= 2, with 1 reserved for generic/unexpected errors.

## Unit Tests

MUST be isolated; if they modify files directly, the test MUST generate the scenario in a temp
directory.

### Tests That Need Git

A test that needs a git repository MUST get it from the shared hermetic fixture library for its
language. It MUST NOT build one by hand, and it MUST NOT touch the ambient repository (the one the
test process happens to be running in).

Go (`github.com/phillipgreenii/x`, repo `phillipgreenii-x`):

```go
repo := gittest.New(t, gitfixture.RepoOptions{})
```

- Tests MUST use `x/gittest` (the `*testing.T` adapter). Non-test helpers MAY use `x/gitfixture`,
  which is deliberately free of the `testing` import.
- Code under test that shells out to git SHOULD go through `x/gitclient`, whose child-process
  environment is an allowlist.

Bash/bats (`git-fixture-harness.bash`; canonical copy in `phillipg-nix-repo-base` under
`lib/scripts/`, vendored into some `test-support/` directories):

```bash
setup() { gfh_setup "my-suite"; }
teardown() { gfh_teardown; }
```

- Tests MUST use `gfh_setup`/`gfh_teardown`. A second repository under one setup MUST come from the
  harness primitive for its kind, never a hand-rolled `git init`/`git clone`:
  - `gfh_init_repo <path> <suite-name> [--no-identity]` for an additional working repository;
  - `gfh_init_bare <path>` for a bare remote (branch `main`, hooks disabled);
  - `gfh_clone <src> <dest> <suite-name> [--no-identity]` for a clone (hooks disabled even during
    the clone).

  Keep every `<path>`/`<dest>` under `GFH_WORK`.

- `--no-identity` (on `gfh_init_repo`/`gfh_clone`) MUST be used by a test of code that has to
  handle a repo where git cannot resolve an author: it sets `user.useConfigOnly=true` and no local
  identity. A test MUST NOT export `GIT_AUTHOR_*`/`GIT_COMMITTER_*` or an identity after
  `gfh_setup` when relying on it.
- `gfh_reset_env` (called by `gfh_setup`) unsets every exported variable not on its allowlist. A
  suite that needs one to survive (for example `SCRIPTS_DIR` or a nix-injected tool path) MUST
  preserve it with `gfh_save_env VAR...` BEFORE `gfh_setup` and `gfh_restore_env` AFTER it, and MUST
  NOT re-export any `GIT_*`-family variable that way. `gfh_save_env` replaces hand-rolled
  copy-to-an-unexported-name-and-back code.
- A vendored copy under a `test-support/` directory MAY lag the canonical copy and lack the
  primitives above; a suite that needs one MUST re-vendor the canonical file rather than hand-roll
  the fixture.

Both libraries are hermetic BY CONSTRUCTION: the fixture lives under a temp root, `HOME` and the
system git config are neutralised, and the environment is rebuilt from an allowlist rather than
scrubbed against a list of known-leaky `GIT_*` names. A test therefore MUST NOT:

- run `git init`, or `exec.Command("git", ...)`, outside these libraries;
- run git against the working directory, a bare relative path, or a `-C` path that is not under the
  fixture root;
- "protect" a hand-rolled fixture by unsetting `GIT_DIR`/`GIT_WORK_TREE` and friends. An enumerated
  scrub is only as complete as its list, and it is how this class of bug keeps recurring.

If a test needs something the library lacks, it MUST extend the library, not bypass it. Code that
predates this rule MAY keep its hand-rolled setup until the file is next changed; at that point it
MUST be migrated.

Why this is a hard rule: hook environments (a `pre-commit` run, a linked worktree) export
`GIT_DIR`/`GIT_INDEX_FILE` pointing at the OUTER repository. A fixture `git init` that inherits
them writes its temp path into the canonical clone's `.git/config` as `core.worktree`; the temp
dir is later deleted, and every git command in the clone then fails with `fatal: Invalid path`
(this also breaks `pn workspace apply`). Observed repeatedly: `pg2-12795`, `pg2-3fz2s`,
`pg2-oixbs`, `pg2-rrhw2`, `pg2-6drqh`, `pg2-yj06c`, `pg2-zw8s7`, and again in `pg2-7byxb`
(2026-10-06), where a hand-rolled test fixture bypassed `x/gittest`.

## Structured Data Files

MUST use `jq`/`yq`/`tq` for JSON/YAML/TOML manipulation over text-based editing (sed, awk, python).
