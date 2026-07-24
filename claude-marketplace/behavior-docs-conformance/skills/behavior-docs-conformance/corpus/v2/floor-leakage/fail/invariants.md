# Invariants — sample (floor-leakage FAIL fixture, INV-2/10)

- **`INV-2`** — On a transient failure the dispatcher retries 3 times with a 250ms backoff (see
  `orchestrator.go:142`) before surfacing the error.

V2 expectation: FLAG on `floor-leakage` — `file:line`, a retry count, and a backoff constant are
below-floor realization details.
