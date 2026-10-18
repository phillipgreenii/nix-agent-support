# Concern: intent

What is this item, from the operator's perspective, not how to build it. You are the reviewer for this concern; end your work by recording one verdict for it with record-verdict.

## Questions to ask

1. Is the item clear? Can you state in one sentence what the operator wants?
2. Is it a single concept? If it conflates several independent concepts, name them so the worker can split it into children.
3. Is it internally consistent? Do the title, body and acceptance agree with each other, with no contradictory requirements?
4. Is it a duplicate? Check the candidates in WI_DUPLICATES. A candidate that asks for the same outcome is a duplicate.
5. Does it share a root cause with a related item? Check the same-component candidates (WI_RELATED). A candidate that may share a root cause, without being the same request, is related.

## Evidence needed to answer ready

- You can state the operator-facing outcome in one sentence, and the item says the same.
- No contradiction between title, body and acceptance.
- No candidate in WI_DUPLICATES duplicates the item, and no same-component candidate plausibly shares its root cause. Say you checked each candidate.

## Verdicts

- ready: all of the evidence above is present.
- gaps: the intent is unclear or underspecified. Each gap carries a gap (what is missing) and a research question that would close it.
- blocked: only a person can resolve the ambiguity. Use block.trigger intent (or info, conflict or stall where one of those fits better) and put the question in block.question.
- duplicate: the item duplicates a candidate. Put the candidate ids in of.
- related: a same-component candidate may share a root cause with the item. Put the candidate ids in of.
