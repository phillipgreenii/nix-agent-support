# Shepherding my PRs to merge

**Status:** Living source of truth (per-project overlay). See the
[glossary](glossary.md) and [invariants](invariants.md) for shared terms and IDs.

## Purpose

How the system carries a change **I authored** from creation to landed with as
little of my involvement as possible — implementing it, keeping it current,
addressing feedback, and stopping at the line only I may cross. I step in only when
the work genuinely needs a human.

## Actors

- **Me** — author and, for PR-driven repos, the merge/release authority.
- **Work agent** — makes the changes and addresses feedback.
- **Review agent** — gives my change a first-pass review too, in the common review
  format ([`reviews.md`](reviews.md)). _(The **format** is shared; the handling
  differs from others' PRs — see the draft note below.)_
- **The system** — tracks the change, runs agents, gates work, surfaces state.

## Integration style decides how it lands (`INV-AUTH-2`)

The merge model is **per-repo configuration**, not universal:

- **PR-driven** (a GitHub-style PR flow, the common case): a change becomes a PR; it
  lands only when its gates are satisfied — **CI green + required approval** — **and
  I grant explicit permission**. An agent **MUST NOT** merge without that;
  **automerge is off by default**.
- **merge-to-main** (e.g. some personal repos): an agent handles the whole thing —
  work in a worktree, **rebase onto main, ff-merge back to main** — no human merge
  step. Fully agent-handled.

## User stories

- The system works my change **until it lands**, escalating only when it truly
  needs me.
- My PRs **start as draft** and become ready automatically once CI is green.
- Incoming feedback is **addressed automatically**, respecting **me > human > agent**
  (`INV-AUTH-1`).
- The PR **title and description always reflect** its current state.
- Large changes are broken into **stacked PRs**, tracked, with downstream PRs
  rebased as upstream ones merge (`INV-WORK-3`).
- The agent's first-pass review of my own change is folded into feedback processing.
- Agents **raise a concern to me** whenever they're unsure (`INV-AUTH-3`).

## Journey

```mermaid
flowchart TD
    create["I (or an agent) create the change + tracking object"] --> style{"integration style?"}
    style -->|PR-driven| draftstate["open PR as DRAFT"]
    style -->|merge-to-main| wtree["work in a worktree"]

    draftstate --> ci{"CI green?"}
    ci -->|no| work["work agent iterates"] --> ci
    ci -->|yes| ready["mark PR ready (non-draft)"]
    ready --> fb{"unresolved feedback?"}
    fb -->|yes| resolve["address (me>human>agent); keep title/description current"] --> fb
    fb -->|no| ok{"approved + all resolved?"}
    ok -->|yes| gate["awaiting my explicit merge permission (no automerge)"]
    gate --> merge["merge on my grant"]

    wtree --> wci{"tests green?"}
    wci -->|no| work
    wci -->|yes| ff["rebase onto main → ff-merge to main (agent-handled)"]

    work -.ambiguous / conflicting.-> escalate["raise a concern → NEEDS ME (INV-AUTH-3)"]
    subgraph stack["stacked PRs (INV-WORK-3)"]
      up["upstream PR merges"] --> rebase["rebase downstream PR(s)"] --> ci
    end
```

## Invariants (this workflow)

- Merge authority follows the integration style (`INV-AUTH-2`): PR-driven → no
  agent merge without my explicit per-change permission, automerge off by default;
  merge-to-main → agents may complete the rebase + ff-merge.
- A PR **MUST** start as a draft and become non-draft only once CI is green.
- Feedback resolution **MUST** follow **me > human > agent**; a **live** human
  conflicting with an **earlier** note of mine **escalates**, not silently
  overridden (`INV-AUTH-1`).
- The PR's title and description **MUST** reflect its current state.
- In a stack, downstream work **MUST** be gated on upstream merging.
- Shared by reference: never lose work `INV-CONT-1`; stuck→park `INV-CONT-2`;
  one tracking object `INV-TRACK-1`; role-scoped claim `INV-CLAIM-1`.

**Decision (2026-07-09):** unlike others' PRs, the agent's first-pass review of
**my** PR **MAY run while the PR is still a draft** — rationale: it speeds my change
toward ready. This is a deliberate divergence from "reviews wait until non-draft"
(which stays true for others' PRs). _(Candidate for an ADR.)_

## Example — my changes in flight (glance-view)

Same surface as the review glance-view, filtered to "mine." `NEEDS ME` pinned on
top; `→` states sort actionable-first. Illustrative:

```
as of 12:04  (fresh)
NEEDS ME (1)
  #1440  search: split ranker   stack rebase conflict on ranker.go:88–140
         tried: auto-rebase onto merged base → conflict   → reply: resolve | drop from stack

MINE (2)
  #1438  search: extract scorer (base)   READY  CI ✓  feedback 2 (1 human)  → awaiting your merge OK (green + approved + resolved)
  #1440  search: split ranker (1/3)       DRAFT  CI ✗  feedback 0            → work: fix failing CI
```

## Usage scenarios

- **See my changes in flight:** the "mine" glance-view. Verify a new PR appears as
  DRAFT within one sync cycle.
- **Grant merge (PR-driven):** from the "awaiting your merge (N)" list — each row
  states _why_ it's mergeable (green + approved + all resolved) so one action grants
  it, and a batch "approve all" exists for the common case. Verify nothing merges
  before I grant, and automerge is never enabled.
- **Watch a merge-to-main repo land itself:** verify the agent rebases onto main and
  ff-merges with no human merge step.
- **Watch a stack advance:** merge the base; verify the next PR rebases and unblocks.

## Failure conditions

- **CI regresses after ready:** the PR reverts to draft and re-enters the work loop;
  persistently failing/flapping CI is a stuck condition → park (`INV-CONT-2`).
- **Conflicts with the base branch (non-stacked):** attempt an automatic
  rebase/merge; if not confidently resolvable, escalate (`INV-AUTH-3`).
- **Rebase conflict on a stacked PR:** escalate.
- **I push commits to / edit the title-description of my own PR mid-flight:** the
  agent **MUST** detect head/metadata changes it didn't author and rebase onto /
  defer to them, never clobber my changes.
- **Stuck / usage limit / non-retryable:** park-and-continue / pause / escalate per
  `INV-FAIL-1`.

## Open questions

- Should the agent's first-pass review of **my own** PR be **exposed to the
  provider** (as a draft on the PR) or kept internal to feedback processing?
- How is "**explicit permission to merge**" granted (a tracker action, a label, a
  reply)? — the highest-frequency human touch; the UX matters most here.
- Exact "CI green → mark ready" trigger: all checks, or a required subset?
- Who initiates a stack, and when (up front vs. when a PR grows too large)?
