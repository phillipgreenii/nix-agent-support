# Stage: groom

Groom is the entry stage. Unmarked items land here first. Your job is to settle what the item is
from the operator's perspective (not how to build it), then end the stage with exactly one verb.

## 1. Classify

Classify the item's kind and topics.

- Record kind and component with `pg-wi-flow annotate <id> --kind <kind> --component <component>`.
- Apply topic labels directly from `WI_TOPICS`. `annotate` has no topic option, so write each label
  directly with `bd update <id> --add-label topic:<x>`.
- If the kind you chose is `pointer` (its `WI_KINDS` description carries the cue), take the
  pointer path at the end of this file.

## 2. Review

Run the reviewers per the review mode (parallel or batched). Each reviewer covers one concern.
The concerns are:

- **intent**: Is the item clear, single-concept and internally consistent? Is it a duplicate
  (candidates are in `WI_DUPLICATES`)? Does it share a root cause with a related item (same-component
  candidates)?
- **conformance**: Which current docs, rules, specs and processes apply, and are they cited?
  `WI_APPLICABLE_DOCS` is a script-computed candidate list (keyword match of the item's terms over
  rule, ADR and plan titles and headings under the configured docs search roots), so confirm
  citations instead of hunting.
- **secondary-effects**: What else could break or need updating, and what verification would prove
  it did not? If the item creates a scheduled job or service, what happens when its chosen platform
  is down, and was an alternative target considered? Does it create a new external target,
  credential or bucket, and does the existing topology already cover it? Does it arm an alert whose
  underlying signal does not yet exist?
- **destructive-actions**: Does the item plan an action that destroys or irreversibly alters data,
  infrastructure or credentials? If so, which repo doc (rule, ADR or runbook) authorizes it, cited
  by path? Verdict ready with the citation; verdict blocked with a `q:conflict` question when no
  doc exists.
- Plus any concerns that resolve from the configured concern triggers (keyed by topic).

Each reviewer records its own verdict directly, in its own Bash call, with
`pg-wi-flow record-verdict <id> --concern <c> --json <file|->`, rather than returning it to you.

## 3. The round loop

1. Reviewers run (step 2).
2. Call `pg-wi-flow round <id>`. It reads every verdict recorded for the current round from item
   metadata (pass nothing in by hand), merges them, increments the round counter and prints the
   merged verdict: ready, gaps, blocked, or `duplicate <id>` / `related <ids>` when the intent
   verdict says so. When the counter reaches the iteration bound without ready, it prints
   `WI_MUST_ESCALATE=true`.
3. On gaps with `WI_MUST_ESCALATE` unset: run researchers (step 4), annotate the findings, and go
   back to step 1.
4. On `WI_MUST_ESCALATE=true`: run `pg-wi-flow escalate <id> --question "<why>" --trigger q:stall` with the last
   verdicts attached. Consult `pg-wi-flow escalate --help` for the exact syntax.
5. When a concern's verdict was blocked and you escalate because of it, run
   `pg-wi-flow escalate <id> --question "<why>" --trigger <q:...> --concern <that concern's name>` so a destructive-actions block is
   recognizable to `resolve`.

## 4. Research

Run one researcher per named gap. Write the findings back with `pg-wi-flow annotate`: acceptance,
cited docs, chosen version, verification steps and dependents. Where the acceptance depends on a
concrete external target (a URL, host, dashboard or service), also pass `--premise` naming it.

## 5. Verdict

End the stage with exactly one of these verbs:

- `pg-wi-flow close-duplicate` when the item duplicates another.
- `pg-wi-flow merge` when it should be folded into a related item.
- `pg-wi-flow create-child` when the item conflates several concepts. The parent becomes a
  container and stays open; each new child starts at the workflow's default stage.
- `pg-wi-flow advance <id> --to plan` when the item is clear but needs a plan.
- `pg-wi-flow advance <id> --to implement` when the item is small and fully specified. `advance`
  never creates a land item, so an item sent straight to implement from groom simply gets none;
  that is the whole of the trivial-path relief.
- `pg-wi-flow escalate` when it cannot be settled here.
- `pg-wi-flow close --trace` on the pointer path.

Approving a destructive question that has no authorizing doc is a resolver duty, not a groom-time
action.

## Pointer path

A Resume:-style item whose body lists follow-ups is NOT split. Enumerate every bullet in the body.
Close with `pg-wi-flow close <id> --trace "<bullet>=<id|label|filed:<new-id>>"`, one `--trace` per
bullet. The CLI refuses the close until every parsed bullet has a disposition, adds a related link
per traced id, and files any `filed:` item at the workflow's entry stage with the pointer's
priority.
