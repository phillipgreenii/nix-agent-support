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

**Amended** (operator ruling, Phillip, 2026-10-05, option C; bead `pg2-fy2pm`). A `handler-error`
whose text carries the **upstream-killed sentinel** (`scriptout:` with `signal: killed`, or with
`deadline exceeded after` as amended by `pg2-gp50o` below; `interfaces.md`) is counted with
`reason="upstream-killed"`. Precedence is
`triager-failure`, then `budget-exceeded`, then `upstream-killed`. Why: the connector's 30s exec
timeout SIGKILLs a slow `gh` call; those kills are transient GitHub slowness that the 30-minute PR
sweep heals (21 of 22 failed dispatches were followed by an ok dispatch of the same PR), yet each one
paged `pg-router-failure-rate`. Both substrings are required because bare `signal: killed` also
matches an out-of-memory kill of any worker or ccpool session, which MUST keep paging individually.
The residual rule now excludes `upstream-killed`; a separate rule (`pg-router-upstream-killed`,
registered in `pg-router-probe`) pages when a role records **10 or more** such errors in a 30-minute
window (`sum by (role) (increase(...[30m])) >= 10`, `for: 0m` because the window already encodes
"sustained"). Of the three bursts observed 2026-10-03/04, only the 14-in-20-minute one pages (7 in 22
minutes and 3 in about 33 do not). Isolated kills stay visible in the metric
(`pg_router_failures_total{reason="upstream-killed"}`), not as a page. **Accepted limitation:** a
brand-new series does not count its first increment, so the first kill after a pg-router restart is
not counted and 10 kills on a fresh series read as 9 (the 11th pages); the series is not
pre-initialized because the core does not know the role set. **Not covered:** a slow-GitHub failure
that lacks the sentinel (for example `error connecting to api.github.com`) keeps paging the residual
rule individually. Same bounds as above: the reason is a fixed constant and `role` stays
config-bounded.

**Clarified** (bead `pg2-gp50o`, the second triage of the desk-pr failure series; scope-preserving,
not a new ruling). The same connector exec-timeout event can read `deadline exceeded after <elapsed>
(limit <limit>) in op=...` ending in `context deadline exceeded` instead of `signal: killed`,
depending on what the killed child reported (2 of 28 desk-pr deadline failures logged from
2026-10-07 to 2026-10-14, both a `gh pr view` call). Those slipped past the sentinel and paged the residual
rule although they are the very kill option C excludes. The sentinel is therefore `scriptout:` with
`signal: killed` OR `deadline exceeded after`; `scriptout:` stays mandatory, so the out-of-memory
reasoning above is unchanged and the `error connecting to api.github.com` exclusion above still
stands.

**Amended** (operator ruling A, Phillip, 2026-10-05; bead `pg2-vn4jb`). The budget-stop rule
(`pg-router-budget-stops`) is **repeat-only**: it pages when a role records **2 or more**
`reason="budget-exceeded"` handler errors in a 1-hour window
(`sum by (role) (increase(...[1h])) >= 2`, `for: 0m` because the window already encodes
"repeated"). One stop in an hour is benign contention and **MUST NOT** page; the `pg2-68005`
shape (the same beads re-failing 2-3 times per hour per role) pages. Repeats page through
`pg-router-budget-stops`, **not** through `pg-router-failure-rate` (the residual still excludes
`budget-exceeded`). Why `for: 0m`: a `for: 10m` hold on a 1-hour window would suppress pairs 51 to
59 minutes apart and delay the rest for no benefit. The old rule (`rate(...[10m]) > 0`, `for: 10m`)
never paged an isolated stop in the replayed model (the rate stays above 0 for just under 10 minutes, shorter than the
hold) but also missed a repeat 25 minutes apart, because each stop's hold ended before the next
began; the replayed model is `internal/alertrules/budget_stops_test.go`. Cardinality is unchanged
(`role` only; per-bead+role detection and a launch-timeout reason were **not** chosen, and the
orphaned-session-budget gap stays with `pg2-3j76b`). **Accepted limitation:** a brand-new series
does not count its first increment, so the first budget stop for a role after a pg-router restart
is not counted and two stops on a fresh series read as 1 (the 3rd pages); the series is not
pre-initialized because the core does not know the role set. Singles stay visible in the metric
(`pg_router_failures_total{reason="budget-exceeded"}`).

