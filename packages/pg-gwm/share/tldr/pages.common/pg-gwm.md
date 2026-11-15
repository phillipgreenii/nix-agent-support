# pg-gwm

> Lease reusable, already-warm checkouts of a repository to agent sessions.
> A lease is keyed on the owner session id; the work item id is only an affinity hint.
> Every command prints JSON; failures exit non-zero (1 internal, 2 usage, 4 lease-not-found, 9 slot-busy, 11 owner-already-leases, 20 pool-exhausted, 21 low-disk, 22 ref-unresolvable, 23 quarantine-only, 24 git-wedged).
> More information: `pg-gwm --help`.

- Lease a checkout of a ref for a session (prefers the slot last used for the work item):

`pg-gwm lease --repo {{/path/to/clone}} --ref {{origin/main}} --owner {{session-id}} --bead {{work-item}}`

- Lease again for a session that may already hold one (a redispatch): the live lease is renewed and returned unchanged (same lease id, slot and path; the checkout is not reset). Without `--reuse` the second lease exits 11:

`pg-gwm lease --repo {{/path/to/clone}} --ref {{origin/main}} --owner {{session-id}} --reuse`

- Extend a lease before it expires:

`pg-gwm renew --lease {{lease-id}} --ttl {{30m}}`

- Give the checkout back: reset it for the next lease (uncommitted tracked work is captured to a rescue ref first):

`pg-gwm release --lease {{lease-id}}`

- Leave the checkout as it is and hold it for the operator:

`pg-gwm release --lease {{lease-id}} --disposition {{keep}}`

- Show what the janitor would reclaim from expired leases, then reclaim:

`pg-gwm reap --dry-run`

`pg-gwm reap`

- Show every slot, its state, owner and expiry:

`pg-gwm status`

- Print the Prometheus metrics (slot gauges plus reset, wait, failure and rescue counters), or write them atomically to a file:

`pg-gwm metrics`

`pg-gwm metrics --out {{/path/to/pg_gwm.prom}}`

- Serve the same exposition on a loopback `/metrics` port, rendered on each scrape (the launchd exporter runs this):

`pg-gwm serve --addr {{127.0.0.1:9148}}`

- Also expose Go's pprof endpoint while serving (the binary opens it only when the variable is set, and the launchd exporter module sets it by default (`pprof.enable`); an absolute unix-socket path or a loopback `host:port`):

`PG_GWM_PPROF_ADDR={{/path/to/pprof.sock}} pg-gwm serve`

- Grow a cold pool to a number of slots, one slot at a time with a pause between creations (refused below the free-space floor):

`pg-gwm prewarm --repo {{/path/to/clone}} --ref {{origin/main}} --count {{4}} --delay {{30s}}`

- After an apply, check every idle slot and quarantine any that is no longer a healthy checkout:

`pg-gwm revalidate --repo {{/path/to/clone}}`
