# CETA keeps abstaining on heredoc file writes outside the write allowlist

**Status**: Accepted (follows up `pg2-4godq`; resolves `pg2-v5gkzm`)
**Date**: 2026-10-27
**Deciders**: the drain worker on `pg2-v5gkzm`, applying the bead's instruction to bias
conservative and change nothing the evidence does not clearly justify. The operator MAY overrule
any verdict below by editing the machine's `extraReadWriteRoots` (no code change is needed).

## Context

`pg2-4godq` made `cat > FILE <<'EOF'` and `cat >> FILE <<'EOF'` approve when `FILE` is
write-allowed (`HeredocFileWriteCleared` in `internal/cmdparse`, the fourth narrowing of
`heredocFloor` in `internal/engine`). It deliberately left two things unchanged, and this ADR
records the verdict on each: the write targets that are NOT write-allowed, and a plain
`cat <<'EOF'` that writes no file.

**Where the write allowlist lives.** It is NOT in this package's code or in
`~/.config/claude-extended-tool-approver/rules.json`. `patheval.PathEvaluator.classify` has a fixed
zone ladder (project root, `WORKSPACE_ROOT`, `/tmp`, sandbox `allowWrite`, the tool's own data
directory) and then falls through to the roots in `CETA_EXTRA_READWRITE_ROOTS`, which the nix
module `phillipgreenii.programs.claude-extended-tool-approver.extraReadWriteRoots` bakes into the
installed wrapper (`home/programs/claude-extended-tool-approver/default.nix`). A root is a LITERAL
directory prefix, not a glob. So a verdict "allow" is a per-machine config change (for this
operator: the machine flake that consumes this repo), and "keep
abstain" is the absence of one. Every extra read-write root widens EVERY write-capable check (`rm`,
`mv`, `cp`, `tee`, redirections, the Edit and Write tools), not just heredocs.

**A replay trap.** `claude-extended-tool-approver evaluate` run from an unwrapped binary (a
`go build` or `go run`) has no `CETA_EXTRA_*` environment, so it replays every row against only
the built-in zones. `pg2-4godq`'s "49 rows outside the write allowlist" counted
`/Volumes/gitrepos/claude_code_tmp/...` among them for that reason. A replay that judges the live
policy MUST source the wrapper's `CETA_EXTRA_READWRITE_ROOTS`, `CETA_EXTRA_READONLY_ROOTS`,
`CETA_EXTRA_TEMP_ROOTS` and `CETA_DENIED_ROOTS` exports first (they are the `export` lines of
`$(command -v claude-extended-tool-approver)`).

**Live replay (2026-10-27)**, HEAD of this branch (`cad1cb4f2`), wrapper environment sourced:
`evaluate --format json` over the whole ask log (934309 rows), restricted to Bash rows whose
summary starts `cat >`, `cat >>` or `cat <<`, excluding `stale-cwd` and `unresolved` rows: 775
rows, of which 366 replay `allow`, 401 `abstain`, 5 `ask` and 3 `deny`. Of the 401 abstains,
116 are a heredoc write to one of the four families below, 10 are a stdout-only `cat <<` that the
heredoc floor holds, and the rest are compound commands whose OTHER leaf abstains (for example a
`cat > /tmp/x <<'EOF' && ~/gc/.../notify.sh`) or dynamically-expanded redirection targets (`$D/x`,
`$F`), none of which this decision touches.

| Family                                  | Rows | First / last row    | Replay verdict (live env)     |
| --------------------------------------- | ---- | ------------------- | ----------------------------- |
| `~/.cache/<task>/...`                   | 68   | 2026-10-22 to 10-27 | abstain, non-writable path    |
| `~/<notes-dir>/...` (home notes tree)   | 29   | 2026-09-30 to 10-01 | abstain, non-writable path    |
| `~/<project>-*/...` (dated report dirs) | 19   | 2026-10-23 to 10-26 | abstain, non-writable path    |
| `/Volumes/gitrepos/claude_code_tmp/...` | 8    | not applicable      | allow (already write-allowed) |

## Decision

