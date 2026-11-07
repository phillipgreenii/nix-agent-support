# pg-checkout-pool

> Lease reusable, already-warm checkouts of a repository to agent sessions.
> A lease is keyed on the owner session id; the work item id is only an affinity hint.
> Every command prints JSON; failures exit 2 or more (3 pool-exhausted, 4 low-disk, 5 ref-unresolvable, 6 quarantine-only, 7 git-wedged, 8 lease-not-found, 10 slot-busy, 11 owner-already-leases).
> More information: `pg-checkout-pool --help`.

- Lease a checkout of a ref for a session (prefers the slot last used for the work item):

`pg-checkout-pool lease --repo {{/path/to/clone}} --ref {{origin/main}} --owner {{session-id}} --bead {{work-item}}`

- Lease again for a session that may already hold one (a redispatch): the live lease is renewed and returned unchanged (same lease id, slot and path; the checkout is not reset). Without `--reuse` the second lease exits 11:

`pg-checkout-pool lease --repo {{/path/to/clone}} --ref {{origin/main}} --owner {{session-id}} --reuse`

- Extend a lease before it expires:

`pg-checkout-pool renew --lease {{lease-id}} --ttl {{30m}}`

- Give the checkout back: reset it for the next lease (uncommitted tracked work is captured to a rescue ref first):

`pg-checkout-pool release --lease {{lease-id}}`

- Leave the checkout as it is and hold it for the operator:

`pg-checkout-pool release --lease {{lease-id}} --disposition {{keep}}`

- Show what the janitor would reclaim from expired leases, then reclaim:

`pg-checkout-pool reap --dry-run`

`pg-checkout-pool reap`

- Show every slot, its state, owner and expiry:

`pg-checkout-pool status`

- Print the Prometheus metrics (slot gauges plus reset, wait, failure and rescue counters), or write them atomically to a file:

`pg-checkout-pool metrics`

`pg-checkout-pool metrics --out {{/path/to/pg_checkout_pool.prom}}`

- Serve the same exposition on a loopback `/metrics` port, rendered on each scrape (the launchd exporter runs this):

`pg-checkout-pool serve --addr {{127.0.0.1:9148}}`

- Grow a cold pool to a number of slots, one slot at a time with a pause between creations (refused below the free-space floor):

`pg-checkout-pool prewarm --repo {{/path/to/clone}} --ref {{origin/main}} --count {{4}} --delay {{30s}}`

- After an apply, check every idle slot and quarantine any that is no longer a healthy checkout:

`pg-checkout-pool revalidate --repo {{/path/to/clone}}`
