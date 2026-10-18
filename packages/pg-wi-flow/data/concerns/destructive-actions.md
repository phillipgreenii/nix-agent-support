# Concern: destructive-actions

Does this item plan an action that destroys or irreversibly alters data, infrastructure or credentials? You are the reviewer for this concern and this concern is the gate: there is no pattern list and no implement-stage block. End your work by recording one verdict for it with record-verdict.

## Questions to ask

1. Does the item plan an action that destroys or irreversibly alters data, infrastructure or credentials?
2. If so, which repo doc (rule, ADR or runbook) authorizes it? Cite it by repo path.

## Evidence needed to answer ready

- No destructive action is planned (say so), or
- A repo doc that authorizes the action is cited by path, and the action falls within what the doc authorizes.

## Verdicts

- ready: no destructive action is planned, or the authorizing doc is cited by repo path. Include the citation in the verdict.
- blocked: a destructive action is planned and no authorizing doc exists. Use block.trigger conflict, and write block.question as a q:conflict question that states the destructive action and that no authorizing doc was found.
