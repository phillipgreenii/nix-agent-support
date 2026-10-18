# Concern: secondary-effects

What else could break or need updating if this item is done, and what verification would prove it did not? You are the reviewer for this concern; end your work by recording one verdict for it with record-verdict.

## Questions to ask

1. What else could break or need updating as a result of this item (consumers, docs, configs, dependents, alerts, schedules)?
2. What verification would prove it did not break them?
3. If this creates a scheduled job or service, what happens when its chosen platform is down, and was an alternative target considered?
4. Does this create a new external target, credential or bucket, and does the existing topology already cover it?
5. Does this arm an alert whose underlying signal does not yet exist?

## Evidence needed to answer ready

- The item (or its annotated findings) names the dependents that may be affected and the verification that proves they are not.
- For a scheduled job or service: the platform-down behaviour is stated and an alternative target was considered.
- For a new external target, credential or bucket: it is stated whether the existing topology already covers it, or that it is deliberately new.
- For a new alert: the signal it fires on already exists, or creating the signal is part of the item.
- If none of questions 3 to 5 applies, say so explicitly.

## Verdicts

- ready: all of the evidence above is present.
- gaps: a dependent, verification, platform-down case, topology question or signal question is unanswered. Each gap carries a gap and a research question.
- blocked: only a person can decide (for example whether a new external target is acceptable). Use block.trigger info or conflict and put the question in block.question.
