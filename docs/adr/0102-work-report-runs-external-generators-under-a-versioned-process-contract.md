# work-report runs external generators under a versioned process contract

**Status**: Accepted (resolves `pg2-6oop0.3`)
**Date**: 2026-10-20
**Deciders**: Phillip Green II

## Context

`work-report report` used to render every report kind in-process. The report generators and presets design
(bead `pg2-6oop`, decisions D1 to D16) moves rendering out of the common executable: a generator is an
external command registered in config, and `work-report` owns everything around it (range and narrowing,
the output directory, validating what the generator claims it wrote, and the run record). The behavior
docs state the guarantees but deliberately leave out the wire shapes, exit codes, paths and file names.
This ADR records them, so a generator author, the `report` command, `config validate` and the narrative
generator all read one contract. The runner lives in `packages/work-report/internal/runner`.

All example names below are placeholders (`example-weekly`, `example-source`).

## Decision

### 1. Invocation

- The registered command is run as an argv vector and never through a shell. A bare first element is
  resolved on `PATH` when the run starts; a missing executable is the `not_found` outcome.
- In generate mode the working directory is the per-run output directory. In validate mode (`config
validate`) it is a fresh empty temporary directory.
- stdin is the envelope (section 2), stdout is the manifest (section 3), stderr is captured.
- The environment is an allowlist, never the caller's environment: `PATH`, `HOME`, `USER`, `LANG`,
  `TMPDIR`, `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME`, the names in the generator's `env` list,
  `WORK_REPORT_CONTRACT=1` and `WORK_REPORT_MODE=generate|validate`. A name that is unset in the caller's
  environment is omitted, not set empty. The envelope's `mode` field is authoritative; the variable is a
  convenience mirror. `config validate` uses the same function (`runner.Environ`), so validation sees what
  a real run sees.
- The timeout (default 5 minutes, configurable per generator) covers the whole run, including writing the
  envelope. On expiry or cancellation the generator's process group receives `SIGTERM`, then `SIGKILL` after
  a grace period (default 5 seconds; a test seam). The final `Wait` is bounded by `WaitDelay` so a
  grandchild holding a pipe cannot hang the run. On every exit the remaining members of the group are
  killed before any file is hashed.
- A write error (`EPIPE`) on stdin is ignored when the generator exits 0 without reading the envelope.
- stdout is capped at 1 MiB (exactly 1 MiB is accepted; more is `stdout_cap` and the generator is
  terminated). stderr is drained in full but retained as the first 32 KiB and the last 32 KiB with a
  `[truncated N bytes]` marker line between them, cut on UTF-8 boundaries.
- Limits on the result: at most 1000 files, each at most 1 GiB, at most 4 GiB in total.

### 2. Envelope (stdin)

One JSON object. A generator MUST ignore unknown fields, so the contract can grow within a version.

```json
{
  "contract_version": 1,
  "mode": "generate",
  "run_id": "20261020T012800Z-ab12",
  "report": { "preset": "example-weekly", "generator": "narrative" },
  "range": {
    "spec": "last-week",
    "since": "2026-10-12T00:00:00-04:00",
    "before": "2026-10-19T00:00:00-04:00",
    "open_start": false,
    "zone": "America/New_York"
  },
  "narrowing": { "labels": [], "sources": [], "types": [] },
  "entries": [
    {
      "id": "e1",
      "external_id": "x1",
      "source_id": "example-source",
      "type": "commit",
      "occurred_at": "2026-10-14T00:41:12Z",
      "summary": "text",
      "url": "",
      "labels": ["kind:commit"],
      "fields": {}
    }
  ],
  "footer": [
    {
      "source_id": "example-source",
      "count": 12,
      "last_success_at": "2026-10-19T21:02:00Z",
      "last_outcome": "succeeded"
    }
  ],
  "baseline": "# Work report ...",
  "config": {},
  "output_dir": "/state/work-report/reports/example-weekly/20261020T012800Z-ab12"
}
```

- `entries` holds exactly the entries that match the range and narrowing, in the baseline order. The
  generator MUST NOT re-apply or second-guess the filter. An empty range sends `entries: []` and the
  generator is still invoked.
