# Stage: plan

Plan decides how, at the decision level. The item was groomed, so it says what. Your job is to pick
an approach and end the stage with exactly one verb.

## 1. Research and decide

Research repo standards and external practice. `WI_APPLICABLE_DOCS` is again your candidate list of
docs; confirm and extend it rather than hunting. Pick an approach from 2-3 candidates with their
trade-offs, and record the choice and the trade-offs in `design` with
`pg-wi-flow annotate <id> --design <text>` (consult `pg-wi-flow annotate --help` for the exact
syntax). Then decide whether the work is one item or several.

## 2. Land bead (single-item plan)

For a single-item plan, also decide whether the work will land through a land bead. If so, create
it before advancing:

`pg-wi-flow create-child <item> --title "land <slug>" --kind land --stage implement --blocked-by <item>`

A direct-commit change simply has none. `advance` never creates a land bead; only this decision
does.

## 3. Review

Run one reviewer for the concern **sufficiency**: does the plan follow established patterns and
omit nothing the groomed item implies? The reviewer records its verdict itself, in its own Bash
call, with `pg-wi-flow record-verdict <id> --concern sufficiency --json <file|->`. Then call
`pg-wi-flow round <id>`, which merges the recorded verdict and prints ready, gaps or blocked. On
gaps with `WI_MUST_ESCALATE` unset, run one researcher per named gap, annotate the findings, and
review again. On `WI_MUST_ESCALATE=true`, run
`pg-wi-flow escalate <id> --question "<why>" --trigger q:stall` with the last verdicts attached.

**E5**: when you escalate because a concern's verdict was blocked, pass the concern's name:
`pg-wi-flow escalate <id> --question "<why>" --trigger <q:...> --concern <that concern's name>`.

## 4. Verdict

End the stage with exactly one of these verbs:

- `pg-wi-flow advance <id> --to decompose` when the plan is reviewed ready and the work is several
  items.
- `pg-wi-flow advance <id> --to implement` when the plan is reviewed ready and the work is one
  item.
- `pg-wi-flow escalate` when the review is blocked or the plan cannot be settled here.
