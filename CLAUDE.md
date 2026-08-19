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

## Public Repository — No ZipRecruiter Disclosure

This repo (including the pg-pr Go module) is a standalone PUBLIC nix flake. NEVER hardcode or
disclose ZipRecruiter-specific details — repo names, project/service identifiers, CI workflow
names, Jira base URL/project keys, Slack workspace/channel IDs, incident semantics, bot
identities — in code, tests, or docs. All ZR-specific configuration lives in
`phillipg-nix-ziprecruiter` and is supplied at runtime via config; keep the tools here
generic and config-driven. (User constraint, 2026-06-24.)

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
  contribute, including downstream flakes like `phillipg-nix-ziprecruiter`).
- **Ordering convention** (see `docs/adr/0020-status-line-parts-ordering-convention.md`):
  the list is merged across modules by ascending priority band, so contributors MUST place
  their parts with an explicit order helper and MUST NOT use plain assignment (which lands
  at the default band and orders by module-import order — non-deterministic across
  contributors). Bands: `lib.mkBefore` (500) leads; the base default set is `lib.mkOrder
1000`; `lib.mkAfter` (1500) trails (e.g. ZR's `aws` / `workspace` parts). For finer
  placement use `lib.mkOrder N` with N between bands. Within one definition list, order is
  the list order.
- **Part contract**: a part reads its data from the exported `CLAUDE_SL_*` env vars
  (`CLAUDE_SL_SESSION_NAME`, `_SESSION_ID`, `_WORKTREE`, `_BRANCH`, `_VERSION`, `_MODEL`,
  `_CONTEXT_USED_PCT`, `_EXCEEDS_200K`, `_REPO_OWNER`, `_REPO_NAME`, `_PR_NUMBER`, `_PR_URL`,
  `_PR_REVIEW_STATE`, `_EFFORT`, `_THINKING`, `_VIM_MODE`, `_AGENT`, `_5H_PCT`, `_5H_RESET`,
  `_7D_PCT`, `_7D_RESET`)
  or its own environment; prints **one** formatted segment to stdout
  (ANSI colors allowed); and exits non-zero to be skipped silently. Keep segments compact —
  they share rows and the right edge is reserved for notifications.
- **Default segment order** (base set, `home/programs/claude-status-line/scripts.nix`):
  vim, session name?, session id, location (repo + worktree + branch + `PR#<n>`), model
  (+effort +thinking), agent, context (+200k alert), limits (5h + 7d), version. The PR sub-part
  is appended after branch inside the single location segment (colored by `pr.review_state`,
  no glyph prefix); the `CLAUDE_SL_PR_*` vars remain exported for custom parts.
- **Nerd-font glyphs**: `phillipgreenii.programs.claude.status-line-nerd-font` (bool, default
  false) picks MDI glyphs vs text fallbacks. The choice is baked at Nix eval time (no runtime
  branch) via the `nerdFont` arg threaded into `scripts.nix`. In text (off) mode the location
  sub-parts carry `repo:` / `wt:` / `br:` labels (matching the `ctx:` / `5h:` / `7d:` idiom); in
  glyph (on) mode the MDI marker replaces the label. Glyphs are emitted as precomputed
  raw UTF-8 bytes (`printf '\xNN...'`), NOT `printf '\U...'`, because `\U` needs a UTF-8 active
  locale (the nix build sandbox and `LC_ALL=C` shells have none) — byte escapes are
  locale-independent. Both a nerd-off and a nerd-on test package are built in `flake.nix`
  (`test-claude-status-line`, `test-claude-status-line-nerdfont`); the shared bats file branches
  on the `CLAUDE_SL_TEST_NERD_FONT` env marker the nerd-on package sets.
- **Locale-safe width**: the wrapper forces a UTF-8 locale (baked: `en_US.UTF-8` on darwin,
  `C.UTF-8` elsewhere) for the visible-width math when the active `LC_CTYPE` isn't UTF-8, so a
  4-byte MDI glyph counts as one character (`${#}` == 1) instead of over-wrapping.
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

## Beads Labels

Every bead filed against this repo MUST carry the repo label `agent-support`.

If the bead concerns one specific project, it MUST also carry that project's label. The project
label is the basename of the top-level directory — under `packages/` or `claude-marketplace/` —
that the bead's subject matter lives in (e.g. `packages/pa-monitor` → `pa-monitor`,
`claude-marketplace/pg-ccaudit` → `pg-ccaudit`). Run `ls packages claude-marketplace` for the
current valid set: that directory listing IS the source of truth, not an enumerated table here,
so it cannot go stale as projects are added, removed, or renamed.

