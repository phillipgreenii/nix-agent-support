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

Widened by operator ruling (Phillip, 2026-08-24, bead `pg2-tphcc`): public repos MUST carry no
user identifier of any kind — real name, login, or handle — other than the operator's own
(`phillipgreenii` / `phillipg@ziprecruiter.com`). No denylist of forbidden tokens (plaintext or
hashed) may be committed to this or any repo to enforce it; a short-lived denylist MAY live only
in this workspace's local, uncommitted `CLAUDE.md` (see its "Workspace Policies").

**Mechanical guard**: `packages/pg-pr/cmd/pg-pr/identifier_allowlist_test.go`
(`TestIdentifierAllowlistGuard`) inverts the check into a small, committable ALLOWLIST of
known-safe identifiers (the operator's own identity plus named public bot/product accounts —
`coderabbitai`, `policy-bot`, `dependabot` — and this module's existing generic test placeholders)
and flags any other username/login/handle-shaped token found in a structured identity field
(a JSON `login`/`author`/`reviewer`/`user` key, a `name.name.TICKET-NNN`-shaped branch-name
component, or a git `Author:`/`Committer:` trailer). It runs automatically as part of
`checks.<system>.pg-pr-go-tests` in `flake.nix` — no separate check attribute. Guarded scope is a
RATCHET: today it covers only `packages/pg-pr`'s `testdata/` fixtures, widening as other
directories are scrubbed (`pg2-dssp6`, `pg2-n3gez`, `pg2-k23s6`). It does NOT catch a real name
embedded in free-text prose with no structural marker — that stays a manual-review
responsibility. See the test file's doc comments for the full scope rationale.

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

Full contract (part-script protocol, ordering convention, glyph/width/locale mechanics) moved to
the `.claude/rules/claude-status-line.md` path-rule (tc-ql0o Stage D, 2026-08-26) — it rides in
only while editing files under `home/programs/claude-status-line/`.

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

Full contract (per-source-digest versioning, the gomod2nix engine, the whole-module Go test gate)
moved to the `.claude/rules/package-versioning.md` path-rule (tc-ql0o Stage D, 2026-08-26) — it
rides in only while working under `packages/`.

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
- TRIPWIRE PATTERN (tc-ql0o Stage C, `claude-marketplace/beads-lifecycle`, 2026-08-26): a rule
  pack whose violation window has an OBSERVABLE in-session trigger (a `bd` verb, a
  park/release/accept action, a label mutation) MAY move its full body into a skill, provided
  `pgii-agent-rules.md` keeps a short MUST-invoke stub naming the skill and the trigger. A pack
  with NO such trigger (a bare prohibition, a conversation-time ruling) MUST stay always-on in
  `pgii-agent-rules.md` itself — the tripwire pattern does not relax that.
- PATH-SCOPED RULE PATTERN (tc-ql0o Stage D, 2026-08-26): file-keyed detail whose violation
  window is scoped to a file FAMILY (not a `bd`/git verb) MAY move into `.claude/rules/*.md` with
  `paths:` frontmatter instead — a repo-level example is this repo's own
  `claude-status-line.md`/`package-versioning.md`/`markdown-conventions.md`; the user-level
  analogue (delivered by `home/programs/agent-rules`) is `nix-how-to.md`/`code-file-standards.md`
  under `~/.claude/rules/`. `paths:` (NOT `applies-to:`) is the recognized scoping key — confirmed
  empirically against deployed Claude Code 2.1.233 (tc-ql0o Stage B.1 spike): a rule scoped with
  `paths:` is absent at session start and injected only on a matching Read; an UNRECOGNIZED key
  like `applies-to:` is silently ignored, so the rule fails OPEN (always-loads) rather than
  erroring. That is why `workspace/.claude/rules/beads-remote-server.md` deliberately keeps
  `applies-to:` — it is meant to always-load regardless of which file is being read — rather than
  being "corrected" to `paths:`, which would newly scope it down to `.beads/**/*` reads only. A
  gate obligation that triggers on a REPO PROPERTY rather than a file read (pre-commit hooks
  exist; `flake.nix` exists) cannot be carried by a path-rule and MUST stay in the always-on core
  (the `pg2-3nb2t` class: a Go edit in a flake repo never reads a `.nix` file).
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

Full contract (the prettier-safe backtick-wrapping convention) moved to the
`.claude/rules/markdown-conventions.md` path-rule (tc-ql0o Stage D, 2026-08-26) — it rides in only
while editing a `*.md` file.

---

## Pre-commit Hook Installation

When you modify the pre-commit hook configuration in `flake.nix` (the `pre-commit` block), you must re-install the hooks so the generated `.pre-commit-config.yaml` is updated:

```bash
nix run .#install-pre-commit-hooks
```

Run this before committing to ensure the new/changed hooks are active.
