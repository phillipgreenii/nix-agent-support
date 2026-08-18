# CETA engine A/B replay runbook

How to verify a CETA engine fix by replaying the decision corpus through the pre-fix and post-fix
engines — and the measurement traps that make the naive approach report a catastrophe that is not
there. (Extracted 2026-08-17 from a bd memory; methodology proven on `pg2-899h3` and its sibling
verify beads `pg2-gsi4z`, `pg2-xzc30`, `pg2-lotbo`, `pg2-3gbxm`.)

**Not what you want if you are actively editing a rule module right now.** This runbook is for
RETROACTIVELY comparing two already-existing commits (typically post-hoc, verifying a change that
already landed), which is why it builds two out-of-repo binaries. While iterating on a rule change
in place, `evaluate --baseline <file>` (pg2-f1vss) is a single command: run it once before editing
(it captures), edit/rebuild/test as normal, run the identical command again (it compares) — no
second binary, no git ref. See `cmd/claude-extended-tool-approver/cmd_evaluate.go`'s `--baseline`
help. Reach for THIS runbook when the two revisions already exist as commits and you were not the
one who captured a "before" snapshot.

## The two replays — do not confuse them

The transition tables in "verify X after apply" beads are a PRE-FIX vs POST-FIX **engine A/B**
replay, NOT a logged-decision vs current-engine replay. Getting this wrong inverts the verdict.

- **Tell-tale of an engine A/B**: it is internally self-proving — the allow delta is fully
  accounted for by the listed classes and NOTHING else moves. `pg2-899h3`'s table:
  approve→abstain 392 + approve→ask 3 = −395 allow, abstain +392, ask +3, reject unchanged. Only
  two builds of the same engine can produce that.
- **The trap, measured 2026-07-30 on `pg2-899h3`**: the naive logged-vs-current replay reported
  13,882 changed Bash rows including abstain→allow 2,811, ask→allow 158, deny→allow 29 = 3,275
  "toward allow". Read against a zero-toward-allow criterion that looks like a catastrophic
  security failure. It is not — it is months of engine evolution between when rows were logged
  and now.

## Correct method

Build both revisions out-of-repo and diff their verdicts on the same corpus:

```bash
git -C <repo> archive <pre-sha>  -- packages/claude-extended-tool-approver | tar -x -C src-pre
git -C <repo> archive <post-sha> -- packages/claude-extended-tool-approver | tar -x -C src-post
(cd src-pre/packages/claude-extended-tool-approver  && GOFLAGS=-mod=mod go build -o ceta-pre  ./cmd/claude-extended-tool-approver)
(cd src-post/packages/claude-extended-tool-approver && GOFLAGS=-mod=mod go build -o ceta-post ./cmd/claude-extended-tool-approver)
XDG_DATA_HOME=<snapshot> ./ceta-pre  evaluate --format json   # then diff the two verdict streams
XDG_DATA_HOME=<snapshot> ./ceta-post evaluate --format json
```

- Confirm `cmd/` is byte-identical between the two trees (`diff -r`) so the fix's files are the
  only variable.
- `evaluate` replays every non-excluded row through `setup.NewEngineForCWD` +
  `eng.EvaluateHook`; stale-cwd rows report as skipped.
- Prove the post build IS the deployed engine by diffing its verdicts against the deployed
  wrapper across the whole corpus (`pg2-899h3`: 327,286 rows, 0 differing).

**Caveat**: `evaluate` is ENGINE-ONLY. inputproc (the `rtk rewrite` `CETA_INPUT_PROCESSOR`) is
applied only on the hook path (`main.go`), not in `evaluate`. Fine for an A/B since both arms omit
it, but the full deployed pipeline is only covered by probes driven through the wrapper.

## Asklog read access — three distinct failures

**As of pg2-cbihz, this is no longer something a caller of the binary's own subcommands has to
manage**: `evaluate`, `show`, `report`, `baseline`, and `compare` all open the asklog through
`asklog.NewReadOnlyStore` internally, which already applies the `immutable=1` DSN below (see that
function's doc comment in `internal/asklog/store.go` for the full write-up, including why
`mode=ro` is deliberately NOT added alongside it — combining them momentarily touched the
-wal/-shm sidecars against the live corpus, which a true read-only open must not do). Before that
bead, `cmd_evaluate` (and its four siblings) opened the store READ-WRITE, which is what made a
manual `XDG_DATA_HOME` redirect a required mitigation for driving them against a live corpus; that
requirement is now closed for THESE FIVE subcommands specifically. The three failures below remain
live for anyone doing ad-hoc, raw `sqlite3` (or hand-rolled scripting) access to the corpus outside
those subcommands — e.g. the pre/post binary comparison in "Correct method" above, run against a
frozen snapshot for A/B reproducibility rather than for write-safety:

```text
sqlite3 -readonly <db>          -> SQLite error 14
sqlite3 'file:<db>?mode=ro'     -> SQLite error 14
sqlite3 'file:<db>?immutable=1' -> opens, but a FULL SCAN of the live db gives
                                   "database disk image is malformed (11)"
```

The (11) is a torn-page artifact of reading a hot WAL database, NOT corruption —
`pragma quick_check` on the live db returns ok. So: `immutable=1` for small/targeted reads, and
run full scans against a SNAPSHOT COPY.

## Reporting

Absolute counts in verify beads are stale snapshots and drift with the live corpus; the CLASSES
and the zero-toward-allow invariant are the criteria. Report measured values, never restate the
bead's.
