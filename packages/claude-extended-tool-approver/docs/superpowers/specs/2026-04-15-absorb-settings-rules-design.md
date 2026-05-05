# Absorb 8 Settings Rules into Go Rule Engine

**Status**: Approved
**Date**: 2026-04-15

## Context

The `identify-hook-misses` skill created 8 beads, each representing a
`settings.local.json` permission rule that should be absorbed into the Go rule
engine. These rules bypass the hook's safety analysis — moving them into Go
modules gives the engine proper control over when to approve, with path-aware
and context-aware decisions rather than blanket pattern matches.

**Settings file:** `~/.claude/settings.local.json`

**Target project:**
`~/phillipg_mbp/phillipgreenii-nix-support-apps/packages/claude-extended-tool-approver/`

## Implementation Order

Easiest-first. Each change is implemented, tested, and evaluated independently.
If a change causes regressions, defer it and continue with the next.

## Per-Change Lifecycle

Every change follows this sequence:

1. Add/fix rule logic in the target Go module
2. Add test cases covering the sample evidence rows
3. `go test ./...`
4. `claude-extended-tool-approver evaluate --settings=~/.claude/settings.local.json`
   — confirm no increase in `miss-uncaught`
5. Remove the corresponding `settings.local.json` entry via `jq`
6. Commit
7. `bd close <bead-id>`

## Change 1: contained-claude (pg2-2xpv)

**Module:** `internal/rules/safecmds/safecmds.go`
**Settings rule:** `Bash(contained-claude:*)`

**What:** Add `"contained-claude"` to the `alwaysSafe` slice. `claude` is
already present; `contained-claude` is a Docker-wrapped equivalent with
identical CLI surface.

**Test cases:**

- `contained-claude --version 2>&1` → Approve

## Change 2: bash -n (pg2-81ei)

**Module:** `internal/rules/safecmds/safecmds.go`
**Settings rule:** `Bash(bash -n .../fixtures.bash)`

**What:** Add special-case handling for `bash` and `sh` when `-n` is present
among the flags. `bash -n` parses syntax without executing — no side effects,
no command substitutions evaluated, no traps fired. The file argument is
read-only input; validate it as readable.

**Logic:**

- If command is `bash` or `sh` and args contain `-n`: extract the file path
  argument, validate as readable, approve if valid
- Compound form `bash -n <file> && echo "OK"` works naturally since `echo` is
  in `alwaysSafe`

**Test cases:**

- `bash -n /readable/file.bash` → Approve
- `bash -n /readable/file.bash && echo "OK"` → Approve
- `bash -n /unknown/file.bash` → Abstain

## Change 3: cue vet (pg2-vblf)

**Module:** `internal/rules/buildtools/buildtools.go`
**Settings rule:** `Bash(cue vet:*)`

**What:** Add `cue` handling with `vet` as an approved subcommand. `cue vet` is
read-only schema validation. Follow the existing `devbox search` pattern.

**Test cases:**

- `cue vet ./schemas/ 2>&1` → Approve
- `cue export ./schemas/` → Abstain (not an approved subcommand)

## Change 4: yq (pg2-boqw)

**Module:** `internal/rules/safecmds/safecmds.go` (already handled)
**Settings rule:** `Bash(yq:*)`

**What:** yq already has special handling (read by default, write if `-i`). The
sample rows show `hook=(empty)` — the hook wasn't deployed when those decisions
were logged. The current engine likely handles these correctly.

**Approach:**

1. Run evaluate first to check if yq rows are now `correct`
2. If correct: just remove the settings rule
3. If still missing: investigate path evaluation for `~/.colima/default/colima.yaml`
   — likely `PathUnknown` since `~/.colima/` isn't a configured zone
4. If `PathUnknown` is the cause: the settings rule was overly broad. Close the
   bead noting that abstain-on-unknown-path is correct behavior, and leave the
   settings rule in place (or accept the abstain)

**Test cases:** Verify existing yq tests cover read-path validation.

## Change 5: unzip (pg2-lquv)

