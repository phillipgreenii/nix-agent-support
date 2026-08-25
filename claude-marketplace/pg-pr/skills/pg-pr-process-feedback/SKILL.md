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
(`code-comment-thread` | `pr-comments` | `ci-failure` | `review-request` | `jira-link` |
`self-review`), `status`, `author_kind` (`human` | `agent`), `agent_name`, `body`, and thread
context.

`self-review` items are the agent's own review findings on one of MY PRs (ingested by the
my-PR review sink, not posted to GitHub). Like unresolved `ci-failure` items, **unresolved
`self-review` findings BLOCK auto-merge until dispositioned** — disposition each one (`will-fix`
/ `wont-fix` / `no-action`) exactly as you would any other item to clear the merge gate.

## One cycle per PR

A processing-cycle bead is keyed on **(repo, PR number)** — its title tail — so **at most one
open cycle exists per PR**. pg-pr UPDATES that cycle (appending a summary note) when new
feedback arrives rather than opening a second one, and it opens **no** cycle at all when a sync
surfaced nothing unaddressed. A comment authored by the PR author — including a reply an agent
posted on their behalf, since pg-pr posts under the user's own login — is **not** feedback
needing processing.

- Each cycle's **description** states the count and kinds of unaddressed items (and who raised
  them), so triage it from the bead before reaching for the pg-pr CLI or the VCS API.
- A cycle that says it **supersedes** a closed predecessor is a successor opened because
  genuinely new feedback arrived after that cycle closed; the predecessor's id is in the
  description.
- If you ever find **two open cycles for the same PR**, they are legacy duplicates from before
  this invariant. Work one, and **report the other to the operator** — do not silently close it.
  `pg-pr sync duplicates` reports them read-only.
- Closing the cycle **without dispositioning** every item leaves those items unaddressed, so the
  next genuinely-new finding produces a successor that lists them again. Disposition first
  (step 5), then close — that is what makes the queue settle.

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
   1. Read upstream context (`pg-pr pr view`, `pg-pr pr files`, `pg-pr feedback show <id>`, etc.)
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
