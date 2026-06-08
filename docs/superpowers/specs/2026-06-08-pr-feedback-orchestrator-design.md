# PR-feedback orchestrator — design

**Status**: Draft (revised 2026-06-08 after building the prerequisites)
**Date**: 2026-06-08
**Deciders**: Phillip Green II

## What changed on 2026-06-08 (read this first)

This spec was first drafted assuming "extraction is done, only the consumer is
missing." A review + live investigation found that was only half true, and the
gaps were fixed the same day. Net effect on this design: the **clean
bead-only worker contract now works as written** — no live-`gh` workaround.

- **Feedback beads now exist.** `pg-pr sync` was already extracting feedback
  (CI failures, comment threads, review threads) and trying to create
  `--type=feedback` beads every cycle, but **every write failed** with
  `invalid issue type: feedback` because the `feedback` type was never
  registered. Registered it in the `zr` db → the queue went **0 → 99 feedback
  beads**; they attach reliably as parent-child children of the
  `process-feedback:` cycles (verified: 270/270 parented + `hooked`). The one
  real producer wrinkle is **duplicate cycles per PR** — see Producer-side
  state.
- **The source is the standalone daemon, not `pr-watcher`.** Two syncers
  existed: the `org.nixos.pg-pr-sync` launchd daemon (`pg-pr sync --daemon`,
  5m, → `zr` db, now `BEADS_ACTOR="pg-pr daemon"`) and the gas-city
  `pr-watcher` order (→ HQ `gc` db, the "twin beads"). **`pr-watcher` was
  removed** (city.toml override + deleted from the `pgii-pr-support` pack), so
  the standalone daemon is the single source feeding the `zr` queue.
- **The dolt bead server is gas-city-managed.** The `zr` db is a database
  inside the one `dolt sql-server` on `:24158` that gas-city tooling owns
  (`~/gc/.beads/dolt`, `dolt.auto-start: false`). The orchestrator depends on
  that server being up; it never administers it.
- **Observability now exists for the producer.** The daemon serves Prometheus
  metrics on `:9818`; a `pg-pr / Ops` Grafana dashboard (sync errors, feedback
  created, last-sync age, durations) was added. The silent `253` feedback
  failures are exactly what that dashboard now surfaces.

## Problem

The `pg-pr` sync daemon extracts PR state from GitHub into beads, producing
`process-feedback:` cycle beads with `feedback` children in the `zr` database.
Gas City was supposed to consume those cycles and drive agents to work them,
but fundamental problems with Gas City mean that never happens — the queue
fills and nothing drains it (live fixtures: `zr-l38`
`process-feedback: ZR-Private/ziprecruiter#92600`, parent merge-request
`zr-ma1` with `metadata.author = phillipgziprecruiter` — i.e. **my** PR).

We want to pull the _consumption_ flow out of Gas City. The extraction
(`pg-pr` daemon), the bead store (dolt), the per-cycle processing knowledge
(`pg-pr-process-feedback` skill), and the tmux send-keys primitive
(`pa-monitor`) are **already decoupled** from Gas City. The only piece Gas
City uniquely provided is the **supervisor / materialization loop**: read a
`work_query`, and when work exists and a slot is free, launch an interactive
`claude` in a tmux pane, send-keys a `nudge`, enforce concurrency, drain idle
panes.

This spec covers **step 1** of replacing that supervisor with a standalone,
incrementally-growable subsystem, targeting the ziprecruiter monorepo (`zr`
db) and the user's own PRs.

## End-state vision (context, not step-1 scope)

A "minimal Gas City": an on-demand-then-daemonized agent pool that handles any
bead type, manages git worktrees, and escalates when stuck — so that once a PR
is opened, the daemons monitor it, process feedback, self-fix, and carry it to
merge without human attention unless an escalation fires.

The path there is incremental, proving one capability at a time:

1. **Step 1 (this spec)** — on-demand orchestrator that dispatches a tmux
   `claude` agent to _review_ the feedback on my `process-feedback:` cycles and
   emit action beads. No self-fix, no daemon, no escalation.
2. **Step 2** — self-fix: a worker that consumes the action beads (worktree,
   commit, push).
3. **Later** — daemonize (launchd), add escalation, generalize to a multi-role
   pool over arbitrary bead types + worktrees.

Step 1 is deliberately small but built so each later step is an additive
change, not a rewrite.

