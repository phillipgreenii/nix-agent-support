# pb — phillip-beads: `pn:applied` gates + drain-loop helpers

`pb` writes and resolves **`pn:applied` gates**: beads (issues) that MUST NOT become
workable until the change they depend on has been _applied_ by a `pn workspace apply`.
It is the Phase-2 producer/consumer of the `pn:applied` contract (design spec:
`docs/superpowers/specs/2026-06-25-pn-applied-gates-design.md`; contract: ADR
`docs/adr/0018-pb-tool-and-pn-applied-contract.md`). It also carries `pb drain`, a small
family of helpers for the `/drain-beads` work loop — starting with `drain isolate`,
which sets up a bead's isolated worktree.

A gate is keyed to a change's **`git patch-id`** (not its commit SHA) so it survives the
local rebases this workflow performs — the SHA changes on rebase, the diff (and thus the
patch-id) does not.

## Architecture

A standalone Go [cobra](https://github.com/spf13/cobra) binary that shells out to three
tools on `PATH`:

- **`bd`** (beads) — gate create/list/resolve, metadata, labels. Wrapped onto `PATH`.
- **`git`** — `patch-id`, `log -p`, `merge-base`. Wrapped onto `PATH`.
- **`pn`** — `pn workspace info --json` (the consumed applied-state API). **NOT wrapped**;
  `pn` is an _ambient_ runtime `PATH` dependency (the apply post-hook env and dev shells
  already provide it). agent-support is standalone and cannot reference repo-base's `pn`.

All external execution is behind a `run.Runner` interface, so the logic is unit-tested
with a `FakeRunner` (no real binaries). Real-binary behaviour is pinned by build-tagged
contract tests (`//go:build contract`).

## `pb gate create`

Attaches one or more `pn:applied` gate(s) blocking an existing bead until a change is
applied.

```
pb gate create --blocks <beadid> --repo <repo> [--commit <commit-ish>] [--commits <range>] [--reason <r>] [--json]
```

- `--commit` defaults to `HEAD`. `--commits <range>` creates **one gate per commit** in the
  range (all block the same bead; the bead surfaces only once **all** gates resolve, since
  beads AND their blockers).
- The gate's `await_id` is `<wsid>:<repo>:<patch-id>` and its
  `metadata.applied_baseline` is the repo's `applied_ref` at create time (may be empty).
- The gate is **co-located in the bead's own beads DB** (a cross-DB `blocks` edge does not
  hold a bead out of `bd ready`).
- `pb gate create` does **NOT** create or un-defer the bead. The fleet-race-safe lifecycle
  is the caller's (taught by the Phase-3 plugin):

```mermaid
sequenceDiagram
    participant Caller
    participant bd
    participant pb
    Caller->>bd: bd create "verify ..." --defer 2126-01-01
    Note over bd: bead hidden from `bd ready` -- verify by READINESS, since `status` stays `open`
    Caller->>pb: pb gate create --blocks <bead> --repo <r>
    pb->>bd: bd gate create (pn:applied) + set baseline
    Caller->>bd: bd update <bead> --defer ""  (un-defer)
    Note over bd: still blocked — the gate holds it
```

## `pb gate check`

Run **inside a `pn` workspace** (e.g. as the apply post-hook). Discovers every distinct
beads DB reachable from the workspace, lists open `pn:applied` gates, and resolves each
one for which BOTH of the following hold (ADR 0046).

```
pb gate check [--dry-run] [--strict] [--last-n N] [--stale-handler convert-to-human|close] [--stale-after 3d] [--json]
```

- **Discovery + dedupe:** walks up each repo (and the root) for `.beads`, bounded at the
  workspace root, and dedupes by Dolt identity (`host:port|database|project_id`).
- **Condition 1 — an apply happened:** the gated patch-id appears in the scan range.
  When a gate's `applied_baseline` is an ancestor of the repo's `applied_ref`, scans
  `baseline..applied_ref`; otherwise scans the last `--last-n` commits (default 100).
- **Condition 2 — that apply's lock contained the commit:** for a repo the apply resolved
  **through the terminal's `flake.lock`**, the gated commit must be an ancestor of
  `locked_rev` — the rev that lock pinned **at that apply**, published by
  `pn workspace info` (`phillipg-nix-repo-base` ADR 0025). Condition 1 alone only proves an
  apply ran over a checkout holding the change; a commit never pushed and relocked is not
  in such a build. So such a gate needs **push + relock + apply**, and until then it is
  reported in `blocked` with the remedy.

  It is **SKIPPED** for a repo the apply **OVERRODE** (`overridden` true, requires
  `applied_state_schema >= 3`): `pn workspace apply` passes
  `--override-input <alias> git+file://<clone>` for every terminal lock edge whose clone is
  present, so nix built that repo from the LOCAL CLONE at eval-time HEAD and never consulted
  the lock — condition 1 is the whole truth for it. Also skipped for the terminal repo
  (built from its local directory, so no `locked_rev`) and for a record written by a `pn`
  predating `locked_revs` (`applied_state_schema < 2`). A record from a `pn` that predates
  the override set (`applied_state_schema == 2`) is read as NOT overridden, so condition 2
  is **enforced** — fail-closed, and the `blocked` reason says so. See ADR 0046's amendment
  "condition 2 is CONDITIONAL on whether the apply OVERRODE the repo".

- **Dirty repos:** scanned leniently by default (committed history only); `--strict` skips
  them.