### `DEC-OBS-4` — a source is persistently failing when it has not succeeded for max(3 x its period, 30m); a pause is not a failure <!-- uuid: 91571075-a2d2-4e39-8e31-4ce6e7f020c9 -->

**Decided** (operator approval, Phillip, 2026-10-05; bead `pg2-tv11a`). The core exports two gauges per
pull source, `source_last_success_timestamp` and `source_expected_interval` (`interfaces.md`,
`INTF-MON`'s catalog), and the `pg-router-source-persistent-failure` alert
(`grafana/alerting/alerts.yaml`) fires when `time() - last_success` exceeds
`clamp_min(3 x expected_interval, 1800s)` for 5 minutes. **Amended 2026-10-12** (see "Host sleep"
below): the core exports a third gauge, `source_last_success_age`, measured on its own monotonic clock,
and the alert compares that with the threshold instead of `time() - last_success`.

**Why.** `source_failures` is a counter, so the only rule it supports
(`pg-router-source-failure-rate`: any failure for 10m) cannot tell a source that is down from one that
hit a transient upstream 502/504 storm with a success always intervening: it had 19 Alerting
transitions on 2026-10-05 alone, every one a transient `pr-team` storm (worst 30m window about 11
failures in about 30 ticks), too noisy to escalate. Real outages (the 2026-09-28 bd outage; the
2026-10-01/02 GraphQL rate-limit-reserve episode; `thread-me`'s 2026-10-01 run of six consecutive
failed 30m ticks) leave a source silent far longer than 3 periods. That earlier rule stays as the
early signal and is not escalated.

**Amendment, 2026-10-08 (operator ruling, Phillip: "non-zero failure rate is too tight").**
`pg-router-source-failure-rate` no longer fires on any failure. It now alerts on the failure ratio
over 1h: `increase(source_failures[1h]) / increase(source_duration_seconds_count[1h]) > 0.5`, with at
least 3 failures in that hour, sustained for 15m (title: `pg-router event source failure ratio is
high (by source)`). The denominator counts every attempt that ran to its own end, success or failure
(DEC-OBS-8), so the quotient is a true ratio. A 7-day backtest on live data: the old rule was
active in 7 sources (576 five-minute steps for `pr-team` alone); the new one in 2 (`pr-team` 183
steps, `review-source` 1). The rule's uid, `noDataState: OK`, and severity are unchanged.

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

**Host sleep (amendment, 2026-10-12; bead `pg2-ez292`; implements that bead's recommended option (d),
no separate operator ruling is recorded for it).** The silence the alert measures is **awake time**, not
wall-clock time. The paragraphs above cover a pause (a gated pass advances last-success), a restart
(the clock resets) and no data (the daemon is down); none covered a host that sleeps. The original
rule computed the silence in Prometheus as `time() - last_success`, a wall-clock difference, while the
router counts awake time: it paces a pull source on Go's monotonic clock (`cadenceDue`), and on macOS
that clock does not advance while the host sleeps, so after a wake a slow source is next due only once
the rest of its period has passed in awake time (up to 30 minutes for a 30-minute source, up to an hour
for a 60-minute one). Prometheus and Grafana run on the same laptop and resume at the same wake, so
their first evaluations after a sleep longer than a source's threshold saw a last-success older than
`max(3 x period, 30m)` for every slow source, and the rule fired five minutes later for each source
that had not yet ticked. On 2026-10-12 that was six sources within 16 seconds and six escalated beads,
each costing a triager session, every one of which cleared on its own.

The core now exports a third gauge per pull source, `source_last_success_age` (`interfaces.md`,
`INTF-MON`'s catalog): the seconds since that source's last success or deliberate pause, computed by the
core when the gauge is read, as the elapsed time since a stored last-success time that keeps Go's
monotonic reading. The alert compares it with the same `clamp_min(3 x expected_interval, 1800s)` for the
same 5 minutes, so "persistently failing" now means three periods (and at least 30 minutes) of **awake**
time without a success. `source_last_success_timestamp` is kept unchanged, as the wall-clock record of
when a success happened and for dashboards; the alert no longer reads it. Polling does not change.

- A host sleep adds nothing to the age, so it neither fires the rule nor hides a real outage: a source
  that is genuinely failing alerts after the same awake time as before, later on the wall clock by exactly
  the length of the sleep (`TestHostSleepDelaysButDoesNotHideARealOutage`).
- A dark wake (a few seconds of CPU about every 15 minutes with the lid closed) counts as awake time, as
  it does for the router's own cadence.
- A pause still resets the age and a failure still does not. A restart resets it to 0, so the restart
  semantics above are unchanged except that threshold plus `for` is measured in awake time.
- No-data is unchanged, with one transition effect: a router build that predates the gauge exports no age
  series, so the rule reads as no data (OK) until the new build runs. It is not firing then, and it is
  not protecting either.

Alternatives considered (the bead's own list). (a) A wall-clock due check in the router would fix the
cause, but a dark wake would then also start passes that the next sleep freezes or fails, adding to
`source_failures` and to `pg-router-source-failure-rate`. (b) Marking sources due on a full-wake signal
needs a push path from the power watcher. (c) Requiring a share of the expected scrapes in a trailing
window masks a real outage that starts during or just after a sleep, for up to 90 minutes. (e) Accepting
the false positives costs up to six triager sessions per long sleep. (d) leaves polling and the router's
behaviour at a wake untouched.

**Evidence.** The rule is modelled and replayed in `internal/alertrules/source_persistent_failure_test.go`
against per-tick outcomes extracted from the daemon's stderr log (`testdata/source_ticks.txt`; the
log records only failed ticks, so successes are inferred, and the header there states the rule). The
2026-10-12 sleep is replayed there too, from the daemon's queue log and the host's wake/sleep log
(`pg-router-probe-tick`: 28,505 s of wall clock but 1,767 s of awake time between two ticks of a 1,800 s
period): the retired wall-clock expression fires on it at 16:35:01Z (the observed alert started at
16:36:00Z), the age does not. Registration of this rule with `pg-router-probe` is a separate,
deployment-side change.

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
possible follow-up, not part of this decision (settled in `DEC-OBS-7`).

**Latency buckets.** Latency is measured from the event's enqueue instant through the handler's
synchronous run, so the old 1 ms to 5 s boundaries left every real sample in the overflow bucket.
The boundaries are now 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000, 300000, 600000,
1200000, 1800000 and 3600000 ms; the overflow bucket covers a re-offered event older than an hour.
While old and new `le` sets coexist in one rate window, quantiles are skewed for that window.

### `DEC-OBS-6` — dispatch latency carries a bounded entity `type` label <!-- uuid: 8ba393ee-8e17-4a2d-b623-6e92336ac6c7 -->

**Decided** (operator ruling D10, Phillip, 2026-10-05, design `2026-10-05-fast-per-type-change-check-design.md`
Q9; bead `pg2-sve9v`, follow-up to `pg2-nimab`). The `dispatch-latency` histogram gains a `type`
label, so the wait of one entity's change events (`pr.changed` and its siblings) can be read as a
standing metric. Its labels are now `outcome`, `role` and `type`. `INTF-MON`'s catalog states the
shape; `INV-OBS-1` is unchanged (a label dimension, never a class).

**Value.** The entity type: the event type's segment before the first `.` (`pr.changed` -> `pr`,
`pr.reconcile` -> `pr`, `issue.changed` -> `issue`); an event type with no `.` is its own value. The
per-verb type stays available, unreduced, on `throughput`'s own `type` label, so the two metrics'
`type` labels are deliberately different grains.

**Bounds.** Cardinality is fixed by configuration, never by traffic or ids. Event types are
config-bounded (the core rejects a type no binding declares), reducing to the entity prefix
collapses the per-verb types into one value per entity, and a value that is not a short
(at most 32 bytes) lowercase identifier (`[a-z][a-z0-9_-]*`), such as an empty type, a leading `.` or a
type restored from an older config, maps to the single fallback value `other` instead of minting a
new series. No event id, session or bead becomes a label.

**Dashboards and alerts.** Adding a label to a histogram does not break a selector or aggregation
that does not match on it: the observed consumers (the pg-router dashboard's p50/p95 panels)
aggregate with `sum by (le)`. A query that groups by `outcome` or `role` alone is likewise
unaffected; only a rule that expects exactly one series per (`outcome`, `role`) would change.

### `DEC-OBS-7` — a listener's id is labelled `role` on every pg-router metric that carries one; `participant` and `pgrouter_role` keep their names <!-- uuid: 38fa463e-63b0-44d3-b3a7-26e8c4519934 -->

**Decided** (bead `pg2-q1dcc`, follow-up to `pg2-nimab` and `DEC-OBS-5`, which left the naming
inconsistency open). Four label names overlapped: `role`, `listener`, `participant`, `pgrouter_role`.
They are not four spellings of one concept, so only one is unified:

- `pg_router_gate_drops` renames `listener` to `role`. Its value is the listener id, the same value
  `role` carries on `failures`, `throughput` and `dispatch-latency` (`DEC-OBS-3`, `DEC-OBS-5`), so
  a listener-scoped metric now uses one label name. The observed consumers are none: no dashboard,
  alert rule or query in any workspace repo references `pg_router_gate_drops`, so the rename breaks
  nothing known. Series recorded before the change keep `listener`; a query that wants both
  spellings during the overlap can match either.
- `pg_router_gate_blocked` keeps `participant`. Its value is any registry participant a gate
  stopped, including a polled event **source** as well as a listener, so it is the wider concept
  and `role` would mislabel the source rows.
- ccpool's `pgrouter_role` keeps its name. It is not emitted by pg-router: it is a
  ccpool session label (`pgrouter.role`, allowlisted onto ccpool's metrics and exported with an
  underscore) that identifies the pg-router role a ccpool session was launched for, and the ccpool
  dashboards select on it.
  Renaming it would break those dashboards and join expressions for no gain in clarity.

**Bounds.** Unchanged: `role` is config-bounded (`DEC-OBS-5`). The rename swaps a label key; it adds
no series.

### `DEC-OBS-8` — a router-shutdown cancellation is not a source failure; a timeout kill is its own reason; each attempt is timed and counted in flight <!-- uuid: 4bcd8655-b8ba-4c1c-9610-590c71bd6e24 -->

**Decided** (bead `pg2-zdowv`, from the 2026-10-07 router health review). Four changes to how a pull
source's attempt is observed:

- **A failed attempt while the router's own context is cancelled is not a source failure.**
  `discover.runAndEnqueue` checks `ctx.Err()` after a failed `Query.Run`. If the context is
  cancelled, the cancellation (a restart or shutdown killing the producer tick's child) is
  propagated as the pass's own error, exactly as a cancelled backoff wait already was. It is **not**
  passed to `OnSourceFailure`, not recorded in `ProduceReport.SourceErrors` or `Failure`, and does
  not log a source WARN (`runOneTick` logs the interrupted tick at INFO). On 2026-10-06, 21
  `context canceled` and 4 bare `signal: killed` failures were the router's own ticks cancelled at
  restart, and counted as source failures beside 67 genuine timeout kills. Because the check is on the
  router's context and not on the error text, it is exact: no error string can make a real failure
  disappear.
- **`reason="timeout"` is added to `source_failures`**, taken out of `interrupted`. It matches
  `signal: killed`, `context deadline exceeded` and `errors.Is(DeadlineExceeded)`, and is checked
  before the generic `scriptout: unavailable` wrapper (a backend that reports its own timeout as
  unavailable is still a timeout). `interrupted` remains for a `context canceled` reported from
  inside the backend's own process tree when the router's context is live. Rate-limit and
  unauthenticated are still checked first. A bare SIGKILL from some other cause (an OOM kill) also
  lands in `timeout`; the timeout kill is the only source of SIGKILLs known here, and the
  `elapsed` on the WARN (below) disambiguates.
- **`pg_router_source_duration_seconds{source}`** (histogram) is fed by a new
  `SourceFailureObserver.OnSourceAttemptStart/OnSourceAttemptEnd(source, elapsed, shutdown)` pair
  bracketing each attempt; **`pg_router_source_inflight_children{source}`** (gauge) is the same
  pair's balance, seeded to 0 for every period-bearing pull source. The duration is recorded per
  attempt (a retry is its own sample), for success and failure alike, and skipped for a
  shutdown-cut attempt. The pair rides the existing failure-observer seam, not
  `SourceActivityObserver`, because that interface's single orchestrator slot belongs to the activity
  ring and the metrics emitter already holds this one.
- **The producer-tick `source failed` WARN carries `elapsed` (the give-up attempt's run time),
  `attempts`, `argv` (the source's command line, `query.ArgvOf`) and `load1` (host 1-minute load
  average, `/proc/loadavg` or `sysctl vm.loadavg`)**. Elapsed and argv ride on `discover.FailureInfo`.
  The load lookup runs only on this failure path.

**Bounds.** `source` is config-bounded; the new `reason` value is a fixed constant. The gauge and
histogram add one series per source (the histogram 14 buckets). No argv, load or elapsed becomes a
label; they live in the log line only.

**Dashboards and alerts.** The observed consumers are `pg-router-source-failure-rate` and
`pg-router-source-persistent-failure` (both `sum by (source)`, reason aggregated away, so the new
reason and the removal of shutdown cancellations need no rule change) and the pg-router dashboard's
source-failures panel in `phillipgreenii-nix-support-apps` (`sum by (source) (rate(...))`, likewise
unaffected). No query anywhere in the workspace repos matches `reason="interrupted"`. A shutdown
cancellation stops counting as a failure, which is the intent, and the persistent-failure alert is
unaffected (a cancelled attempt never advanced the last-success gauge either).

**Not decided here.** An alert on a source child stuck in flight, or on the duration histogram's
tail, is a possible follow-up; no rule is added by this entry.

### `DEC-OBS-9` — queue wait and run time are separate metrics, the event log and queue log carry the timeline, and compaction keeps a bounded history <!-- uuid: 5b0f0d5c-7a63-4d3e-9c37-2f6a1e8b9d41 -->

**Decided** (router health review, 2026-10-07; bead `pg2-n7da9`, follow-up to `pg2-nimab` and
`DEC-OBS-5`). The dispatch-latency histogram measures from the event's enqueue to the handler's
return, so a one-at-a-time lane (`INV-CONC-1`) with a deep queue reads as a slow handler. An analysis of
the desk-pr lane found about 98.7 percent of its latency was queue wait, and nothing could show it per
event. Four changes make the split computable from live data:

- **Start instant at in-flight.** The queue stamps the instant an offer becomes in-flight (the
  moment `INV-CONC-1`'s one-outstanding-offer slot is taken). Wait is that instant minus the event's
  enqueue instant (`Event.At`, the origin dispatch latency always used); run is the settle instant
  minus it. A pass settles with a fresh clock reading, so a batched `Dispatch` does not report a zero
  run. The pair is delivered to an optional observer extension fired right after `OnAccept`, so existing
  observers are untouched. `INV-CONC-1` is unchanged: this only reads the slot.
- **Metrics.** `queue_wait` and `run` (histograms), `queue_oldest_age` and `listener_in_flight`
  (gauges); shapes in `INTF-MON`'s catalog. The histograms carry the **full** event type, not the
  entity prefix `DEC-OBS-6` reduces it to, because `pr.changed` and `pr.reconcile` are different
  workloads (83 percent of the desk-pr lane was reconcile). The label is bounded the same way
  (lowercase identifier, at most 64 bytes, at most 64 distinct values, the rest `other`). The old
  histogram is **kept and marked deprecated**: the pg-router dashboard's p50 and p95 panels (in
  `phillipgreenii-nix-support-apps`) still query it by `sum by (le)`, so replacing it would blank them.
  Remove it once those panels move to the new pair.
- **`events.jsonl` dispatch rows** gain `event_type`, `change` (the event id, which for a change-driven
  event is `<entity id>@<seq>`, so two changes to one entity differ; `bead` is the entity alone),
  `enqueued_at`, `started_at` (RFC 3339, UTC) and `duration_ms` (whole milliseconds of handler run).
  They are additive: every key a row already carried is unchanged, and a zero instant is omitted
  rather than written as year one. The same fields appear on the `run-role` row and on a transiently
  failed attempt that is handed back for retry.
- **`queue.jsonl`** accept records carry `at` (settle) and `startedAt`, evict records carry `at` and a
  `reason` (`retired`, `all-accepted`, `reemit`). Compaction used to drop an evicted event's whole record
  set, and it runs at every start, so about three hours of history survived. It now folds each departed
  event into one `archive` record (type, enqueue instant, each accept's role and instants, eviction
  instant and reason) and keeps the newest 2048, additionally capped to one tenth of the soft log limit
  so history is the first thing a limit-driven compaction gives up and can never hold the log above
  its limits. A replay ignores `archive` records, so queue state and delivery are unchanged, and a
  binary that predates them ignores the unknown kind.

**Bounds.** `role` is config-bounded (`DEC-OBS-5`); `type` is bounded as above; neither adds an event
id, bead or session as a label. The oldest-age gauge is a scan of the retained events at scrape time.

**Reading the 24h wait-versus-run table.** Group `events.jsonl` dispatch rows by `role` and
`event_type`; wait is `started_at - enqueued_at`, run is `duration_ms`. The histograms give the same
split as a rate: `sum(rate(pg_router_queue_wait_seconds_sum[1h])) by (role, type)` over the matching
`run` sum.

### `DEC-OBS-10` — a queue alert reads the lane's own state: a backlog with nothing processing it, and arrivals against completions <!-- uuid: e1c7a3f4-92b6-4d58-8a1e-6f0d3b7c2a95 -->

**Decided** (operator ruling, 2026-10-12; bead `pg2-53n39`, split out of the alert record `pg2-nz9ph`).
The `pg-router-queue-stalled` alert judged a lane by completions: depth of 10 or more for 30 minutes
with no accepted dispatch for 30 minutes, held for 30 more. A dispatch settles (and
`pg_router_throughput_total` advances) only when it ends, so any healthy dispatch of an hour or more paged
it: on 2026-10-12 the 63.7 minute `drain.pg2` dispatch did, and 4 of the last 12 drain dispatches ran that
long. The operator replaced the premise: alert when the queue is above 0 and nothing is being processed,
or when consumption stays far below generation.

- **Idle with a backlog (`pg-router-queue-stalled`).** A type pages when its depth held above 0 on every
  scrape of a 10 minute window, the role bound to it was not running a dispatch (it did not read in flight,
  `listener_in_flight`, `DEC-OBS-9`, on 6 consecutive scrapes anywhere in the window: see "Declined
  attempts" below) **and** completed none (the throughput counter did not advance in it), and that held
  for 5 minutes. The second clause exists because a role whose dispatches last seconds is in flight on
  about 3 percent of 30 second scrapes even while it drains a backlog; a dispatch that settled inside the
  window is processing. A dispatch that is running is processing however long it runs.
  Any gate (`SYSTEM_PAUSE` included) holds every listener but its owner, so the rule is suppressed from the
  first scrape of a gate until 10 minutes after it ends; a long hold is `pg-router-gate-held`'s page. The
  depth gauge carries `type` and the in-flight gauge `role`, and the binding is configuration, so the join
  is an explicit table in the rule, one clause per role of the daemon config. The rule needs 18 of 20
  scrapes, which is also its wake grace: a laptop sleep leaves no samples and the condition cannot hold
  again until 10 minutes after the wake (`pg2-9uvm5` owns the dedicated host-awake signal; it will add one
  `unless` clause and change nothing else). The helper `role` label is dropped, so the alert keeps its
  labels and an open bead for its fingerprint keeps matching.
- **Declined attempts are not processing (amended 2026-10-14, bead `pg2-9nfla`).** A declined offer is in
  flight too: the queue marks the role outstanding before the offer and clears it after the offer returns,
  whatever the outcome, and a decline is decided inside the offer (the listener, then the handler's
  admission gate, which reads the pool's sessions and capacity first), about two seconds. The gauge cannot
  tell it from a running dispatch. On 2026-10-13 the account's five-hour usage limit made the handler
  decline every `drain-pg2` offer from 20:54:26Z to 21:59:59Z, one every 13 seconds: nothing was accepted
  from 20:53:50Z to 22:02:39Z, 19 to 24 events waited, and about one scrape in eight landed inside a
  decline, so the 10 minute window restarted every few minutes and the condition held for about 7 of the
  69 minutes (Pending 21:44Z, Alerting 21:49Z, Normal 22:01Z). The ruling ("queue > 0 and nothing is being
  processed") makes a declined attempt idleness, so the rule asks for a dispatch that is RUNNING: the role
  in flight on 6 consecutive scrapes (`min_over_time(listener_in_flight[3m])`) at some point of the window
  (`max_over_time(...[10m:30s])`). A decline reads on a few consecutive scrapes at worst: Prometheus's own
  record of the storm has 17 runs of `listener_in_flight == 1` inside it, none longer than 2 consecutive
  scrapes (one pair), and the replay model, built from the declines in the daemon log and wider than the
  scrapes that really read 1, reads at most 4 at any of the 30 scrape phases, so 6 leaves a margin of 4
  against the record. A running dispatch reads on every scrape until it ends, and the role must still be
  quiet for the window after its last in-flight scrape, as before. A dispatch that settles inside 3 minutes is processing
  through the throughput clause. **Chosen over** keeping a declined attempt out of the gauge in the daemon
  (a new admission signal from the handler, or a second accepted-dispatch gauge: a change to the queue, the
  wire protocol and the ccpool handler), because the rule-side test passes the replay of the recorded storm
  at every scrape phase, needs no deployment of a new router, and keeps `listener_in_flight` meaning what
  `DEC-OBS-9` says. **Consequence, by the ruling:** a lane with a backlog now pages for roughly the length of
  each usage-limit or at-capacity window that declines its offers; the verification found the rule not
  noisy (one Alerting episode, none for `escalated.pg2`). If that proves unwanted, the lever is to count
  at-capacity declines as activity, which needs a new operator ruling.
- **Slow growth (`pg-router-queue-slow-growth`).** A type pages when its arrivals,
  `increase(enqueued_total[6h]) - increase(unconsumed_expired_total[6h])` (see "Re-emitted events" below),
  are above 1.5 times `increase(throughput_total[6h])` (or there are arrivals and no completions series), at
  least 6 arrived, and a backlog of 5 or more did not drain once in the last hour (108 of the hour's 120
  scrapes present, which is also its wake grace), held for 30 minutes. A counter pair was chosen over
  `predict_linear` on the depth gauge, which has no series while a type is drained and has multi-hour holes
  across every laptop sleep; the counters compare the two rates directly and `increase()` is exact across a
  sleep because nothing arrives or completes while the host is asleep.
- **`enqueued`** — a new counter per `type`: events admitted to the queue. A duplicate id, a restored
  event, a refused admission and the Gate Registry's own records are not arrivals; a fresh event that
  reuses a retired id is. The queue retires events unaccepted and the source re-emits them (2026-10-13
  00:18Z to 00:24Z: about 17 such pairs), which this counts as arrivals; the slow-growth rule nets that
  out (the next bullet).
- **Re-emitted events are not arrivals (amended 2026-10-14, bead `pg2-9nfla`).** A drain event is born
  expired, so a decline is its only attempt: the queue retires it unaccepted (`unconsumed_expired`) and the
  source re-emits it on its next tick, which `enqueued` counts as one more arrival. During the decline storm
  of 2026-10-13 (302 declines, 20:54:26Z to 21:59:59Z) the 6 hours to 22:42Z held 312 enqueues of 27
  distinct beads, at most 8 of them new, against 13 completions, a ratio of 24; the alert, which had paged
  correctly for the 14:52Z-20:52Z onset (22 distinct beads against about 6 completions, depth 22 to 35),
  would have stayed Alerting until the burst left the window (about 04:00Z the next day, plus the hour of
  `keepFiringFor`) on a lane whose distinct arrivals kept up. The depth floor does not help a lane that
  holds a standing backlog. Arrivals are now the enqueues less the events retired unconsumed over the same
  window, so a retire-and-re-emit pair nets to zero whichever path retired it (262 of the storm's 302
  retirements were the sweep, 40 a re-emit over the stale entry, so a hook on the stale-retire path alone
  would not have been enough). The counters carry no event id: a retirement that is not re-emitted nets to
  zero as well, so enqueues less retirements is the change in the backlog plus the completions and the rule
  reads "the backlog grew by more than half of what was completed", with the same ratio and floors as
  before and no change to a lane that never retires anything unaccepted (an absent retirements series reads
  as 0). An event that was retired and is not yet re-emitted reads as missing from the backlog: while a
  decline storm lasts the re-emissions trail the retirements by 13 to 16 events (enqueues less retirements
  over the recorded storm: -16 at 21:30Z, -13 at 22:00Z, the depth falling from 35 to 22) and catch up after
  it (+3 at 22:40Z). On the recorded storm that ends the condition at about 21:10Z, once the retirements run
  ahead, and the alert resolves an hour later; the verification's estimate, which counts each distinct bead
  once, would hold the condition to about 23:22Z. Net arrivals in the 6 hours to 22:42Z read 7, where the
  verification counted at most 8 new beads. **Chosen over** a depth-trend guard (the same
  quantity from the gauge, which has no series while a type is drained, holes across every sleep, and
  noise of a few events either way) and over counting an event id once in the daemon, which is the exact
  count but needs the queue to remember a retired id past its sweep (an id memory in `eventqueue`, a new
  observer hook, a router deploy, and a different meaning of `enqueued` for its other readers). **Not
  covered:** a lane that only loses events (every enqueue retired unconsumed) reads no growth here; its
  `unconsumed_expired` counter and its depth are the alarm for that.

**Evidence.** The recorded `drain.pg2` lane of 2026-10-12, reconstructed from the queue log
(`internal/alertrules/testdata/drain_pg2_2026-10-12.txt`), replays through both rules in
`queue_stalled_test.go` and `queue_slow_growth_test.go`: the old expression pages at 18:48Z, as the alert
record says, and the new one never evaluates true during the dispatch; it pages once for a real stretch of
30 minutes (23:03:11Z to 23:33:06Z) in which the lane sat idle with about 20 events queued and the host
awake; the slow-growth rule pages for the 12:00Z to 19:40Z case (11 arrivals, 5 completions). The recorded
`pr.changed` buildup of 2026-09-30 pages as two idle-with-backlog stretches the old rule could not see, and
the absorbed `issue.changed` surge of 2026-10-01 pages nothing. The usage-limit decline storm of 2026-10-13
(`testdata/drain_pg2_2026-10-13.txt`: the queue log and the daemon log, 302 recorded declines) holds the
stalled condition on every evaluation from 21:05Z to 22:00Z at every one of the 30 scrape phases and pages from
about 21:09Z until the dispatch that went in flight at 22:00:10Z is running, where the previous in-flight test
held it for 5 of those 56 minutes in the replay (about 7 in Grafana). The same storm as Prometheus recorded
it (the real `in_flight`, `queue_depth` and `throughput_total` series, 19:30Z to 22:29Z) runs through the
real expression in a `promtool` case: the alert fires at 21:30Z and 21:55Z and is quiet at 20:40Z (a
dispatch running), 22:05Z and 22:20Z (the dispatches settling). The slow-growth rule reads 7 net arrivals
in the 6 hours to 22:42Z (the verification: at most 8 new beads of the 27) and, replayed over the storm, holds
its condition for the onset and ends it at about 21:10Z; the live counters read after the storm agree
(enqueues less retirements over 6 hours 14 to 17 against 5 to 6 completions from 20:00Z to 20:45Z, 10 or
fewer from 21:15Z, inside the counters' first 6 hours, so a cross-check and not a replay).
`grafana/alerting/rule-tests/` runs the same expressions under `promtool`.

**Not covered, by the ruling.** A wedged session that still counts as in flight (the ccpool session rules,
`pool-full-idle` and `needs_input`, own it); a type missing from the rule's role table.
