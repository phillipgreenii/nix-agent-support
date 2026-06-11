# pr-pool user journeys

**Status:** Living doc (2026-06-11; reviewed)
**Purpose:** Describe what `pr-pool` actually does, end to end, so (a) the behavior
is documented independently of the implementation, and (b) the capabilities
pr-pool needs from `ccpool` can be read straight off the journeys (see
"ccpool actions" at the end). Supports the Go port (`pg2-spgx`) and the budget
watchdog (`pg2-y991`).

## Actors & participants

- **Operator** — a human or scheduler that invokes `pr-pool`.
- **Orchestrator** — `pr-pool` itself (mechanical; no LLM in its own loop).
- **Dispatched agents** — a **feedback-processor** or a **worker** (a `claude`
  session, run via ccpool, following its SKILL contract).
- **Participants (systems, not actors):** the **bead store** (`bd`) and the
  **session manager** (`ccpool`).

The orchestrator's completion signal is always **bead status** (via `bd`); ccpool
is used only to run the agent session and report its **liveness**.

---

## Phase-1 journeys (the capability port)

### J1 — Run a drain pass _(top-level, operator)_

Operator/scheduler invokes `pr-pool`. It:

1. **Preflight** — resolve `self_login` (`pg-pr config show`), assert it's pointed
   at the intended bead store.
2. **Gate check** — if a quota/CICD **sentinel file** is present, **pause and
   exit without dispatching** _(note: the gated exit does **not** run teardown —
   it is not a reaping exit)_.
3. **Discover** ready work (sub-phase below).
4. **Drain** each role up to its per-role cap (`cap=0` disables a role).
5. **Teardown-all** — remove **every** role's session, **including strays left by
   a crashed prior run** (the orchestrator's only self-healing behavior; see
   J-reap).
6. Report.

