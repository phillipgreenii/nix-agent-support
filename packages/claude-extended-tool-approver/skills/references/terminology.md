# Terminology

These terms appear in both `identify-hook-misses` and `absorb-settings-rules`. All field names refer to keys in the JSON output of `claude-extended-tool-approver evaluate` and `claude-extended-tool-approver show` (see [database-schema.md](database-schema.md)).

| Term                | Source field                                             | Meaning                                                                                                                                                            |
| ------------------- | -------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `hook_decision`     | `evaluate.hook_decision` / `show.hook_decision`          | The decision the hook returned at the time the row was logged. One of `APPROVE`, `ASK`, `DENY`, `ABSTAIN`.                                                         |
| `replay_result`     | `evaluate.replay_result`                                 | The decision the _current_ rule engine returns when replaying the logged input. Used to detect drift and misses.                                                   |
| `settings_decision` | `evaluate.settings_result` (when `--settings` is passed) | The decision `settings.local.json` rules would have returned for this row. Only present when `evaluate --settings=<path>` is used.                                 |
| `category`          | `evaluate.category`                                      | One of `correct`, `miss-uncaught`, `miss-caught-by-settings`, `needs-review`, `stale-cwd`. See `cmd_evaluate.go`.                                                  |
| `outcome`           | `evaluate.outcome` / `show.outcome`                      | The user's actual decision after the prompt — the ground truth used to grade hook correctness.                                                                     |
| `sandbox_enabled`   | `evaluate.sandbox_enabled` / `show.sandbox_enabled`      | `1` if the OS bash sandbox was active for this invocation, `0` if not, `null` if the row predates sandbox telemetry. See [sandbox-enabled.md](sandbox-enabled.md). |

Note: in skill documents the placeholder `<hook_decision>` (in `identify-hook-misses`) and `<settings_decision>` (in `absorb-settings-rules`) are used inside template strings to indicate "substitute the value from the corresponding field of the row you are reporting on."
