# phillipgreenii-nix-agent-support Repository Rules

## Repository Overview

Agent and AI tooling for macOS and Linux (nix-darwin + NixOS).

This is a self-contained, composable Nix configuration providing agent and AI tooling. It provides
modules designed to be imported by organization or machine-specific flakes.

## Key Architecture

- **Standalone**: Works completely independently without external dependencies
- **Composable**: Designed to be imported by other flakes (like your-private-flake)
- **Modular**: One program per module with options colocated with functionality
- **Declarative**: Programs declare their own dock presence and dependencies

## Configuration Structure

- **Darwin Modules**: `darwin/` contains system-level macOS configuration
- **Home Manager Modules**: `home/` contains user-level configuration
  - `programs/` - One directory per program with its configuration and options

## Module Design Pattern

Each program module:

- Lives in its own directory under `home/programs/<program-name>/`
- Defines its own options using `phillipgreenii.*` namespace
- Contains all configuration for that program
- Respects shell enable flags (bash/zsh)

## Status Line (`home/programs/claude-status-line`)

The Claude Code status line is assembled from an ordered list of "part" scripts. The
wrapper (`scripts.nix` / `mkWrapperScript`) reads Claude's stdin JSON into `CLAUDE_SL_*`
env vars, runs each part, and width-wraps the non-empty outputs across rows (see
`docs/adr/0019-status-line-width-aware-wrapping.md`).

- **Extension point**: append part-script store paths to
  `phillipgreenii.programs.claude.status-line-parts` (a `listOf str` — any module MAY
  contribute, including downstream flakes like `phillipg-nix-ziprecruiter`). Use
  `lib.mkBefore` / `lib.mkAfter` to control where your part lands relative to the
  defaults; plain assignment merges at default priority and order is then import-dependent.
- **Part contract**: a part reads its data from the exported `CLAUDE_SL_*` env vars
  (`CLAUDE_SL_SESSION_NAME`, `_SESSION_ID`, `_WORKTREE`, `_BRANCH`, `_VERSION`, `_MODEL`,
  `_CONTEXT_USED_PCT`, `_REPO_OWNER`, `_REPO_NAME`, `_PR_NUMBER`, `_PR_URL`,
  `_PR_REVIEW_STATE`, `_EFFORT`, `_THINKING`, `_OUTPUT_STYLE`, `_VIM_MODE`, `_AGENT`)
  or its own environment; prints **one** formatted segment to stdout
  (ANSI colors allowed); and exits non-zero to be skipped silently. Keep segments compact —
  they share rows and the right edge is reserved for notifications.
- **New JSON field**: extend the wrapper's `jq` extraction in `mkWrapperScript` to export a
  new `CLAUDE_SL_*` var, then add a part that consumes it.
- **Branch fallback**: `worktree.branch` is only present inside a Claude worktree session.
  In a normal checkout the wrapper derives `CLAUDE_SL_BRANCH` from the repo's `.git/HEAD`
  (walking up from `workspace.current_dir`) using the `read` builtin — deliberately **no
  `git` subprocess**, to preserve the single-process-per-render goal. JSON `worktree.branch`
  always wins when present; a detached HEAD shows no branch.
- **Colors**: override named ANSI codes via `phillipgreenii.programs.claude.status-line-colors`
  (the `claude-theme` module injects Stylix truecolor). Do not hardcode new colors in parts
  without a matching key.
- **Width / wrapping contract**: the wrapper wraps at `COLUMNS - reserve`, where `reserve`
  is `phillipgreenii.programs.claude.status-line-notification-reserve` (default 20),
  applied uniformly to every row. Wrapping is disabled when `COLUMNS` is unset/0/non-numeric.
  A single segment wider than the budget is emitted whole on its own row (never split). When
  adding parts, prefer short labels so rows pack well on narrow terminals.
- **Tests**: `test-claude-status-line.bats`. Width tests MUST pass `COLUMNS` /
  `CLAUDE_SL_RESERVE` via `env` (the wrapper runs in a pipeline; a plain assignment does not
  reach it).

## Development Workflow

- **Format**: Use `nix fmt` for formatting Nix files
- **Test**: Use `nix flake check` to validate configuration
- **Build**: Flake provides reusable modules, not direct machine configs

## Versioning of Custom Packages

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
`go mod tidy && nix run github:nix-community/gomod2nix -- generate` (NOT `nix-update`; there is no
`vendorHash` for this family). A local `replace => ../sibling` (e.g. `../claude-transcript`) is
resolved natively — use the rooted-fileset + `modRoot` form (Pattern B). Authority and the full
A/B pattern: `phillipg-nix-repo-base` ADR 0008 and its `CLAUDE.md` "Go packages" section. Do not
reintroduce `vendorHash`/`buildGoModule`/`localReplaceModules` for these packages.

## Key Principles

