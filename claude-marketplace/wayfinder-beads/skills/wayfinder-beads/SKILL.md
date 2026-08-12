---
name: wayfinder-beads
description: >-
  Use when a skill or workflow asks where this repo's issues live — its "issue tracker" —
  and the answer is beads (`bd`). Fires most often for `/wayfinder` needing its
  "Wayfinding operations": how the map, its child tickets, blocking edges, claiming, and
  the frontier query are physically expressed. Also fires for `/triage`, `/to-tickets`,
  and `/to-spec` needing where to read and write issues and which triage labels to apply,
  and any time an agent is about to fall back to a local-markdown or GitHub-Issues tracker
  in a beads repo. Supplies the verified `bd` mapping — the map as a `blocked` epic
  labelled `wayfinder:map`, tickets as `--parent` children created
  `--no-inherit-labels`, native blocking via `bd dep add --blocked-by`, and
  `bd ready --parent <map-id> --unassigned` as the frontier — plus claim-release hygiene
  and the triage label vocabulary. Do NOT use for ordinary bead CRUD (create/list/close),
  grooming the backlog, the `pn:applied` gate lifecycle, or generic `bd` questions
  unrelated to one of those skills asking for a tracker.
---

# Beads as the issue tracker

Matt Pocock's engineering skills (plugin `mattpocock-skills`) each read a per-repo
"issue tracker" description. They ship templates for GitHub, GitLab, and local
markdown only, and **default silently to local markdown** when no tracker is
provided. This skill is the freeform "Other" case those skills allow, and it is the
single source of truth for the binding.

This skill exists so the binding travels. It is delivered by the nix-built
marketplace, which is registered automatically wherever this flake's home-manager
module is imported — so it is available on every such machine with no absolute
path, no per-repo file, and no per-machine setup.

## Rules

- Issues live in **beads**, reached through the `bd` CLI. `gh issue` / `glab issue`
  MUST NOT be used to track work in a beads repo.
- Markdown TODO lists, `.scratch/<feature>/` files, and `TodoWrite` MUST NOT be used
  as a tracker. The skills' local-markdown fallback is therefore NOT acceptable, and
  an agent MUST NOT take it in a beads repo.
- An agent MUST NOT run `/setup-matt-pocock-skills`. It proposes GitHub whenever a
  `git remote` points there, and writes its own template over whatever the repo had.
  Changing trackers is an operator decision.
- An agent MUST NOT invent labels or statuses beyond those named here.
- `bd list --json` caps at 50 rows, so it MUST NOT be used to prove an issue is
  absent; use `bd show <id>` or `bd search`.

## Operation mapping

| Operation            | `bd`                                                    |
| -------------------- | ------------------------------------------------------- |
| Create an issue      | `bd create "<title>" -t <type> -p <0-4> -d "<body>"`    |
| Read one             | `bd show <id>` (add `--json` for fields)                |
| Find claimable work  | `bd ready`                                              |
| Claim before working | `bd update <id> --claim --actor "<session-id>"`         |
| Comment              | `bd comment <id> "<text>"`                              |
| Close                | `bd close <id> --reason "<why>" --actor "<session-id>"` |
| Label                | `bd update <id> --add-label a,b` / `--remove-label a,b` |
| Block one on another | `bd dep add <blocked-id> --blocked-by <blocker-id>`     |
| Hierarchical child   | `bd create "<title>" --parent <parent-id>`              |

Issue types are `bug`, `feature`, `task`, `epic`, `chore`, `decision`.

### Claim hygiene

Whatever claims a bead MUST release it on every exit path. The release MUST clear the
assignee and set the status in **one** call, or the bead is stranded — `open` with a
non-empty assignee, which `bd ready --claim` skips and `bd update --claim` rejects:

```bash
bd update <id> --status open --assignee "" --actor "<session-id>"
```

`--status open` alone is NOT a release. `bd close` MAY leave the assignee (it records
who did the work).

### Blocking direction

The **first** id is the blocked issue, the **second** is the blocker. Prefer the
`--blocked-by` flag form over the bare positional form, which reads identically
whichever way round it is written, and read the edge back — a reversed edge blocks the
wrong issue silently:

```bash
bd dep add <blocked-id> --blocked-by <blocker-id>
bd dep list <blocked-id>   # each blocker MUST echo back "(open) via blocks"
```

Edges MUST be left at the default `blocks` type. `discovered-from`, `related`, and
`supersedes` edges do **not** gate readiness, so they MUST NOT be used to express
"must finish first".

## Wayfinding operations

This is the section `/wayfinder` consults. Every command here was verified against a
live `bd` (2026-08-12); the four notes flagged **verified** below are behaviours that
are not guessable from the help text.

### The map

The map is one bead of type `epic`, labelled `wayfinder:map`. Its body (the bead's
`description`) holds wayfinder's sections — Destination, Notes, Decisions so far, Not
yet specified, Out of scope.

