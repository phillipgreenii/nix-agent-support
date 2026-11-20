# Live runs

This page records on-demand runs that spend real model time. None of them is part of the unit gate. Values are aggregated or synthetic.

## Live replay (one lens at a time)

Run from `packages/pg-review`, once per lens:

```bash
PG_REVIEW_REPLAY_LENS=agent-workflow nice -n 10 go test -v -tags live -timeout 600s -run TestLiveReplay ./internal/replay/
PG_REVIEW_REPLAY_LENS=config-health nice -n 10 go test -v -tags live -timeout 600s -run TestLiveReplay ./internal/replay/
```

The test runs the lens twice against a real ccpool session and applies the pass criteria of the replay harness. The live analyst has no query tool, so its prompt says the evidence rows are all the evidence. A rejected batch gets the runner's one repair in the same session, and a pass starts a fresh session once more if that fails too.

| Date       | Lens           | Result | Wall time |
| ---------- | -------------- | ------ | --------- |
| 2026-11-20 | agent-workflow | pass   | 119.6 s   |
| 2026-11-20 | config-health  | pass   | 87.4 s    |

An earlier pass of the same day failed both lenses in the first replay pass with `replay: the final message has no fenced JSON block`; the cause is below.

### Failure causes found and fixed (2026-11-20)

- `replay: the final message has no fenced JSON block`: `ccpool result` exited 0 with no text before the final message was readable (the error now quotes the start of the message, which showed it empty). `ccpool.CLI.Result` now re-reads an empty result up to three times, two seconds apart, before reporting it as empty. The unit tests are `TestResultRereadsAnEmptyRead` and `TestResultStillEmptyAfterTheRereadsIsReportedEmpty`.
- The same empty read is the cause of the `pg-review job doctor` session check failing once with `the session finished with an empty reply`: the check reads the turn with the same call. The `invalid character` text in that report came from the diagnostic `ccpool state` read that follows the empty reply, not from the failure itself. The re-read removes the trigger; the doctor flake was not reproduced separately, so it is ruled the same transient, not proven by a doctor run.
- A batch rejected for undefined glossary terms (for example a state word or vocabulary word used without a glossary entry) failed the replay, where the runner repairs once. `Run` now sends the same repair through an analyst that implements `Repairer`.

## Scheduled pairs, run by hand through the router adapter

Each pair was run once, one at a time, as a small watched first batch (no dollar cap exists for analyst runs, so the batch was one lens and one scope each), with the adapter's own argv:

```text
pg-review-router-adapter run <now> <schedule_id> <lens> <scope> <window>
```

| Lens           | Scope     | Window | Schedule entry | Run state      | Elapsed       | Profile      | Model             | Items filed | Repair replies |
| -------------- | --------- | ------ | -------------- | -------------- | ------------- | ------------ | ----------------- | ----------- | -------------- |
| agent-workflow | workspace | 24h    | daily          | Filed (exit 0) | about 1.2 min | deep-review  | claude-opus-5-5   | 0           | 1              |
| config-health  | workspace | 7d     | weekly         | Filed (exit 0) | about 0.4 min | health-check | claude-sonnet-5-5 | 0           | 0              |

- Both run records show model, profile and a prompt hash; elapsed time is far below each profile's minutes bound. No effort setting appears on the run record, so no effort name is recorded.
- The run key built by the adapter had the form `<lens>/<scope>/<window>/<window end>`.
- The schedule ids used were the deployment's `agent-workflow-daily` and `config-health-weekly`; the router listed the review roles and queries.

### Outcome checks

| Check                                                      | Result                                                                                                                                                                                                                                                   |
| ---------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Run reaches Filed                                          | Confirmed for both runs.                                                                                                                                                                                                                                 |
| Items validated and rendered with `human show`             | Not confirmable: neither run filed an item, no item has ever been filed (`human pending` lists none, `human show 1` reports not found). Needs a run that files one.                                                                                      |
| Staging directory deleted                                  | Confirmed: the staging path on both run records is empty and the state directory holds no staging directory.                                                                                                                                             |
| Ccpool session closed                                      | Confirmed for both runs: their pool sessions show state closed with close reason handler. Recorded replay sessions were not closed at the end of the replay (see below).                                                                                 |
| Scratch tree, config dirs and ledger unmodified by analyst | Partly confirmable: the analyst working directory is empty and unchanged since creation. The ledger and config directory mtimes were later changed by maintain and the apply, so a change by the analyst cannot be told apart from those. Not confirmed. |
| Model, profile, prompt hash, elapsed on the run record     | Confirmed (see table).                                                                                                                                                                                                                                   |

No new live run was made for the unconfirmed criteria: a run cannot be made to file an item on demand, and the pool was full when this was checked, so a new run would only have been skipped.

### Observation: replay sessions stay open

After the live replays, the pool held six idle live sessions named after the replay test and had no free slot (`ccpool capacity`: live 6 of 6). Older replay sessions were evicted by the pool's capacity cap rather than closed by the test. Consequence: a `job doctor` run after the replays fails the session check with `ccpool has no free slot`. This was not fixed here, and the sessions were not closed by this packet.

## Doctor

On 2026-11-20, after the replays: `pg-review job doctor` reported PASS for config, ledger, providers, recorder, ccpool and hook, and FAIL for the session check with `ccpool has no free slot; rerun later or skip with --skip-session-check` (the pool-full condition above). With `--skip-session-check` the other checks passed and the session check was skipped. Earlier the same day, doctor passed all checks on two invocations (including the session check, which resolves `pg-review agent q` inside the session) and failed only the session check once with the empty-reply transient described above. The in-session resolution of `pg-review agent q` is therefore recorded from those earlier passes; it was not re-confirmed on the final invocation.

## Second watched pass (2026-11-20, after the pool was freed)

The pool was empty (`ccpool capacity`: free 6 of 6) before this pass.

- `pg-review job doctor`, run twice: PASS for config, ledger, providers, recorder, ccpool and hook; FAIL for the session check both times with `the session finished with an empty reply (session state unknown: ccpool state: decode: invalid character 'a' in literal null (expecting 'u'))`. The empty-result re-read did not clear it, so the doctor session check is not clean and is a live defect still open (the `ccpool state` text in the message is the diagnostic read, which prints a plain `name=... state=...` line that the decoder cannot parse).
- One lens over the workspace scope with a 7-day window failed in evidence collection, before any analyst session: `provider ccaudit: timed out after 120s` (run state Failed, exit 1, no model spend). The 24-hour window works.
- One watched run, agent-workflow over workspace with a 24-hour window (deep-review profile, claude-opus-5-5): state Filed, exit 0, about 1.9 min, 0 queries, 0 repair replies, 0 items filed, prompt hash recorded; its pool session shows closed with close reason handler.
- Integrity: the analyst working directory stayed empty, and the config file is a read-only store symlink whose directory was last changed by the apply before these runs. A before/after ledger snapshot was not taken in a form that can be compared (the first snapshot command used the wrong `stat` flavour), so the ledger-unchanged criterion is still not confirmed.
- Still unmet: items filed, validated and rendered with `pg-review human show` (the run again filed nothing; `human pending` is empty), the ledger snapshot comparison, and a clean doctor run.
