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

## Structured Data Files

MUST use `jq`/`yq`/`tq` for JSON/YAML/TOML manipulation over text-based editing (sed, awk, python).
