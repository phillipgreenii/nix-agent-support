# ccpool Claude-Code Contract Test Harness

A local, on-demand Go test suite (behind the `//go:build contract` tag) that drives the **real**
`claude` binary through `ccpool` to pin the Claude Code TUI contract and localize drift after
Claude Code upgrades.

## Purpose: post-upgrade drift detection

Claude Code's TUI (pane rendering, exit-code timing, hook firing, AskUserQuestion behaviour) is an
unversioned contract that `ccpool` depends on. When you upgrade Claude Code, this suite tells you —
within a single run — whether anything `ccpool` relies on shifted, and **where**. It is the
canary you run by hand after bumping `claude`.

## How to run

```bash
nix run .#ccpool-contract
```

This runs the suite and prints a per-outcome tally (see below). The full `go test -json` stream is
also written to `/tmp/ccpool-contract.json` for inspection.

To run it directly (e.g. a single scenario) from `packages/ccpool`:

```bash
go test -tags contract -timeout=0 -p 1 -json ./cmd/ccpool/... \
  | jq -n -r -f contract/classify.jq | sort | uniq -c
```

`-p 1` is required: the suite shares the real `$HOME` (for OAuth) and must run serially.
`-timeout=0` disables Go's default 10-minute test timeout, since real turns can be slow.

`contract/testdata/go-test-json-classify-sample.jsonl` is a small real excerpt (not a live re-run)
from an actual `go test -json` stream, trimmed to one test per bucket plus a bare test FAIL with no
`OUTCOME=` line. It exists to sanity-check `classify.jq` itself without spending tokens on a live
run:

```bash
jq -n -r -f contract/classify.jq contract/testdata/go-test-json-classify-sample.jsonl | sort | uniq -c
```

## Requirements (and why this is NOT in CI)

- A real `claude` binary on `$PATH`, **already logged in** (OAuth). The suite deliberately does
  **not** override `$HOME` — real `claude` needs it for OAuth. The price: `ccpool`'s truster writes
  a folder-trust entry to the real `~/.claude.json` for each sandbox cwd. Accepted and documented.
- `tmux` and `sqlite3` on `$PATH`. If `tmux`, `claude`, or `sqlite3` is missing, `TestMain` exits
  cleanly (skips) rather than failing.
- It **spends real Claude Code tokens** and takes roughly **8-12 minutes** for a full run.

For these reasons the suite is **excluded from `nix flake check` and from CI**. The `contract`
build tag keeps these files out of the default `nix build .#ccpool` check phase, and
`ccpool-contract` is a `nix run` entrypoint (an app), never a flake check.

## Outcomes

`go test` only has PASS / FAIL / SKIP, which cannot distinguish a real regression from an
expected-deferred behaviour or a broken harness. So each judgement emits one machine-greppable
`OUTCOME=<bucket>` log line; `contract/classify.jq` extracts the bucket and `sort | uniq -c` tallies
them.

| Bucket           | Meaning                                                                                              | Action                                                                                   |
| ---------------- | ---------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| `live`           | An objective check that must hold — passed.                                                          | None. This is the healthy state.                                                         |
| `baseline`       | A check pinning the **currently observed** value (often a known bug) — still matches.                | None now. Each `baseline` is a known-bug/deferred item to fix later.                     |
| `pending`        | A check that cannot be made yet (needs observability that does not exist); recorded then `t.Skip`ed. | None now; it is the v2 observability backlog.                                            |
| `baseline-drift` | A pinned baseline value **changed** — the behaviour (possibly a fix) moved.                          | **Investigate**: re-triage the linked bead; re-pin or convert to `live`.                 |
| `live-fail`      | An objective `live` check **failed** — a real regression in the command under test.                  | **Investigate**: a genuine `ccpool` / Claude Code contract regression.                   |
| `scaffold`       | The harness's own driving broke (e.g. pane-rendering changed so a phase gate never matched).         | **Investigate**: fix the harness (usually the phase-detection regexes), not the product. |

`unclassified-fail` is not one of the harness's own `OUTCOME=` buckets above — `classify.jq`
synthesizes it. A test can fail without ever emitting an `OUTCOME=` line (e.g. `ccpTimed`'s
subprocess-timeout guard in `contract_harness_test.go` fails via a plain hang-timeout message), and
such a failure would otherwise be invisible to the tally even though the overall `go test` result
is FAIL. `classify.jq` tracks, per test, whether it ever emitted an `OUTCOME=` line, and emits
`unclassified-fail` for any test-level failure that didn't. **Investigate** any non-zero count —
read the raw `go test -json` output (or `/tmp/ccpool-contract.json`) for that test's failure message.

Any non-zero `baseline-drift`, `live-fail`, `scaffold`, or `unclassified-fail` count means
**investigate**. A run where every bucket is `live` / `baseline` / `pending` is green.

### About `baseline`s specifically

A `baseline` pins the value `ccpool` exhibits **today**, which is frequently a **known bug** (each is
tagged with a bead, e.g. `pg2-33gl`). Pinning the observed value means that when the bug is later
**fixed**, the baseline trips as `baseline-drift` and forces you to re-triage and re-pin (or promote
the check to `live`). The set of `baseline` calls in the code is the expected-deferred manifest.
