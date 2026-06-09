---
name: pg-pr-process-feedback
description: Process the lifecycle of a processing-cycle bead — claim, review its feedback, create or update work beads (children of the PR bead), close the feedback and the cycle. Use when the user asks to "process feedback", "work the PR feedback queue", or you spot an open processing-cycle bead.
---

# pg-pr process feedback

Lifecycle handler for processing-cycle / feedback / work beads on a merge-request.

## Roles (do only your part)

- **pg-pr (producer):** creates/closes the **PR bead**; creates **cycle** and **feedback** beads. Not you.
- **You — the feedback processor:** review and close **cycle + feedback** beads, and create **work beads** from the feedback. You do **not** implement fixes.
- **Worker agent (someone else):** performs the work described in the work beads. Not you.

## Bead shapes

- **PR bead** — the merge-request. Parent of cycle beads and work beads.
- **processing-cycle** — `process-feedback: …`; child of the PR bead. Tracks the feedback accumulated since the last review. Children are feedback beads.
- **feedback** — one upstream review comment or CI failure; child of a cycle. Carries `repo`, `pr`, `thread_id`, `author`, `author_role`, `path`, `line`.
- **work bead** — a proposed change (`task`/`bug`) you create in response to feedback. A **child of the PR bead**, `discovered-from` the feedback that motivated it.

There can legitimately be more than one open cycle for a PR (pg-pr starts a new cycle when the existing one is already in-progress). That is fine — you work one cycle at a time and de-duplicate at the work-bead level (below).

## Workflow

1. Claim the processing-cycle bead:

   ```bash
   bd update <cycle-id> --claim
   ```

2. Read its feedback children, and resolve the **PR bead** (the cycle's parent):

   ```bash
   bd children <cycle-id>
   bd show <cycle-id> --json | jq -r '.parent'   # -> the PR bead id
   ```

3. List the PR's **existing open work beads** — the ones you must avoid duplicating:

   ```bash
   bd children <PR-bead-id> --status=open        # filter to task/bug (work beads)
   ```

4. For each feedback bead:
   1. Read upstream context (`pg-pr pr show`, `pg-pr pr files`, etc.) and decide the work it implies (or that it is non-actionable).
   2. **De-duplicate:** if that work matches an existing open work bead, **link/update** it — add this feedback as another `discovered-from` and refine the description if warranted — instead of creating a duplicate. Multiple comments, or a later cycle's feedback, commonly map to the same work.
   3. Otherwise create a **new work bead** (`task`/`bug`) as a **child of the PR bead**, `discovered-from` this feedback, describing the needed change.
   4. Do **not** implement the change and do **not** work the new bead — that is the worker agent's job.
   5. Close the feedback bead:
      ```bash
      bd close <feedback-id> --reason="<short verb-phrase>"
      ```

5. Close the processing-cycle bead with a one-line summary.

## Boundaries

- The processing-cycle bead is the unit of work. Never close it before every feedback child is closed.
- You create/link work beads only; you never apply fixes.
- Author precedence on responses: `self > team_member > org_member > bot`.
- Don't strip the 🤖 marker — `pg-pr comment` adds it automatically.
