# Observability — pr-pool decision docs

Realization decisions about **how** the core's observability output leaves the process. The behavior
side — that the core declares a **metric catalog**, that a monitoring sink pulls or pushes a declared
subset of it, that the concrete backend is the sink's own deployment binding, and that an observer
reads the sink and never the core — is `INV-OBS-1` and `INTF-MON` in pr-pool's
[behavior docs](../behavior/interfaces.md).

### `DEC-OBS-1` — OTel is the default emission transport for metrics only, and logs stay JSONL <!-- uuid: 339efa84-34df-46e2-8554-48aea2ed1320 -->

**Decided.** Metrics are emitted over **OTel** by default; **logs are written as JSONL**. Traces are a
later concern and no transport is chosen for them yet.

**Why these two and why they are separate.** OTel is picked as a **neutral standard rather than a
backend**: it commits the core to an emission format that every mainstream metrics store already
consumes, so choosing a store stays a deployment decision and `GOAL-MIN-1` holds — the core gains no
knowledge of any concrete monitoring tool. Logs are deliberately **not** carried over the same
transport: a log line's value is that it survives when the metrics pipeline is the thing that broke,
and JSONL on a stream needs nothing to be running to be readable afterwards. Coupling the two would
make an outage in the metrics path also an outage in the record of it.

**Why this is not behavior.** Substituting either name preserves every stated behavior: the catalog
still has a declared shape, a sink still declares its mode and subset, the observer still reads the
sink. Only the encoding changes, which is the substitution test's answer. What survives the
substitution — the catalog's shape, who reads what, and that the backend is a deployment binding —
stays in the behavior set.

**Not decided here.** The concrete metrics backend and log store are the sink's own binding
(`INTF-MON`), not this entry's, and the catalog's membership is stated in `INTF-MON` because an
enumerated catalog belongs to the interface that carries it.
