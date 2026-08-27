---
name: pg-pr-write-pr-description
description: Generate or update a pull-request description from the diff, commits, and any existing body. Use when the user asks to "create a PR", "create a pull request", "open a PR for this", "write a PR description", "write a PR body", "generate PR description", "update the PR description", "rewrite the PR body", or when the agent autonomously decides to open or refresh a PR.
---

# pg-pr write PR description

This skill is the **canonical prompt** for synthesising a PR description.
The same workflow runs whether the trigger is a direct claude session,
an agent inside a gascity session, or the CLI shelling out via
`pg-pr pr create --generate-description` (which invokes `zr-agent`
underneath, which then loads _this_ SKILL.md).

The agent that runs this skill is responsible for:

1. Gathering context via the `pg-pr` CLI (which already exposes the
   data — this skill stays thin).
2. Synthesising a description that matches the repository's PR
   template, falling back to a generic structure.
3. Emitting the body via the `--body-stdin` variant of `pg-pr pr
create` or `pg-pr pr update`.

The 🤖 marker is appended by the CLI; the skill does not add it.

## Step 0 — read the shared reference

This skill shares its context-gathering and content-rule guidance with
the sibling `pg-pr-write-pr-title` skill. Read it first:

```bash
cat ~/.local/share/pgii-local-plugins/pg-pr/lib/pr-generation-shared.md
```

Everything below assumes you've done so — this skill only adds what's
specific to writing a **PR body**: which template to follow and how the
body itself is structured.

## Step 1 — pick a template

In priority order:

1. `.github/PULL_REQUEST_TEMPLATE.md` in the current repo. If it
   exists, fill out every section it defines.
2. `docs/PULL_REQUEST_TEMPLATE.md` or `.github/pull_request_template.md`
   (case variants).
3. The generic structure below.

### Generic structure (fallback)

```markdown
## Summary

<1-3 sentences describing why this change exists. Lead with the
problem or goal, not the implementation.>

## Changes

- <area>: <one-line change>
- <area>: <one-line change>

(Group by directory or domain; one bullet per logical change. Use the
commit subjects + file paths from `pr files` / `pr commits` as input.)

## Test plan

- [ ] <step the reviewer or CI should verify>
- [ ] <next step>
```

## Step 2 — body-specific content rules

Beyond the shared reference's Include/Exclude/Tone rules, a body also
needs a test plan the reviewer can run (or has been run) — don't
fabricate test steps. If there's no obvious test plan, write
`- [ ] Manual verification: <what the reviewer should check>` and
surface the gap to the user.

## Step 3 — emit the body

**Always** use `--body-stdin` and pipe the description in. Do **not**
inline the body via `--body` — Markdown with newlines round-trips
cleanly through stdin and reliably triggers the CLI's body-source
mutual-exclusion check.

### Creating a new PR

```bash
pg-pr pr create \
  --repo <owner/name> \
  --title '<concise title>' \
  --head <branch> \
  --body-stdin <<'BODY'
## Summary

<...>

## Changes

- <...>

## Test plan

- [ ] <...>
BODY
```

By default the PR opens in draft. Pass `--no-draft` only when the
user explicitly asks for a ready-for-review PR.

### Updating an existing PR

```bash
pg-pr pr update <pr> \
  --repo <owner/name> \
  --body-stdin <<'BODY'
## Summary

<...>

## Changes

- <...>

## Test plan

- [ ] <...>
BODY
```

## Step 4 — what NOT to do

Beyond the shared reference's "don't call merge/automerge, don't post
comments or reviews from either skill" rules:

- Don't strip the 🤖 marker; the CLI appends it automatically.

## When called via `pg-pr pr create --generate-description`

The CLI invokes you through `zr-agent` and **expects the PR body —
and only the PR body — on stdout**. No commentary, no shell prompts,
no "Here's the description:" preamble. The CLI captures stdout
verbatim and feeds it back into `pg-pr pr create` / `pg-pr pr update`
as the `--body-stdin` payload, then attaches the 🤖 marker.

If you need to ask the user a clarifying question, write the
question to stderr and exit non-zero — do not put it on stdout.
