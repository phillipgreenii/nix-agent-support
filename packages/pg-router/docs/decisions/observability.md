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

### `DEC-OBS-3` — a budget-stop handler error is counted with `reason` and `role` labels; pool/limit detail stays in the event text <!-- uuid: 852019c2-9f9f-41f6-92ff-f71838a8df0b -->

**Decided** (operator ruling, Phillip, 2026-09-30; bead `pg2-irowq`). `INV-FAIL-1` said post-accept
outcomes are the handler's own and the core does not classify or count them. That is amended by one
narrow exception: a handler error whose text begins with the **budget-stop sentinel**
(`session budget exceeded`, `interfaces.md`) is counted by the core under the existing
`handler-error` failure class with `reason="budget-exceeded"`, and every `handler-error` series now
carries a `role` label.

**Why.** 72 budget stops (worker and review roles) were indistinguishable from unrelated failures
in one recurring alert; the operator asked for errors to carry enough context to respond. The core
already receives the handler's error text through the synchronous dispatch reply, so this adds no
status callback and no new stream: `INV-FAIL-1`'s "no per-run status stream" and "never re-offers"
clauses still hold.

**Bounds.** `role` is config-bounded (one value per configured role). `pool`, `limit`, `used`,
`cap`, `bead`, `session`, `elapsed` are NOT labels (unbounded or high-cardinality); they live in the
error text and the handler's eventlog `hard_stop` record only. Matching is on the leading
substring because the core cannot test error identity across the process boundary; the substring is
therefore a documented contract (`interfaces.md`, "Budget-stop sentinel"). The alert rules split by
this `reason` (`grafana/alerting/alerts.yaml`).

**Amended** (bead `pg2-u2yub`, extracted from the `pg2-68005` triager follow-ups). A `handler-error`
from an **escalation-triage role** (role name contains `triage`, the handler's own convention) is
counted with `reason="triager-failure"` instead, and that reason wins over `budget-exceeded`. Why:
a triager's dispatch failures (including a triager that cannot observe an item's completion) are
unrelated to worker/review health, yet shared the residual `pg-router-failure-rate` series and
fired it. The residual rule now excludes `triager-failure`; a separate rule
(`pg-router-triager-failures`) keeps those failures visible under their own series and `role` label.
Same bounds as above: the reason is a fixed constant and `role` stays config-bounded. This is a
classification of an error the core already receives, not a status stream, so `INV-FAIL-1`'s
other clauses are unchanged.

### `DEC-OBS-4` — a source is persistently failing when it has not succeeded for max(3 x its period, 30m); a pause is not a failure <!-- uuid: 91571075-a2d2-4e39-8e31-4ce6e7f020c9 -->

**Decided** (operator approval, Phillip, 2026-10-05; bead `pg2-tv11a`). The core exports two gauges per
pull source, `source_last_success_timestamp` and `source_expected_interval` (`interfaces.md`,
`INTF-MON`'s catalog), and the `pg-router-source-persistent-failure` alert
(`grafana/alerting/alerts.yaml`) fires when `time() - last_success` exceeds
`clamp_min(3 x expected_interval, 1800s)` for 5 minutes.

**Why.** `source_failures` is a counter, so the only rule it supports
(`pg-router-source-failure-rate`: any failure for 10m) cannot tell a source that is down from one that
hit a transient upstream 502/504 storm with a success always intervening: it had 19 Alerting
transitions on 2026-10-05 alone, every one a transient `pr-team` storm (worst 30m window about 11
failures in about 30 ticks), too noisy to escalate. Real outages (the 2026-09-28 bd outage; the
2026-10-01/02 GraphQL rate-limit-reserve episode; `thread-me`'s 2026-10-01 run of six consecutive
failed 30m ticks) leave a source silent far longer than 3 periods. That earlier rule stays as the
desktop-only early signal and is not escalated.

**Threshold.** `max(3 x period, 30m)`: three missed periods, with a 30-minute floor so a fast source
(10s to 1m) needs a genuine half-hour outage. The comparison is strict, and `for: 5m` is added on top.
Known boundary: for a 30-minute source (threshold 90m) whose observed tick spacing is about 35m, two
consecutive failed ticks followed by a success put the last success about 99m old, so that case does
reach Alerting briefly; the approved factor is kept and this is recorded rather than tuned away.

**A pause is not a failure.** While a gate blocks a source or the log-size limit halts the polled
emitters, the pass is skipped and **advances** the last-success time like a success. Without this, a
60-minute pg-router pause or log-limit halt would fire the rule for every pull source at once. A
**failed** pass never advances it. Which sources are exported: enabled, non-excluded **pull**
sources that have a period (a threshold or manual source with no explicit `expected_interval` has no
cadence to alert against); none for disabled, excluded or push sources.

**Restart semantics.** The timestamp starts at **process start**, not zero, so a restart resets the
clock: a source that keeps failing across a restart re-alerts only after the threshold plus `for`
following that restart. This is accepted because the failure counter also resets on restart and a
restart is itself a strong recovery attempt.

**No-data.** The series exist from daemon start, so the rule keeps `noDataState: OK`: absence means
the daemon (or its metrics endpoint) is down, which `pg-router-liveness-down` covers.

**Evidence.** The rule is modelled and replayed in `internal/alertrules/source_persistent_failure_test.go`
against per-tick outcomes extracted from the daemon's stderr log (`testdata/source_ticks.txt`; the
log records only failed ticks, so successes are inferred, and the header there states the rule). Registration of
this rule with `pg-router-probe` is a separate, deployment-side change.

### `DEC-OBS-5` — `role` labels every delivery-side failure class, throughput and dispatch latency; latency buckets span seconds to hours <!-- uuid: c26b6639-0970-4ce9-b42c-a40579df041f -->

**Decided** (operator request, Phillip, 2026-10-05; bead `pg2-nimab`). `role` (the listener id, which
is the configured role name) is now recorded on `failures` for **all three** delivery-side classes
(`declined` and `dispatch-failure` join `handler-error`, `DEC-OBS-3`), on `throughput` (per `type`
and `role`), and on `dispatch-latency` (per `outcome` and `role`). `INTF-MON`'s catalog states the
shapes. `INV-OBS-1`'s "exactly two delivery-side classes" is unchanged: this adds a label
dimension, never a class (the precedent is the `reason` label added to `declined` under the
metrics-catalog growth of bead `pg2-j4uwg`).

**Why.** One starved or slow role was invisible: the per-lane problems the operator was chasing
(a role bound to a type that stops draining, a slow handler) averaged away across roles. The call
sites already knew the role (`OnDeclined` and `OnAccept` received the listener id and discarded it
"for interface symmetry"); the dispatch-failure signal gained it by carrying the failing listener
through the queue's signal fan-out.

**Bounds.** Same reasoning as `DEC-OBS-3`: `role` is config-bounded, one value per configured role
(14 roles x 17 latency series), so the cardinality cost is fixed by configuration, not by event
traffic. No other identifier (event id, session, bead) becomes a label. Existing labels are
unchanged, including the inconsistent names other metrics use for the same concept (`listener` on
gate-drops, `participant` on gate-blocked, `pgrouter_role` on ccpool metrics); unifying them is a
possible follow-up, not part of this decision.

**Latency buckets.** Latency is measured from the event's enqueue instant through the handler's
synchronous run, so the old 1 ms to 5 s boundaries left every real sample in the overflow bucket.
The boundaries are now 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000, 300000, 600000,
1200000, 1800000 and 3600000 ms; the overflow bucket covers a re-offered event older than an hour.
While old and new `le` sets coexist in one rate window, quantiles are skewed for that window.
