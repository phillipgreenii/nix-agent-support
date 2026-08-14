# pg-ccaudit

Indexes Claude Code transcripts into SQLite so that asking "which tool calls are
failing, at what measured cost, and where does each fix belong" is a **query**
rather than a scan of a multi-gigabyte JSONL corpus.

Scanning the corpus raw is not a theoretical problem: the census that produced
this tool tried it twice and stalled a supervising agent's 600-second progress
watchdog both times. It completed only once the mechanical extraction was lifted
out of the agent entirely. This package is that extraction, made incremental,
resumable, and schedulable.

## Commands

| Command                           | What it does                                                   |
| --------------------------------- | -------------------------------------------------------------- |
| `pg-ccaudit ingest`               | Index new and appended transcripts. Writes. Single-instance.   |
| `pg-ccaudit status`               | Coverage and staleness. Read-only.                             |
| `pg-ccaudit query <name> [args…]` | Run a named, versioned canned query. Read-only.                |
| `pg-ccaudit queries [--verbose]`  | List the canned queries, with notes and SQL under `--verbose`. |
| `pg-ccaudit schema [--thinking]`  | Print the DDL.                                                 |

Paths resolve from `--db` / `--root`, else `PG_CCAUDIT_DB` / `PG_CCAUDIT_ROOT`
(the home-manager module exports both), else
`$XDG_DATA_HOME/pg-ccaudit/transcripts.db` and `~/.claude/projects`.

The database is **global, not per-project**: cross-project analysis over every
project directory is the point of the index, so a per-repo database would answer
none of the questions it exists for.

## The properties that matter

### Ingestion is incremental, resumable, and idempotent

Change is detected on `(path, size, mtime)`, and a changed file is re-parsed from
a stored **byte offset** rather than from the start. That is what makes a ~15
minute sweep affordable at all: transcripts are append-only while a session runs,
so a naive re-scan would re-parse the ACTIVE session's file on every single tick.

- A file whose size **shrank**, or whose mtime moved **backward**, was rewritten
  (compaction does this) and is re-indexed from zero, its stale rows purged first.
- A stored offset is verified to land just after a newline before it is trusted.
  One byte of extra reading converts a silently corrupt resume — parsing from the
  middle of a record — into a clean re-ingest.
- Every insert is an upsert on a natural key (`(path, seq)`, or `tool_use_id`), and
  the records plus the new offset commit in **one transaction**. So no crash can
  leave the offset ahead of the data, and replaying a byte range can never
  double-count. Correctness is chosen over throughput here deliberately; the full
  corpus still indexes in well under a minute.

### Malformed input costs a LINE, not a FILE

A line that does not decode increments `lines_bad` and is skipped. Records on both
sides of it are still indexed. `lines_ok` / `lines_bad` are recorded per file, so
coverage is **provable** rather than assumed.

An unterminated **trailing** line is different: it is a torn write from a session
that is still appending. It is neither parsed nor counted as malformed, the offset
stops before it, and the file is recorded `complete = 0` so a later tick picks the
record up whole. Partial ingestion is therefore always distinguishable from
complete ingestion.

### `is_error` is present-and-true, never a boolean default

`"is_error": false` appears thousands of times in the corpus. "Not an error" and
"no result recorded" are distinct conditions, and treating the field as a plain
bool with a zero value would silently merge them — miscounting every rate derived
from it, in a direction nothing else would reveal.

### Successful result bodies are NOT stored — length only

Measured on a stratified sample scaled to the corpus: successful tool-result bodies
scale to **~322 MB**, error bodies to **~1.8 MB**. That is 180x the volume for zero
analytical value, because a census of FAILURES never reads a SUCCESS body. So a
successful result contributes `content_len` and nothing else, while error bodies
and tool-call inputs are stored **untruncated**.

Truncating at extraction time is not a harmless economy. The shell prototype capped
tool inputs at 160 characters, which cut the closing quote off every long command
and produced a 470-row phantom `NOCMD` bucket — an artefact that looked exactly
like a finding.

### The writer is single-instance; the query path is read-only

`ingest` takes an advisory file lock (`ingest.lock`, beside the database). A second
concurrent ingest **detects it, does nothing, and exits zero** — an overlapping
tick at a 15 minute cadence is expected, not an error. Two writers racing on the
same transcript's resume offset is the one way this design could corrupt its own
coverage accounting.

`query` and `status` latch `PRAGMA query_only`, so they cannot write, and they
**never trigger an ingest**. They report how far behind the index is and stop. That
mirrors this machine's standing posture against tools that transparently start
their own background work.

### Scheduled, not hooked

