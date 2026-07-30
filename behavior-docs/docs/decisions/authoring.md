# Authoring — decision docs for the behavior-docs method

Realization decisions about how the behavior docs themselves are organized, written, and
cross-referenced — by humans and by agents.

### `IMPL-2` — Author behavior docs as a cross-linked markdown wiki <!-- uuid: 2e66e8b3-0cbb-4fad-b87b-564ded2a8a59 -->

_Captured 2026-07-16. Not yet decided — the `IMPL-` prefix carries that; promotion to
`DEC-AUTHOR-1` will preserve the UUID._

## Context

The behavior-docs method stays layout-agnostic: it prescribes no file layout and treats the
one it uses as illustrative (`INV-10` floor; README "layout is illustrative"). _How_ the docs
are organized and cross-referenced for reading and writing — including by agents — is a
realization decision, so it lives here rather than in the method.

The only behavioral requirement the method makes is that references are navigable and
checkable for agreement (`INV-8` / `INV-18`). This entry chooses a mechanism that satisfies it.

**Open points to settle**

Author behavior docs as a cross-linked markdown wiki — a page per concept with backlinks,
in the "LLM-wiki" style that aids both human and agent reading/writing. Details (tooling,
link conventions, generation/validation) to be worked out.

## Consequences

To be documented once decided.