## Hard constraint: Gas City is untouched

This subsystem must not modify Gas City in any way. Non-negotiable:

- **No writes** to `~/gc` — not its config, packs, orders, formulas, agents,
  `.gc/` runtime, `settings.json`, sentinels, or cache.
- **No `gc` commands.** Neither the orchestrator nor the dispatched worker
  invokes `gc …` (no `gc sling`, `gc mail`, `gc runtime`, `gc session`, etc.).
- **Its own tmux socket** (`-L pgpool`), never Gas City's `-L gc`.
- **Its own logs/state** outside `~/gc` (see Error handling).

What it _does_ touch is the **`zr` rig's bead data** in the monorepo's `.beads`
(`/Volumes/ziprecruiter/monorepo/.beads`, prefix `zr`): it reads cycle +
feedback beads and writes new action beads / closes feedback + cycles via
`bd` + `pg-pr`. That is the intended, already-decoupled data flow. The dolt
store is shared state on a **gas-city-managed server** — the orchestrator
reads/writes its bead data but never starts, stops, or reconfigures the
server, and never touches the HQ `gc` database.

## Architecture

Three layers; only the orchestrator is new.

```
Bead store: zr database, a db inside the gas-city-managed dolt sql-server :24158
   (data ~/gc/.beads/dolt; reached via /Volumes/ziprecruiter/monorepo/.beads,
   prefix zr). Shared state, NOT the Gas City supervisor. (HQ gc db untouched.)
        ▲                              ▲
        │ pg-pr sync --daemon          │ bd / pg-pr  (run from monorepo root)
   org.nixos.pg-pr-sync (launchd, 5m)  NEW: orchestrator (bash, monorepo root)
   BEADS_ACTOR="pg-pr daemon"          discover MY cycles → dispatch claude in
   creates merge-request + cycle +     tmux → send-keys nudge → wait → loop
   feedback(ci/comment/review) beads             │ tmux -L pgpool new-session + send-keys
                                       worker: interactive `claude` in a pane
                                       reads feedback beads → emits action
                                       beads → closes cycle → exits
```

What we delete from the loop: only Gas City's supervisor role. The dolt server
and the `pg-pr` sync daemon stay.

## The orchestrator (step-1 bash script)

A single bash script, structured into small testable functions (realistically
~200 lines once readiness/completion timeouts, dedup, and unclaim-on-failure
are included). Invoked manually from the **ziprecruiter monorepo root**
(`/Volumes/ziprecruiter/monorepo`); it does **not** `cd` to `~/gc` and does
**not** inherit Gas City env.

### Preconditions (fail loud)

Before any work the script asserts:

1. `$REPO_ROOT/.beads` exists and its prefix is `zr` (else it could touch the
   wrong db).
2. The dolt server is reachable (a `bd list … --limit 1` succeeds). The server
   is gas-city-managed and `dolt.auto-start: false`, so the orchestrator cannot
   start it — it must fail with a clear message rather than hang.

### Bead-store connection (run-from-monorepo-root, taint-free)

The orchestrator runs from the monorepo root, where the monorepo's own
`.beads/config.yaml` (prefix `zr`) makes `bd` resolve to the **zr database**.
No `~/gc` pinning. The only thing to clean is the ambient `~/phillipg_mbp/.envrc`
taint (`BEADS_DIR=$PWD/.beads`, `WORKSPACE_ROOT=$PWD`), which would otherwise
hijack `bd` to the wrong store. So every `bd` / `pg-pr` call runs with those
two scrubbed:

```bash
REPO_ROOT="${REPO_ROOT:-$PWD}"                       # the monorepo root
bd()    { env -u BEADS_DIR -u WORKSPACE_ROOT command bd "$@"; }
pg-pr() { env -u BEADS_DIR -u WORKSPACE_ROOT command pg-pr "$@"; }
```

Verified live: `env -u BEADS_DIR -u WORKSPACE_ROOT bash -c 'cd
/Volumes/ziprecruiter/monorepo && bd list …'` resolves to `zr`.

### Functions

