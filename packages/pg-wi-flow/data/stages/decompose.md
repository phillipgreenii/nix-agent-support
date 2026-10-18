# Stage: decompose

Decompose turns a planned item that is several pieces of work into self-contained children, each
at `stage:implement`, and decides how each worktree lands. The item was planned, so its `design`
says how; your job is to split that design into implementable items and end the stage with exactly
one verb.

## 1. Write the children

Create one child per implementable item:

`pg-wi-flow create-child <parent> --title "<title>" --stage implement --description "<text>"`

Add one `--blocked-by <child>` per ordering edge (repeat the option for several blockers) so a
child is never ready before what it depends on. The parent becomes a container and stays open.

Every child description MUST be self-contained, because the implementer that claims it has no
concerns of its own and will not read the parent. It carries:

- **What to do**: the exact files, paths and changes, so nobody has to look anything up. Cite each
  applicable doc by repo path.
- **Ordering**: what it waits for (mirrored by its `--blocked-by` edges) and what waits for it.
- **Worktree assignment**: which worktree the work happens in.
- **Verification**: the commands or checks that prove the item is done, and the acceptance
  criteria to close on.
- **If amiss**: what to do when something does not match the description (normally `escalate`
  rather than improvise).

Keep each child small enough for one implementer to finish in one pass. Children that touch the
same files MUST be ordered with a `--blocked-by` edge; children that touch disjoint files stay
unordered so they can run in parallel.

## 2. Land beads

Decide landing per worktree. For each worktree that will land through a land bead, create exactly
one:

`pg-wi-flow create-child <parent> --title "land <slug>" --kind land --stage implement --blocked-by <child>...`

with one `--blocked-by` per child in that worktree. A worktree whose work commits directly has no
land bead. `advance` never creates a land bead; only this decision does. Land a multi-repo set one
worktree at a time with a land bead each; this stage does not design any other mechanism.

## 3. Instructions for the parent

A decomposed parent already exists, so its "when the children are complete" instructions are
written with the content-writing verb:

`pg-wi-flow annotate <parent> --append-description "<text>"`

Say what to check once every child has closed and what closing the parent requires. When the last
child closes, a worker reads these instructions and closes the parent on its own judgement, or
escalates if unsure.

## 4. Review

Run the reviewers per the review mode (parallel). Each reviewer covers one concern, applied per
child. The concerns are:

- **decomposition**: Is each child self-contained, sized to one pass, file-disjoint from its
  unordered siblings, covered by dependency edges, and described well enough to act on?
- **conformance**: Which current docs, rules, specs and processes apply to the child, and are they
  cited? `WI_APPLICABLE_DOCS` is a script-computed candidate list.
- **secondary-effects**: What else could the child break or need updating, and what verification
  would prove it did not?
- **destructive-actions**: Does the child plan an action that destroys or irreversibly alters data,
  infrastructure or credentials, and does it cite the repo doc that authorizes it?
- Plus any concerns that resolve from the configured concern triggers (keyed by topic).

A reviewer is pointed at the PARENT item. It runs `pg-wi-flow explain` and `pg-wi-flow context` on
each child and records ONE verdict per concern for the parent, with
`pg-wi-flow record-verdict <parent> --concern <c> --json <file|->`. Its gaps entries name the
failing child ids; ready means no child fails that concern. A child that violates a rule is caught
here, before it reaches an implementer.

## 5. The round loop

1. Reviewers run (step 4).
2. Call `pg-wi-flow round <parent>`. It reads every verdict recorded for the current round, merges
   them, increments the round counter and prints the merged verdict: ready, gaps or blocked. When
   the counter reaches the iteration bound without ready, it prints `WI_MUST_ESCALATE=true`.
3. On gaps with `WI_MUST_ESCALATE` unset: repair each named child with
   `pg-wi-flow annotate <child> ...` (replace wrong text rather than appending beside it), run
   researchers where a gap needs facts, and go back to step 1.
4. On `WI_MUST_ESCALATE=true`: run `pg-wi-flow escalate <parent> --question "<why>" --trigger q:stall`
   with the last verdicts attached. Consult `pg-wi-flow escalate --help` for the exact syntax.

**E5**: when you escalate because a concern's verdict was blocked, pass the concern's name:
`pg-wi-flow escalate <parent> --question "<why>" --trigger <q:...> --concern <that concern's name>`.

## 6. Verdict

End the stage with exactly one of these verbs:

- `pg-wi-flow advance <parent> --to implement` when every concern is ready. The parent is already a
  container, and it stays open until its children are complete.
- `pg-wi-flow escalate` when the review is blocked or the decomposition cannot be settled here.
