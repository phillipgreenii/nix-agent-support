# Next-session prompt — ccpool/pr-pool session-metadata follow-ups (after 2026-06-24)

Paste the block below as the kickoff prompt for the next session.

---

Continue the ccpool/pr-pool session-metadata follow-ups. The 2026-06-24 session
finished the actionable beads; what remains is MANUAL / CONDITIONAL / DEFERRED. Run
`bd prime` first, then `bd show <id>`.

## DONE (this prior session — all merged to LOCAL main, NOTHING pushed to origin)

- **pg2-5o5i** (CLOSED) — pr-pool consumes ccpool `sessionmeta`: writes
  `prpool.bead`/`prpool.role`/`prpool.pool` atomically at dispatch via
  `ccpool new --meta`; new read-only `pr-pool sessions` reads them back via
  `ListByMeta`+`Meta` from the pool `CCPOOL_POOL` resolves.
- **pg2-87ly** (CLOSED) — discovered blocker: `ccpool new --meta KEY=VAL` +
  `EnsureOpts.Meta`, atomic on all Ensure paths; reuse⇒new clears prior metadata via
  the existing prune-cascade.
- **pg2-ju3r** (CLOSED) — `pr-pool config --show` prints the worker dispatch scalars
  (permission-mode, allowed-tools VERBATIM, autonomous, confirm-ingest, budget) for
  read-only audit; fixed a stale README default.
- **pg2-2yn2** (CLOSED) — decision PRESERVE: `run-role` no longer purges a
  `needs_input` session (shared `closeUnlessNeedsInput` helper with `teardownAll`).

Design spec + per-bead plans are on main: `docs/superpowers/specs/2026-06-24-session-metadata-at-dispatch-design.md`
and `docs/superpowers/plans/2026-06-24-ccpool-session-meta-at-dispatch.md` /
`...-pr-pool-consume-sessionmeta.md`.

## REMAINING (suggested order)

1. **pg2-4ib8** (P3, MANUAL) — live-verify deployed features needing a running stack.
   Do ONLY when the observability stack + a live attended Claude session are available.
   Four items (see the bead + its 2026-06-24 comment): (1) Loki query for a ccpool
   `level:error` line; (2) `ccpool state <id>` reflects the claude-transcript registry
   verdict for a REAL attended session; (3) `$XDG_CONFIG_HOME/pr-pool/config.toml`
   budget takes effect end-to-end in a real drain; (4) NEW — with the DEPLOYED
   nix-wrapped binaries, dispatch a bead via pr-pool (real `ccpool new --meta`), then
   `pr-pool sessions` lists it with the right bead/role from the shared pool (the only
   part of the metadata feature not covered headlessly). Does NOT need a real Claude —
   metadata is Claude-agnostic. Also eyeball `pr-pool config --show` prints the
   deployed allowlist verbatim.
2. **pg2-ovu4** (P3, CONDITIONAL) — `sessionmeta` SQLITE_BUSY retry/backoff. SKIP
   unless SQLITE_BUSY is actually observed in practice (ccpool holds only short txns;
   not seen as of 2026-06-24).
3. **pg2-44k9** (P3, DEFERRED) — the ccpool "slot metadata" tier (tmux/slot-scoped,
   distinct from the Claude-session-scoped "session metadata" already shipped). Build
   ONLY when an actual consumer reuses a slot across Claude sessions and needs
   slot-level metadata. pr-pool does not (unique external_id per attempt).

## GOTCHA (relevant to pg2-4ib8)

The deployed pr-pool/ccpool are nix wrappers that re-prepend the real store paths to
PATH, so a fake-ccpool-on-PATH can't intercept. To observe pr-pool's real dispatch
argv (incl. `--meta`), invoke `.pr-pool-wrapped` directly with your PATH.

## WORKFLOW (the pattern that worked)

- LOCAL-main workflow: local `main` is ~150 commits ahead of origin; do NOT push to
  origin unless asked. Other sessions advance `main` concurrently.
- Per bead: isolated worktree off LOCAL main — `git worktree add <path> -b <branch> main`
  (NOT EnterWorktree's stale origin/main default). TDD; gate with `go test ./...`, then
  `prek run --all-files` AND `nix flake check` (both required before "done").
- Integrate: rebase onto main, then FF-merge — `git push . <branch>:main` (FF-only;
  retry on race). Then close the bead, remove the worktree, delete the branch.
- `bd` resolves its DB from the git-repo root's `.beads`; run it from inside
  agent-support (or a worktree of it). `bd` works from worktrees too.
