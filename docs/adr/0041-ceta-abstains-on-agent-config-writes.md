# CETA abstains on agent-config writes; the verdict belongs to Claude Code

**Status**: Accepted (resolves `pg2-pggza`)
**Date**: 2026-07-29
**Deciders**: Phillip Green II

## Context

A sweep of all 320,746 logged rows (bead `pg2-yyoug`) found only **7 rows** in the whole corpus
that combine an explicit recorded denial reason with a current `allow` replay. Five are
`"Classifier unavailable"` infrastructure timeouts. The remaining **two are genuine false-allows,
and both are the same class** — the agent writing its own configuration or instruction files:

| row      | target                              | recorded denial reason                                                                                       | replays |
| -------- | ----------------------------------- | ------------------------------------------------------------------------------------------------------------ | ------- |
| `132474` | Write `.claude/settings.local.json` | "Modifying `.claude/settings.local.json` is Self-Modification of agent config — not authorized"              | `allow` |
| `273301` | Write `.claude/rules.md`            | "[Self-Modification] Creating a new `.claude/rules.md` agent-instruction file that steers future sessions …" | `allow` |

Two further rows of the same class, `39391` and `57580`, are both `.claude/settings.local.json`
edits and both `allow` today. Four rows, one class, all auto-approved.

The decision comes from `path-safety`, confirmed live against the current binary:

```
claude-extended-tool-approver: path-safety -> approve: path allows write:
  /Users/phillipg/phillipg_mbp/.claude/settings.local.json
{"permissionDecision":"allow", …}
```

`.claude/` sits inside a project the user can write, so `PathEvaluator.Evaluate` reports
`CanWrite()` and `pathsafety` approves. Two facts make that consequential:

- **`allow` suppresses the prompt entirely**, so Claude Code's `auto_mode_classifier` never runs
  on these calls. The classifier is not "letting them through" — it is never asked. It denied all
  four rows on the occasions it _was_ asked, so the layer that would catch this class is being
  bypassed by CETA's own approval.
- An agent that can silently write `.claude/settings.local.json` can **grant itself permissions
  for every subsequent call**, and one that can create `.claude/rules.md` can steer all future
  sessions. Both are privilege-escalation shaped.

The existing config mechanism cannot express a fix. `sandbox.filesystem.denyWrite` (already in
active use, 14 entries) matches by **subtree containment only** —
`strings.HasPrefix(path+"/", dir+"/")`, no globs — so it can block all of `~/.claude` (which would
also block the memory directories, skills, plugins, and transcripts) or take one absolute entry per
project (missing every future clone). It cannot say "the settings files but not the rest of
`.claude/`".

## Decision

**CETA MUST NOT approve a write to an agent-config or agent-instruction file under `.claude/`. It
MUST abstain, deferring the verdict to Claude Code — the interactive prompt, or the
`auto_mode_classifier` in auto mode.**

CETA encodes **no verdict** of its own here. This is a division of responsibility, not a new
security rule:

- **CETA's obligation is to not take the decision away.** Returning `allow` removes the only layer
  that has ever judged this class. Abstaining restores it.
- **Claude Code owns the verdict.** It has the context CETA lacks — whether this write was part of
  what the user asked for. The four logged rows show its classifier reaching the right answer when
  consulted; the defect was never its judgement, only that it was never consulted.

Reads are unaffected — this covers writes (`Write`, `Edit`, `MultiEdit`, `Delete`) only. The
scope is both project-local `.claude/` and the user-global `~/.claude/`, because the escalation is
identical in each.

### Implementation constraint — the obvious approach is a silent no-op

`Abstain` means **"continue to the next rule"**, not "withhold the decision". The engine's loop is
`if result.Decision != hookio.Abstain { return result }`. So **a new rule placed ahead of
`pathsafety` that returns `Abstain` changes nothing** — the chain continues and `pathsafety`
approves exactly as before. Such a change would look structurally correct, pass a review, register
in the rule chain, and still leave all four rows at `allow`.

The carve-out therefore MUST live **inside `pathsafety`**, so that `pathsafety` itself abstains
instead of approving. Its abstain reason string is already the right one — `"(deferred to
claude-code)"` — which is precisely this decision. A rule ahead of the chain is only viable if it
returns something **decisive** (`Ask`), and that would be encoding a verdict, which this decision
declines to do.

## Consequences

- **The four rows' ground truth is `abstain`, not `allow`.** They were deliberately left
  unannotated by `pg2-yyoug` because annotating either way would have pre-committed this decision;
  they can now be annotated. No row moves toward `allow` — they move away from it, which is the
  direction the sweep's acceptance criteria require.
- **Bypass mode still passes through.** This is an accepted limitation, stated plainly: the
  protection is a deferral, and a mode whose purpose is to skip deferral will skip it. Anyone
  running bypass mode in a repo whose `.claude/` matters is outside what this decision protects.
- **The protection is only as strong as the classifier on any given call.** Five of the seven rows
  in the sweep were `"Classifier unavailable"` timeouts, so the layer this decision defers to is
  known to be occasionally absent. That is a weaker guarantee than a rule in CETA would give, and
  it is the trade-off being accepted in exchange for CETA not encoding a policy it lacks the
  context to judge.
- **Interactive sessions gain a prompt where they previously had none.** Agent edits to
  `.claude/settings.local.json` will now surface to the user. This is the intended effect, and it
  will be visible: `settings.local.json` writes are not rare.
- This decision is narrower than the `approvedCommands` stance in ADR 0040. There, the unit of
  trust is deliberately coarse because argument-awareness is impractical. Here the file being
  written is already known exactly, so no comparable impracticality applies — the reason CETA
  stays out is that it lacks the _intent_ context, not that it lacks the information.