**Discovery (sub-phase of J1, not a standalone journey).** Query `bd ready` per
role: **feedback** = open `task` beads titled `process-feedback:` whose parent
merge-request bead's `metadata.author == self_login`; **worker** = `bd ready
--label worker-ready --exclude-label human` (a native `bd` filter, not a jq
post-filter). Emit `role→bead` dispatches in priority order, bounded by caps. No
sessions are touched.

### J3 — Work a worker bead _(happy path)_

Dispatch a `worker-ready` bead:

1. **Ensure** a fresh session — per-bead id, `cwd` = monorepo, **env injected**
   (`BEADS_ACTOR`, `BEADS_DIR`, `WORKSPACE_ROOT`), **claude launch flags**
   (`--dangerously-skip-permissions`, `--effort max`, model). Fresh id ⇒ fresh
   context (no `/clear` needed).
2. **Send** the worker nudge (async — pr-pool does not block on the turn).
3. The **worker**: claims the bead; resolves the PR/branch **bead-first**;
   **asserts** the branch starts with `phillipg.` **and** the PR author is me;
   works in an isolated worktree; commits (**pushes only if the bead instructs**);
   **records a note first, then transitions the bead last** (closes it).
4. pr-pool **polls the bead**: done when it **leaves `in_progress`** (here:
   `closed`). _Startup-race guard: a freshly-dispatched bead is still `open`
   before the worker claims it; pr-pool must observe `in_progress` (`seen_claimed`)
   before it will treat `open` as a hand-back, so a pre-claim `open` is not
   mistaken for completion._
5. **Remove** the session.

### J4 — Already-done bead

As J3, but the worker finds the work **already present at branch HEAD** → records
a verification note and **closes**. → remove session. (No commit, no push.)

### J5 — Hand-back _(low context)_

The worker commits what it has + a note, then **unclaims to `open`** (status
`open`, assignee cleared, **no `human` label**). pr-pool sees the bead left
`in_progress` (now `open`, after `seen_claimed`) → remove session. The bead is
**re-dispatchable next pass** — that continuation simply **re-enters J3** with no
special "continuation" state.

### J6 — Needs a human _(hard block)_

The worker hits a hard block (no resolvable/open PR, **not my PR**, branch **not
`phillipg.`-prefixed**, or an **unrecoverable dirty worktree**) → adds the
**`human`** label + a comment, makes **no code changes**. → remove session. The
bead is excluded from future discovery (`--exclude-label human`).

### J7 — Crash / timeout _(orchestrator-detected)_

The session dies (`ccpool` reports it **not live**, or `failed`) or `MAX_WAIT`
elapses. pr-pool **re-checks the bead status**:

- if the bead **already left `in_progress`** (closed, or open-after-`seen_claimed`)
  → **success, no flag** — a bead that finished in the same instant the session
  ended is not a failure;
- only if it is **still `in_progress`** → flag **`human`**, **never unclaim** (a
  dead worker may hold a half-built worktree; blind retry is unsafe).

→ remove session.

### J8 — Work a feedback cycle

Dispatch a `process-feedback:` cycle → Ensure session → Send the feedback nudge →
the **feedback-processor** dedups/creates work beads and **closes the cycle** →
pr-pool sees the cycle `closed` → remove session.

### J-reap — Reap a stray session _(self-healing, part of J1 teardown)_

On every pass, teardown removes **all** role sessions, not only those dispatched
this pass — so a session orphaned by a crashed prior `pr-pool` run is reaped on
the next pass. _(Under the Go fresh-session-per-bead model, sessions are named
per-bead rather than per-role, so "reap all of mine" is scoped by a
`pr-pool-`-name convention rather than two fixed role names — an intentional
semantic shift to preserve this behavior.)_

### J-dispatch-fail — Nudge could not be sent

If `Send` fails before the agent ever runs (e.g. misconfiguration): **feedback →
unclaim**; **worker → not unclaimed** (left for human inspection); the session is
torn down and the pass continues. (A distinct failure from J7 — the nudge never
landed.)

### J11 — Mixed pass, no starvation

When a feedback cycle **and** a worker bead are both ready, **one of each** is
worked in the same pass — per-role caps prevent either role from starving the
other.

### The failure-action contract (spans J6/J7/J8/J-dispatch-fail)

- **Worker failure** (hard block, crash, timeout, send-failure) → add **`human`**,
  **never unclaim** — the dead worker may hold partial worktree state.
- **Feedback failure** → **unclaim** (status `open`) so the next pass retries.

---

## Future journeys (chunk B — documented, not built here)

### J9 — Budget watchdog _(B; `pg2-y991`)_

While a worker runs, pr-pool tracks the session's **token usage** (read from its
**transcript**) and **wall-clock time** against a configured budget:

- **~70–75%** → send a **queued reminder** that the limit is near;
- **90%** → **cancel** the turn + a strong "wrap up now, save notes, finish"
  message;
- **100%** → second **cancel** → ERROR state: reset the git worktree (never commit
  unknown code), note the bead ("interrupted — budget"), **unclaim** on the
  agent's behalf, remove the session.

The budget is also stated **in the worker prompt** so the agent is aware of it.

### Operator recovery _(out-of-process for pr-pool; noted for completeness)_

A human drains the `human` queue (`bd list --label human`): inspects the bead,
fixes/decides, **clears `human`**, and **re-arms `worker-ready`** — which lets the
bead re-enter J3 on a later pass. pr-pool does not perform this; it only produces
the queue.

---

## ccpool actions, read off the journeys

| Journey              | ccpool action(s)                                                           | Phase  |
| -------------------- | -------------------------------------------------------------------------- | ------ |
| J1 / J-reap          | Ensure, Send, List (liveness), **Close** (all)                             | 1      |
| Discovery, J10/gated | **none** (pure `bd`)                                                       | 1      |
| J3 / J4 / J5 / J6    | **Ensure** (env + flags), **Send** (async), **List** (liveness), **Close** | 1      |
| J7                   | **List** (not-live / `failed`), **Close**                                  | 1      |
| J8 / J11             | Ensure, Send, List, Close                                                  | 1      |
| J-dispatch-fail      | Ensure, Close (Send errors)                                                | 1      |
| **J9 (B)**           | **List** (`transcript_path`), **Cancel**, Send (**queued** mode)           | future |

### Derived ccpool contract

**Phase-1 capabilities pr-pool requires** (validated complete by the journeys —
no gap, no Phase-1 dead capability):

| Ref    | Capability                                                                                    | ccpool today                          | Work?                                                          |
| ------ | --------------------------------------------------------------------------------------------- | ------------------------------------- | -------------------------------------------------------------- |
| **E1** | Create a session by **id** (cwd, block-until-ready)                                           | ✅ `ccpool new <name>`                | augmented by N1+N2                                             |
| **N1** | **Inject per-session env** at create                                                          | ❌ env hardcoded in `session.Service` | **new**                                                        |
| **N2** | **Apply claude launch flags** at create (`--dangerously-skip-permissions`, `--effort`, model) | ❌ launch emits none                  | **new (required)**                                             |
| **E2** | **Send a prompt async**                                                                       | ✅ `ccpool reply --no-wait`           | none                                                           |
| **N3** | **`list --json`**: per-session `state` + `live` + `transcript_path`                           | ❌ `list` is text                     | **new** (state+live used now; `transcript_path` consumed by B) |
| **E4** | **Remove** a session by id (ccpool owns `/exit` vs kill, and "fresh = new id")                | ✅ `ccpool close`                     | none                                                           |

**B-seams (future — consumed only by J9, not Phase-1 work):**

| Ref    | Capability                                     | ccpool today                                 |
| ------ | ---------------------------------------------- | -------------------------------------------- |
| **E3** | **Cancel** the current turn (90/100%)          | ✅ `ccpool cancel` (no new work; B wires it) |
| —      | **Queued send** mode (70% reminder)            | ✅ `ccpool reply --queue-message`            |
| —      | `transcript_path` consumption (token tracking) | provided by **N3**                           |

**So the new ccpool work is exactly N1, N2, N3.** E1/E2/E3/E4 already exist; the
breakdown can add light "confirm it meets the contract" checks but no new code.

## Related

- `docs/superpowers/specs/2026-06-11-pr-pool-go-port-design.md` — the Go port
  these journeys support.
- `docs/superpowers/specs/2026-06-11-pr-pool-worker-contract-design.md` — the
  worker/feedback contracts the journeys reference.
- Beads: `pg2-spgx` (A: Go port), `pg2-y991` (B: budget/J9), `pg2-01ys` (parked:
  ccpool metadata/search — not needed by these journeys).