1. **`~/.cache`: KEEP ABSTAIN.** It is not a scratch directory; it is the XDG cache root, and the
   machine's `~/.cache` holds `nix`, `pre-commit`, `prek`, `uv`, `pip`, `gh`, `tldr`, `treefmt`,
   `nvim` and `node` next to the 245-odd agent-created task directories. Writing under
   `pre-commit`/`prek` plants hook environments and scripts that run at the next commit; writing
   under `nix` or `uv` poisons a build input. The agent-created subdirectories share no naming
   pattern (`drain-*`, `pg2-*`, `unblock-human-beads`, `q6a96-*`, `p17`, `verify1`, ...), so no
   narrower glob exists, and an allowlist with a deny list of tool caches would fail open on every
   cache directory a new tool creates. The 68 rows are agent staging files (`*.sh`, `*.md` bodies)
   that already have a write-allowed home: `/tmp` and `claude_code_tmp`. If the operator wants
   `~/.cache/<task>/` to stay the convention, the safe shape is ONE dedicated root (for example
   `~/.cache/agent-scratch`) listed in `extraReadWriteRoots`, which is a convention change for the
   skills that name `~/.cache/<task>/`, not a CETA change.
2. **A home-directory notes tree (`~/<notes-dir>`): KEEP ABSTAIN.** The directory does not exist on this machine now; its 29 rows
   are all from a two-day window (2026-09-30 to 10-01) of design-note writes under
   `<topic>/research/`, from a work-repo session. A home-directory notes
   tree is operator work product, not scratch, and a root for a path that no longer exists would
   pre-authorize its recreation as a write target. A repo-rooted tree is already covered by the
   project-root zone.
3. **A glob of dated report directories (`~/<project>-*`): KEEP ABSTAIN.** `extraReadWriteRoots` takes literal
   prefixes, so the glob cannot be written; the only expressible entry is the one dated directory
   `<project>-epic-2026-10-22`, a one-off report workspace (README, writeup,
   `review-*` folders; not a git repo) whose entry would rot with the epic. It holds deliverables the
   operator reads, so a write into it is a decision the operator should see. The 19 rows were
   written in a work-repo session by an agent that could have used `claude_code_tmp`.
4. **`/Volumes/gitrepos/claude_code_tmp`: ALLOW, and already in effect; no change.** It is
   `CLAUDE_CODE_TMPDIR`, created by `phillipgreenii.programs.claude-code-tmp` on this machine
   (operator ruling D9, 2026-10-20), which appends the root to BOTH `extraTempRoots` and
   `extraReadWriteRoots`; the installed wrapper already exports it. It is a swept scratch root (marker
   file `.claude-code-tmp-root`, mode `0700`, idle-session sweep) that no source tree or tool
   depends on. The 8 rows replay `allow` once the wrapper environment is sourced; they abstain only
   in an unwrapped replay (see "A replay trap"). Nothing to widen.
5. **A plain stdout-only `cat <<'EOF'`: KEEP ABSTAIN.** The effect model does not treat it as
   no-effect: `heredocFloor` holds every heredoc-bearing leaf except its four narrowings, and
   `HeredocFileWriteCleared` condition 6 refuses a leaf that writes no file ON PURPOSE. The shape is
   pinned `NoOpinion` by two existing real-evaluator tests (`"cat with a heredoc is not
approved"` in `internal/engine/engine_integration_test.go` and `"plain cat heredoc with no file
write"` in `internal/engine/heredoc_filewrite_integration_test.go`); approving it would invert
   them. The replay finds 6 such rows in the whole log (ids 162682, 162694, 163399, 841869,
   841873, 872194, plus `939944` which pipes into `sed`), all the agent printing its own prose.
   Approving it would buy no usable throughput and would move a deliberate floor.

No code, config or test changes accompany this ADR, so the replay is unchanged by construction:
the same `evaluate` command, run before and after, reports the same 775/366/401/5/3 split.

## Consequences

- The 116 family rows and the 10 plain-heredoc rows stay `miss-uncaught` in replays. They are
  correct as abstain; `identify-hook-misses` SHOULD NOT file them again.
- Replays that judge live policy MUST run with the wrapper environment (see "A replay trap");
  without it `claude_code_tmp`, `/Users/phillipg/worktrees`, `/Users/phillipg/Code` and the work
  volume all read as non-writable.
- Reopening a family needs a NEW operator ruling plus a literal root in the machine's
  `extraReadWriteRoots`; a code change MUST NOT widen it.
- Follow-up observation, not decided here: `$TMPDIR` on this machine
  (`/var/folders/.../T/`) is not on the fixed ladder (only `/tmp` is) and is in no extra root, so
  heredoc writes to literal paths under it also abstain (2 rows). `$TMPDIR/...` with an unresolved
  variable abstains regardless.
