# `claude-extended-tool-approver` Output Schema

This reference describes the JSON fields produced by `evaluate` and `show`. Source of truth: `cmd/claude-extended-tool-approver/cmd_evaluate.go` and `cmd_show.go`.

## `evaluate --format=json`

Each row in the output array has the following fields:

| Field             | Type        | Description                                                                             |
| ----------------- | ----------- | --------------------------------------------------------------------------------------- |
| `id`              | int         | Row id in the asks database.                                                            |
| `tool_name`       | string      | Tool that was invoked (e.g. `Bash`, `Read`, `Edit`).                                    |
| `tool_summary`    | string      | One-line summary of the invocation, suitable for grouping similar calls.                |
| `hook_decision`   | string      | The decision the hook returned at log time: `APPROVE`, `ASK`, `DENY`, or `ABSTAIN`.     |
| `replay_result`   | string      | The decision the current rule engine returns when replaying this row.                   |
| `settings_result` | string      | (Only with `--settings=<path>`) The decision `settings.local.json` would have returned. |
| `category`        | string      | `correct`, `miss-uncaught`, `miss-caught-by-settings`, `needs-review`, or `stale-cwd`.  |
| `outcome`         | string      | The user's actual decision — ground truth.                                              |
| `sandbox_enabled` | int or null | `1`, `0`, or `null`. See [sandbox-enabled.md](sandbox-enabled.md).                      |

## `show <id...> --format=json`

`show` returns the same fields as `evaluate`, plus:

| Field                               | Type   | Description                                                                                           |
| ----------------------------------- | ------ | ----------------------------------------------------------------------------------------------------- |
| `correct_hook_decision`             | string | The user-recorded "correct" decision (from `set-correct-decision`), if any.                           |
| `correct_hook_decision_explanation` | string | Free-form rationale for the correct decision.                                                         |
| `trace`                             | array  | (Only when `CLAUDE_TOOL_APPROVER_TRACE=1` was set at hook time) Per-rule decision chain with reasons. |

## Categories

From `cmd_evaluate.go`:

- `correct` — replay matches outcome.
- `miss-uncaught` — hook abstained / wrong, no settings rule covers it either.
- `miss-caught-by-settings` — hook abstained / wrong, but `settings.local.json` would have decided correctly.
- `needs-review` — ground truth missing or ambiguous.
- `stale-cwd` — row's working directory is no longer relevant.
