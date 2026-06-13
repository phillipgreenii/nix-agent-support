---
name: bead-grooming
description: >-
  Use when the user wants to bring their open beads (bd/beads issues) up to plan-ready
  quality — a batch review of the backlog, not working, creating, or scheduling issues.
  Fires on intent to groom, refine, or triage open issues; make them plannable or ready to
  hand to an implementer; judge whether each has enough detail to actually be worked; add
  acceptance criteria to the ones that are clear enough; and flag the too-vague or undecided
  ones for a human to decide. Commonly phrased as prepping before a standup or planning
  session, or doing a backlog-quality sweep — even without the words "groom" or "acceptance
  criteria." Do NOT use for finding which beads are ready to work right now, creating or
  editing a single bead, adding a label to one issue, reviewing code or PRs, measuring test
  coverage, or scheduling automated grooming runs.
---

# Bead Grooming

Turn under-specified open beads into plan-ready ones. For each bead in scope you write
**acceptance criteria** at the right altitude for its type, do light **polish** (title,
type, priority, description) where something is clearly wrong, and — when you genuinely
can't define criteria — leave good partial work plus **specific open questions** and hand
the bead to a human. Groomed beads get the `has-acceptance-criteria` label so the work is
durable and the run is re-entrant.

This is an **autonomous batch** workflow: you process every matching bead in one pass, then
report a summary. You don't pause for per-bead approval — the `has-acceptance-criteria` /
`human` labels and the end-of-run summary are how the user audits what happened.

## Why acceptance criteria, and why the altitude matters

Acceptance criteria are the contract a planner or implementer checks against to know the
work is done. The single most important judgment in this skill is **matching the criteria's
specificity to how well-defined the work actually is.** Forcing precise, step-level criteria
onto a feature that hasn't been scoped invents false certainty that a later planning session
will have to unwind. Leaving a bug's criteria vague makes the fix unverifiable. So:

- **bug** → **specific and testable.** A bug is, by definition, understood: something does X,
  it should do Y. Write criteria that name the exact wrong behavior that must stop, the
  correct behavior that must appear, and ideally a regression guard. These should read like a
  test plan.
- **task / chore** → **concrete and bounded.** State the exact done-state: the file exists,
  the command exits 0, the config key is set, the doc section is added.
- **feature** → **high-level outcomes.** Features usually aren't fully scoped until a planning
  session. Write what success looks like from the user's side and what's observably true when
  it works — not implementation steps that haven't been decided. It's fine, and honest, to add
  a line like "detailed criteria pending planning" and list what's explicitly out of scope.
- **epic** → **outcome-level only.** Describe the end state the epic delivers and the major
  capabilities that must exist. Children carry the detail.

When you're unsure which altitude fits, ask: "Could a reasonable engineer disagree about what
'done' means here?" If the answer is no (bug, task), be specific. If yes (feature, epic), stay
high-level and note what planning still has to settle.

See `references/criteria-examples.md` for worked examples per type — read it before writing
your first few criteria so your output matches the expected shape and bd's `--acceptance`
field conventions.

## The loop

### 1. Find the work

```bash
bd list --status open --exclude-label human,has-acceptance-criteria --limit 0
```

`--exclude-label` drops any bead carrying **either** the `human` label (already escalated) or
`has-acceptance-criteria` (already groomed), so this is the exact "open, un-groomed, not yet
escalated" set. `--limit 0` means no cap. Report the count to the user up front, then work
through them in priority order (P0 first). Use `--json` if you want to script the iteration.

A large batch is fine — the labels make every bead's progress durable, so a re-run picks up
exactly what's left. If the user named a subset (a label, a priority, a single id), narrow the
query accordingly instead of grooming everything.

### 2. Understand the bead

```bash
bd show <id>
```

Read the description, notes, design, comments, type, priority, and any dependencies or
parent/child links. Note what's actually missing for _planning_ — not stylistic nitpicks.

### 3. Research to fill gaps (cheapest, most-relevant first)

Stop as soon as you can write criteria; don't research exhaustively. Per-bead, work outward:

