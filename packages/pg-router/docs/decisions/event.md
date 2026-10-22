# Event — pg-router decision docs

Realization decisions about the event the core routes and the queue that carries it: the event's
expiry contract, the queue's durability/ordering/delivery shape, and the questions each closed.

### `DEC-EVENT-1` — expiry is an absolute instant, so there is no clock origin to pick <!-- uuid: f2bbe7cf-726d-4ea1-99f5-9582ef4d16c4 -->

**Decided.** An event carries an optional `at` (the source stamp, defaulting to the core's own "now"
at ingest) and an optional `expiresAt` (an absolute instant, defaulting to `at`). The earlier
duration-valued field it replaces is gone from the event, from the schema, and from configuration —
nothing computes a duration, and no configuration declares an expiry. The behavior side of this is
`INV-EVT-1` and `INV-EVT-4`.

**What this resolves, and how.** pg-router's behavior docs previously carried an open question asking
which instant started the expiry clock: the event's `at`, or the moment the core ingested it. That
question is **resolved by restructure, not by picking one of the two answers.** A duration needs an
origin to be measured from, which is why the question existed at all; an **absolute instant does
not**. `expiresAt` names the moment itself, so there is no origin left to choose and the question has
no content once the field changes shape. Recording it here rather than as a settled open question is
deliberate: the question is not answered, it is **dissolved**, and a reader who finds the old wording
in history needs to know which of the two it was — neither.

The consequences the behavior docs now state, rather than leave to be discovered:

- **The default event is born expired.** With neither field set, `expiresAt` resolves to `at`, which
  resolves to ingest-now. So the default behavior is "offer once to every matching handler, then
  drop" — a best-effort default that needs no configuration.
- **`expiresAt` is the retry window.** Before it, the re-offer behavior of `INV-FAIL-1` is unchanged;
  absent it, there is no retry. Requesting retries and widening the de-duplication window are the
  same one knob.
- **De-duplication narrows with it.** The retained-id set lives exactly as long as the event does
  (`INV-EVT-3`), so under the default the de-dup window is roughly one dispatch cycle and a pull
  query's next-trigger re-emit is not absorbed. That is "re-emission, not resurrection", and it is a
  real change in behavior rather than a restatement.
- **`unconsumed-expired` becomes a stronger signal.** Because the expiry check happens at attempt
  time, an event can no longer expire without having been offered to a busy handler, so the metric
  now counts a genuine miss (`INV-DISP-3`).

**Why the check is stateless.** The rule is "if the event is already expired at the moment an attempt
is made, that attempt is the last one for that handler". Phrased that way the core keeps **no attempt
history**: one comparison at attempt time is the whole decision, and the delivery-opportunity
guarantee still holds because a born-expired event's first attempt is also its last. An
attempt-counting alternative would have had to persist per-handler counters across restarts to mean
anything, which is durable state bought for no behavioral gain.

**Relation to `ADR 0031`.** `ADR 0031` decided the durable, ordered, de-duped queue and deliberately
left the clock origin open. This entry closes that residue; the queue decision itself is unchanged.

**Not decided here.** The wire encoding of the two fields and the schema artifact that carries them
are the implementation's own; the conformance suite (`INV-INTF-2`) is where the two sides reconcile.

### `DEC-EVENT-2` — the queue is durable, ordered, de-duped and retention-bounded, delivering at-least-once with per-handler serial FIFO <!-- uuid: 17672d4d-21fd-4bf3-8adb-e61118485f5c -->

