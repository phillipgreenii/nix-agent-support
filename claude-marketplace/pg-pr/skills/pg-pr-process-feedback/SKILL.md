---
name: pg-pr-process-feedback
description: Process the lifecycle of a processing-cycle bead — claim, pull feedback via pg-connector/pg-desk, create or update work beads (children of the PR bead), disposition each feedback item, then close the cycle. Use when the user asks to "process feedback", "work the PR feedback queue", or you spot an open processing-cycle bead.
---

# pg-pr process feedback

Lifecycle handler for processing-cycle / work beads on a merge-request.

## Roles (do only your part)

- **pg-desk (producer):** creates/closes the **PR bead**; creates **cycle** beads and owns the feedback store (comments/review-threads and their dispositions). Not you.
- **You — the feedback processor:** claim the cycle bead, pull feedback via `pg-connector pr show` and `pg-desk feedback list`, create **work beads**, and record a disposition for every feedback item via `pg-desk feedback set`. You do **not** implement fixes.
- **Worker agent (someone else):** performs the work described in the work beads. Not you.

## Bead shapes

- **PR bead** — the merge-request. Parent of cycle beads and work beads.
- **processing-cycle** — `process-feedback: …`; child of the PR bead. Tracks one review pass.
- **work bead** — a proposed change (`task`/`bug`) you create in response to feedback. A **child of the PR bead**, `discovered-from` the feedback item that motivated it. Labeled `worker-ready` when the change is clear-cut, or `human` (never both) when it instead poses a question only the PR author can resolve — see step 5.3.

Feedback items are the PR's own comments and review-thread entries (not beads) — read via
`pg-connector pr show <id>`. Each carries `id`, `author`, `body`, `resolved`, and, for a
review-thread comment, `path`, `line`, and `thread_id`. `pg-desk feedback list <pr>` lists the
same comment/thread ids paired with their _current disposition_ — `open`, `will-fix`,
`wont-fix`, or `no-action` — from `pg-desk`'s store; an id with no override still reads `open`
(unaddressed) until a disposition is recorded.

## One cycle per PR

A processing-cycle bead is keyed on **(repo, PR number)** — its title tail — so **at most one
open cycle exists per PR**. pg-desk sync UPDATES that cycle (appending a summary note) when new
feedback arrives rather than opening a second one, and it opens **no** cycle at all when a sync
surfaced nothing unaddressed. A comment authored by the PR author — including a reply an agent
posted on their behalf, since pg-pr posts under the user's own login — is **not** feedback
needing processing.

- Each cycle's **description** states the count and kinds of unaddressed items (and who raised
  them), so triage it from the bead before reaching for the CLI (pg-connector/pg-desk) or the VCS API.
- A cycle that says it **supersedes** a closed predecessor is a successor opened because
  genuinely new feedback arrived after that cycle closed; the predecessor's id is in the
  description.
- If you ever find **two open cycles for the same PR**, they are legacy duplicates from before
  this invariant. Work one, and **report the other to the operator** — do not silently close it.
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

3. **Read the PR's current feedback:**

   ```bash
   pg-connector pr show <repo>#<pr_number>
   ```

   pg-connector defaults to JSON output (`--output json`), so this is directly parseable. The
   result's `comments` array is the PR's top-level comments; each `reviews[].comments` array is
   that review's own review-thread comments (carrying `path`/`line`/`thread_id`). Together these
   are every feedback item on the PR.

   ```bash
   pg-desk feedback list <pr_number>
   ```

   prints every comment/thread id from that same PR paired with its current disposition. Any id
   listed `open` is unaddressed and needs processing this cycle; `will-fix`/`wont-fix`/`no-action`
   ids were already dispositioned (by you or the rule set) and can be skipped unless new context
   changes the call.

4. List the PR's **existing open work beads** — the ones you must avoid duplicating:

   ```bash
   bd children <PR-bead-id> --status=open        # filter to task/bug (work beads)
   ```

5. For each `open`-dispositioned feedback item (id) from step 3:
   1. Look up that id in the `pg-connector pr show` result (by comment `id`, or by `thread_id`
      for a review-thread comment) to read its body/author/path/line context, and decide the
      work it implies (or that it is non-actionable). (`pg-connector pr files <id>` covers a
      files-only view if a narrower read is wanted.)
   2. **De-duplicate:** if that work matches an existing open work bead, **link/update** it —
      add this feedback as another `discovered-from` and refine the description if warranted —
      instead of creating a duplicate. Multiple comments, or a later cycle's feedback, commonly
      map to the same work.
   3. Otherwise create a **new work bead** (`task`/`bug`) as a **child of the PR bead**,
      `discovered-from` this feedback item's id, describing the needed change — then judge
      whether that change is clear-cut or needs a human call:
      - **Clear-cut** (unambiguous engineering work): label it `worker-ready`, as before, so the
        worker role picks it up.
      - **Needs a human call** (the comment poses a genuine question, a design tradeoff, or an
        ambiguity you cannot resolve from the PR/repo alone): label it `human` instead —
        **never** `worker-ready` — and write the specific question into the bead's description.
        This keeps it out of the worker queue (which already excludes `human`-labeled beads)
        instead of letting a worker discover the same dead end later. **Do not wait for it**:
        keep processing this cycle's remaining feedback items exactly as normal, and a
        `human`-labeled item never blocks closing the cycle (step 6) or working any other item.
   4. Do **not** implement the change and do **not** work the new bead — that is the worker
      agent's job (a `human`-labeled bead additionally waits on the PR author's answer, not a
      worker).
   5. **Record your disposition** for the item:

      ```bash
      # For actionable feedback (work bead created or linked, worker-ready or human alike):
      pg-desk feedback set <pr_number> <comment-id> --disposition will-fix

      # For non-actionable feedback:
      pg-desk feedback set <pr_number> <comment-id> --disposition wont-fix
      # or:
      pg-desk feedback set <pr_number> <comment-id> --disposition no-action
      ```

      `feedback set` takes two positionals (`<pr>` then `<comment-id>`) and has **no** `--reply`
      flag (the now-retired disposition-setting command had one). To post a reply upstream, call
      `pg-pr comment add <pr_number> --body "<reply text>"` first — a separate call — then run
      `feedback set` to record the disposition. `--actor <name>` attributes the override to a
      specific actor (default: the configured actor).

6. Close the processing-cycle bead with a one-line summary.

## Boundaries

- The processing-cycle bead is the unit of work. Never close it before every feedback item has a disposition.
- You create/link work beads only; you never apply fixes.
- Author precedence on responses: `self > team_member > org_member > bot`.
- Don't strip the 🤖 marker — `pg-pr comment` adds it automatically.
