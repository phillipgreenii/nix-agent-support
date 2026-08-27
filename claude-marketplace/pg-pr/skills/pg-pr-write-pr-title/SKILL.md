---
name: pg-pr-write-pr-title
description: Generate a concise pull-request title from the diff, commits, and branch. Use when the user asks to "generate a PR title", "write a title for this PR", "suggest a PR title", "come up with a title", or when the CLI shells out via `pg-pr pr create --generate-title`.
---

# pg-pr write PR title

This skill is the **canonical prompt** for synthesising a PR title. It
runs whether the trigger is a direct claude session, an agent inside
another session, or the CLI shelling out via
`pg-pr pr create --generate-title` (which invokes the same agent CLI
`pg-pr-write-pr-description` uses, and loads _this_ SKILL.md).

`pg-pr pr create --generate-title` is create-only. `pg-pr pr update`
never registers this flag: it has no `--title` parameter to piggyback
on at all, unlike `--generate-description`'s body-only update path.
`--generate-title` works standalone — it does not require
`--generate-description` — and, when both are passed together, each
generator runs as its own independent invocation.

## Step 0 — read the shared reference

The context-gathering and content-rule guidance both PR-generation
skills need lives in one place, not copied into each SKILL.md. Read it
before continuing:

```bash
cat ~/.local/share/pgii-local-plugins/pg-pr/lib/pr-generation-shared.md
```

Skip its "existing PR body" context step — title generation only ever
runs at PR **creation**, so there is no existing title to revise.

## Step 1 — write the title

Short: one line, no wrapping. Register matches the repository's recent
PRs (see the shared reference's tone guidance) — either imperative
("Add X", "Fix Y") or descriptive, whichever the repo's own recent PR
titles favor; when in doubt, prefer imperative. No trailing period
unless the title genuinely reads as a full sentence that needs one. No
markdown formatting: no backticks, no bold, no leading "PR:" or
"Title:" label, no surrounding quotes.

## Step 2 — use the title

### Direct invocation (this skill is opening the PR itself)

Pass the generated title straight to `pg-pr pr create`:

```bash
pg-pr pr create \
  --repo <owner/name> \
  --title '<generated title>' \
  --head <branch> \
  --body-stdin <<'BODY'
<... PR body, e.g. via pg-pr-write-pr-description, or your own summary ...>
BODY
```

### Shelled out via `pg-pr pr create --generate-title`

See "When called via `pg-pr pr create --generate-title`" below — in
this mode you do not call `pg-pr pr create` yourself at all; you only
emit the title text.

## What NOT to do

The shared reference's human-only-verb and no-comments-or-reviews rules
apply here too. On top of those:

- Don't wrap the title in quotes or markdown formatting.
- Don't invent a title that contradicts the actual diff/commits — if
  the change's intent is genuinely unclear, write the most literal,
  descriptive title you can rather than guessing at intent.

## When called via `pg-pr pr create --generate-title`

The CLI invokes you through the configured agent CLI and **expects the
title, and only the title, on stdout**. No commentary, no shell
prompts, no "Here's the title:" preamble, no surrounding quotes, no
trailing period unless the title itself ends one, no markdown
formatting. The CLI captures stdout verbatim (after trimming
leading/trailing whitespace) and passes it to `pg-pr pr create` as
`--title`.

If you need to ask the user a clarifying question, write the question
to stderr and exit non-zero — do not put it on stdout.