- **`--dry-run`** mutates nothing (reports `would_resolve` / would-be stale actions).
- **Stale handling:** gates older than `--stale-after` (default `3d`; units `ms`..`d`,
  rejects `<1ms`) that still cannot be resolved get the `--stale-handler` action:
  `convert-to-human` (adds the `human` label → surfaces in `bd human list`) or `close`
  (resolves the gate, unblocking the bead).
- **`blocked` vs `skipped`:** `blocked` gates were DETERMINED to be correctly still
  closed (condition 2 said no) and do NOT affect the exit code — otherwise the apply
  post-hook would exit non-zero, and `pn` warn, on every normal pending gate. `skipped`
  gates are UNDETERMINABLE (unknown repo, scan failure, dirty under `--strict`, an apply
  that recorded no locked rev for an input it consumes).
- **Best-effort:** undeterminable gates are skipped and reported; the command exits
  non-zero if anything was skipped.

## `pb gate attach-verified-child`

Runs the whole deferred-first post-deploy gate sequence for a landed implementation
bead in one call, rather than leaving the caller to script the `bd create --defer` /
`pb gate create` / `bd update --defer ""` steps (and their ordering) by hand: creates
the verification child bead **deferred**, proves it is absent from `bd ready`, attaches
one `pn:applied` gate per `--gate <repo-key>=<sha>`, un-defers the child, re-proves
absence (now held by the gates, not the defer), and comments the child's id back onto
`--impl`. The ordering is load-bearing — the child is never simultaneously workable
and ungated, closing the fleet-claim race where a peer agent claims the child and
"verifies" code that was never applied.

```
pb gate attach-verified-child --impl <beadid> --title <t> --gate <repo>=<sha> [--gate <repo>=<sha> ...] --actor <a> [--reason <r>] [--json]
```

- `--impl`, `--title`, `--gate` (repeatable — one per changed repo) and `--actor` are
  required. `--reason` defaults to `post-deploy verify for <impl>`.
- Human output: `child=<id> gates=<n>`. `--json` emits the `AttachResult` envelope
  (`child`, `gates`, `comment_failed`) instead.

| Exit | Meaning                                                                                                                  |
| ---- | ------------------------------------------------------------------------------------------------------------------------ |
| `0`  | Fully gated: child created, gated, un-deferred, and proven absent from `bd ready`.                                       |
| `1`  | Generic failure (e.g. bad flags, `pn`/`bd` unreachable) — nothing to clean up.                                           |
| `3`  | Gating incomplete; the child was **left deferred** — safe, no peer can claim it. Route the impl bead to STUCK and retry. |
| `4`  | The child could **not be proven un-workable** — do **NOT** close the impl bead until this is resolved by hand.           |

```bash
pb gate attach-verified-child \
  --impl pg2-huyhg \
  --title "verify tldr wsplan renders after apply (pg2-huyhg): run tldr wsplan, compare against a known-good sibling page" \
  --gate phillipg-nix-repo-base=9167a60 \
  --actor "$CLAUDE_SESSION_ID-drain"
```

## `pb drain isolate`

Idempotent isolation for one bead in the `/drain-beads` work loop: creates or reuses
`.worktrees/<bead>` on branch `drain/<bead>` (branching off the repo's primary branch when
neither the worktree nor the branch already exists), then links the canonical clone's
gitignored, nix-generated `.pre-commit-config.yaml` into the worktree so commits there run
the hooks (`phillipg-nix-repo-base` ADR 0016). Safe to re-run: an existing worktree or parked
branch is reused rather than recreated, and an already-linked pre-commit config is left alone.

```
pb drain isolate --bead <id> --repo <abs-path> [--json]
```

- `--bead` and `--repo` are required. `--repo` MUST be an absolute path to the canonical
  clone — orchestrators are expected to pass an observed absolute root, not a relative path
  or (despite the name overlap) `pb gate create --repo`'s workspace repo _key_; an `IsAbs`
  check rejects a key passed by mistake.
- `--bead` is validated against `^[A-Za-z0-9._-]+$` (letters, digits, dot, dash, underscore)
  since the id lands in both a filesystem path and a branch ref; bare `.`/`..` are also
  rejected. Dots are otherwise legal — live ids such as `pg2-4dz88.2.3` exist.
- Human output is one line:
  `worktree=<abs> branch=drain/<id> reused=<none|worktree|branch> precommit=<linked|present|none>`.
  `--json` emits the same fields as a JSON object instead.

| Exit | Meaning                                                                                                                                                  |
| ---- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `0`  | Isolated (worktree created, or an existing worktree/branch reused).                                                                                      |
| `1`  | Generic failure (bad flags, git unreachable, etc).                                                                                                       |
| `3`  | Conflicting isolation state — the worktree path holds another branch, or `drain/<bead>` is checked out elsewhere. Never forced; route the bead to STUCK. |

```bash
pb drain isolate --bead pg2-1qcro.7 --repo /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
```

## Versioning

Per-source content digest (agent-support "Versioning"): `mkGoApp` stamps `main.Version`.
Refresh third-party deps with `go mod tidy && nix run github:nix-community/gomod2nix -- generate`.

## Tests

```bash
cd packages/pb
go test ./...                       # unit tests (FakeRunner; real-git tests need git on PATH)
go test -tags contract -p 1 ./...   # contract tests (real bd/git/pn; skip when absent)
```
