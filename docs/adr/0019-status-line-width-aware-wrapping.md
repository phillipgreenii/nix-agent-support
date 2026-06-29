# Status Line Width-Aware Wrapping

**Status**: Accepted
**Date**: 2026-06-29
**Deciders**: phillipg

## Context

The Claude Code status line is assembled by `home/programs/claude-status-line` from an
ordered list of "part" scripts (`phillipgreenii.programs.claude.status-line-parts`, a
`listOf str`). The wrapper ran every part, kept the non-empty outputs, and joined them
with `" | "` onto a **single line**.

On narrow terminals (or when many parts are registered — the work machine already has 8:
host/container, session, worktree, branch, model+ctx, version, AWS profile, workspace),
the single line is wider than the window and the rightmost segments are clipped, so the
information the user most wants can be the part that gets cut off. This will worsen as
more parts are added.

Claude Code exposes the terminal width to the status-line command as the `COLUMNS`
environment variable (v2.1.153+; the width is NOT in the stdin JSON). It also renders its
own right-aligned **system notifications** (MCP errors, auto-update notices) on the status
line, but the official docs do not quantify how much width those reserve, do not say which
row carries them on a multi-line status line, and provide no env var / stdin field to
detect their presence or width. So the notification margin cannot be measured at runtime —
only reserved conservatively.

## Decision

The wrapper wraps segments across multiple rows based on `COLUMNS`, in part order.

- `budget = COLUMNS - reserve`, where `reserve` is a new int option
  `phillipgreenii.programs.claude.status-line-notification-reserve` (default `20`),
  overridable per render via the `CLAUDE_SL_RESERVE` env var (a test / power-user seam).
- The reserve is applied **uniformly to every row**, not just one, because which row
  carries Claude Code's notification is undocumented; a uniform margin is collision-proof
  regardless of where the notification lands.
- Segments are packed greedily in list order: a segment starts a new row only when
  appending it to a **non-empty** row would push that row's visible width past `budget`.
  The first segment of any row is placed unconditionally.
- **Oversized-segment invariant**: because the first segment of a row is unconditional, a
  single segment wider than `budget` is always emitted whole on its own row — never split
  across rows, never dropped. The terminal handles the visual overflow.
- Wrapping is disabled (single row, legacy behavior) when `COLUMNS` is unset, `0`, or
  non-numeric — covers timer refreshes and non-interactive invocations.
- Visible width is measured by stripping ANSI SGR escapes in **pure bash** (no subprocess)
  before counting characters; the colored original is emitted unchanged.
- The final print changed from `printf "%b\n"` to `printf '%s\n'`. Each part already emits
  real ESC bytes (its own `printf` expands `\033` in the format-string position), so `%b`
  re-interpreted escapes needlessly and would mangle a literal backslash in, e.g., a branch
  or session name. `%s` is byte-identical for today's data and strictly safer.

`mkWrapperScript` now takes an attrset `{ parts, reserve ? 20 }` (was a bare `parts`
positional). Both call sites — `default.nix` and the `flake.nix` `test-claude-status-line`
check — pass the attrset form.

## Consequences

### Positive

- No segment is silently clipped on a narrow terminal; content reflows onto more rows in a
  stable, order-preserving way.
- Headroom to add more parts (e.g. PR/repo, mode flags) without losing visibility.
- `reserve` is tunable per machine; `CLAUDE_SL_RESERVE` gives a no-rebuild override and a
  deterministic test seam.

### Negative

- The uniform reserve wastes a few columns on rows that do not actually carry a
  notification — unavoidable given the notification row is undocumented.
- The reserve is a guess, not a measurement; a very long notification could still collide.

### Neutral

- `strip_ansi` swallows a malformed trailing `ESC[` with no terminating `m` (the dangling
  sequence and anything after it is dropped). Not reachable from current parts, which
  always emit complete SGR sequences.
- Wrapping requires Claude Code v2.1.153+ for `COLUMNS`; older versions simply never wrap
  (budget 0), which is the prior behavior.

## Alternatives Considered

### Detect the notification width at runtime

Rejected — Claude Code exposes no signal (env var or stdin field) for notification
presence or width. A fixed conservative reserve is the only option.

### Reserve only on the last (likely notification) row

Rejected — the notification row is not documented as the last row; reserving only there
risks a collision if the assumption is wrong. Uniform reserve trades a little width for
correctness.

### Strip ANSI with `sed`/`awk`

Rejected — adds a subprocess per segment per render (the status line runs on every message
plus any `refreshInterval`) and reintroduces GNU-vs-BSD `sed` portability concerns. The
pure-bash strip is robust and allocation-free.

## Related Decisions

- Cross-module contribution to `status-line-parts` uses the `listOf` merging pattern.
  See also: phillipgreenii-nix-personal docs/adr/0034-listof-merging-pattern-for-cross-module-options.md
- Status-line colors are themed separately via `status-line-colors` (the `claude-theme`
  module injects Stylix truecolor).

### Out of scope (tracked as follow-up beads)

- Adding new JSON-derived parts (PR/repo, mode flags), and consolidating the wrapper's
  per-field `jq` calls into one.
- Making cross-module part ordering explicit (today: one base assignment + a ZR `mkAfter`).
- The `branch` part only shows inside a git worktree (`worktree.branch`); a normal checkout
  shows no branch.
