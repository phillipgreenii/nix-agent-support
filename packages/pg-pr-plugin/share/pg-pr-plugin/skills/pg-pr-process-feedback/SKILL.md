---
name: pg-pr-process-feedback
description: Process the lifecycle of a processing-cycle bead — claim, pull feedback from pg-pr CLI, create or update work beads (children of the PR bead), disposition each feedback item, then close the cycle. Use when the user asks to "process feedback", "work the PR feedback queue", or you spot an open processing-cycle bead.
---

# pg-pr process feedback

Lifecycle handler for processing-cycle / work beads on a merge-request.

## Roles (do only your part)

- **pg-pr (producer):** creates/closes the **PR bead**; creates **cycle** beads and manages the feedback store. Not you.
- **You — the feedback processor:** claim the cycle bead, pull feedback from the pg-pr CLI, create **work beads**, and record a disposition for every feedback item. You do **not** implement fixes.
- **Worker agent (someone else):** performs the work described in the work beads. Not you.

## Bead shapes

- **PR bead** — the merge-request. Parent of cycle beads and work beads.
- **processing-cycle** — `process-feedback: …`; child of the PR bead. Tracks one review pass.
- **work bead** — a proposed change (`task`/`bug`) you create in response to feedback. A **child of the PR bead**, `discovered-from` the feedback item that motivated it.

Feedback items live in **pg-pr's own store** (not as beads). Each item has: `id`, `kind`
(`code-comment-thread` | `pr-comments` | `ci-failure` | `review-request` | `jira-link`),
`status`, `author_kind` (`human` | `agent`), `agent_name`, `body`, and thread context.

There can legitimately be more than one open cycle for a PR (pg-pr starts a new cycle when the
existing one is already in-progress). That is fine — you work one cycle at a time and
de-duplicate at the work-bead level (below).

## Workflow

1. Claim the processing-cycle bead:

   ```bash
   bd update <cycle-id> --claim
   ```

2. Resolve the **PR bead** (the cycle's parent) and extract `repo` / `pr_number`:

   ```bash
   bd show <cycle-id> --json | jq -r '.parent'          # -> the PR bead id
   bd show <PR-bead-id> --json | jq -r '.metadata | {repo, pr_number}'
   ```

3. **Pull feedback from pg-pr:**

   ```bash
   pg-pr feedback list <repo> <pr_number> --json
   ```

   Each item in the returned array carries `id`, `kind`, `status`, `author_kind`, `body`, etc.
   Use `pg-pr feedback show <item-id> --json` to fetch thread context for a specific item.

4. List the PR's **existing open work beads** — the ones you must avoid duplicating:

   ```bash
   bd children <PR-bead-id> --status=open        # filter to task/bug (work beads)
   ```

5. For each feedback item:
   1. Read upstream context (`pg-pr pr show`, `pg-pr pr files`, `pg-pr feedback show <id>`, etc.)
      and decide the work it implies (or that it is non-actionable).
   2. **De-duplicate:** if that work matches an existing open work bead, **link/update** it —
      add this feedback as another `discovered-from` and refine the description if warranted —
      instead of creating a duplicate. Multiple comments, or a later cycle's feedback, commonly
      map to the same work.
   3. Otherwise create a **new work bead** (`task`/`bug`) as a **child of the PR bead**,
      `discovered-from` this feedback item's id, describing the needed change.
   4. Do **not** implement the change and do **not** work the new bead — that is the worker agent's job.
   5. **Record your disposition** for the item:

      ```bash
      # For actionable feedback (work bead created or linked):
      pg-pr feedback disposition <item-id> --action=will-fix --note="<short verb-phrase; bead <work-bead-id>>"

      # For non-actionable feedback:
      pg-pr feedback disposition <item-id> --action=wont-fix --note="<reason>"
      # or:
      pg-pr feedback disposition <item-id> --action=no-action --note="<reason>"

      # Append --reply="..." when you want pg-pr to post a reply upstream:
      pg-pr feedback disposition <item-id> --action=wont-fix --note="<reason>" --reply="<reply text>"
      ```

6. Close the processing-cycle bead with a one-line summary.

## Boundaries

- The processing-cycle bead is the unit of work. Never close it before every feedback item has a disposition.
- You create/link work beads only; you never apply fixes.
- Author precedence on responses: `self > team_member > org_member > bot`.
- Don't strip the 🤖 marker — `pg-pr comment` adds it automatically.