A bead about repo-wide or cross-cutting concerns (flake-level, CI, docs shared across projects)
MUST carry only the repo label, no project label.

**Known exception**: `packages/integrate-branch-support` and `claude-marketplace/integrate-branch`
are the same project under two different basenames, so the path rule cannot resolve a single
label for it by name alone — use `integrate-branch-support` (the package name) until this is
reconciled (tracked: `pg2-vuqaf`). Audited 2026-08-19 (`pg2-vuqaf`): every other pair of
`packages/`/`claude-marketplace/` entries with matching basenames lines up exactly, and no other
tool/project is spelled or organized differently across the two roots — this is the only
cross-root split in this repo.

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

**Go test gate**: a Go package with `subPackages` set means `nix build .#<pkg>` compiles only
`cmd/` — packages outside `cmd/` are never compiled and their tests never run, so a green package
build is NOT a whole-module test gate (proven 2026-08-12, bead `pg2-3nb2t`: `nix build .#pg-pr`
exited 0 while `checks.pg-pr-go-tests` had been red for a week). The whole-module gate is
`nix build .#checks.<system>.<pkg>-go-tests`, or the full `nix flake check` — which builds
`checks.*` but NOT `packages.*`.

## Key Principles

- **Self-Contained**: No external flake dependencies beyond declared inputs
- **Option Colocation**: Options defined in the module that uses them, not centrally
- **Shell Flexibility**: Bash enabled by default, zsh disabled by default
- **No Assumptions**: Consuming flakes override git email, zsh status, etc.

## Configuration Options Namespace

All options use the `phillipgreenii.*` namespace.

Per `phillipgreenii-nix-personal` ADR 0047's "Policy: wrap everything", all machine-flake-facing
options live under `phillipgreenii.*`. That ADR is in **another repo** — see
"Architecture Decision Records" → "Citation conventions" below for why the owning repo must always
be named.

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

- If `.pre-commit-config.yaml` exists: the pre-commit hooks MUST pass on the changed files. The `git commit` hook run (on staged files) is the gate; validate beforehand with `prek run --files <changed files>` (or `pre-commit run --files …`). Do NOT rely on `--all-files` as the per-change gate — it duplicates the commit run, forces the slow bats/nix hooks on unrelated diffs, and can false-block on a pre-existing violation elsewhere; reserve it for a deliberate full-repo sweep.
- If `flake.nix` exists: `nix flake check` MUST pass

## File Locations

- **Darwin Config**: `darwin/` directory
- **Home Manager Config**: `home/` directory
- **Program Modules**: `home/programs/<program-name>/default.nix`
- **Documentation**:
  - `README.md` - Main documentation
  - `CLAUDE.md` - This file

---

## pg-pr / pr-pool Development Rules

- **Behavior docs are the source of truth** (working principle, user 2026-07-09): changes to the
  pg-pr ↔ pr-pool system MUST flow through the living docs at `docs/behavior/` first, then derive
  throwaway spec → design → plan → code. The docs are product-level and timeless (stories,
  journeys, invariants in RFC 2119 language); code paths and tool internals stay out of the
  narrative. When review/workflow behavior changes, the relevant `docs/behavior/` doc MUST be
  edited in the SAME change. The old `docs/pr-review-flow.md` is a downstream implementation
  reference — the behavior doc wins on disagreement.
- **Enrichment is compute-only** (user directive, 2026-06-24): pg-pr PR enrichment
  (kind/languages/size/urgency) MUST be computed deterministically with NO LLMs; libraries are
  fine, perfection is not required. Any sub-signal genuinely requiring an LLM MUST be deferred to
  a separate LLM-gated bead, never implemented with an LLM. The same rule applies to diff-review's
  reviewer-picking inputs.
- **pr-pool config testing trap**: a config.toml declaring `[[query]]` but NO `[[role]]` makes
  pr-pool log "config present but defines no [[role]]; using built-in roles" and fall back to the
  BUILT-IN query set — your queries are silently discarded, so a smoke test can exit 0 having
  never evaluated the source type under test (hit 2026-08-13, `pg2-lmyts`). A test config MUST
  declare at least one role. Reliable recipe: `pr-pool config --print-defaults > cfg.toml`, then
  retype ONE existing query to the type under test. A hand-rolled ccpool role needs `actor` AND
  (`prompt` XOR `prompt_file`) AND `completion` AND `on_failure` AND `on_dispatch_fail`
  (enums: `completion` = close-only|close-or-handback; the failure fields = unclaim|add-human).
  To prove the backing-command check actually RAN, run the same config against the unwrapped
  binary `bin/.pr-pool-wrapped` under `env -i PATH=/usr/bin:/bin` — it must exit 1 with
  `backing command "<cmd>" cannot be invoked`; without that negative control, an exit 0 through
  the nix wrapper is vacuous (the wrapper injects the tools onto PATH).

