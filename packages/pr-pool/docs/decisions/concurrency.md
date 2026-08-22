# Concurrency — pr-pool decision docs

Realization decisions about pr-pool's **serialize-mark configuration surface** — the concrete way an
operator marks an event **type** to serialize (`INV-CONC-1`). The behavior side — that concurrency is
handler-enforced with no core-tracked ceiling, that a `type` MAY be marked to serialize so its events
never run in parallel across every handler, and the release condition (settled, not evicted) that
lets the next event of a marked type be offered — is in pr-pool's
[behavior docs](../behavior/invariants.md). Which config keys exist, and where they are decoded, is
recorded here.

### `DEC-CONC-1` — the serialize mark is a type-scoped `[pool]` list, not a per-binding attribute <!-- uuid: 7dc77f61-9c27-4153-9496-270ae5b58584 -->

**Decided.** An event type is marked to serialize with a pool-wide list:

```toml
[pool]
serialize_types = ["shutdown", "time-of-day"]
```

`serialize_types` names event **types**, never a binding or a role. This is the resolution of the
former `OQ-CONC-MARK` ("a per-type config flag vs. a binding attribute"): a **type** can be bound by
several roles (`INV-DISP-1`), and INV-CONC-1's own wording — "a `type` MAY be marked to serialize" —
is already type-scoped, not per-binding. A binding attribute would have to be repeated, and kept
consistent, on every role that binds the type; a type-scoped list has exactly one place to set or
change the mark, and cannot desync across bindings the way a per-binding copy could.

**Realized.** `[pool].serialize_types` decodes into `Config.SerializeTypes` (`internal/config`) and
is threaded into the queue as `eventqueue.WithSerializeTypes(cfg.SerializeTypes...)`
(`cmd/pr-pool/run.go`'s `bootCore`) — the same overlay/threading shape `[pool].retry` and
`[pool].pull_failure_backoff` already use for their own pool-wide defaults. A type absent from the
list is completely unaffected; an empty/absent list (the default) changes nothing for an existing
deployment.

**Not decided here.** Whether a role- or query-level override should ever exist (mirroring
`[role.retry]` overlaying `[pool].retry`) is left for if and when a deployment needs a _narrower_ mark
than "the whole type, everywhere" — nothing here forecloses it, and nothing today asks for it.
