# pg-review-provider-ccaudit notes

The provider turns the seven canned `pg-ccaudit` queries into evidence rows
(one JSON array on stdout, schema `evidence-rows`).

```text
pg-review-provider-ccaudit --since T --until T --scope NAME
    [--query NAME]... [--cwd-prefix P]... [--exclude-cwd-prefix P]...
```

Exit 0 on success, 2 on a usage error (missing flag, bound without an offset,
unknown query, until before since), 1 when any query fails (nothing is written
to stdout).

## Choices recorded here

- **Output form of every query, including `hook-refusals-in-body`: JSON.**
  The tab form cannot carry a signature that spans lines or contains a tab, so
  the provider reads `--format json` only. Measured basis: a one-day window
  against the real index returned both forms with exit 0, the tab form in about
  4 seconds and the JSON form in under 1 second (cold and warm cache
  respectively, so the difference is not meaningful), and the JSON output
  carries the same rows. The earlier report of the JSON form running past a
  60 second foreground limit did not reproduce for a one-day window. No path or
  figure from the output is recorded.
- **Window bounds** are parsed as RFC3339 with an offset, converted to UTC and
  written with a `Z` suffix (fractional seconds kept) before the call, because
  `pg-ccaudit` compares ISO-8601 text. A bound with no offset is a usage error.
- **Scope** is written only into each row's `scope`; the caller supplies
  `--cwd-prefix` flags, which pass through unchanged. Review-job sessions are
  excluded by the caller's `--exclude-cwd-prefix` (the analyst's neutral
  directory), also passed through.
- **Subject keys.** `tool:<tool_name>`; `sig:<label>:<hash>` (label: signature
  with paths replaced by `<path>` and numbers by `N`, whitespace collapsed,
  first 40 runes; hash: first 8 hex of SHA-256 over the raw signature);
  `lead:<lead_cmd>` (the tool name when the lead command is empty);
  `session:<cluster>` where the cluster id is 8 hex of a hash of the session
  and the cluster's first line, and rows of one session at most 10 seconds
  apart form one cluster. Session ids, paths and sequence numbers appear only
  as samples (`session=... path=... seq=...`, at most 5, sorted).
- **Kinds and `type`.** `error-rate` (type `tool`), `error` (type
  `signature`; top-signatures, merged cost, and hook refusals share one row per
  signature), `retry` (`signature`), `denied` (`lead`), `interruption`
  (`session`).
- **Cost merge.** `cost-by-signature` contributes `elapsed_ms_sum`,
  `elapsed_ms_mean`, `elapsed_ms_p50`, `elapsed_ms_max`, `idle_calls` and
  `idle_ms_sum` to the matching error row; `duration_ms_sum` is never used and
  `idle_threshold_ms` stays at its default. A cost row with no matching row is
  dropped when `top-signatures` ran in the same call (it is outside the ranked
  set) and otherwise becomes its own error row.
- **Row times.** `first_seen` and `last_seen` come from the rows' timestamps
  where the query has them, and are the window bounds otherwise.
- **Ids** are `E-001` onward over rows sorted byte-wise by (kind, subject key);
  the runner renumbers after merging providers. The first sample of a
  signature row is `signature: <raw signature>` (clipped to 500 runes).

## Checks

Unit tests use a stub `pg-ccaudit` on PATH printing the synthetic fixtures under
`testdata/provider-ccaudit/` and compare each query's rows byte for byte with
`testdata/provider-ccaudit/golden/` (`.golden` files, left unformatted so they stay byte-exact). Regenerate goldens with
`go test ./internal/provider/ccaudit/ -update` and review the diff.
