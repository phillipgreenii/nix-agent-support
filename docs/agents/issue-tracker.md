# Issue tracker: beads (`bd`)

This is the tracker doc that Matt Pocock's engineering skills (plugin
`mattpocock-skills`, enabled via `phillipg-nix-ziprecruiter`
`home/ziprecruiter/packages/default.nix` `officialPlugins`) consult. Those skills
support GitHub, GitLab, and local markdown out of the box; this workspace uses
neither — it uses **beads**, which their setup skill treats as the freeform
"Other" case. This file is that freeform description, written by hand.

It was authored rather than generated: `/setup-matt-pocock-skills` would have
proposed GitHub Issues (a `git remote` pointing at GitHub is its default
posture) and written `docs/agents/issue-tracker.md` from its GitHub template.
Re-running that skill in any repo of this workspace will overwrite this file
with the GitHub template — don't, unless the tracker choice is genuinely being
changed.

## Scope

**This file is the tracker doc for every repo in the `pn-workspace`**, not just
this one. All of them share one beads database (issue prefix `pg2`), so a
per-repo copy would be duplication that drifts. Agents working in a sibling repo
MUST read it here:

```text
/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/docs/agents/issue-tracker.md
```

## Rules

- Issues live in **beads**, reached through the `bd` CLI. There is no
  GitHub/GitLab issue surface for this workspace; `gh issue` / `glab issue` MUST
  NOT be used to track work here.
- Markdown TODO lists, `.scratch/<feature>/` files, and `TodoWrite` MUST NOT be
  used as a tracker. This is the workspace `CLAUDE.md` rule, and it is why the
  skills' local-markdown fallback is not acceptable here.
- An agent MUST NOT invent labels or statuses beyond those named below.
- `bd list --json` caps at 50 rows, so it MUST NOT be used to prove an issue is
  absent; use `bd show <id>` or `bd search`.

## Operation mapping

What the engineering skills need, and the `bd` command that does it:

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

Whatever claims a bead MUST release it on every exit path. A release MUST clear
the assignee and set the status in **one** call, or the bead is stranded — `open`
with a non-empty assignee, which `bd ready --claim` skips and
`bd update --claim` rejects:

```bash
bd update <id> --status open --assignee "" --actor "<session-id>"
```

`--status open` alone is NOT a release. `bd close` MAY leave the assignee (it
records who did the work). The full policy is in the always-on agent rules
(`home/programs/agent-rules/pgii-agent-rules.md`, "Beads Claim Hygiene").

### Blocking direction

The **first** id is the blocked issue, the **second** is the blocker. Prefer the
`--blocked-by` flag form over the bare positional form, which reads identically
whichever way round it is written, and read the edge back — a reversed edge
blocks the wrong issue silently:

```bash
bd dep add <blocked-id> --blocked-by <blocker-id>
bd dep list <blocked-id>   # each blocker MUST echo back "(open) via blocks"
```

Edges MUST be left at the default `blocks` type. `discovered-from`, `related`,
and `supersedes` edges do **not** gate readiness, so they MUST NOT be used to
express "must finish first".

## Wayfinding operations

This is the section `/wayfinder` consults for how this repo expresses its map,
child tickets, blocking, and frontier queries. Every command below was verified
against the live `bd` on 2026-08-12.

### The map