**Decided.** The core holds one **durable, ordered, de-duped, retention-bounded** event queue, and
delivery through it is **at-least-once**: the durable record is written only **after acceptance is
confirmed**, so a narrow crash window MAY redeliver an accepted event — the reason a handler MUST be
idempotent (`INV-EVT-2`). The core **attempts delivery until a handler accepts**, and acceptance is
the **retry boundary**: before it, a pre-accept decline is the core's to retry; after it, the handler
owns persistence, resume, and retry, and the core neither re-offers nor classifies what happens next
(`INV-FAIL-1`). Ordering is **per-handler serial FIFO** — the core keeps **one outstanding offer per
handler** and offers that handler its next matching event only once the current one is accepted or
expires. Per-handler cursors are independent and order is **never global**: fan-out across handlers
is supported, and acceptance is tracked per `(event, handler)` (`INV-CONC-1`). Capacity stays
**handler-enforced** — a pre-accept `busy` decline, never a number the core tracks — exactly as `ADR
0031` decided it.

**The next offer follows the accept, not the next pass** (bead `pg2-ag31a`). "Only once the current
one is accepted" is the whole gate: the core offers a handler its next eligible head the moment the
current offer is accepted (`INV-LIFE-3`), still one outstanding offer per handler, instead of
waiting for the drive loop's next pass. Before this a pass offered each handler at most once, so a
handler that accepts in 0.1 s was capped at one event per pass interval (about 300 events an hour
at a 10 to 13 s interval) and a 5900-event backlog never drained. Chosen over running the offer
pass on a faster timer, which re-scans every handler's head on a clock instead of reacting to the
one accept that frees it; the cost is that one fast handler is served back to back through its own
backlog (other handlers are unaffected: each has its own cursor and offers run concurrently).

**Head-of-line blocking is the accepted cost of per-handler FIFO.** An event a handler cannot yet
accept stalls only that handler's own stream until the event expires; the mitigation is a short
`expiresAt`, not reordering around the stuck head.

**Relation to `ADR 0031` and `DEC-EVENT-1`.** `ADR 0031` decided this queue and delivery shape —
durable, ordered, de-duped, at-least-once, retry-only-until-acceptance, per-listener (now
per-handler) serial FIFO, handler-enforced capacity — together with the event's now-superseded
duration-valued bound. `DEC-EVENT-1` is the decisions-doc record for the **expiry** half of that
decision (a clock origin dissolved by making expiry an absolute instant); this entry is the
decisions-doc record for the **queue and delivery mechanics** half, which `ADR 0031` left standing
and which pg-router's behavior docs cite directly for that reason.

**Not decided here.** The storage mechanism (jsonl / embedded DB / write-ahead log) is an open
realization choice, not behavior or this entry's — `ADR 0031`'s own "Consequences" leaves it so.
Whether a deployment opts in to evicting an accepted event before its retention window ends is
stated directly in the behavior set's glossary and is not repeated here.

### `DEC-EVENT-3` — the durable queue is size-bounded: a soft threshold halts polled emitters, the maximum refuses admission with a classified reason <!-- uuid: 8e5ff850-31f1-4c4a-bf55-875701f70455 -->

**Decided** (operator rulings, Phillip, 2026-10-02, bead `pg2-5d3ui`; the soft-step mechanism on
the same day, option "B"). The queue's write-ahead log (`queue.jsonl`, events **and** gates) has a
configured maximum size, `max_log_bytes` (`[pool].max_log_bytes` / `PG_ROUTER_MAX_LOG_BYTES`,
default 64 MiB; the env var accepts units such as `64MiB` and an unparseable value is an error, never
silently the default). Two **derived** thresholds, one user-visible knob: **soft** = 90% of the
maximum, **hard** = the maximum; `Validate` enforces `compact_threshold_bytes < soft < max_log_bytes`.
This **replaces** the earlier idea of per-event-type count caps with drop-oldest eviction, which the
ruling rejected: nothing already queued is ever evicted to make room.

- **Order at startup.** Compaction runs first (`pg2-8e0m6`), then the limits are evaluated — the
  live log was ~33 MB of mostly dead history, so a default below that would otherwise refuse events
  at boot.
