# CETA engine A/B replay runbook

How to verify a CETA engine fix by replaying the decision corpus through the pre-fix and post-fix
engines — and the measurement traps that make the naive approach report a catastrophe that is not
there. (Extracted 2026-08-17 from a bd memory; methodology proven on `pg2-899h3` and its sibling
verify beads `pg2-gsi4z`, `pg2-xzc30`, `pg2-lotbo`, `pg2-3gbxm`.)

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
