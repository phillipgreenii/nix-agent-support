# Invariants — sample (floor-leakage PASS fixture, INV-2/10)

- **`INV-2`** — On a transient failure the dispatcher retries a bounded number of times before
  surfacing the error; the retry policy is a realization detail below this set's floor.

INTRA expectation: CLEAN — the rule states intended behavior without leaking realization detail.