- **Self-Contained**: No external flake dependencies beyond declared inputs
- **Option Colocation**: Options defined in the module that uses them, not centrally
- **Shell Flexibility**: Bash enabled by default, zsh disabled by default
- **No Assumptions**: Consuming flakes override git email, zsh status, etc.

## Configuration Options Namespace

All options use the `phillipgreenii.*` namespace.

Per ADR-0047 §"Policy: wrap everything", all machine-flake-facing options live under `phillipgreenii.*`.

## AI Agent Package Sourcing

When adding any AI agent, LLM tool, or coding assistant, use this lookup order:

1. **`github:numtide/llm-agents.nix`** — check first. Updated 4× daily, binary
   cache at `cache.numtide.com`. Browse packages at
   `https://github.com/numtide/llm-agents.nix/tree/main/packages`. Covers
   Claude Code, coding agents, usage analytics, and workflow tools.
2. **`pkgs.unstable`** — fall back if not in llm-agents and update frequency is
   not critical.
3. **Local derivation + update script** — last resort when absent from both.

## When Making Changes

1. Maintain standalone functionality - don't add dependencies on other custom flakes
2. Keep modules focused - one program per directory
3. Test with `nix flake check` before committing
4. Follow the established option pattern (define in module that uses it)
5. Respect shell enable flags in all shell integrations
6. **MUST review and update relevant documentation after completing any task**:
   - Update `README.md` if module structure changes
   - Update this file if patterns change

**Before claiming any change is complete:**

- If `.pre-commit-config.yaml` exists: `prek run --all-files` or `pre-commit run --all-files` (whichever is available) MUST pass
- If `flake.nix` exists: `nix flake check` MUST pass

## File Locations

- **Darwin Config**: `darwin/` directory
- **Home Manager Config**: `home/` directory
- **Program Modules**: `home/programs/<program-name>/default.nix`
- **Documentation**:
  - `README.md` - Main documentation
  - `CLAUDE.md` - This file

---

## Architecture Decision Records (ADRs)

### When to Create an ADR

Create an ADR when making a decision that:

- Changes how the project is structured or organized
- Chooses one technology, library, or tool over alternatives
- Establishes a pattern or convention that future work should follow
- Changes build, deployment, or dependency management strategy
- Affects cross-project consistency or conventions
- Involves a non-obvious trade-off worth documenting for future reference

Do NOT create ADRs for:

- Implementation details (variable naming, code formatting)
- Temporary workarounds or quick fixes
- Obvious choices with no meaningful alternatives
- Process decisions (meeting times, review cadence)

### ADR Location and Naming

- Store ADRs in `docs/adr/` at the repository root
- Draft ADRs: `docs/adr/draft-{short-title}.md`
- Accepted ADRs: `docs/adr/NNNN-{short-title}.md` (sequentially numbered, e.g., `0000-use-adrs.md`)
- Each repository maintains its own numbering sequence

### ADR Template

Use this template for all ADRs:

    # [Short Title of Decision]

    **Status**: [Draft | Accepted | Deprecated | Superseded]
    **Date**: YYYY-MM-DD
    **Deciders**: [Who was involved]

    ## Context

    [Describe the problem, forces at play, constraints, requirements]

    ## Decision

    [The decision made -- clearly stated]

    ## Consequences

    ### Positive
    [Good outcomes]

    ### Negative
    [Downsides and trade-offs]

    ### Neutral
    [Neither good nor bad, just facts]

    ## Alternatives Considered

    ### [Alternative 1]
    [Why it was rejected]

    ## Related Decisions

    [Links to related ADRs in this repo or cross-repo references]

### Cross-Repo References

When an ADR relates to a decision in a sibling repository, reference it in the
"Related Decisions" section using the format:

    See also: <repo-name> docs/adr/NNNN-short-title.md

Example:

    See also: your-private-flake docs/adr/0003-adopt-beads.md

### ADR Index

Maintain `docs/adr/index.md` as a table listing all ADRs with links. When
adding, deprecating, or superseding an ADR, update the index to reflect the
change.

### Before Making Changes

Before significant modifications, check `docs/adr/index.md` for relevant
architectural decisions and read them for context. If your change would make an
existing ADR obsolete, update its status to Deprecated or Superseded and link to
the replacement in both the ADR file and the index.

---

## Markdown Authoring Conventions

This project uses prettier to format `*.md` files. Always wrap glob patterns, cron expressions,
file paths with underscores, and Python identifiers in backticks to prevent prettier from
interpreting them as markdown emphasis or bold markup.

---

## Pre-commit Hook Installation

When you modify the pre-commit hook configuration in `flake.nix` (the `pre-commit` block), you must re-install the hooks so the generated `.pre-commit-config.yaml` is updated:

```bash
nix run .#install-pre-commit-hooks
```

Run this before committing to ensure the new/changed hooks are active.
