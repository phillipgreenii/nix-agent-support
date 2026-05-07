# Rules

> The section `## Rules for Interactive Sessions Only` applies only when working with the user directly.
> Autonomous agents invoked via `claude -p` (e.g. background workers, polecats, dogs)
> MUST ignore that section and apply only the rules under `## Always-Apply Rules`.

## Always-Apply Rules

### Design & Documentation Standards

- MUST use design pattern terminology when discussing designs
- MUST use separate code blocks per file in markdown-supporting files
- MUST write policies using RFC 2119 language (MUST/SHOULD/MAY/etc.)
- MUST use mermaid diagrams instead of images in documentation

### Workflow Sequence

1. **Search First** — confirm functionality exists or doesn't before implementing
2. **Reuse First** — extend existing code/patterns before creating new; minimize changes
3. **No Assumptions** — only use files read, user messages, tool results. IF missing info: search first, then ask
4. **Challenge Approach** — identify and state flaws/risks/better approaches directly

### Development Standards

#### Validation

**CRITICAL**: Before claiming any change is complete:

- If the project has `.pre-commit-config.yaml`: `pre-commit run --all-files` MUST pass
- If the project has `flake.nix`: `nix flake check && darwin-rebuild check --flake .` MUST pass
- IF no tests exist for changed code: create them
- NEVER claim code is complete without passing tests

#### Structured Data Files

MUST use `jq`/`yq`/`tq` for JSON/YAML/TOML manipulation over text-based editing (sed, awk, python).

#### Unit Tests

MUST be isolated; if they modify files directly, the test MUST generate the scenario in a temp directory.

### General Guidelines

- Before recommending paid/licensed software, confirm the cost with the user.

### Git Workflow

- Always commit to the correct branch. Before committing, run `git branch --show-current` to verify. If changes were made on the wrong branch, alert the user before proceeding.
- When pre-commit hooks exist, always run `git diff --cached` and address any formatting/lint issues before attempting to commit. If subagents generate changes, ensure files are properly staged.

### Prohibited Actions

#### System Commands

- **CRITICAL**: NEVER run system activation commands (e.g., `darwin-rebuild switch`) without explicit user request — these are user-only commands
- **CRITICAL**: NEVER use `sudo`
- When building/validating nix changes without activation, use a build-only command

#### Version Control

- Include the Jira issue as `Refs: TICKET-ID` on the line immediately after the subject (before the body). Extract the ticket ID from the branch name (format: `username.TICKET-ID.description`). A valid ticket ID matches `[A-Z]+-\d+` (e.g., `FINDEV-9208`, `CI-1494`). If the branch contains `NO-JIRA`, `NOJIRA`, or any variation instead of a real ticket ID, omit the `Refs:` line entirely.
- **CRITICAL**: NEVER use `--no-verify` (or `-n`) on git commands without explicit user approval
- IF git hooks report violations: MUST fix the violations rather than bypassing hooks

#### Numeric Data

- **CRITICAL**: NEVER include calculated numbers without showing calculation method

#### Estimates

- **CRITICAL**: NEVER provide time estimates
- IF signaling effort needed: use t-shirt sizes (S/M/L/XL)

## Rules for Interactive Sessions Only

### Interaction Protocol

- MUST provide direct answers to questions without making code/file changes
- IF question implies work: confirm intent before proceeding
- MUST question assumptions, offer counterpoints, and state problems directly — prioritize correctness over agreement

### Development Standards

#### Planning & Design

- DEFAULT: iterative discussion → plan approval → implementation
- MUST NOT start coding without confirmation
- EXCEPTION: MAY proceed immediately when explicitly provided an implementation plan
- MUST critique non-trivial plans via independent subagent; iterate until no adjustments needed
- IF user input required during critique: ask before continuing
