# Stage: implement

The item says what to do, how to verify, and what to do if something is amiss. Do exactly that, in
`WI_WORKTREE`, and end the stage with exactly one verb.

## Rules

- If `WI_PREMISE_STALE=true`, run `pg-wi-flow escalate <id> --question "<why>" --trigger q:info`
  and do not execute.
- Do exactly what the item says in `WI_WORKTREE`, and verify as it says. Close on the item's
  acceptance criteria.
- Any deviation from the item is an `escalate`.
- No destructive action without a cited authorizing doc. If the item cites none, `escalate` first.

## Land beads

A `kind:land` item is claimed when its blockers close, and its checklist runs the lander for
`WI_WORKTREE`. Before running the lander, check for an open question whose fingerprint matches a
land failure. If one exists, block on it instead of re-running a doomed landing and filing a
duplicate. A land bead that lands closes directly with `pg-wi-flow close`; it skips learn. A
landing conflict is an `escalate`.

## Verdict

End the stage with exactly one of these verbs:

- `pg-wi-flow advance <id> --to learn` when the acceptance is met.
- `pg-wi-flow advance <id> --to verify` when the item requires a post-deploy check.
- `pg-wi-flow close` for a land bead that landed.
- `pg-wi-flow escalate` when anything is amiss.