```bash
bd create "<destination name>" -t epic -l wayfinder:map \
  --body-file <path-to-map-body> --actor "<session-id>"
```

**The map MUST then be set to `blocked`:**

```bash
bd update <map-id> --status blocked --assignee "" --actor "<session-id>"
```

This is load-bearing and non-obvious. An **open** epic is permanently present in plain
`bd ready`, so a map left open parks itself at the head of the shared work queue and
every queue-draining session pays a claim cycle for it. **Verified:** `blocked` removes
it from `bd ready` while leaving it fully readable and still yielding its children to
the frontier query below. Do **not** use `--defer` for this — defer cascades to
children and hides the whole map.

Use `--body-file` (or `--stdin`) rather than `-d` for the map body; it is multi-section
markdown. Update it with `bd update <map-id> --body-file <path>`.

### Tickets

Each ticket is a **hierarchical child** of the map, which gives it a dotted id
(`pg2-abc12.3`) showing its parentage at a glance:

```bash
bd create "<question>" --parent <map-id> --no-inherit-labels \
  -l wayfinder:<type> -d "## Question

<the decision this ticket resolves>" --actor "<session-id>"
```

`--no-inherit-labels` is **required**. **Verified:** children inherit the parent's
labels by default, so without it every ticket is born carrying `wayfinder:map` and the
map stops being identifiable by its own label.

`<type>` is one of `research`, `prototype`, `grilling`, `task` — wayfinder's four
ticket types. The bead's own **type** stays `task` (the `bd` default); the
`wayfinder:<type>` label carries wayfinder's classification, because `bd`'s type
vocabulary means something different.

Pass `--parent` a **non-empty** id. **Verified:** `bd create --parent ""` does not
error — it silently creates a flat, unparented issue that the frontier query can never
find. If the id came from a shell variable, confirm it is set first.

### Blocking

Use the native edge, per "Blocking direction" above:

```bash
bd dep add <blocked-ticket> --blocked-by <blocker-ticket>
```

Wayfinder creates tickets first and wires edges in a second pass, which suits `bd` —
the dotted ids do not exist until the children are created.

### The frontier

The frontier is the open, unblocked, unclaimed children of the map. One command is
exactly that query:

```bash
bd ready --parent <map-id> --unassigned
```

`bd ready` is blocker-aware and already excludes `in_progress`, `blocked`, `deferred`,
and hooked issues; `--parent` scopes to descendants of the map and excludes the map
itself; `--unassigned` enforces wayfinder's "an open, unassigned ticket is unclaimed".

**Verified** for that one command: a ticket given a `blocks` edge drops out of the
frontier; a ticket claimed with `--claim` drops out of the frontier; and the map being
`blocked` does not suppress its children.

So there is no body convention to maintain and no fallback needed — `bd` has native
blocking, and this query renders the frontier. Wayfinder's requirement that blocking be
native, so the frontier is visible in the tracker's own UI, is satisfied.

### Claiming

A session claims a ticket **before any work**, so concurrent sessions skip it:

```bash
bd update <ticket-id> --claim --actor "<session-id>"
```

`--claim` sets the assignee and moves the ticket to `in_progress`, removing it from the
frontier query on both counts. Always pass an explicit `--actor`; without it the
assignee resolves to the human's display name, and an abandoned claim then looks like
the operator deliberately took the ticket. A session that ends without resolving its
ticket MUST release it per "Claim hygiene".

### Resolving a ticket

```bash
bd comment <ticket-id> "<the answer>"
bd close <ticket-id> --reason "<one-line gist>" --actor "<session-id>"
```

Then append a line to the map's **Decisions so far** pointing at the closed ticket.

### Out of scope

Ruling a ticket out of scope is a close, not a resolution:

```bash
bd close <ticket-id> --reason "out of scope: <why>" --actor "<session-id>"
```

Record one line in the map's **Out of scope** section; it stays out of Decisions so far.

### Referring to tickets

Wayfinder requires that everything a human reads names issues by **title**, not id.
This matters more here than on a numeric tracker: `bd` ids are opaque tokens
(`pg2-qr07d.2`), so a wall of them is unreadable even to someone who knows the map.
Write the title, and carry the id inside it.

## Triage labels

`/triage` expects five label strings. Beads has no pre-seeded label vocabulary, so the
canonical defaults are kept **except** where they would collide with existing meaning:

| Triage role       | Label here        |
| ----------------- | ----------------- |
| `needs-triage`    | `needs-triage`    |
| `needs-info`      | `needs-info`      |
| `ready-for-agent` | `ready-for-agent` |
| `ready-for-human` | `human`           |
| `wontfix`         | `wontfix`         |

`ready-for-human` maps onto the existing **`human`** label deliberately: `human` is
what the human-queue command claims on, so a triage decision writing
`ready-for-human` instead would be invisible to that queue.

Before applying `human`, classify the blocker: another **issue** that must finish first
is a dependency (`bd dep add`), not a `human` label. `human` means a **person** is the
blocker.