- **`discover_cycles`** — the work_query. List open `process-feedback:` task
  beads, then keep only those whose **parent merge-request's
  `metadata.author == $SELF_LOGIN`** (`phillipgziprecruiter`, from
  `pg-pr config show .self_login`). This is the "my PRs" filter — the `role`
  field is unpopulated on every merge-request, so author is the reliable
  signal. Includes `zr-l38` (#92600, mine); excludes e.g. `zr-12q` (#92952,
  KenMGJ's). **Dedup by PR** (`parent.metadata.repo` + `pr_number`): if two
  open cycles point at the same PR, prefer the cycle that has feedback children
  (else the newest) and flag the rest — duplicate cycles have been observed
  (PR #92804 → `zr-99i` with 11 children + `zr-vfw` with 0).
- **`dispatch <cycle-id>`** — create a detached tmux session running an
  interactive `claude`. Modeled on (not "mirroring") Gas City's invocation,
  with deliberate divergences noted:

  ```bash
  env -u BEADS_DIR -u WORKSPACE_ROOT \
    tmux -u -L "$SOCKET" new-session -d -s "pf-<cycle-id>" -c "$REPO_ROOT" \
      -e BEADS_ACTOR=pgii-pool__process-feedback \
      claude --dangerously-skip-permissions --effort max --session-id "$(uuidgen)"
  ```

  - `tmux -u` forces UTF-8 so `capture-pane` renders the `❯` (U+276F) ready
    prompt — without it `wait_ready` can miss the prompt.
  - **Divergence from the deacon command, on purpose:** a fresh
    `--session-id "$(uuidgen)"` (not `--resume`), and **no `--settings`** — the
    worker runs with the user's default `~/.claude` config, standalone, not Gas
    City's `~/gc/.gc/settings.json`.
  - `--dangerously-skip-permissions` so the worker never hangs on a permission
    prompt the orchestrator can't answer.
  - `-c "$REPO_ROOT"` + the `env -u` scrub so the **worker's** own `bd`/`pg-pr`
    resolve to `zr` (the pane inherits the tmux server's env).
  - Dedicated socket `-L pgpool`, never Gas City's `-L gc`.

- **`wait_ready <pane>`** then **`send_nudge <pane> <cycle-id>`** — poll
  `tmux capture-pane -p -t <pane>` until the ready-prompt prefix `❯ ` appears
  (how Gas City signals readiness via `GC_READY_PROMPT_PREFIX`), then
  `tmux send-keys`. `wait_ready` has **its own timeout** (mirror pa-monitor's
  5s-bounded tmux calls) — if `claude` never reaches a prompt (auth/launch
  failure, immediate exit) it falls into the flag path rather than spinning.
- **`wait_done <cycle-id> <pane>`** — see Completion below.
- **`main`** — concurrency cap `MAX=1` (a variable), iterate `discover_cycles`,
  dispatch/nudge/wait, loop until the query returns empty, then exit.

### The nudge / worker contract

The dispatched `claude` is directed (via send-keys) to:

1. Read the processing instructions from the **pinned absolute path** of
   `pg-pr-process-feedback`'s `SKILL.md` — a top-of-script variable, asserted
   `[ -f ]` before dispatch. (Today only the pack-src path exists:
   `…/packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-process-feedback/SKILL.md`;
   no nix-store copy yet. The skill is **not** installed in `~/.claude`. A
   proper plugin install is a later cleanup.) The nudge points **only** at
   `SKILL.md`, not the `pr-self-fixer` prompt, which is Gas-City-coupled
   (`gc mail`, `gc runtime`, `~/gc` sentinels). `SKILL.md`'s step-3 "implement
   the change" is **overridden** by the nudge: in step 1 create an action bead
   instead of applying a fix.
2. Claim the cycle bead (`bd update <id> --claim`).
3. **Read the feedback children** — `bd children <cycle-id>` returns the
   `feedback` beads linked to the cycle (parent-child). Each carries `kind`
   (`ci-failure` | `comment-thread` | `review-thread`), `external_id`,
   `fingerprint`, `author_role`, and the upstream body in its description.
   **Everything needed is in the beads — no `gh`, no live fetch.** (Note: a
   feedback bead carries _no_ `repo`/`pr` of its own — its only PR association
   is the parent-child link, so the worker resolves repo/PR by walking up to
   the cycle's merge-request parent. See Producer-side state.)
4. For feedback needing a **code change**: create an action bead (`task` /
   `bug` per ADR 0009) linked to the feedback via `discovered-from`. **Do not
   apply the change** (step 2, self-fix) and **do not pick up the new action
   bead** this run.
5. For no-code feedback (resolved threads, non-actionable / informational bot
   comments — e.g. CodeRabbit "no actionable comments", Claude review
   "approved"): close the feedback bead with a structured reason.
6. Close the cycle bead with a one-line summary reason.
7. Exit.

### Completion & draining

- **Primary signal**: poll `bd show <cycle-id> --json` until `status` is
  closed.
- **Liveness fallback**: if the pane's `claude` process exits, _or_ `MAX_WAIT`
  elapses with the cycle still open → free the slot and **flag** it (log; leave
  the cycle for the next run / future escalation). **Never auto-close on
  timeout.**
- **Crash-unclaim (correctness):** the worker `--claim`s the cycle, moving it
  to `status=in_progress`. Because `discover_cycles` filters `--status=open`, a
  cycle the worker claimed-then-died on becomes **invisible to the next run**.
  So the flag path **must unclaim** (`bd update <id> --status=open
--assignee=""`) on timeout/exit-without-close, or `discover_cycles` must also
  re-include `in_progress` cycles owned by `pgii-pool__process-feedback`. Step 1
  takes the unclaim approach.

## Producer-side state (investigated live 2026-06-08)

What was checked and what's actually true (an earlier draft over-flagged
"orphaned feedback / intermittent linking / status=hooked rejected" — those
were a **false alarm** from sampling a feedback bead mid-creation; corrected
here):

- **Feedback linking is reliable — not a gap.** All **270** feedback beads are
  parented (`parent_null: 0`) and `status: hooked`; `feedback_created_total`
  (270) equals the DB count (no churn). The transient `parent: null` /
  `status: open` seen on `zr-cde4` was a read **inside `CreateFeedback`'s
  create→`dep add` window** (it now reads `parent: zr-wl9, hooked`). The worker
  can rely on `bd children <cycle-id>`.
- **Duplicate processing-cycles per PR — the one real producer bug.** 48 open
  `process-feedback:` cycles for **27** PRs (~2 each; e.g. #92600 →
  `zr-t0k` + `zr-l38`, both correctly linked to the same single MR `zr-ma1` —
  MRs are _not_ duplicated). `isChildOf` resolves the parent correctly today
  (`bd dep list <cycle>` shows the parent-child link), so this is **not**
  actively reproducing via mis-detection; the twins date to 2026-06-05 and are
  most plausibly historical accumulation from the non-atomic create→link window
  / cross-restart races (the in-process dedup cache prevents same-run dupes).
  Feedback attaches to whichever twin sync resolves, so one twin holds the
  children and the other is empty. **Consumer tolerance:** `discover_cycles`
  dedups by PR (`repo`+`pr_number`) and prefers the twin that has feedback
  children (else newest); the rest are flagged, not silently worked.
- **Observability gap:** the daemon logs failed sync iterations as
  `{"level":"ERROR","msg":"sync iteration failed","error":null}` — the error
  detail is dropped (it only reaches the per-repo state file, which is
  overwritten each sync). Diagnosing producer failures is therefore hard. Worth
  a producer fix (log the actual error); the new `pg-pr / Ops` dashboard
  surfaces the _rate_ but not the cause.

Producer-hardening status (done 2026-06-08, separate from this consumer):
**fixed** — `FindOpenProcessingCycle` (`pkg/beads/processingcycle.go`) now does
a single MR-children query and **propagates `bd` errors** instead of swallowing
them into a silent "not found", so a transient failure no longer spawns a
duplicate (TDD'd: `TestFindOpenProcessingCycle_FailsSafeOnDepError`). Takes
effect on the daemon after the next rebuild. **Cleaned up** — the 21 empty twin
cycles were closed (`48 → 28`, 0 dupes remain). Still open: log non-null sync
errors (the `error:null` observability gap). None blocked step 1 — the
consumer's PR-dedup tolerates duplicates regardless.

## Why this is daemon-ready

The on-demand script becomes the daemon with additive changes only:

- wrap invocation in a launchd user-agent (the same
  `phillipgreenii.system.launchdServices.userAgents` pattern the
  `org.nixos.pg-pr-sync` daemon already uses);
- drop the exit-on-empty so `main` loops forever with a sleep;
- raise `MAX`;
- add an escalation branch in the flag path;
- emit Prometheus metrics (mirror the daemon's `:9818`) so the orchestrator is
  observable on the same Grafana stack.

`work_query`, `nudge`, `MAX`, `SOCKET`, `SELF_LOGIN`, and the `SKILL.md` path
are top-of-script variables — the seed of the per-role config table a
multi-agent pool needs.

## Components & isolation

| Unit                        | Responsibility                                 | Depends on                  |
| --------------------------- | ---------------------------------------------- | --------------------------- |
| `discover_cycles`           | query → my cycle IDs (author filter, PR-dedup) | `bd`, `pg-pr`, `jq`         |
| `dispatch`                  | create tmux pane running `claude`              | `tmux`, `claude`, `uuidgen` |
| `wait_ready` / `send_nudge` | readiness poll (timeout) + send-keys           | `tmux`                      |
| `wait_done`                 | completion + liveness/timeout flag + unclaim   | `bd`, `tmux`                |
| `main`                      | concurrency + drain loop                       | the above                   |
| worker (`claude` pane)      | per-cycle feedback processing                  | `SKILL.md`, `bd`, `pg-pr`   |

Each function is independently testable by shimming the `bd`/`tmux` functions
(the Go side already mocks `Runner`/`RunCmd` this way).

## Placement & packaging

Prototype as a plain script at
`packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`. Nix-package once
proven. Name `pr-pool.sh` is a default — easily changed. (Note: `pr-watcher.sh`
was removed from this pack on 2026-06-08; `pr-pool.sh` is its conceptual
successor on the _consumer_ side.)

## Error handling

- Sentinel gates are **read-only and optional**. Gate paths are top-of-script
  variables (default empty = disabled); if set to the Gas-City sentinels
  (`~/gc/QUOTA_PAUSED`, `~/gc/CICD_DOWN`) the script only `[ -f ]`-tests them
  and exits early — never creates/edits/removes them.
- `claude` / `tmux` / `bd` failures during dispatch → log, unclaim + leave the
  cycle open, continue to the next (never crash the whole drain on one cycle).
- All runs logged **outside `~/gc`** —
  `${XDG_STATE_HOME:-$HOME/.local/state}/pr-pool/`.

## Testing

- Unit: shim `bd`/`tmux`; assert `discover_cycles` applies the author filter +
  PR-dedup, `dispatch` emits the correct `tmux -u … new-session` argv,
  `wait_done` flags + **unclaims** (never closes) on timeout, `wait_ready`
  times out instead of spinning.
- Integration: from the monorepo root against a live **my-PR** cycle (`zr-l38`,
  #92600, which now has feedback children) with `MAX=1`; confirm a pane spawns,
  the nudge lands, the worker reads the feedback beads, creates action beads for
  code-change items, closes the cycle, and exits. Bats per the repo's
  `bash-scripting` conventions.

## Out of scope (deferred to later steps)

- Self-fix (worktree + commit + push; consuming action beads).
- Daemonization (launchd), continuous loop, escalation mechanism.
- Multi-role pool (reviewer, triage, generic worker), worktree management.
- Migrating to a Go orchestrator (reusing `pa-monitor`'s `TmuxSignaler` +
  liveness) once pool logic outgrows bash.
- Sweeping the orphaned HQ `gc` twin cycles left by the removed `pr-watcher`.

(`role`/author "my PRs" filter is now **in scope** for step 1 — see
`discover_cycles`.)

## Related

- `docs/adr/0009-pg-pr-bead-schema.md` — bead types, cascade, `discovered-from`.
- `docs/superpowers/specs/2026-05-19-pg-pr-design.md` — bead schema, verbs.
- `packages/pg-pr-plugin/share/pg-pr-plugin/skills/pg-pr-process-feedback/SKILL.md`
  — the worker's instructions (the pinned path the nudge references).
- `packages/pg-pr/pkg/beads/feedback.go` — `CreateFeedback` (the now-live
  producer of the feedback children the worker reads).
- `phillipg-nix-ziprecruiter/darwin/services/pg-pr-sync/default.nix` — the
  standalone sync daemon (`BEADS_ACTOR="pg-pr daemon"`).
- `phillipgreenii-nix-support-apps/darwin/modules/observability/dashboards/pg-pr-ops.json`
  — producer-side metrics dashboard.
- `packages/pa-monitor/internal/signal/tmux.go` — the send-keys primitive (Go)
  for the eventual daemon.
