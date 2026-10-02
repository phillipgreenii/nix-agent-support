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
