# Event — pr-pool decision docs

Realization decisions about the event the core routes: its expiry contract, and the questions that
contract closed.

### `DEC-EVENT-1` — expiry is an absolute instant, so there is no clock origin to pick <!-- uuid: f2bbe7cf-726d-4ea1-99f5-9582ef4d16c4 -->

**Decided.** An event carries an optional `at` (the source stamp, defaulting to the core's own "now"
at ingest) and an optional `expiresAt` (an absolute instant, defaulting to `at`). The earlier
duration-valued field it replaces is gone from the event, from the schema, and from configuration —
nothing computes a duration, and no configuration declares an expiry. The behavior side of this is
`INV-EVT-1` and `INV-EVT-4`.

**What this resolves, and how.** pr-pool's behavior docs previously carried an open question asking
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
