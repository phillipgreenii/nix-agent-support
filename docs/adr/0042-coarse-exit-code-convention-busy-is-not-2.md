# Coarse exit-code convention: low codes are global, `BUSY` moves off 2

**Status**: Accepted (resolves `pg2-kzzam`)
**Date**: 2026-07-29
**Deciders**: Phillip Green II

## Context

pr-pool ended up with **two incompatible operator exit-code conventions**, and
`docs/behavior/interfaces.md` is satisfied by one and violated by the other.

`interfaces.md` (the "Output" bullet) states the common contract: "Coarse exit codes follow the
common contract (`0` ok, non-zero on error; **a usage error is distinct from a runtime error**)."

The two conventions in the code:

- `cmd/pr-pool/drain.go` — and its siblings `run-role` / `config` — follow the inherited ccpool
  convention, declared in a **local** const block: `exitOK = 0`, `exitGeneric = 1`,
  `exitUsage = 2`, `exitPrecheck = 3`, commented "1 generic, 2 usage, ≥3 specific".
- `push-inject` and `ingest-event` **cannot** spend 2 on usage, because 2 is reserved for `BUSY`
  on the callback transport they share — `INV-CONC-1`'s pre-accept capacity decline. So they
  return **1 for both** usage and runtime, which is exactly what the `interfaces.md` sentence
  forbids. Their tests pin this: `push_inject_test.go` and `ingest_event_test.go` assert an
  unknown flag exits `conformance.ExitError` and "never `ExitBusy`".

The codes are declared centrally in `conformance/transport.go`:

```go
ExitOK    = 0 // ok; rich outcome in the JSON reply
ExitError = 1 // unexpected / usage / malformed error
ExitBusy  = 2 // at capacity — pre-accept busy decline (no body required)
```

Note `ExitError`'s comment already conflates "unexpected / usage", which is the doc violation
written into the constant itself.

Three narrow fixes were considered: relax the `interfaces.md` sentence where 2 is taken; move
usage to ≥3 on the callback-sharing subcommands only; or re-align the siblings. Each accepts the
premise that `BUSY` owns 2. **A measurement rules the second one out regardless:** `drain.go`
already defines `exitPrecheck = 3`, so 3 is spoken for as a _specific_ code under the very
convention being applied — putting usage there breaks "≥3 specific" rather than honoring it.

## Decision

**Low exit codes are reserved for meanings that are general across every app, not tuned to one
app's domain. `BUSY` is not such a meaning, so it moves off 2.**

The convention, applying to all of these apps and not only pr-pool:

| code | meaning                                  |
| ---- | ---------------------------------------- |
| `0`  | ok                                       |
| `1`  | unexpected error                         |
| `2`  | **usage error**                          |
| `≥3` | app-specific codes                       |
| `9`  | `BUSY` — at capacity, pre-accept decline |

Rationale: **any** app can have a usage problem, so `2` earns a low, globally-consistent slot.
"Busy" is domain-specific — it means something only to a participant on a capacity-bounded
transport — so it belongs out in the app-specific range rather than squatting on reserved space.

This dissolves the three-way conflict rather than conceding to it: with 2 free, `push-inject` and
`ingest-event` use it for usage exactly as their siblings do, so `interfaces.md`'s
usage-distinct-from-runtime rule holds under **one** convention across every operator subcommand.
`interfaces.md` needs no relaxation.

## Consequences

- **`BUSY` 2 → 9 is a change to a wire contract, not an internal refactor.** The pre-accept
  decline is observed by the _caller_ of a participant (`INV-CONC-1`), so both sides must move in
  the same change. A caller still checking for 2 would read a `BUSY` decline as a usage error and
  a usage error as `BUSY` — a silent, inverted misread. This is the one hazard in the change.
- It is mostly mechanical because the codes are centralized as `conformance.Exit*` and referenced
  by name. The exception is **`drain.go`'s duplicate local const block**, a second source of
  truth for the same convention; it MUST be collapsed onto the `conformance` constants rather
  than updated in parallel, or the two will drift again.
- `conformance/transport.go` gains `ExitUsage = 2`, and `ExitError`'s comment drops "usage" — it
  means _unexpected_ only. Leaving that comment as-is would re-encode the original defect.
- **The conformance suite IS the declared contract** (`INV-INTF-1` / `INV-INTF-2`), so its
  exit-code expectations and any goldens move with the decision; the suite is not a downstream
  consumer to be fixed afterwards.
- `interfaces.md` and `invariants.md`'s `INV-CONC-1` should state the numbers, so the reserved
  meaning of 2 and the location of `BUSY` are discoverable from the behavior docs rather than
  only from a const block.
- `9` is picked as a round, memorable value well clear of the `≥3` specific range in current use
  (`drain`'s `exitPrecheck = 3`). It is not a POSIX-significant number; nothing else in the
  contract depends on the specific value, only on it being outside the reserved low band.
