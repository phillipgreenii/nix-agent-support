# Set — sample (realization-register PASS fixture, INV-23)

## Realization gaps

One row per gap, naming the element id the gap is against, what the docs require, and where the
implementation stands. The register is set-level and sits outside every element definition.

| Element | Intended                                        | Where the implementation stands                   |
| ------- | ----------------------------------------------- | ------------------------------------------------- |
| `INV-1` | every accepted event is delivered at least once | delivery is best-effort and is dropped on restart |

INTRA expectation: CLEAN — the register is present and named, it is keyed by element id, and no gap
is recorded as an `OQ-` element (`INV-23`).