- **Soft step — an in-memory flag, deliberately NOT a gate.** After a compaction attempt, if the log
  is still over soft, the queue sets `emittersHalted`: the producer skips every polled source except
  the timer emitter, checked right next to the gate check. It is re-derived **every tick** from the
  log size, is volatile (nothing is persisted, so a restart simply re-derives it), and is **not**
  cleared by `gate clear` / `resume --all`. The gate registry (`BlockingGate`, `CheckPull`, the
  `NonBlockingGates` opt-out, `dropGateBlockedLocked`) and the gated drain-and-exit are untouched —
  making this a gate would have meant a new emitter-only gate class and an amendment to `INV-LIFE-2`
  (rejected as option "A"), and a gate also blocks listeners, which would stop the very dispatch that
  frees the space. Listeners keep dispatching and the drain keeps draining while halted; timer
  emitters and pushed events keep being admitted — **only the hard limit stops those.** Because it
  is not a gate, its whole surface is `status`, the `tui` and a metric, which state plainly what is
  halted and what still runs.
- **Hard limit — admission only.** At or above the maximum, `Enqueue` refuses new events with
  `ErrLogFull` (`log_full: ...`). The limit applies to **admission and nothing else**: accept, evict
  and gate records still append above it (the soft-to-hard headroom exists for this; refusing an
  accept record would make the event redeliver on every restart). A still-retained duplicate id
  stays a no-write success.
- **Unwritable.** The first append error (disk full, I/O error) marks the log unwritable
  (`ErrLogUnwritable`, `log_unwritable: ...`): admission is refused and polled emitters halt. The
  mark is in memory and never depends on a record being writable; it clears on the next successful
  append or on a per-tick recovery probe (a small file written and removed beside the log; a store
  with no probe is retried half-open). A failed write is rolled back by truncating to the pre-write
  length, so a short write cannot fuse onto the next record.
- **Refusal is non-fatal to the loop.** A classified refusal from a pull or timer source is recorded
  as a source error and a per-source reject count and the pass continues; the tick always runs the
  limit controller, `Kick`, `Expire` and `PublishTick` even when producing errors, and
  `run-until-idle` drains before exiting — otherwise the never-gated timer source would wedge the
  loop at the maximum and nothing could ever free space.
- **Classified reasons.** The reply's `reason` begins with a fixed prefix, `log_full:` or
  `log_unwritable:`; the exit code stays `1`, because the reply schema carries only `{id, reason}`
  and the prefix is the contract. Refusals are counted at the one choke point (`Enqueue`), push and
  pull together, by reason (`pg_router_enqueue_rejected`); the pull side is additionally broken out
  per source in the produce report.
- **Not in scope.** Per-type fairness: one noisy type can fill the file for all (accepted; event TTL
  still bounds how long any event waits). `events.jsonl` and `launchd-stderr.log` in the same state
  directory are separate and unbounded, though they share the disk and can cause `log_unwritable`.

### `DEC-EVENT-4` — the queue log has one owner at a time, and `log compact [--dry-run]` is the operator's way to reclaim dead history <!-- uuid: 7339dfe9-1171-4e66-8856-dc382142ef8a -->

**Decided** by the implementing agent (bead `pg2-maxn1`, finishing what `pg2-8e0m6` specified and
never shipped) where `pg2-8e0m6` left a choice open; the operator MAY overrule any point. The
operator's own request (Phillip, 2026-10-02) was that size and percentage be visible in status and
the TUI and that there be a way to trigger a compaction and a dry-run compaction from the CLI.

- **One owner: fail fast.** `NewFileStore` takes an exclusive, non-blocking `flock` on
  `queue.jsonl.lock` before touching the log and holds it until `Close` (the kernel drops it on
  death). A second opener gets `*ErrLogLocked` and the daemon refuses to start. `pg2-8e0m6` allowed
  "fails fast or at least skips compaction"; skipping only compaction would still let two processes
  append to one file, and the daemon's own `ErrAlreadyRunning` fires only after the queue has already
  replayed and compacted the log. The lock is a **sibling file** because compaction renames a new
  inode over the log, which would silently end a `flock` on the log itself. A stale compaction temp
  file is removed only after the lock is held.
