# Live runs

This page records on-demand runs that spend real model time. None of them is part of the unit gate.

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

### Failure causes found and fixed (2026-11-20)

- `replay: the final message has no fenced JSON block`: `ccpool result` exited 0 with no text before the final message was readable (the error now quotes the start of the message, which showed it empty). `ccpool.CLI.Result` now re-reads an empty result up to three times, two seconds apart, before reporting it as empty. The unit tests are `TestResultRereadsAnEmptyRead` and `TestResultStillEmptyAfterTheRereadsIsReportedEmpty`.
- The same empty read is the cause of the `pg-review job doctor` session check failing once with `the session finished with an empty reply`: the check reads the turn with the same call. The `invalid character` text in that report came from the diagnostic `ccpool state` read that follows the empty reply, not from the failure itself. The re-read removes the trigger; the doctor flake was not reproduced separately, so it is ruled the same transient, not proven by a doctor run.
- A batch rejected for undefined glossary terms (for example a state word or vocabulary word used without a glossary entry) failed the replay, where the runner repairs once. `Run` now sends the same repair through an analyst that implements `Repairer`.
