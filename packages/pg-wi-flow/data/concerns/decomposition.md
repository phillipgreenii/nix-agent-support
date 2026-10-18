# Concern: decomposition

Are the children of this decomposed item good implementable items? You are the reviewer for this concern; end your work by recording one verdict for it with record-verdict.

You are pointed at the parent item. Run explain and context on each child, judge each child per child, and record ONE verdict for the parent. Name the failing child ids in the gaps entries.

## Questions to ask

1. Self-containment: could an implementer act on each child per child without reading the parent or looking anything up? Does it inline file paths, the exact changes, and cite applicable docs by repo path?
2. Context budget: is each child small enough for one implementer to finish in one pass, or should it be split?
3. File-disjointness: do children that are not ordered against each other touch disjoint files? Do children that touch the same files have a blocked-by edge between them?
4. Dependency coverage: is every ordering requirement (something a later child uses that an earlier child creates) expressed as a blocked-by edge, with no cycle and no edge to a child outside the parent?
5. Description quality: does each child carry its ordering, worktree assignment, verification (with acceptance criteria) and if-amiss instructions, with concrete changes rather than vague phrases?
6. Coherence: does each child leave the codebase working when it lands, or does it depend on a sibling in a way the edges do not capture?
7. Landing: does every worktree that lands through a land bead have exactly one, blocked by every child in that worktree?
8. Parent: does the parent carry its "when the children are complete" instructions?

## Evidence needed to answer ready

- For every child, each question above is answered yes from the child's own description and edges.
- The children together cover everything the parent's design calls for, and nothing in a child is outside it.

## Verdicts

- ready: no child fails this concern.
- gaps: one or more children fail. Each gap names the failing child id, what is missing, and a research question where facts are needed.
- blocked: the design cannot be split into valid children without a decision only a person can make. Use block.trigger intent or conflict as fits, and put the question in block.question.
