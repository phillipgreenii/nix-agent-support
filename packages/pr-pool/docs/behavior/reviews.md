# Reviews

See the [glossary](glossary.md) and [invariants](invariants.md) for shared terms
and IDs.

## Purpose

Define what a **review** is and the shape of its output — independently of which
workflow asked for it. The **substance** of a review (e.g. a code review) should
not vary meaningfully between workflows; only **how a review is handled** (where
it's posted, whether it gates work, whether a human augments it) varies. Defining
the review artifact once lets every workflow — reviewing others' PRs, shepherding
my PRs, and the `INV-WORK-2` do→review→resolve loop — share one format.

## What a review is

A review is a structured set of **comments** about a change, produced by a reviewer
(agent or human), **anchored to the exact change state it was produced against** (so
a re-review after the change advances is unambiguous).

Comments exist at three **scopes**:

- **review** — a comment about the whole review (an overall summary / top-level
  note); there may be more than one.
- **file** — a comment attached to a specific file.
- **block** — a comment attached to a range of lines within a file.

## Output format

A review is expressed as **JSON**. The example below is **illustrative** — it shows
the _shape_ (three scopes, anchoring); the exact field names and the interchange
schema are a downstream concern, not fixed here.

```json
{
  "reviewed_change": "<identifier of the exact change state reviewed>",
  "comments": [
    {
      "scope": "review",
      "body": "Overall solid; two edge cases to cover before merge."
    },
    {
      "scope": "file",
      "path": "auth/token_store.go",
      "body": "Mixes storage and validation; consider splitting."
    },
    {
      "scope": "block",
      "path": "auth/token_store.go",
      "start_line": 40,
      "end_line": 52,
      "body": "Read-modify-write without a lock — racy under concurrent refresh."
    }
  ]
}
```

## Invariants (MUST / MUST-NOT)

- A review **MUST** be expressible in the common format above, regardless of the
  workflow that requested it.
- A review **MUST** be anchored to the exact change state it was produced against,
  so re-review after the change advances is unambiguous.
- When an agent produces the review, every comment **MUST** be marked bot-generated
  (`INV-SEC-2`).

## How a review is handled (varies by workflow — pointers, not rules here)

- **Others' PRs:** posted as an un-submitted/draft review where the provider
  supports it; waits until the PR is out of draft; on re-review the prior pending
  draft is **superseded**, not stacked (keeps one pending review per PR).
- **My PRs:** produced as feedback for processing; may run even while the PR is a
  draft; whether it's also surfaced on the PR is an open question in that workflow.
- **do→review→resolve loop (backlog):** the review feeds the resolve step; a
  "clean / not-clean" outcome gates whether the work is done.

## Open questions

- Should a comment carry **severity / category** (blocking vs. nit vs. question)?
- Should a review carry an overall **verdict** (clean / needs-changes) used by the
  do→review→resolve loop — distinct from any provider review "event," which for
  others' PRs is always left un-submitted (draft)?
- Should comments support **suggested edits** (concrete replacement text)?
- Threading / resolution state — does the artifact track whether a comment was
  addressed, or is that the tracker's job?