The sweep is a nix-declared launchd user agent, **not** a Claude Code session-end
hook. A hook fires only when a session ends cleanly, and abnormally-terminated
sessions are disproportionately the interesting ones — a stalled or crashed session
is itself evidence of the waste being measured. Hooking session end would drop the
strongest signal in the corpus while appearing to have full coverage. A flake check
(`test-pg-ccaudit-declares-no-hooks`) enforces the absence.

## Canned queries

Named **and versioned**, so two audits produce comparable numbers and an agent runs
`pg-ccaudit query error-rate-by-tool` instead of re-deriving SQL that differs for
reasons nobody can reconstruct. A version bump means the SQL's meaning changed; the
registry test pins every name to its current version.

| Name                    | Answers                                                                  |
| ----------------------- | ------------------------------------------------------------------------ |
| `error-rate-by-tool`    | Per-tool error counts **with denominators**                              |
| `top-signatures`        | Ranked normalized error signatures                                       |
| `bash-by-lead-cmd`      | Per-leading-command Bash rates                                           |
| `session-concentration` | The runaway discount: total / distinct sessions / worst session          |
| `retry-chains`          | Same tool re-called after a failure within N line ordinals               |
| `error-then-narration`  | The prose written on the line right after a failure                      |
| `sidechain-split`       | Every signature split by `is_sidechain` — decides where a fix belongs    |
| `cost-by-signature`     | Measured cost per signature (read its notes)                             |
| `hook-rejections`       | True totals from recorded `hookErrors`, not an error-text grep           |
| `first-seen`            | Earliest/latest occurrence, ranked by first — did this class start when? |
| `last-seen`             | Same columns ranked by most recent — did the documented fix work?        |
| `coverage`              | Indexed coverage: the proof behind every number above                    |

Every query takes `--since` / `--until` (ISO-8601 prefixes, `--until` exclusive) and
`--format table|tsv|json`. Every rendering is stamped with the query name, its
version and the window, so a pasted result is self-describing.

### Two things worth knowing before you quote a number

**`cost-by-signature` reports two cost columns, and the obvious one is not the
answer.** Claude Code writes a top-level `durationMs` only on `system` events —
turn and hook summaries — and **never** on the user event that carries a
`tool_result`. So `duration_ms_sum`, summed over the failing results' own events
rows, is legitimately near zero and MUST NOT be reported as the cost of the
failures. `elapsed_ms_sum` is the real measurement: wall time between the
`tool_use` line's recorded timestamp and its result's. It is still MEASURED, not
estimated. For a batch of parallel sibling calls the per-call values overlap, so
treat the sum as an upper bound on serial cost.

**`retry-chains` uses a window of 6 line ordinals by default.** A retry cycle is at
minimum `[tool_use] -> [tool_result] -> [tool_use]`, three lines, and Claude writes
one line per content block, so a batch of parallel sibling calls plus their results
can separate a failure from its retry by several lines. Six spans roughly two
intervening turns while still excluding a same-tool call from a genuinely later
phase of the session. Candidates are scoped to the same `session_id` **and** the
same file, because `seq` is a per-file line ordinal — a gap computed across two
files is meaningless. `identical_input = 1` is the strongest signal in the set: the
same input re-sent after a failure.

## Schema

`pg-ccaudit schema` prints it. `files`, `events`, `tool_calls`, `tool_results`,
`assistant_text` — exactly those five tables by default, asserted by a test, so
anyone comparing against the specification finds no surprises. `thinking` is
created **only** under `ingest --thinking`, which is off by default: thinking blocks
are ~94 MB corpus-wide and no shipped query reads them yet.

## Nix

Built with `mkGoApp` on the **gomod2nix** engine, Pattern A (`phillipg-nix-repo-base`
ADR 0008): `go.mod` and the committed `gomod2nix.toml` sit side by side at the
package root, there is no `vendorHash`, and no local `replace` means no `modRoot`.
Refresh dependencies with:

```bash
../update-gomod2nix-deps.sh .
```

`claude-transcript` was evaluated and **not** reused. Its `Event`/`Block` types
carry only what its three existing consumers need and omit every field this index is
about — the `tool_use` input, the `tool_result` `content` and `is_error`,
`parentUuid`, `isSidechain`, `cwd`, `gitBranch`, `durationMs` — and its reader is a
whole-file scan with no byte-offset resume, which is the one property T-1a makes
mandatory here. Adding those fields would change a type three other packages depend
on, for a smaller result than writing the ~150 lines of decoding this package needs.

## Tests

`go test ./...`, gated in `nix flake check` as `pg-ccaudit-go-tests`. Every
filesystem scenario is built in `t.TempDir()` from the committed fixture corpus
under `internal/ingest/testdata/corpus`; **no test reads the real transcript corpus
or the real index**. Every canned query is asserted against hand-computed answers
over that fixture — "returns without error" is not the bar, because a query that
silently groups the wrong thing returns cleanly and reports a wrong number.
