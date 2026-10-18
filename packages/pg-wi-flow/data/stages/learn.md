# Stage: learn

Every work item except the kinds in `learn.skip_kinds` passes through learn before close. Your job
is to capture what was learned, then close.

## Steps

1. Read the verdict trail with `pg-wi-flow history <id>` and read the item's notes.
2. Record any durable lesson, that is, what was ruled out and should not be tried again, with
   `bd remember "<what was ruled out>" --key lesson-<component>-<topic-slug>`, where `<component>`
   is the item's component value and `<topic-slug>` is a short lowercase `[a-z0-9-]` slug. The
   component-keyed prefix is what `WI_LESSONS` reads back for later items of the same component.
3. If there is no durable lesson, record "nothing to learn" with `pg-wi-flow annotate <id> --append-notes "nothing to learn"`.
4. End the stage with `pg-wi-flow close <id>`.

Escalate with `pg-wi-flow escalate` only if the item cannot be closed.