- **Where `log compact` runs.** Against a running core it is a socket verb (`log-compact`,
  `cli.log-compact` / `cli.log-compact-reply`) and the daemon does the work, so the process holding
  the lock is the one touching the file. It uses the compaction the startup and threshold triggers
  use (trigger `manual`): the landed scheme folds a prefix **without** holding the queue lock and
  splices the tail under the store's own mutex, rather than `pg2-8e0m6`'s "holds `q.mu`", which is
  the better property and is kept. With no core running it compacts **offline**, taking the lock
  itself and refusing (exit `1`) when another process holds it; a core named explicitly by
  `--socket` never falls back to offline. The socket verb alone gets a longer connection deadline
  (2 minutes, set only after the token check) because it does real file work.
- **Dry run.** Reads only: no rename, no temp file, no lock kept, no lock file created, no counter.
  It reports bytes and records before and after, events kept and dropped, gates kept, the percent of
  `max_log_bytes` afterwards, a torn tail, and whether a real run would be refused (the offline case:
  the log is locked) or would do nothing.
- **"Nothing to do" is success.** A real run whose plan finds no progress possible (the log is
  already live state only) rewrites nothing, does not count as a compaction and exits `0`; it is not
  a refusal. Exit codes: `0` ok (including nothing to do), `1` refusal or failure, `2` usage.
- **Status.** `queueLog.lastCompaction` {`at`, `trigger` = `startup` | `threshold` | `limit` |
  `manual`, bytes and records before and after, `durationMs`}, in `status`, `--json` and the TUI.
- **Remedy.** The size-limit notices, the `log_full` reason text, the README and the Grafana rule
  name `pg-router log compact` (and its `--dry-run`) as the remedy for a log that is large because of
  dead history. It never discards a queued event, so it does not help when the backlog itself is the
  problem.

The behavior-doc side is `INV-EVT-1`'s "one sanctioned refusal at ingest" paragraph and the
`ingest-event` / inspection sections of the interfaces doc.

### `DEC-EVENT-5` — a restart over a dispatch left running: the first dispatch pass comes before the first producer tick, and the adopting handler, not the queue, carries the dispatch's original start <!-- uuid: 7c3e9a14-5d82-4b6f-9e07-d1a8f4c26b35 -->