The map is one bead of type `epic`, labelled `wayfinder:map`. Its body (the
bead's `description`) holds wayfinder's four sections — Destination, Notes,
Decisions so far, Not yet specified, Out of scope.

```bash
bd create "<destination name>" -t epic -l wayfinder:map \
  --body-file <path-to-map-body> --actor "<session-id>"
```

**The map MUST then be set to `blocked`:**

```bash
bd update <map-id> --status blocked --assignee "" --actor "<session-id>"
```

This is load-bearing and non-obvious. An **open** epic is permanently present in
plain `bd ready`, so a map left open sits at the head of the shared work queue
forever and every `/drain-beads` session has to claim and dismiss it. `blocked`
removes it from `bd ready` while leaving it fully readable and — verified — still
yielding its children to the frontier query below. Do **not** use `--defer` for
this: defer cascades to children and hides the whole map.

Use `--body-file` (or `--stdin`) rather than `-d` for the map body; it is
multi-section markdown. Update it with `bd update <map-id> --body-file <path>`.

### Tickets

Each ticket is a **hierarchical child** of the map, which gives it a dotted id
(`pg2-abc12.3`) that shows its parentage at a glance:

```bash
bd create "<question>" --parent <map-id> --no-inherit-labels \
  -l wayfinder:<type> -d "## Question

<the decision this ticket resolves>" --actor "<session-id>"
```

`--no-inherit-labels` is **required**. Children inherit the parent's labels by
default, so without it every ticket is born carrying `wayfinder:map` and the map
is no longer identifiable by its label. Verified: omitting the flag produced a
child labelled `wayfinder:map`.

`<type>` is one of `research`, `prototype`, `grilling`, `task` — wayfinder's four
ticket types. Ticket **type** stays `task` (the `bd` default); the
`wayfinder:<type>` label carries wayfinder's classification, since `bd`'s own
type vocabulary means something different.

Pass `--parent` a **non-empty** id. `bd create --parent ""` does not error — it
silently creates a flat, unparented issue that the frontier query below can never
find. If the id came from a shell variable, confirm it is set first.

### Blocking

Use the native edge, per "Blocking direction" above:

```bash
bd dep add <blocked-ticket> --blocked-by <blocker-ticket>
```

Wayfinder creates tickets first and wires edges in a second pass, which suits
`bd` — the dotted ids do not exist until the children are created.

### The frontier

The frontier is the open, unblocked, unclaimed children of the map. One command
is exactly that query:

```bash
bd ready --parent <map-id> --unassigned
```

`bd ready` is blocker-aware and already excludes `in_progress`, `blocked`,
`deferred`, and hooked issues; `--parent` scopes to descendants of the map (and
excludes the map itself); `--unassigned` enforces wayfinder's "an open,
unassigned ticket is unclaimed".

Verified behaviour of that one command:

- a ticket given a `blocks` edge drops out of the frontier;
- a ticket claimed with `--claim` drops out of the frontier;
- the map being `blocked` does **not** suppress its children.

So there is no body convention to maintain and no fallback needed — `bd` has
native blocking, and this query renders the frontier.

### Claiming

A session claims a ticket **before any work**, so concurrent sessions skip it:

```bash
bd update <ticket-id> --claim --actor "<session-id>"
```

`--claim` sets the assignee and moves the ticket to `in_progress`, which removes
it from the frontier query on both counts. Always pass an explicit `--actor`;
without it the assignee resolves to the human's display name, and an abandoned
claim then looks like the operator deliberately took the ticket.

If the session ends without resolving the ticket, it MUST be released per "Claim
hygiene" above.

### Resolving a ticket

```bash
bd comment <ticket-id> "<the answer>"
bd close <ticket-id> --reason "<one-line gist>" --actor "<session-id>"
```

Then append a line to the map's **Decisions so far** section pointing at the
closed ticket.

### Referring to tickets

Wayfinder requires that everything a human reads names issues by **title**, not
id. This matters more here than on a numeric tracker: `bd` ids are opaque tokens
(`pg2-qr07d.2`), so a wall of them is unreadable even to someone who knows the
map. Write the title, and carry the id inside it.

### Out of scope

Ruling a ticket out of scope is a close, not a resolution:

```bash
bd close <ticket-id> --reason "out of scope: <why>" --actor "<session-id>"
```

Record one line in the map's **Out of scope** section. It stays out of Decisions
so far.

## Triage labels

The `triage` skill from the same plugin expects five label strings. Beads has no
pre-seeded label vocabulary, and this workspace already uses `human` for
"a person is the blocker", so the canonical defaults are kept **except** where
they would collide with existing meaning:

| Triage role       | Label here        |
| ----------------- | ----------------- |
| `needs-triage`    | `needs-triage`    |
| `needs-info`      | `needs-info`      |
| `ready-for-agent` | `ready-for-agent` |
| `ready-for-human` | `human`           |
| `wontfix`         | `wontfix`         |

`ready-for-human` maps onto the existing **`human`** label deliberately: `human`
is what the `/unblock-human-beads` queue claims on, so a triage decision that
wrote `ready-for-human` instead would be invisible to it.

Before applying `human`, classify the blocker: another **issue** that must finish
first is a dependency (`bd dep add`), not a `human` label. `human` means a
**person** is the blocker.