- `range.since` is `null` when `open_start` is true. `footer[].last_success_at` is `null` for a source that
  has never pulled. `last_outcome` is the source's last pull status (empty when it never pulled).
  `run_id` is the output directory's base name.
- `narrowing` is informational and MUST NOT drive filtering.
- `baseline` is present only when the generator is registered with `inputs: [baseline]`.
- `config` is the preset's config object, passed through unchanged (`{}` for an ad hoc run).
- `output_dir` is absolute and already exists.
- `entries`, `fields`, `summary`, `url` and `baseline` are untrusted text. A generator that passes them to
  a model MUST treat them as data.

### 3. Manifest (stdout)

```json
{
  "contract_version": 1,
  "files": [
    {
      "path": "report.md",
      "content_type": "text/markdown",
      "title": "Weekly report"
    },
    { "path": "data/entries.csv", "content_type": "text/csv" }
  ],
  "summary": "Rendered 412 entries into 2 files.",
  "warnings": []
}
```

`work-report` enforces these rules. A violation is the `invalid_manifest` outcome whose reason names the
rule and the offending path; the rule names are the identifiers below.

| Rule                                      | Requirement                                                                                                                                              |
| ----------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `manifest_json`                           | stdout is exactly one JSON object                                                                                                                        |
| `contract_version`                        | present and an integer; a supported value is 1, any other integer is the `contract_version` outcome                                                      |
| `files_non_empty`                         | `files` is a non-empty array (`null` or missing is invalid)                                                                                              |
| `files_max`                               | at most 1000 files                                                                                                                                       |
| `file_item`                               | each item is an object with a string `path`                                                                                                              |
| `path_relative`                           | the path is relative and non-empty                                                                                                                       |
| `path_no_dotdot`, `path_clean`            | no `..` element, and `path.Clean(p) == p`                                                                                                                |
| `path_no_nul`, `path_no_backslash`        | no NUL and no `\`                                                                                                                                        |
| `path_no_trailing_slash`                  | no trailing `/`                                                                                                                                          |
| `path_reserved`                           | not `manifest.json` or `stderr.log` under case-folding and NFC normalization                                                                             |
| `path_unique`                             | unique under case-folding and NFC normalization                                                                                                          |
| `path_resolves`                           | the file exists                                                                                                                                          |
| `inside_output_dir`                       | the file and the output directory are resolved with symlink evaluation (macOS `/var` is a symlink); the resolved file lies inside the resolved directory |
| `resolved_path_reserved`                  | the resolved relative path is not a reserved name (a symlink to `manifest.json` is rejected)                                                             |
| `resolved_path_unique`                    | resolved relative paths are unique (two names for one file are rejected)                                                                                 |
| `regular_file`, `link_count`, `non_empty` | a regular file, link count 1, not empty                                                                                                                  |
| `file_size`, `total_size`                 | at most 1 GiB per file and 4 GiB in total                                                                                                                |
| `content_type`                            | present and parses as a media type (parameters such as `; charset=utf-8` are allowed)                                                                    |
| `summary`, `warnings`                     | optional; a string and an array of strings                                                                                                               |

A symlink inside the directory that resolves to a regular file inside it is accepted. Files in the
directory that the manifest does not list are left in place and not reported. Containment protects what
`work-report` hashes and reports; it does not sandbox the generator, which runs as the operator.

### 4. Generator exit codes and outcome kinds

| Generator exit | Meaning                                                  |
| -------------- | -------------------------------------------------------- |
| 0              | Success; stdout is a valid manifest                      |
| 1              | Unexpected failure; stderr carries the reason            |
| 2              | Usage error                                              |
| 3              | Invalid generator config; stderr carries one reason each |
| other          | Failure; stderr carries the reason                       |

`outcome_kind` is the closed set `ok`, `timeout`, `cancelled`, `exit_nonzero`, `config_invalid`,
`invalid_manifest`, `not_found`, `contract_version`, `stdout_cap`, `output_dir_unwritable`. Exit 3 is
`config_invalid`; any other non-zero exit, and a death by a signal that work-report did not send, is
`exit_nonzero`.

### 5. Paths and file names

- The output parent is an absolute directory chosen by the caller (the default is
  `$XDG_STATE_HOME/work-report/reports/<preset>`, `reports/adhoc` for `--generator`). A missing parent is
  created mode 0700.
- Each run gets a unique child `<UTC timestamp>-<4 hex>` (for example `20261020T012800Z-ab12`) created
  with an atomic `mkdir` mode 0700, retried with a new suffix on `EEXIST` at most 10 times and then
  `output_dir_unwritable`.
- After a successful run the parent's `latest` symlink is replaced by creating a temporary symlink and
  renaming it over `latest`. A `latest` that exists and is not a symlink is left alone and the operator is
  told.
- `stderr.log` (the retained stderr) and `manifest.json` (the run record) are written into the run
  directory, `stderr.log` first and `manifest.json` last, each by the same protocol: unlink any existing
  entry (an existing directory of that name is never written through; the run fails with
  `output_dir_unwritable`), create a temporary file with `O_EXCL` in the same directory, rename it into
  place. Files are mode 0600. A failed run keeps its directory. The absence of `manifest.json` means the
  run did not finish.
- The built-in baseline generator goes through the same pipeline in process, with `generator.argv0`
  recorded as `builtin:baseline`.

### 6. manifest.json (the run record)

```json
{
  "status": "ok",
  "outcome_kind": "ok",
  "generator_exit": 0,
  "signal": null,
  "timed_out": false,
  "tool_version": "26.10.20.00000+abcd1234",
  "contract_version": 1,
  "run_id": "20261020T012800Z-ab12",
  "started_at": "2026-10-20T01:28:00Z",
  "ended_at": "2026-10-20T01:28:09Z",
  "generator_ms": 8120,
  "total_ms": 9004,
  "preset": "example-weekly",
  "generator": { "name": "narrative", "argv0": "work-report-narrative" },
  "config": { "model": "sonnet" },
  "config_sha256": "...",
  "input_sha256": "...",
  "range": {
    "spec": "last-week",
    "since": "2026-10-12T00:00:00-04:00",
    "before": "2026-10-19T00:00:00-04:00",
    "open_start": false,
    "zone": "America/New_York"
  },
  "narrowing": { "labels": [], "sources": [], "types": [] },
  "entry_count": 412,
  "overrides": [],
  "files": [
    {
      "path": "report.md",
      "content_type": "text/markdown",
      "title": "Weekly report",
      "size_bytes": 9120,
      "sha256": "..."
    }
  ],
  "summary": "Rendered 412 entries into 2 files.",
  "warnings": []
}
```

- `status` is `ok` or `failed`. `outcome_kind` is always present. A failed run adds `reason` and omits
  `files`, `summary` and `warnings`. `generator_exit` is `null` when the process was killed by a signal or
  never started; `signal` is the name of the terminating signal or `null`.
- `preset` is `null` for a run with `--generator`. `overrides` lists command-line flag names only.
- `config` is the preset's config with redaction: any key whose name matches the unanchored,
  case-insensitive pattern `key|token|secret|password|credential` is recorded as `"***"` at any depth, and
  an object or array value under a matching key is replaced as a whole (`monkey` and `maxTokens` are
  over-redacted on purpose). `config_sha256` is the SHA-256 of the unredacted config's canonical JSON
  (object keys sorted).
- `input_sha256` is the SHA-256 of the canonical JSON of the envelope's `range`, `narrowing`, `entries`,
  `config` and `baseline` fields. It does not depend on the clock, the run id or the output directory, so
  a later cache could key on it (caching is not built, decision D8).
- `files[]` has `path`, `content_type`, optional `title`, `size_bytes` and `sha256` (hex).
- Times are RFC 3339; `started_at` and `ended_at` are UTC.

## Consequences

### Positive

- A generator is any program that reads one JSON object and prints one JSON object, in any language.
- Everything an operator needs to audit a run is in one directory: the files, `stderr.log` and the record.
- The environment allowlist keeps ambient secrets from reaching a generator by accident, and
  `config validate` sees the same environment as a run.

### Negative

- Containment bounds what `work-report` hashes and reports, not what a generator can do as the operator;
  the spec claims no sandbox.
- A run directory per run is never pruned by `work-report` (non-goal N6); the operator manages them.

### Neutral

- Contract version 1 is the only supported version. A change a version-1 generator cannot ignore bumps the
  integer; `work-report` refuses a manifest declaring another version and names the supported ones.
