---
name: bash-scripting
description: Use when creating, modifying, reviewing, or deleting bash scripts (.sh, .bash), bats tests, completions, tldr pages, or mkBashScript/mkBashLibrary/mkBashModule nix packaging in this workspace. Authoritative reference for the mkBashBuilders framework.
---

# Bash Scripting in This Workspace

All bash scripts use the **mkBashBuilders framework** at `phillipgreenii-nix-personal/lib/bash-builders.nix`. This skill is the authoritative reference.

## Contents

- Framework overview & instantiation
- Source file rules (the #1 source of mistakes)
- Argument parsing, help, version, errors
- Shellcheck conventions
- The `.sh` / `.bash` split
- Config injection
- Libraries
- Testing (basics — see `references/testing-advanced.md` for wrappers and shared helpers)
- Required artifacts table
- Common mistakes
- Workflows: add / modify / delete a script
- **References**: [`wiring.md`](references/wiring.md), [`completions.md`](references/completions.md), [`tldr.md`](references/tldr.md), [`testing-advanced.md`](references/testing-advanced.md)
- **Assets** (copy-ready): [`test_helper.bash`](assets/test_helper.bash), [`completion.bash`](assets/completion.bash), [`_command-name`](assets/_command-name) (zsh completion template; rename to `_<your-command>` in real use), [`tldr.md`](assets/tldr.md)

## Framework Overview

Three composable builders:

| Builder         | Purpose                                              | Returns                                                                    |
| --------------- | ---------------------------------------------------- | -------------------------------------------------------------------------- |
| `mkBashLibrary` | Sourceable `.bash` library with dependency chaining  | `{ lib, check }`                                                           |
| `mkBashScript`  | Complete command (script + man + tldr + completions) | `{ script, manPage, tldr, completion, check, packages, internalPackages }` |
| `mkBashModule`  | Optional aggregator for multiple scripts/libraries   | `{ packages, checks, tldr, libraries, scripts }`                           |

Instantiate at the flake level (where `self` is available):

```nix
bashBuilders = phillipgreenii-nix-personal.lib.mkBashBuilders {
  inherit pkgs lib self;
};
```

**Never instantiate `bashBuilders` inside a home-manager module** — `self` isn't available, and faking it breaks version hashes. Build at the flake overlay level, consume via `mkPackageOption`.

## Directory Structure

### Per-command directory

```text
command-name/
├── default.nix                   # Calls mkBashScript
├── command-name.sh               # Entry point (args, config, orchestration)
├── command-name.bash             # Optional: core logic as testable functions
├── command-name.md               # Tldr page (required if public)
├── completions/                  # Required if public
│   ├── command-name.bash
│   └── _command-name
└── tests/
    ├── test-command-name.bats
    └── test-command-name-lib.bats  # if .bash exists
```

### Per-library directory

```text
lib/
├── default.nix                   # Calls mkBashLibrary
├── module-lib.bash
└── tests/
    └── test-module-lib.bats      # required
```

### Module level

```text
modules/<name>/
├── command-one/                  # per-command structure
├── command-two/
├── internal-helper/              # public = false
├── lib/                          # shared library
├── test-support/
│   ├── test_helper.bash          # shared setup, mocks, assertions
│   └── fixtures.bash             # optional
├── scripts.nix                   # wires commands + lib, takes bashBuilders
└── default.nix                   # home-manager module (thin consumer)
```

## Source File Rules

These three rules cause more migration failures than anything else. The builder injects what's missing — duplicating it breaks things. **The most common cause of breakage: copying an existing script and forgetting to strip the shebang and `set -euo pipefail`.**

Every `.sh` and `.bash` source file MUST:

1. **Have NO shebang** — the builder adds `#!/usr/bin/env bash` at build time.
2. **Have NO `set -euo pipefail`** — the builder injects strict mode via `writeShellApplication`.
3. **Start with `# shellcheck shell=bash`** — tells shellcheck/editors it's bash despite the missing shebang. Without it, shellcheck errors with SC2148.

Libraries (`.bash`) additionally:

- Are not executable (`chmod 644`).
- Contain only function definitions and constants — no top-level code that runs on source, no `main`.

## Argument Parsing

Use a `case` inside `while` — not `getopt`, not `getopts`:

```bash
show_help() {
  cat <<'HELP'
command-name: Short description

Usage: command-name [OPTIONS] [ARGS]

Options:
  -h, --help     Show this help message
  -v, --version  Show version information
  -f, --flag     Description of flag
HELP
}

while [[ $# -gt 0 ]]; do
  case $1 in
    -h|--help) show_help; exit 0 ;;
    -f|--flag) FLAG_VAR="$2"; shift ;;
    --) shift; break ;;
    --*) echo "Unknown option: $1" >&2; exit 1 ;;
    *) POSITIONAL_ARGS+=("$1") ;;
  esac
  shift
done
# Restore positional args: set -- "${POSITIONAL_ARGS[@]}"
```

## Help and Version

- **`--help`/`-h`**: Write a `show_help()` function in `.sh`. `help2man` reads this to generate the man page, so format matters.
- **`--version`/`-v`**: NEVER write version handling. The builder injects a handler that runs before user code. Version format: `YY.MM.DD.SSSSS+gitHash`, computed at build time.

## Error Handling

```bash
die() { echo "error: $1" >&2; exit "${2:-1}"; }
```

Errors to stderr, non-zero exit on failure.

## Shellcheck

The framework auto-excludes `SC1091` (can't follow non-constant source). All other exclusions go **inline with explanation**:

```bash
# shellcheck disable=SC2086  # Word splitting intentional for flag expansion
some_command $FLAGS
```

**Never** pass `excludeShellChecks` in `default.nix` — the framework rejects it.

Common legitimate exclusions (inline syntax: `# shellcheck disable=SC2086  # word splitting intentional here`):

| Code     | Reason                                                   |
| -------- | -------------------------------------------------------- |
| `SC1090` | Sourcing from runtime-computed path                      |
| `SC2086` | Word splitting intentional for flag arrays               |
| `SC2317` | `exit` inside a `while` loop — only when actually needed |

## The `.sh` / `.bash` Split

Scripts can split entry-point concerns from logic:

- `.sh` = argument parsing, config reading, orchestration, help text
- `.bash` = core logic as testable functions

The builder sources `.bash` before `.sh`. In local tests, the test helper resolves the `.bash` file via `SCRIPTS_DIR`.

**Not every command needs both.** Simple scripts are `.sh` only. Split when you want unit tests for logic functions without going through arg parsing.

## Config Injection

Pass nix-computed values via `config` (local) or `exportedConfig` (env vars):

```nix
mkBashScript {
  # strings → injected as local shell vars; non-strings → serialized to JSON, path injected as a var
  config = {
    SCALAR_VAR = "/some/path";           # string → local var
    JSON_CONFIG = { worktrees = [ "a" ]; team = "alice"; };  # non-string → JSON file path
  };
  exportedConfig = {
    DOCKER_ENV = "prod";                 # exported to child processes
  };
}
```

Detection: `builtins.isString value` → scalar local var. Otherwise → JSON-serialized via `pkgs.writeText`, the var holds the file path.

**Use `config` by default. Use `exportedConfig` only when child processes genuinely need the value** (e.g., Docker containers, subprocesses reading the env var directly). Exporting pollutes the environment of every child.

Consume JSON config with `jq`:

```bash
worktrees=$(jq -r '.worktrees[]' "$JSON_CONFIG")
```

## Libraries

Depend on a library by passing it via `libraries = [ my-lib ]`. The builder prepends `source ${my-lib.lib}` to the script.

Libraries can depend on other libraries:

```nix
mkBashLibrary {
  name = "module-lib";
  src = ./.;
  description = "...";
  libraries = [ zr-lib ];
}
```

The composed library file contains `source ${zr-lib}` prepended to `module-lib.bash`.

Circular library dependencies cause nix evaluation to fail; if you hit this, extract the shared logic into a third library.

## Testing

**Core principle**: tests MUST work without nix build. Run locally with `bats tests/` from the command or library directory.

Drop [`assets/test_helper.bash`](assets/test_helper.bash) into `test-support/` (module-level) or alongside tests (per-script). It handles `SCRIPTS_DIR` / `LIB_PATH` resolution and standard `TEST_DIR` / `HOME` isolation.

### Test isolation rules

1. Every test uses `TEST_DIR="$(mktemp -d)"`.
2. Override `HOME` to keep tests off the real system.
3. Cleanup in `teardown`: `rm -rf "$TEST_DIR"`.
4. Mock external commands by creating scripts in a temp dir and prepending to `PATH`.
5. Use `command <real-cmd>` to bypass mocks for setup (e.g., real git).
6. **Mocks must live OUTSIDE the test's git working tree** if the script runs `git clean` — see [testing-advanced.md](references/testing-advanced.md).

### Testing the split

- **`.bash` unit tests** (`test-<name>-lib.bats`): source the library directly, call functions, assert results.
- **`.sh` integration tests** (`test-<name>.bats`): run the script as a subprocess, check exit codes and output.

For wrapper patterns (scripts that source libraries), shared `test_helper` loading, and bats footguns, see [`references/testing-advanced.md`](references/testing-advanced.md).

### What to test

Flag handling, primary behavior, error/edge cases, config reading (fixture the config file), external command interactions (mock and verify args).

### What NOT to test

The version output (builder injects it), the shebang/executable bit (don't exist on source), anything requiring a full nix build.

## Required Artifacts

| Artifact          | Public command | Internal (`public = false`) | Library  |
| ----------------- | -------------- | --------------------------- | -------- |
| `.sh` source      | Required       | Required                    | N/A      |
| `.bash` logic     | Optional       | Optional                    | Required |
| `default.nix`     | Required       | Required                    | Required |
| `--help` text     | Required       | Required                    | N/A      |
| Tldr page (`.md`) | Required       | Optional                    | N/A      |
| Bash completion   | Required       | Optional                    | N/A      |
| Zsh completion    | Required       | Optional                    | N/A      |
| Bats tests        | Required       | Required                    | Required |

For completion templates and rules, see [`references/completions.md`](references/completions.md). For tldr format, see [`references/tldr.md`](references/tldr.md).

## Common Mistakes

### 1. Using `excludeShellChecks` in `default.nix`

The framework doesn't accept this parameter — build fails. Use inline `# shellcheck disable=SCxxxx  # explanation` instead.

### 2. Over-exporting config

Putting everything in `exportedConfig` pollutes every child process. Default to `config`; export only when a child genuinely reads the env var.

### 3. Relative path assumptions in tests

See [`references/testing-advanced.md`](references/testing-advanced.md) — use `LIB_PATH` with a fallback, never `source ../lib/...`.

### 4. Writing `--version` tests

Tests run against raw source. The version handler is injected by the builder — it doesn't exist in source. Don't test it.

### 5. Mocks inside the test's git working directory

If the script runs `git clean -fd`, mocks get deleted mid-test. Put mocks in a separate `mktemp -d` outside the git tree.

### 6. Forgetting to update completions when flags change

Whenever you add/remove/rename a flag, option, or subcommand: update **both** completion files AND the tldr page (if common usage changed) AND `show_help()`.

### 7. Circular script dependencies

`script-a` depends on `script-b.script` and vice versa → infinite recursion in nix eval. Extract shared logic into a library; scripts depend on libraries, not each other.

### 8. Shadowing bats built-ins

Defining a helper named `fail` or `skip` overrides bats internals and tests crash silently. Use `failing_step`, `mock_failure`, etc.

### 9. `COMPREPLY=($(compgen ...))` in completions

Trips SC2207 and breaks on filenames with spaces. Use `mapfile -t COMPREPLY < <(compgen ...)`.

## Workflow: Adding a New Script

1. Choose the right module: public CLI → support-apps; personal tool → phillipgreenii-nix-personal; work-specific → phillipg-nix-ziprecruiter.
2. Create the directory structure per the templates above.
3. Write `<name>.sh` with `# shellcheck shell=bash`, `show_help()`, case-based arg parsing, no shebang/set flags.
4. Create the tldr page (`<name>.md`) if public — start from [`assets/tldr.md`](assets/tldr.md).
5. Create completions (both files) if public — start from [`assets/completion.bash`](assets/completion.bash) and [`assets/_command-name`](assets/_command-name).
6. Write tests in `tests/test-<name>.bats` using [`assets/test_helper.bash`](assets/test_helper.bash).
7. Create `default.nix` calling `mkBashScript` with `name`, `src = ./.`, `description`, `runtimeDeps`.
8. Wire into `scripts.nix` at module or package level — see [`references/wiring.md`](references/wiring.md).
9. Verify locally: `cd path/to/command && bats tests/`.
10. Verify nix: `nix build .#<package>` and `nix flake check`.
11. Commit with `feat:` or `refactor:` prefix.

## Workflow: Modifying an Existing Script

1. Read the script, its tests, completions, and tldr page first.
2. Make the change to `.sh` and/or `.bash`.
3. Update tests for the new behavior.
4. Update completions if flags/options/subcommands changed.
5. Update tldr page if common usage examples changed.
6. Update `show_help()`.
7. Run `bats tests/` then `nix flake check`.
8. Commit.

## Workflow: Deleting a Script

1. `grep -r 'script-name' .` across all three repos.
2. Check `flake.nix` wiring — shellcheck lists, test checks, pre-commit hooks.
3. Fix consumers that use it as a `runtimeDep` first.
4. Delete the script directory.
5. Remove from `scripts.nix` (callPackage + allScripts list + checks).
6. Remove wiring from `flake.nix`.
7. Regenerate pre-commit if needed: `nix run .#install-pre-commit-hooks`.
8. `nix flake check`.
9. Commit.

## Implementation references

- **Framework implementation**: `phillipgreenii-nix-personal/lib/bash-builders.nix`
- **Framework design spec**: `phillipgreenii-nix-personal/docs/superpowers/specs/2026-04-02-mkbashbuilders-framework-design.md`
- **Framework test fixtures**: `phillipgreenii-nix-personal/lib/bash-builders-tests/`
- **ADR 0033** (ziprecruiter): Nix-wrapped `.sh` format (no shebang, no set flags, shellcheck directive)
- **ADR 0022** (ziprecruiter): Tldr pages inside module directories