**Module:** `internal/rules/safecmds/safecmds.go`
**Settings rule:** None (unzip wasn't in settings)

**What:** Add `unzip` handling. Parse flags to identify:

- **Input archive** (first non-flag positional arg): validate as readable
- **Output destination** (`-d <dir>`): validate as writable
- **No `-d`**: extraction goes to cwd, validate cwd as writable
- **`-l` or `-t` flags**: list/test operations are read-only — only validate
  archive as readable, skip write check

**Flag parsing:**

- Value flags (consume next arg): `-d`, `-x` (exclude), `-P` (password)
- Boolean flags: `-o`, `-q`, `-n`, `-j`, `-l`, `-t`, `-Z`

**Test cases:**

- `unzip /readable/archive.zip` (cwd is writable) → Approve
- `unzip -d /tmp /readable/archive.zip` → Approve
- `unzip -l /readable/archive.zip` → Approve (read-only, no write check)
- `unzip -t /readable/archive.zip` → Approve (read-only, no write check)
- `unzip /unknown/archive.zip` → Abstain
- `unzip -d /unknown/path /readable/archive.zip` → Abstain

## Change 6: chmod (pg2-47gs)

**Module:** `internal/rules/safecmds/safecmds.go` (already in `safeWriteCmds`)
**Settings rule:** `Bash(chmod:*)`

**What:** chmod is already in `safeWriteCmds` but sample rows abstain. Two
hypotheses:

**Hypothesis A — Compound command:** Row 26520 is
`chmod +x .../bin/wf && .../bin/wf 2>&1...`. The `&&` chain includes executing
the binary after chmod. Safecmds approves `chmod` but encounters `.../bin/wf` as
unknown and abstains on the chain. This is _correct_ — the settings rule was
overly permissive.

**Hypothesis B — Path not recognized as writable.**

**Approach:**

1. Run evaluate on these specific rows to confirm which hypothesis
2. If compound-command: the current behavior is correct. Remove the overly
   broad settings rule and close the bead
3. If path issue: fix path evaluation

**Test cases:** Verify compound-command chaining behavior.

## Change 7: git checkout (pg2-55vq)

**Module:** `internal/rules/git/git.go`
**Settings rule:** `Bash(git checkout:*)`

**What:** Currently `git checkout -- .` unconditionally returns Abstain (treated
as destructive). Change to unconditional Approve (no path writability check).
Rationale: the git module doesn't have access to patheval, and `git checkout`
operates on the repo working tree which is always writable by definition (it's
the project root). Claude Code's own safety prompts provide defense against
unintended data loss.

**Constraints from the bead:**

- `git stash drop` MUST remain Abstain
- `git clean -fd` MUST remain Abstain
- Compound commands with mixed decisions MUST Abstain overall

The compound cases work naturally: `git checkout -- . && git clean -fd` is
evaluated per-command in the `&&` chain. `git checkout` approves, `git clean`
causes Ask/Abstain, so the overall chain abstains.

**Risk:** This removes a safety guardrail. The bead explicitly requests it, and
the engine evaluates `&&` chains independently, so compound-command safety is
preserved.

**Test cases:**

- Update `git checkout -- .` from Abstain → Approve
- `git checkout -- path/to/file` → Approve
- `git checkout branch-name` → Approve (already works)
- `git checkout -- . && git clean -fd` → verify chain Abstain
- `git checkout -- . && git stash drop` → verify chain Abstain

## Change 8: sqlite3 (pg2-kdjr)

**Module:** `internal/rules/sqlite3/sqlite3.go`
**Settings rule:** `Bash(sqlite3:*)`

**What:** The sqlite3 module classifies queries and checks paths, but 7 sample
rows abstain. Primary hypothesis:

**Hypothesis — Path zone:** The db path
`~/.local/share/claude-extended-tool-approver/asks.db` maps to the
`<xdgDataHome>/claude-extended-tool-approver/` zone, which is `PathReadOnly`.
UPDATE queries need `PathReadWrite`, so they correctly abstain. SELECT queries
on this path should approve (PathReadOnly is sufficient).

**Approach:**

1. Run evaluate on the specific rows
2. Check which are SELECTs vs UPDATEs
3. If SELECTs are abstaining: fix query parsing or path evaluation
4. If UPDATEs are abstaining on a read-only path: behavior is correct. Options:
   a. Promote the path to `PathReadWrite` in patheval (if writes are legitimate)
   b. Accept the abstain and remove the overly broad settings rule
5. Likely outcome: promote the tool-approver data dir to `PathReadWrite` since
   the tool's own database is a legitimate write target

**Test cases:** Add cases for the specific query patterns from the samples.

## Settings Removal

After all changes, these entries are removed from
`~/.claude/settings.local.json`:

| Line | Entry                             | Bead                   |
| ---- | --------------------------------- | ---------------------- |
| 31   | `Bash(contained-claude:*)`        | pg2-2xpv               |
| 35   | `Bash(bash -n .../fixtures.bash)` | pg2-81ei               |
| 20   | `Bash(cue vet:*)`                 | pg2-vblf               |
| 33   | `Bash(yq:*)`                      | pg2-boqw (conditional) |
| 21   | `Bash(chmod:*)`                   | pg2-47gs               |
| 19   | `Bash(git checkout:*)`            | pg2-55vq               |
| 29   | `Bash(sqlite3:*)`                 | pg2-kdjr (conditional) |

`unzip` (pg2-lquv) has no settings entry to remove.

"Conditional" means the settings rule is only removed if evaluate confirms the
Go engine now handles the cases correctly. If the Go engine correctly abstains
where the settings rule was overly broad, the settings rule is removed anyway
and the abstain is accepted as the correct behavior.

## Out of Scope

- Changes to the ZipRecruiter `settings.local.json` at
  `phillipg-nix-ziprecruiter/modules/claude-code/settings.local.json` — that
  file has its own similar rules but is managed separately
- Changes to `patheval` zones beyond what's needed for sqlite3 (change 8)
- Refactoring of existing rule modules