1. **The bead itself** — description, notes, comments, linked spec (`spec_id`), dependencies.
2. **Local project context** — `CLAUDE.md`, `AGENTS.md`, `README`, `docs/`, ADRs. These define
   conventions, constraints, and vocabulary you should mirror.
3. **Related beads** — `bd search "<key terms>"`, the parent/children, and already-groomed
   beads (they're your style template). `bd dep show <id>` for what blocks/relates.
4. **The codebase** — if the bead names files, functions, or errors, look at them.
5. **External sources, when a specific fact is needed** — Notion (PRDs/specs), Slack (where a
   decision was made), web search (library docs, an error string). Reach here only when a
   concrete unknown is blocking a criterion and the source is likely to settle it quickly.

### 4. Write acceptance criteria

```bash
bd update <id> --acceptance "$(cat <<'EOF'
- [ ] <verifiable statement 1>
- [ ] <verifiable statement 2>
EOF
)"
```

Use bd's dedicated `--acceptance` field (not notes) — it's what `bd lint`/`--validate` checks
and what planners read. Format as a checklist of independently verifiable statements. Keep
each criterion something you could hand to someone and they'd know unambiguously whether it's
met. Match altitude to type per the rubric above.

### 5. Polish what's clearly wrong (full polish)

While you have the bead loaded, fix clear defects — but stay conservative. The goal is a
_plannable_ bead, not your preferred phrasing. Same `bd update` call can carry several fields:

- **title** — rewrite vague titles ("fix the thing", "investigate") to be specific and
  outcome-oriented. Almost always safe.
- **description** — when thin, **append** researched context under a clear heading; don't
  overwrite the author's original words. Preserve intent, add clarity.
- **type** — correct obvious mis-types (a "task" that's plainly a bug). Only use types the
  project actually has — check existing beads if unsure; custom types need config and will
  error otherwise.
- **priority** — adjust **only** when clearly inconsistent with stated impact, and lean toward
  leaving it. Priority is usually a human call; when in doubt, leave it and raise it as a
  question rather than changing it.

If a change is judgment-heavy (priority, scope cuts), prefer leaving it and noting it over
imposing it. Record every change you do make in the summary. **Exception:** quick-capture
beads, below — there the restraint is counterproductive.

#### Quick-capture beads — relax the restraint

Some beads are created in a hurry (e.g. `bd q "..."`, or any one-liner capture) and share a
recognizable shape: **no description, an overly long sentence-like title, and the default type
`task`.** Here the title is really a brain-dump, and the type and priority are unconsidered
defaults — not decisions. The conservatism above exists to protect an author's deliberate
choices; a quick-capture bead has none to protect, so the helpful move is to finish the thought
they didn't have time to, not to preserve placeholders. When a bead looks quick-captured:

- **Lift the title into the description.** Seed the description with the original long title,
  then build it out with your research. This keeps the author's words (now as the description)
  while freeing the title to become a real title. (This is the one case where reworking the
  title/description wholesale is right, not the append-only rule above.)
- **Write a succinct title** — a short, specific summary of the problem or change.
- **Reconsider the type freely.** A hurried capture of "X is broken / Y crashes / Z is wrong"
  is almost always a bug the author didn't stop to retype. Set the type that actually fits.
- **Reconsider priority with more latitude.** A default priority on a quick capture reflects no
  judgment, so set it to match the impact the description implies (a crash or data-loss capture
  isn't really a P2-by-default). Still note the change in the summary.

Then write acceptance criteria for the now-clarified bead as usual, at the altitude its (new)
type warrants.

### 6. Mark it groomed

```bash
bd update <id> --add-label has-acceptance-criteria
```

You can fold this into the criteria/polish update:
`bd update <id> --acceptance "..." --title "..." --add-label has-acceptance-criteria`

Only add this label when the bead truly has usable acceptance criteria at the right altitude.

## When you can't define criteria — flag for a human

Sometimes even high-level criteria require a decision you can't make: the intent is genuinely
ambiguous, "done" depends on a product/design choice, or a critical fact isn't knowable from
any available source. When that happens, **do the partial work, then escalate** — don't guess.

1. Write whatever criteria you _can_ (partial criteria are valuable).
2. Append your open questions as notes — **specific and answerable**, not "what is this?":

   ```bash
   bd update <id> --append-notes "$(cat <<'EOF'
   ## Grooming — open questions (blocking acceptance criteria)
   - Should X cover case Y, or is Y out of scope for this bead?
   - What's the expected behavior when Z fails — retry, error, or skip?
   EOF
   )"
   ```

   Use `--append-notes`, never `--notes`, so you don't clobber existing notes.

3. Add the `human` label:

   ```bash
   bd update <id> --add-label human
   ```

4. **Do not** add `has-acceptance-criteria` — it's not done.

The `human` label is the project's escalation queue (`bd human list`). By attaching concrete,
pointed questions you turn a vague "needs attention" into something the human can answer in a
minute, then a future grooming run finishes it.

A bead can be **both** partially groomed and flagged: write the criteria you're confident in,
leave the rest as questions, add `human`, and skip `has-acceptance-criteria`.

### Planning / "plan out" / design tasks

A bead whose deliverable is itself a _plan_ (titles like "plan out X", "design Y", "figure out
how to Z") is a common trap. You can almost always frame a tidy "produce a design that answers
these questions" deliverable and feel done — but if the task hinges on **undecided intent or
motivation** (_why_ this work, _whether_ to do it, who it's for, what "good" looks like), then
that framing just defers those decisions into a planning session that may head the wrong way.

So: when a planning task's intent isn't established in the bead or derivable from project
context, treat the undecided intent as the blocker. Write the high-level shape of the plan as
partial criteria, put the intent/motivation questions in notes, add `human`, and **withhold**
`has-acceptance-criteria` — the human sets direction before this becomes plan-ready. Reserve a
groomed label for planning tasks whose goal is already clear and where only the technical
approach remains to be worked out. When in doubt on a "plan out" bead, flag the human; a
plausible-but-wrong design deliverable is more expensive than a quick intent check.

## End-of-run summary

After the batch, give the user a compact report so they can audit without opening every bead:

```
Groomed N beads (M flagged for human):

| id        | type    | action                                    |
|-----------|---------|-------------------------------------------|
| pg2-abc   | bug     | criteria added (specific); priority P3→P2 |
| pg2-def   | feature | criteria added (high-level)               |
| pg2-ghi   | task    | criteria added; title clarified           |
| pg2-jkl   | feature | PARTIAL + flagged human — 2 open questions |

Flagged for human (bd human list):
- pg2-jkl: needs a product call on whether anonymous users are in scope.
```

List anything you deliberately left alone and why (e.g., priority you chose not to touch).

## Safety and idempotency

- The selection query already skips groomed and escalated beads, so **re-running is safe** and
  resumes where you left off.
- **Never close beads** and never change status — grooming readies work, it doesn't do it.
- **Never** add `has-acceptance-criteria` to a bead you flagged `human`.
- Always `--append-notes`, never `--notes` (which replaces). Preserve the author's description;
  add, don't overwrite.
- Avoid `bd edit` (it opens an interactive editor and will hang).

## Command quick reference

| need                       | command                                                                               |
| -------------------------- | ------------------------------------------------------------------------------------- |
| find un-groomed open beads | `bd list --status open --exclude-label human,has-acceptance-criteria --limit 0`       |
| inspect a bead             | `bd show <id>`                                                                        |
| find related beads         | `bd search "<terms>"`                                                                 |
| write criteria             | `bd update <id> --acceptance "..."`                                                   |
| mark groomed               | `bd update <id> --add-label has-acceptance-criteria`                                  |
| append questions           | `bd update <id> --append-notes "..."`                                                 |
| flag for human             | `bd update <id> --add-label human`                                                    |
| polish fields              | `bd update <id> --title "..." --type <t> -p <n> --description "..."`                  |
| do it all at once          | `bd update <id> --acceptance "..." --title "..." --add-label has-acceptance-criteria` |
