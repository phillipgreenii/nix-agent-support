# Observability — pg-router decision docs

Realization decisions about **how** the core's observability output leaves the process. The behavior
side — that the core declares a **metric catalog**, that a monitoring sink pulls or pushes a declared
subset of it, that the concrete backend is the sink's own deployment binding, and that an observer
reads the sink and never the core — is `INV-OBS-1` and `INTF-MON` in pg-router's
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

### `DEC-OBS-2` — per-participant history widens the existing activity ring; in-flight status is read, not newly tracked <!-- uuid: 84a80f2d-4b3f-4c12-8d58-f1760d137039 -->

**Decided.** `INV-OBS-2`'s per-participant recent-history and in-flight signal (pg-router's TUI
drill-down) is realized by **widening the existing single, pool-wide activity ring** (a `Participant`
field added to `internal/activity.Entry`) rather than standing up a second, per-participant ring
alongside it; and by **exposing state the dispatch/produce path already tracks internally** rather
than adding new bookkeeping to derive "in flight."

**Why one ring, widened, and not a second ring.** The call sites that already append a ring entry for
a handler outcome (`eventqueue.Observer.OnAccept`/`OnDeclined`) already receive the listener id as a
parameter — the identity was being discarded, not missing. Threading it into the appended `Entry`
needed no new plumbing. A second, per-participant ring would duplicate the first ring's own
retention/eviction/thread-safety machinery (already covered by its own test suite) for every
participant, and would need its own, separately-argued bound — a decision this approach avoids
entirely by reusing the one already-bounded, already-tested ring and filtering on read. The accepted
trade-off, stated plainly: a single shared ring bounds every participant's combined history together,
so a very active participant can in principle push a quiet one's entries out of the retained window
before a reader asks — acceptable here because this is inspection, not a correctness- or
delivery-relevant record (`INV-OBS-2`'s own "inspection only" clause), so `INV-PREC-1` does not apply.

**Why the in-flight signal is read, not re-derived.** A listener's outstanding-offer window is already
tracked, per listener id, by `eventqueue.Queue`'s own `inFlight` index — set the moment an offer is
minted and cleared the moment it settles, spanning the real duration of the listener's `Offer` call
however long that call actually runs (`INV-CONC-1`'s "one outstanding offer per handler"). Making this
visible needed only a new read-only accessor over already-mutated state, not a new tracked fact. A
pull source's fetch has no equivalent existing index (a source's `Query.Run` call was not observed by
anything before or after it ran), so that side **does** add a small new observer, bracketing the call
the same way `discover.SourceFailureObserver` already brackets a failed attempt — the smallest
addition that makes the already-happening event visible, not a parallel tracking mechanism.

**Not decided here.** The exact history-window size (how many entries a drill-down shows) and the
exact new `Entry.Outcome` vocabulary a source-side pass may report (`"produced"`, `"source_failed"`)
are implementation detail with no behavioral consequence — substituting either preserves everything
`INV-OBS-2` states — and are documented at the call site, not restated here.
