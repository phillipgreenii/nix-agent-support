---
name: claude-status-line
description: The Claude Code status line's part-script contract, ordering convention, and env-var/glyph/width mechanics.
paths: ["home/programs/claude-status-line/**"]
---

# Status Line (`home/programs/claude-status-line`)

Moved out of the repo's always-on `CLAUDE.md` (tc-ql0o Stage D, 2026-08-26): this detail only
matters while editing the status-line module itself.

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
