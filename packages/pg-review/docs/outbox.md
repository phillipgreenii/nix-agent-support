# Adopted work-bead outbox

An `adopted` ruling stores the decision first (`bead_state = pending`, an idempotency key
`pg-review:decision-<id>:rev-<revision>`) and then files exactly one work bead. The decision row is
the outbox.

## Filing protocol

1. Claim by compare-and-set on `bead_state`: `pending`, or `filing` with an expired lease, becomes
   `filing` with a lease of 10 minutes and one more attempt. A held lease is not taken.
2. Look for a bead whose external reference is the key (`bd list --all --external-ref <key> --json`).
   File one (`bd create --silent --external-ref <key> -l <label> --body-file - -- <title>`) only if
   none exists, so a crash between the tracker call and the row update never files a second bead.
3. Record the bead id and set `filed`.

A tracker failure leaves the ruling standing and sends the decision back to `pending`. The fifth
failed attempt sets `failed`, which is terminal until `rule <id> reopen` (it resets the bead state
and the attempt count). `outbox_failed` counts `failed` decisions. `rule adopted` files once,
synchronously; `maintain` calls `FileAdoptedBeads`, which retries `pending` and expired `filing` rows.

The bead body is headed `Untrusted origin: written by an automated analyst`, followed by the
rendered review item.

## Tracker and repo-label configuration

The design does not say how the tracker location or the repo label follows from a finding, so the
tool reads this table of `config.toml`:

```toml
[tracker]
command = "bd"                    # the tracker CLI (default "bd")
dir = "/path/to/tracker-root"     # passed as -C; empty means the working directory
default_label = "some-repo"       # used when no [[tracker.repo]] entry matches

[[tracker.repo]]
prefix = "pkg-a/"                 # a path prefix of a finding's first matching target
label = "repo-a"
```

The label is the one of the first finding `target` that starts with a `prefix` (entries are tried in
order), else `default_label`. Targets are paths relative to a scope root. The resolved `dir` and
label are stored on the decision at ruling time. When no label can be determined the ruling still
stands, the filing fails visibly on every attempt (counted, then `failed`), and nothing is sent to
the tracker.
