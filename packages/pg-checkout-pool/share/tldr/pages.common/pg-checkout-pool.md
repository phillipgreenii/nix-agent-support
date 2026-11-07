# pg-checkout-pool

> Lease reusable, already-warm checkouts of a repository to agent sessions.
> A lease is keyed on the owner session id; the work item id is only an affinity hint.
> Every command prints JSON; failures exit 2 or more (3 pool-exhausted, 4 low-disk, 5 ref-unresolvable, 6 quarantine-only, 7 git-wedged, 8 lease-not-found, 10 slot-busy, 11 owner-already-leases).
> More information: `pg-checkout-pool --help`.

- Lease a checkout of a ref for a session (prefers the slot last used for the work item):

`pg-checkout-pool lease --repo {{/path/to/clone}} --ref {{origin/main}} --owner {{session-id}} --bead {{work-item}}`

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

- Print the Prometheus metrics (slot gauges plus reset, wait, failure and rescue counters), or write them atomically for a textfile collector:

`pg-checkout-pool metrics`

`pg-checkout-pool metrics --out {{/path/to/pg_checkout_pool.prom}}`
