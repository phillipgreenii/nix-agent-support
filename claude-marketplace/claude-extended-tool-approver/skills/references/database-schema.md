# `claude-extended-tool-approver` Output Schema

This reference describes the JSON fields produced by `evaluate` and `show`. Source of truth: `cmd/claude-extended-tool-approver/cmd_evaluate.go` and `cmd_show.go`.

## `evaluate --format=json`

Each row in the output array has the following fields:

| Field             | Type           | Description                                                                                                                                                          |
| ----------------- | -------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `id`              | int            | Row id in the asks database.                                                                                                                                         |
| `tool_name`       | string         | Tool that was invoked (e.g. `Bash`, `Read`, `Edit`).                                                                                                                 |
| `tool_summary`    | string         | One-line DISPLAY summary of the invocation. Truncated — MUST NOT be used as a grouping key; use `command_class` instead.                                             |
| `command_class`   | string         | Stable, non-truncated grouping key for the invocation. Analysis that buckets rows by "same command" MUST group on this.                                              |
| `hook_decision`   | string         | The decision the hook returned at log time: `allow`, `ask`, `deny`, or `abstain` (or empty for a built-in ASK with no CETA row).                                     |
| `replay_result`   | string         | The decision the current rule engine returns when replaying this row.                                                                                                |
| `settings_result` | string         | (Only with `--settings=<path>`) The decision `settings.local.json` would have returned.                                                                              |
| `category`        | string         | `correct`, `miss-uncaught`, `miss-caught-by-settings`, `needs-review`, or `stale-cwd`.                                                                               |
| `outcome`         | string         | The user's actual decision — ground truth (`approved`, `denied`, `pending`).                                                                                         |
| `sandbox_enabled` | int or null    | `1`, `0`, or `null`. See [sandbox-enabled.md](sandbox-enabled.md).                                                                                                   |
| `approval_source` | string         | Derived approval-MECHANISM bucket. One of `unknown`, `bypass`, `auto`, `settings`, `hook`, `user`. See below.                                                        |
| `permission_mode` | string or null | Raw Claude Code permission mode at log time, stored VERBATIM (e.g. `default`, `plan`, `acceptEdits`, `dontAsk`, `auto`, `bypassPermissions`). `null` on pre-v5 rows. |
| `agent_type`      | string or null | The subagent type that issued the call (e.g. `Explore`), or `null` for the main agent. A SEPARATE axis from `approval_source`.                                       |
| `outcome_notes`   | string or null | Free-form notes attached at resolution (e.g. the `auto_mode_classifier: <reason>` string on an auto-mode denial).                                                    |
| `tool_response`   | object or null | The PostToolUse result payload as a nested JSON object, or `null`. See [`tool_response` shape](#tool_response-shape).                                                |

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

## `approval_source`

`approval_source` is the approval-**mechanism** axis — WHO/WHAT let the tool run.
It is derived from `permission_mode`, `prompt_id`, and `hook_decision` by
`asklog.ApprovalSource`; it is NOT a stored column. It classifies **context, not
outcome**: a `denied` or `pending` row still gets a bucket (an auto-mode denial
buckets as `auto`), which is what the false-denial calibration relies on.

`subagent` is deliberately NOT a value on this axis — it would conflate two
orthogonal axes. Segment subagents by crossing this axis with the separate
`agent_type` column (`agent_type IS NOT NULL`), not by a merged enum.

The derivation is an ordered decision list (first match wins):

1. `permission_mode` is `null` → `unknown` — every pre-v5 (pre-migration) row, since the field was not captured then.
2. `permission_mode == "bypassPermissions"` → `bypass`.
3. `permission_mode` in {`auto`, `dontAsk`} → `auto`.
4. no prompt (`prompt_id` absent): CETA's own decision returned Approve (`hook_decision == "allow"`) → `hook`; otherwise the tool ran with no prompt and no CETA approval, i.e. the user pre-authorized it in settings → `settings`.
5. otherwise (a prompt fired, `prompt_id` present) → `user`.

`acceptEdits` and `default`/`plan`/empty are NOT their own buckets — they fall
through to steps 4/5 (`acceptEdits` auto-accepts edits, not Bash).

**Known limit:** historical rows have a `null` `prompt_id`, so a no-prompt row
logged before `prompt_id` was persisted cannot be split from a prompted one; such
rows resolve via step 4 (`settings`/`hook`) and never reach `user`.

## `tool_response` shape

`tool_response` is the raw PostToolUse result payload, emitted as a nested JSON
object (or `null` when the row predates capture or no PostToolUse fired). Its
shape is tool-dependent, but a failed tool call is signalled by the boolean key
**`is_error`**: `is_error == true` means the tool call errored. Consumers that
define an "errored" call (e.g. the `identify-hook-misses` skill) MUST key off
`tool_response.is_error`, treating a missing/`null` `tool_response` as "unknown /
not errored".

## Calibration tiers (auto-mode two-way signal)

The `identify-hook-misses` skill uses these fields to grade candidates for
rule changes. The mapping, so it is defined in one place:

| Concept                        | Definition (fields)                                                                                                                                   | Feeds                      |
| ------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------- |
| APPROVE candidate              | `hook_decision == "abstain"` AND `outcome == "approved"`                                                                                              | new APPROVE rule           |
| Human (PRIMARY) tier           | APPROVE candidate with `approval_source == "user"` AND `agent_type == null`                                                                           | strongest APPROVE evidence |
| Weaker tier                    | APPROVE candidate with `approval_source IN (auto, bypass)` OR `agent_type != null`; segment by `approval_source × agent_type`                         | weak APPROVE evidence      |
| "Errored" / down-weighting     | `tool_response.is_error == true` (missing/`null` = not errored) — exclude from APPROVE candidates                                                     | demote APPROVE candidate   |
| False-denial                   | `hook_decision == "abstain"` AND `outcome == "denied"` AND `outcome_notes` matches `auto_mode_classifier`                                             | candidate CETA APPROVE     |
| "Actually risky"               | `replay_result IN ("deny", "ask")` (the current engine self-consistently rejects the row; no curated list)                                            | risk signal                |
| False-approval / over-approval | ran under `auto`/`bypass` and "actually risky", OR a `hook_decision == "allow"` row whose `command_class` the `auto_mode_classifier` denied elsewhere | candidate ASK/DENY         |

`subagent` is deliberately NOT an `approval_source` value — the weaker tier
crosses `approval_source` with the separate `agent_type` axis rather than
inventing a merged bucket. The cross-reference join for over-approval is on the
FULL normalized command (`command_class`), NOT the leading executable.

Interpretation (when to write an APPROVE rule vs an ASK/DENY rule):
[auto-mode-signal.md](auto-mode-signal.md).
