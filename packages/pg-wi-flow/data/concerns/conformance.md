# Concern: conformance

Which current docs, rules, specs and processes apply to this item, and are they cited? You are the reviewer for this concern; end your work by recording one verdict for it with record-verdict.

## Questions to ask

1. Which current docs, rules, ADRs, specs or documented processes govern the work this item describes?
2. Does the item cite them, by repo path?
3. Does anything the item asks for conflict with one of them?

WI_APPLICABLE_DOCS is a script-computed candidate list: a keyword match of the item's terms over rule, ADR and plan titles and headings. Confirm the item's citations against that list instead of hunting for docs yourself. Add a doc the list missed only when you have a concrete reason it applies.

## Evidence needed to answer ready

- Each applicable doc is cited in the item (or in its annotated findings) by repo path.
- Every candidate in WI_APPLICABLE_DOCS is either cited or judged not applicable, with a short reason.
- No requirement of the item contradicts a cited doc.

A missing doc is normal: if WI_APPLICABLE_DOCS is empty and nothing applies, that is ready.

## Verdicts

- ready: all of the evidence above is present.
- gaps: an applicable doc is not cited, or applicability is unclear. Each gap carries a gap (which doc or question) and a research question.
- blocked: the item conflicts with a cited doc and only a person can decide. Use block.trigger conflict and put the question in block.question.
