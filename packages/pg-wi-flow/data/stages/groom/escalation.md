# Escalation: groom

This file tells the resolver what it may decide alone, what it must bump, and what it must check
after settling a question.

## Decide alone, or bump

Decide alone whenever the answer follows from the item, the repo's docs or the question text. Use
one of:

- `pg-wi-flow resolve ... --decision <text> --rationale <text>`
- `pg-wi-flow resolve ... --answer <text>`
- `pg-wi-flow resolve ... --abandon --reason-code <moot-premise|superseded|wont-do|duplicate>`

Bump by running `pg-wi-flow escalate` on the question (the question becomes human for the operator)
when only the operator can supply the answer, and ALWAYS for a `q:intent` question.

## Duties

- **E1**: A `q:intent` question can never be settled by the resolver. Bump such a question to the
  operator (escalate on the question).
- **E2**: When you settle a `q:conflict` or `q:intent` on an item that has siblings under the same
  container, check each sibling in `WI_SIBLINGS` for the same stale assumption and annotate a
  correction, replacing the wrong text rather than appending beside it.
- **E3**: Resolve together every other open question sharing the fingerprint
  (`WI_SAME_FINGERPRINT`) rather than independently.
- **E4**: When you approve a destructive-actions question that had no authorizing doc, rely on
  `resolve`'s follow-up (it files the `kind:docs` follow-up at the workflow's entry stage) and do
  not create a second one. When an authorizing doc IS cited, pass
  `resolve --decision ... --authorizing-doc <repo-path>` so no follow-up is filed.
