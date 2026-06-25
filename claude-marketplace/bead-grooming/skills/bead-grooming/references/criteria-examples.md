# Acceptance Criteria — Worked Examples

Read this before writing your first criteria so the shape and altitude are right. The guiding
rule: **specificity should match how well-defined the work is.** Bugs and tasks are
well-defined → concrete, testable criteria. Features and epics usually aren't fully scoped →
high-level outcomes, plus an honest note about what planning still has to settle.

Each criterion should be independently verifiable: someone could pick it up and know
unambiguously whether it's met, without asking you what you meant.

---

## bug — specific and testable

A bug states a wrong behavior that must stop and a correct one that must appear. Criteria read
like a test plan, including a regression guard.

**Bead:** "pa-monitor didn't detect `⏺ API Error: Stream idle timeout`"

```
- [ ] pa-monitor flags a session whose output contains "API Error: Stream idle timeout" within one poll cycle
- [ ] The existing detected error patterns still trigger (no regression)
- [ ] Detection is case-insensitive and matches the line regardless of the leading ⏺ glyph
- [ ] A regression test fixture containing the stream-idle-timeout line is added and passes
```

Why: the wrong behavior (missed pattern) and the correct behavior (flag within a cycle) are
both concrete. The regression test makes "fixed" durable.

---

## task / chore — concrete and bounded

The work is mechanical or well-scoped; criteria name the exact end state.

**Bead:** "Add `--dry-run` flag to the deploy script"

```
- [ ] `deploy.sh --dry-run` prints the actions it would take and exits 0 without mutating anything
- [ ] Running without `--dry-run` behaves exactly as before
- [ ] `--help` documents the new flag
- [ ] A bats test covers the dry-run path
```

---

## feature — high-level outcomes

The feature isn't fully scoped. Describe success from the user's side and what's observably
true when it works. Do **not** invent implementation steps or edge-case rules that haven't been
decided — that's planning's job. Say so explicitly.

**Bead:** "Let users export their dashboard data"

```
- [ ] A user can export the data behind their current dashboard view to a file
- [ ] The export reflects the same filters/date-range the user currently sees
- [ ] The user gets clear feedback when the export is ready or fails

Out of scope for this bead (defer to planning):
- file format(s), size limits, async vs sync delivery
- which roles may export, and any PII handling rules

Note: detailed, testable criteria pending a planning session.
```

Why: the outcome ("user can export what they see") is real and verifiable, but format, limits,
and permissions are genuine product decisions. Pinning them now would be guessing.

---

## epic — outcome-level only

An epic's criteria describe the end state it delivers; the children carry detail.

**Bead:** "Migrate issue storage from SQLite to Dolt"

```
- [ ] All existing issues are readable and writable via the Dolt backend with no data loss
- [ ] Every command that worked on SQLite works on Dolt (parity)
- [ ] There is a documented rollback path if the migration must be reverted
- [ ] The SQLite backend can be removed without breaking any in-tree workflow

Tracked in child beads: schema mapping, data migration, command audit, rollback tooling.
```

---

## Partial + flagged-for-human

You could define some criteria but a real decision blocks the rest. Write what you're sure of,
list pointed questions, add `human`, and **don't** add `has-acceptance-criteria`.

**Bead:** "Improve onboarding" (vague)

Criteria written (the part you're confident about):

```
- [ ] A new user reaches their first successful action without reading external docs
- [ ] Onboarding completion is measurable (an event/metric exists)
```

Notes appended:

```
## Grooming — open questions (blocking acceptance criteria)
- What is "first successful action" for our product — is there a defined activation event?
- Is this onboarding for end users, admins, or both? The flows differ.
- Is there a target activation rate this bead should move, or is it qualitative for now?
```

Then: `bd update <id> --add-label human` (and not `has-acceptance-criteria`).

Why: "reaches a successful action" and "is measurable" are safe outcomes. But _which_ action
and _which_ audience are product calls — guessing would send planning down the wrong path. The
questions are specific enough to answer in a minute.

---

## Quick-capture bead — restructure first, then groom

Shape: no description, a long sentence-like title, type `task` (a `bd q`-style hasty capture).
Restructure before writing criteria.

**Bead (as captured):**

- title: "when I run pn build twice in a row the second still rebuilds everything even though nothing changed and it wastes like ten minutes"
- type: task, description: (none)

**After grooming:**

- title → "pn build rebuilds everything on a no-op second run"
- type → **bug** (it's a defect, not a chore)
- priority → raised to match the impact (repeated 10-min waste), noted in the summary
- description → seeded from the original title: "Original capture: when I run pn build twice in a row the second still rebuilds everything even though nothing changed and it wastes like ten minutes." plus any researched context.
- acceptance_criteria (now a bug → specific):

```
- [ ] A second `pn build` with no intervening change rebuilds 0 derivations
- [ ] The expected rebuild set still triggers when a source file does change
- [ ] The fix is covered so the no-op case stays fast (regression guard)
```

Why: the long title was a brain-dump, and `task`/default-priority were never decisions — so
finishing the thought (real title, right type, honest priority, title→description) is the help
the bead needs, not preservation of placeholders.

## Anti-patterns

- **False precision on a feature** — step-by-step criteria for work nobody has scoped. Creates
  rework when planning decides differently.
- **Vague criteria on a bug** — "the bug is fixed" isn't verifiable. Name the behavior.
- **Restating the title** — "- [ ] the feature works" adds nothing. Each criterion is a
  distinct, checkable fact.
- **Questions that aren't answerable** — "what is this?" wastes the human's time. Ask the
  specific decision that's blocking you.
- **Overwriting the author** — appending researched context is good; replacing their words with
  yours loses intent and trust.