**Decided** (bead `pg2-x3zkb`, 2026-10-14, by the implementing agent on the evidence of `pg2-b1srv`;
the operator MAY overrule any point). The bead asks two things about a daemon restart that leaves a
session-backed dispatch running (this repo's ADR 0085: "the next daemon re-offers their events and
adopts the sessions"): **(a)** who carries the dispatch's original start into the accept record and
the metrics, and **(b)** whether the new daemon re-offers before its first producer tick, or at least
reports in-flight truthfully from its first scrape. **(b) is decided and built in this change; (a) is
decided and left as implementation slices**, because it crosses the wire contract, the core and the
session-backed handler. Neither changes what `listener_in_flight`, `queue_wait` or `run` measure
(`DEC-OBS-9`): (b) leaves the gauge an outstanding offer, and (a) makes an adopted dispatch measure the
wait and run they already name.

**What a restart does, from source.**

- The new process replays the queue log. The surviving dispatch's event has no accept record, because
  an accept is written only when its offer settles, so it replays un-accepted (ADR 0085's evidence,
  question 1).
- The daemon boots the core, sends each enabled role `postStartup`, then loops. Each iteration runs the
  producer tick and only then the dispatch pass. A fresh process has no last-fire time for any source,
  so every period source is due and the first producer tick polls all of them, one after another,
  each with its own retry and backoff. It is the one tick on which every period source is due, and it
  stood in front of the re-offer.
- An offer's start is the instant its pass takes the listener's slot. The accept record, the
  queue-wait and run histograms and the event log row all read it, so a re-offered dispatch is timed
  from the re-offer.

**Evidence** (`pg2-b1srv`: raw Prometheus samples and the daemon log, as that bead's 2026-10-14
comment corrected them; items marked "not re-checked" are that investigation's own).

- The 2026-10-12 restart: the new process was serving metrics at 23:28:38Z, its startup compaction
  ended at 23:28:44.25Z and its first dispatch pass re-offered the event at 23:33:06.6Z. Process start
  to re-offer is 23:33:06.6 - 23:28:38 = 268.6 s (4 min 28.6 s). Boot, to the end of the compaction,
  is 23:28:44.25 - 23:28:38 = 6.25 s. The remaining 268.6 - 6.25 = 262.35 s is the `postStartup`
  hooks plus the first producer tick; **the evidence does not separate the two**. The role's gauge
  read 1 at 23:27:44Z on the old process, 0 on 8 scrapes from 23:28:53Z to 23:32:57Z and 1 again at
  23:33:25Z.
- That dispatch's accept record says it ran 2,696,857 ms, 2,696,857 / 60,000 = 44.95 min, against a
  real 4,459.52 s, 4,459.52 / 60 = 74.3 min: 74.3 - 44.95 = 29.4 min of run time recorded as queue
  wait.
- Five restarts in three days re-timed a drain-pg2 start, by 29.4, 19.8, 61.9, 3.2 and 12.5 minutes
  (the last four not re-checked): 29.4 + 19.8 + 61.9 + 3.2 + 12.5 = 126.8 minutes of run time read as
  queue wait.
- The restart holes seen, process start to re-offer (not re-checked): 4:28, 3:52, 2:17, 2:23 and 1:58,
  that is 268, 232, 137, 143 and 118 s.
- `pg-router-queue-stalled` needs a hole of about 12 minutes to page: the condition holds from 10
  minutes after the last in-flight scrape until a run of 6 scrapes completes 3 minutes after the
  re-offer, and `for` is 5, so (H + 3) - 10 >= 5 gives H >= 12 minutes (`DEC-OBS-10`). The longest
  hole seen, 268 / 60 = 4.47 min, is 12 - 4.47 = 7.5 min short of it.

```mermaid
flowchart LR
    boot["boot: replay the queue log, startup compaction"] --> hooks["postStartup hooks"]
    hooks --> pass["first dispatch pass: re-offer what was restored"]
    pass --> tick["first producer tick: poll every source"]
    tick --> loop["each later tick: producer tick, then dispatch pass"]
```

**(b) Decided and built: offer before producing, at startup only.**

- The daemon **MUST** run one dispatch pass after its `postStartup` hooks have returned and before its
  first producer tick, and **MUST NOT** run it once shutdown has been requested (the rule every
  later pass already follows). It **SHOULD** log the pass with the number of offers it launched.
  Every later iteration is unchanged.
- `listener_in_flight` **MUST** keep its meaning, an offer outstanding (`DEC-OBS-9`), and **MUST NOT**
  be pre-set from restored queue state. After this change it reads 1 from the first pass, not from the
  end of the first producer tick; it reads 0 only for the boot and the hooks, and nothing at all for a
  scrape that lands before the queue is built (the "scrape before the queue exists" bullet below).
- **Residual.** The hole is now the boot plus the `postStartup` hooks. Boot was measured at 6.25 s; the
  hooks are not measured (a session-backed handler's own `postStartup` does no work beyond starting its
  process, but a process start under a host load average of 195 is not free).
- **A scrape before the queue exists is not a reading at all** (found while building (b), fixed by
  bead `pg2-nzkjl`, 2026-10-14). The daemon starts serving `/metrics` before it boots the core, and
  `bootCore` registers the gauge callbacks before it constructs the queue (the queue takes the metrics
  emitter as its observer, so the emitter comes first), so a collect in between called them with a nil
  queue and dereferenced it. The window is the queue's construction, above all its startup compaction
  (6.03 s on 2026-10-12). A probe on 2026-10-14 (a manual reader collecting in a loop while `bootCore`
  ran, 300 boots at most) panicked in the depth callback (`DepthByType`).
  - **Decided.** Every gauge that reads the queue (queue depth and backlog, oldest-owed age,
    `listener_in_flight`, the event-log size and limit gauges and the active-gate gauge) **MUST** observe
    nothing until `bootCore` has built the queue and registered its listeners, and **MUST NOT** call a
    function that reads the queue before then. A collect in the window therefore reads no series for
    them: not a panic, and not a zero, because there is no queue to read a zero from, and a zero would
    say "nothing queued, no offer outstanding" of a daemon that has not replayed its log. `bootCore`
    publishes the queue once, as its last step, through an atomic pointer (the collecting goroutine is a
    scrape's, not `bootCore`'s, so the plain variable the callbacks used to close over was also an
    unsynchronized read), and `metrics.WithQueueReady` holds those gauges silent until then. Every other
    instrument is unaffected.
  - **What stays true.** From the first scrape that sees the built queue the series are present as
    `DEC-OBS-9` and `DEC-OBS-10` state (`listener_in_flight` is 0 or 1 for every registered role). A
    scrape in the window reads no series, like a scrape before the instruments exist, and
    `pg-router-queue-stalled` is indifferent to it: its depth side needs 18 of 20 scrapes, so it
    tolerates a missing one, and with no in-flight sample at all its `and on(role)` clause matches
    nothing, so a missing series can only keep it quiet, never fire it.
  - **Severity: a failed scrape, not a crash** (from source; pinned by
    `TestStartMetricsServer_APanickingGaugeCallbackFailsOneScrapeNotTheProcess`). When `--metrics-addr`
    (`PG_ROUTER_METRICS_ADDR`) is set, the daemon's reader is the OTel Prometheus exporter
    (`exporters/prometheus` v0.56.0 over `sdk/metric` v1.46.0's manual reader), registered on its own
    `prometheus.Registry` and served by `promhttp.HandlerFor` (`startMetricsServer`). A scrape runs the
    callbacks on a collect-worker goroutine that `Registry.Gather` (`client_golang` v1.24.1,
    `prometheus/registry.go`) starts, not on the HTTP handler's goroutine and not on `bootCore`'s.
    Neither `pipeline.produce` (`sdk/metric`, whose deferred unlock still runs) nor the exporter's
    `Collect` recovers a panic, but `Registry.Gather` runs every collector through `safeCollect`, which
    turns a panic into an error, and `promhttp.HandlerFor` with its default `HandlerOpts`
    (`HTTPErrorOnError`) answers an error with HTTP 500 and the error, stack included, as the body. One
    scrape fails; the process, its listener and the SDK survive, and the next scrape reads normally.
    `pg-router-liveness-down` waits `for: 5m`, so a lone failed scrape does not page. Without
    `--metrics-addr` nothing collects before `bootCore` returns: the default manual reader is read only
    by the core's `status` and `mon.read` verbs, which are served after it. The panic the 2026-10-14
    probe saw ran on its own collecting goroutine with nothing to recover it, which is not this path.
- **Test.** `TestRunLongRunning_restartReoffersTheSurvivingDispatchBeforeTheFirstProducerTick` restarts
  a daemon over a surviving dispatch with a first producer tick that never ends and asserts the
  re-offer and the gauge; `TestRunLongRunning_noStartupDispatchPassOnceShutdownWasRequested` pins the
  shutdown rule. `TestBootCore_ACollectBeforeTheQueueExistsReadsNothing` runs a collect inside the
  window deterministically (a `slog` handler runs it from the startup-compaction record, which
  `eventqueue.New` emits from inside the window) and asserts no panic, no series for any queue-bound
  gauge, and every series from the first collect after `bootCore` returns;
  `TestQueueReadyGatesEveryQueueBoundGauge` pins the option in `internal/metrics`.

**(a) Decided and built, the core side (bead `pg2-dm6de`) and the session-backed handler's report (bead
`pg2-m2x35`): the adopting handler reports the original start.**

- A handler that adopts a session left running by an earlier daemon **MUST** be able to report the
  dispatch's original start in an **optional** `startedAt` field (RFC 3339, UTC) of its dispatch
  reply, and a session-backed handler **SHOULD**: it already takes the session's recorded launch time
  (`pgrouter.launched_at`) as the start of an adopted session's budget, so the same value is the start
  of the dispatch.
- The core **MUST** use a reported start as the dispatch's start in the accept record's `startedAt`,
  in `DispatchTiming` (queue wait = reported start - enqueue instant, run = settle - reported start)
  and in the event log row (`started_at`, and `duration_ms` as settle - reported start). It **MUST**
  ignore, and log, a reported start earlier than the event's enqueue instant or later than the offer's
  own start. A reply without the field **MUST** behave exactly as today, so a fresh session, an older
  handler and every other handler are unchanged.
- The core **MUST NOT** derive the start itself and **MUST NOT** persist per-attempt state for it
  (`DEC-EVENT-1`).
- The reported instant is wall-clock with second precision. The launch time is later than the offer
  that caused it by about 2.5 s on 2026-10-12 (the session was created at 23:03:46.48Z for an offer at
  about 23:03:43.98Z, 23:03:46.48 - 23:03:43.98 = 2.50 s), against the 29.4 min it corrects.
- Metric names, labels and buckets do not change; only the start the existing members measure from.
- **Consistency with `pg2-9uvm5`** (open, human-labelled, not resolved here). Nothing here adds a
  clock, renames a series or answers whether a host-awake signal is needed. The corrected start is a
  wall-clock instant, the basis that bead's draft design already gives a restored event's wait, so the
  two agree. One interaction to carry into it: an awake-adjusted queue wait computed when the offer is
  created would end at the re-offer, whereas the wait of an adopted dispatch ends at the reported
  start, which the core learns only when the reply arrives.
- **Built in the core (bead `pg2-dm6de`, 2026-10-14), the details the slice settled.** The field is
  `startedAt` in `handler.dispatch-reply`, optional on both reply shapes, an RFC 3339 instant
  (fractional seconds and a numeric offset allowed). `wireclient` decodes it as `Reply.StartedAt` and
  ignores, with a warning, a value that is not such an instant, whatever its JSON type, so a handler's
  slip in an advisory field never fails (and so never re-runs) a dispatch that worked. The valid range
  is `[the event's At, the offer's own start]`, **both ends included**, and a start outside it is
  **ignored, not clamped to the nearer end**: a value that wrong says nothing about where the true one
  lies. The check (`eventqueue.CheckAdoptedStart`) runs twice on purpose, once in the role listener,
  which relays a vetted start as `OfferResult.StartedAt` and times the `events.jsonl` row from it, and
  once in the queue, before the durable accept record is written, because that record is replayed and
  compacted forever and one bad start must not outlive its process. The row's `started_at` is the
  reported start and its `duration_ms` is the offer's start minus the reported start plus the offer's
  own run, which is `settle - reported start` and so what the `run` histogram holds. A failed dispatch
  ignores the field. `run-role`'s single-event path is not queue-driven and does not read it. Still
  **Built in the session-backed handler (bead `pg2-m2x35`, 2026-10-14).** The executor's
  adopt path returns the adopted session's `pgrouter.launched_at` as the dispatch result's start, and
  the `dispatch` subcommand writes it as the inline reply's `startedAt` (RFC 3339, UTC, second
  precision). A fresh session, and an adopted session that recorded no launch time (an older build),
  reply with no field. Still open, as post-apply evidence only: that an installed daemon and handler
  carry both slices, read as the accept record's `startedAt` equal to the session's launch time over a
  restart.

**Alternatives rejected.**

- **Memento in the durable log** (the core persists each offer's start, or an "in flight" marker at
  shutdown, and the next process restores it) for (a). Rejected on three counts. A record per offer
  adds one fsynced write per attempt, declines included: the 2026-10-13 decline storm had 302 in
  65.55 min (20:54:26Z to 21:59:59Z, 3,933 s, one per 3,933 / 302 = 13.0 s on one lane), and
  `DEC-EVENT-1` keeps no attempt history for exactly this reason. A marker written only at shutdown
  misses a SIGKILL, an out-of-memory kill and a panic, which ADR 0085 says leave the same state. And
  neither can tell an adopted session from a fresh one launched after the old session settled (ADR
  0094), so it would carry a start into a dispatch that did not begin then. The handler holds the
  fact (it is the Information Expert) and the core only needs it relayed.
- **Derive the start in the core** from the session name or a queue-side stamp. Rejected: the core
  knows no handler's naming (`GOAL-MIN-1`).
- **Flag and exclude** (mark a re-timed record and let consumers skip it) as the end state for (a).
  Rejected: the measurement stays lost. It is what the lane fixtures do for the recorded days.
- **Leave it** for (a). Rejected: 126.8 minutes of run time read as queue wait in three days, on the
  histograms the saturation reading uses (`pg2-9uvm5`).
- **Dispatch before every producer tick** (reorder the loop body) for (b). Rejected: every event the
  tick produces would wait one more poll interval (10 s when none is configured) to be offered, for a
  benefit that exists only at process start.
- **Decouple production and dispatch** into two loops (Producer-Consumer) for (b). The right shape if
  a producer tick other than a process's first is found to hold up dispatch; rejected for now because
  it changes `INV-LIFE-2` and `INV-LIFE-3`'s single loop, the limit controller's place before
  producing, and tick publishing, which is not a small change for a delay seen only at start. Only a
  process's first tick finds every period source due: later ticks poll the sources whose period
  elapsed.
- **Presume the head in flight** (set the gauge from a restored, un-accepted head of a surviving role)
  for (b). Rejected: a restored head may be merely queued, and presuming it running hides exactly the
  idle-with-backlog that `pg-router-queue-stalled` exists to catch for as long as the first pass is
  delayed. The gauge is defined as an outstanding offer.
- **Withhold the series until the first pass** for (b). Rejected: `listener_in_flight` is present, 0 or
  1, on every scrape, which the rule's tests and its `max_over_time` window rely on, and it leaves the
  answer unknown for as long as the first pass is delayed.
- **A restart grace in the alert** for (b). Rejected: the longest hole seen is 7.5 min short of
  paging, and a grace would also hide a real stall that follows a restart.
- **The pass before `postStartup`** for (b). Rejected: a participant's boot-time setup (starting a
  daemon, warming a cache, `DEC-WIRE-3`) is only worth a hook if it is done before that participant's
  first dispatch, and the saving here is only the hooks' time. See the trigger below.

**Revisit when** (the implementer of either trigger **SHOULD** reopen this entry rather than patch
around it): on a build with this change, a restart over a surviving session still leaves
`listener_in_flight` at 0 for more than 4 scrape intervals, 4 x 30 s = 120 s, after the process
started, which makes the `postStartup` hooks the cause (the pass **SHOULD** then move ahead of them or
run beside them); or a producer tick other than a process's first is observed delaying a dispatch pass
by more than 1 minute (decouple the two stages).

**Not decided here.** The reply field's schema text, the clamping details and the handler's mapping
from a session's launch time to the reply are the implementation slices' own. How `pg2-9uvm5` reads
queue wait is that bead's. The behavior-doc side of (b) is `INV-LIFE-3`'s restart paragraph and the
stopping-state and `listener_in_flight` text of the interfaces doc. (a)'s intent is stated in the
interfaces doc's `queue_wait` text; it was registered as a gap against `INTF-MON` in the behavior
set's "Realization gaps" until the last slice (bead `pg2-m2x35`) removed that row.