## Claude Code Rule / Skill / Plugin Delivery

Verified against Claude Code 2.1.186:

- A plugin-root CLAUDE.md is NOT loaded (inert). A skill's BODY loads on-invoke; only its
  name+description are always-on.
- User/project CLAUDE.md (memory) IS loaded in `claude -p` headless mode (verified empirically).
  There is NO reliable interactive-vs-headless signal for hooks — do not assume a hook can scope
  rules to interactive-only.
- CONVENTION: always-on rules live in `home/programs/agent-rules/pgii-agent-rules.md` (delivered
  to `~/.claude/CLAUDE.md` via home.file); skills/plugins ship via the nix-managed marketplace.
  Rules CANNOT ride in a plugin, so "plugin owns its rules" is impossible.
- Plugin `bin/` dirs put executables on the Bash-tool PATH only (not login shells),
  auto-discovered with no manifest entry — BUT the marketplace directory-source cache copy SKIPS
  symlinks pointing outside the plugin dir, so a `/nix/store` symlink would be silently dropped.
  The established pattern instead (bead `pg2-sikj3`): the plugin's skill/hook invokes a BARE
  command, and the binary rides `home.packages` co-gated on `claude.enable` (precedents: pg-pr,
  claude-extended-tool-approver). No workspace plugin uses `bin/`.

---

## Architecture Decision Records

ADRs live in `docs/adr/` (`index.md` lists them). Read relevant ADRs before changing the area they
cover; see `docs/adr/0000-use-architecture-decision-records.md` for the process.

### Citation conventions

Rules for writing a citation into code, comments, or docs in this repo (RFC 2119):

1. **A citation MUST name its owning repo unless the target is in this repo's `docs/adr/`.**
   ADR numbers are per-repo and they **COLLIDE across this pn-workspace**, so a bare `ADR-NNNN`
   written here is not a unique reference. Live examples of the same number meaning two different
   decisions:

   | Number | `phillipgreenii-nix-personal`                            | `phillipg-nix-ziprecruiter`                                |
   | ------ | -------------------------------------------------------- | ---------------------------------------------------------- |
   | `0047` | phillipgreenii option namespace and platform conventions | Preserve-by-default merge policy for `settings.local.json` |
   | `0049` | launchd stable-path indirection                          | `nix`: enforce `pn-workspace.toml` keys                    |

   This repo's own series grows over time (check `docs/adr/index.md` for the current tail), so
   whether a bare number resolves locally or collides cross-repo can FLIP as ADRs land — never
   reason from a remembered tail. Write `` `phillipgreenii-nix-personal` ADR 0047 `` (the
   style already used for `` `phillipg-nix-repo-base` ADR 0008 `` above), never a bare `ADR-0047`.

2. **A citation MUST name the target section by its PROSE heading, never by a section number or a
   section sign (`§`).** Write `ADR 0038's Context`, not `ADR 0038 §7`. Heading names survive
   edits; section numbers renumber silently.

3. **Code MUST NOT cite the ephemeral design specs under the pn-workspace root's
   `docs/superpowers/specs/`.** Those files are in **no repository**, are extraction sources used
   once and then abandoned, and are slated for deletion — so every reference to them is guaranteed
   to dangle. State the rule in the code itself, or cite a durable in-repo ADR.

Rule 3 is enforced **mechanically over the ccpool surface only** (bead `pg2-oxrha`, widened by
`pg2-qkk8n`), by two guards that between them ban the section sign outright:

- `packages/ccpool/cmd/ccpool/spec_citations_test.go` — the ccpool **Go module**
  (`packages/ccpool/**`, every file type, tests included).
- `checks.<system>.test-ccpool-surface-spec-citations` in `flake.nix` — the ccpool surface
  **outside** that module (`home/programs/ccpool/`, `darwin/modules/ccpool/`,
  `nixos/modules/ccpool/`), which the Go test structurally cannot reach. A new ccpool-surface
  directory MUST be added to that check's fileset.

The guarded scope is **deliberately the ccpool surface, not the repo**: repo-wide the section sign
appears 534 times across 146 files (ADR prose, historical `docs/superpowers/plans/`, other
packages' own in-repo citations), nearly all of it legitimate, so a repo-wide ban would need an
allowlist longer than the rule. Everywhere else rules 1-3 are convention, not a test.

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
