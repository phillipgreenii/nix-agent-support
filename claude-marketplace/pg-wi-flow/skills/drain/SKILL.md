---
name: drain
description: Drain the pg-wi-flow work-item queue — dispatch one dispatcher subagent per ready item in a loop until nothing is left, a fixed item finishes, or an iteration cap is hit. Use when the user says "/drain", "drain the queue", "drain pg-wi-flow", "work the queue", or names a specific item to drain. Default is unattended (escalations that reach human are parked, not interviewed); pass --attended to interview them in-session, or --questions to visit ONLY attention items. Absorbs drain-beads, unblock-human-beads, tc-run, tc-grind, epic-runner, auto-orchestrator.
---

# `/drain` — the pg-wi-flow loop

**You (the interactive session) ARE the loop.** `/drain` has no model of its own and is never
a dispatched agent [design: `## Components` → "/drain" section, closing sentence, verbatim].
The only tunable part is the dispatcher (haiku), which you dispatch once per iteration.

Every `pg-wi-flow` call you make here runs unmodified inside this session: never pass
`--actor`, never compose or type an identity yourself — the CLI derives it from your own
session automatically.

## Flag surface [design: `## Components` → "/drain" section, verbatim]

`/drain [<id>] [--attended | --unattended] [--stage <s>]... [--concurrency <n>] [--questions]`

Raw filters are not accepted — no named-query wording, no "overnight" concept. Parse
`$ARGUMENTS` for exactly these:

- `<id>` (optional, positional): work exactly this one bead — and, if it is a container, its
  descent per "Containers" — ignoring every other filter below except attended/unattended.
  Finishes when the item is closed, is parked (escalated to `human`, unattended mode), or
  errors; for a container, when its descent yields no ready descendant.
- `--attended` / `--unattended`: interview mode. Default is `--unattended`: escalations that
  reach `human` are parked (bead labeled, loop moves on) with no questions to the operator.
  `--attended` interviews human escalations in-session as they occur.
- `--stage <s>` (repeatable): restrict to items at those stages.
- `--concurrency <n>` (default 1): `n` items in flight at once — `n` dispatcher+worker pairs,
  not merely `n` dispatchers.
- `--questions`: visit ONLY attention items (open questions labeled `human`, plus legacy
  `human` items) and do no new stage work. Implies `--attended`.

Five common intents, for reference:

| Intent                   | Command                           |
| ------------------------ | --------------------------------- |
| Everything, unattended   | `/drain`                          |
| Groom only, attended     | `/drain --attended --stage groom` |
| One bead                 | `/drain tc-x`                     |
| Answer pending questions | `/drain --questions`              |
| Three in flight          | `/drain --concurrency 3`          |

## Loop body, per iteration [design: same section, "Loop body, per iteration" list, verbatim]

**Step 0 — once, before the first iteration.**

List stale reservations and release each one; print the count released:

```
pg-wi-flow list --stale --reserved-hours 2
```

(2 is the default `H`; there is no flag to change it from `/drain` itself.) For each id in the
returned array, `pg-wi-flow release <id>`. Print `released N stale reservation(s)` (N may be
0).

**Step 1 — dispatch.**

Dispatch ONE `dispatcher` subagent (Agent tool, `subagent_type: "dispatcher"`) with ONLY the
flags you were given (`--stage`, and attended/unattended as resolved above) — never anything
else, and never the id filter (a fixed `<id>` run still goes through `next`'s normal ready-set
selection each iteration, then you personally check whether that id is what came back — the
dispatcher itself is not told about the `<id>` restriction; if the item that comes back isn't
`<id>`, treat this iteration as a `none` for the purposes of the `<id>`-run's own stop
condition, and keep iterating). Read the ONE line it reports back. Context cost per item: one
line — do not read anything else out of the dispatcher's work.

With `--concurrency n` (n > 1): keep `n` dispatcher+worker pairs in flight at once. Dispatch
up to `n` dispatcher subagents together (multiple Agent tool calls in a single message, so
they run concurrently); as each one reports back, dispatch a replacement immediately, keeping
`n` in flight until the stop condition is met.

**Step 2 — attended interview (only in `--attended` mode, including `--questions`).**

When a dispatcher reports `<id> human`, that item needs the operator. Render it for the
operator:

- the question text and its trigger (`q:intent`/`q:info`/`q:conflict`/`q:stall`)
- the parent item's title and stage
- the resolver's rationale for bumping to human (whatever it recorded)
- attached parents, when this question was deduped onto an existing one (multiple parents
  blocked by the same question)

Pull these with `pg-wi-flow explain <id>` (and `pg-wi-flow list --attended` / `--questions`
for the full attention set, if useful) — read-only, no claim needed.

Interview the operator with these options:

- `--answer <A>` → `pg-wi-flow resolve <id> --answer <A>`
- `--decision <D> --rationale <R>` → `pg-wi-flow resolve <id> --decision <D> --rationale <R>`
- `--abandon --reason-code <r>` → `pg-wi-flow resolve <id> --abandon --reason-code <r>`
- `--defer <date>` → `pg-wi-flow resolve <id> --defer <date>`
- `skip` → no bead change; the loop moves on and the question stays in the attended queue.

Apply the chosen `resolve` call unless `skip`, then continue the loop.

**Step 3 — stop conditions and summary.**

Stop iterating when any of these is true:

- The dispatcher reports `none` (unattended), or — with `--concurrency n` — ALL `n` in-flight
  pairs have gone idle/reported `none`.
- A fixed `<id>` run: the item finished (closed, parked, or errored — or, for a container, its
  descent yields no ready descendant).
- An iteration cap is reached (a sanity bound against a runaway loop; pick a reasonable one,
  e.g. 200, if nothing else is configured).

On `<id> error <reason>`: halt immediately and show it to the operator — do not keep
iterating past an error.

If the tracker is unreachable (any `pg-wi-flow`/`bd` call fails to connect): stop and ask the
operator — never fall back to a local tracker or guess.

On stop, print ONE line summarizing the run: items worked, items parked awaiting the operator
(with the command to review them — `pg-wi-flow list --attended`), and errors.

## Boundaries

- You never work an item's stage yourself, never call `claim`/`round`/`advance`/`close`/
  `resolve`/`escalate` against a work item directly — those belong to the dispatcher's
  dispatched worker/resolver, or (for `resolve`, in the interview step only) to you acting as
  the operator's hand.
- You never dispatch a worker or resolver directly — always through a dispatcher.
- You are not a dispatched agent; nothing above ever runs you as a subagent.
