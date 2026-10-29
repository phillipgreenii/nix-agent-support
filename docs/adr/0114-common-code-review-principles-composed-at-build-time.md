# Common code-review principles, composed at build time

**Status**: Accepted (operator request, 2026-10-28)
**Date**: 2026-10-28
**Deciders**: phillipg

## Context

Guidance on how an agent reviews code was written separately in several places: the
pg-router review prompt, a reviewer plugin's configuration, workflow critic prompts and
review checklists. The copies drift, and each mixed the judgement of a good review (voice,
severity bar, evidence, restraint) with process mechanics (claiming work, checkout, posting).

## Decision

1. **The judgement lives once**, as markdown fragments under `lib/code-review/fragments/` of
   this repo, exposed as the flake output `lib.codeReview` with a `compose` function.
   Fragments are agnostic of agent, repo, tracker, hosting service, vendor and organisation,
   and use RFC 2119 language.
2. **Each consumer composes at nix evaluation time**: the common fragments plus its own
   scenario addendum. No consumer reads a fragment path at runtime, and none hardcodes a repo
   or path.
3. **Mechanics stay with the consumer.** A consumer's process text (claiming, checkout,
   posting, closing) is not a fragment.
4. **`compose` refuses the Go-template opener by default**, so a fragment can never alter the
   actions of a prompt that is itself a Go template.
5. **Agnosticism is enforced by each consumer.** This public repo commits no word list (its
   public-repo rule forbids denylists), so `checks.<system>.test-code-review` checks structure only
   (inventory, order, composition layout, an RFC 2119 keyword per fragment, no template opener or
   tab). A consumer checks the composed text against its own vocabulary, with a negative control.

## Consequences

- A change to the common guidance reaches every consumer at its next evaluation; a consumer's
  behaviour can therefore change by relocking this input. Consumers MUST carry a check over
  their rendered text.
- Scenario text that was once embedded in a prompt moves to the consumer's addendum.
- The guide `docs/guides/code-review-principles.md` describes the design and how to add a
  consumer.
