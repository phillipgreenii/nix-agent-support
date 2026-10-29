# Event kinds and their data fields

The envelope and the file contract: [README.md](../README.md#event-format). This file fixes each
kind's `data` fields. Field names and types are checked against the build's `claude-code.d.ts`
(Claude Code 2.1.310, written under `types/` in the `plugin-authoring` skill's folder when it loads;
the hook inputs are its `PreToolUseHookInput`, `PostToolUseHookInput`, `PostToolUseFailureHookInput`,
`PermissionRequestHookInput` and `PermissionDeniedHookInput`, the mod inputs its `ToolCallInput`,
`ToolCheckInput`/`ToolCheckResult` and `SessionEndInput`). The mods API is early access: re-check
before each chunk that touches the mod. Later packets take field names only from this file.

Types: `string`, `number`, `boolean`, `object` (any JSON), `?` = may be absent. Absent means the key
is left out, never `null`.

All tool-call kinds carry `tool_use_id` in the envelope, and `agent_id` for a subagent's call.

## Tracker kinds, written by the mod (`source` `tracker`)

### `tracker.call_start` (`tool.call`)

| Field   | Type    | Meaning                                                                                                                             |
| ------- | ------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `tool`  | string  | the tool as the model names it (`Bash`, `mcp__server__tool`); `ToolCallInput.tool`                                                  |
| `input` | object  | the tool's arguments: the `tool.call` input without `tool`, `tool_use_id`, `agentId`, `requestMeta` (`{ "command": ... }` for Bash) |
| `cwd`   | string? | the working directory of the call, when the mod can read it                                                                         |

### `tracker.check` (`tool.check`)

From `ToolCheckResult` as the chain resolves it (`next(e)`).

| Field      | Type    | Meaning                                                                                                                    |
| ---------- | ------- | -------------------------------------------------------------------------------------------------------------------------- |
| `decision` | string  | `allow`, `ask` or `deny` (`ToolCheckDecision`)                                                                             |
| `hook`     | string? | the classic hook event that decided or whose ask the verdict was reached under (`PreToolUse`); absent when no hook decided |
| `reason`   | string? | the reason text                                                                                                            |
| `rule`     | string? | the settings rule that decided, as written (`Bash(git push:*)`)                                                            |
| `setting`  | string? | the option whose own check asked                                                                                           |
| `plugin`   | string? | the plugin whose hook made an ask                                                                                          |
| `approver` | string? | `host` or `person` (`ToolCheckInput.approver`), when set                                                                   |

### `tracker.running` (`ui.render`, component `ToolUse`)

| Field  | Type   | Meaning                                                            |
| ------ | ------ | ------------------------------------------------------------------ |
| `tool` | string | the tool whose row first showed running; interactive sessions only |

### `tracker.call_end` (`tool.call`, after `next`)

| Field     | Type    | Meaning                                                                                     |
| --------- | ------- | ------------------------------------------------------------------------------------------- |
| `outcome` | string  | `ok` (ran, no error), `error` (ran and errored), `refused` (a `deny` result), `interrupted` |
| `error`   | string? | the error text, or the `deny` text for `refused`                                            |

### `tracker.session_end` (`session.end`)

No `tool_use_id`. `agent_id` is never set.

| Field    | Type    | Meaning                                                                                   |
| -------- | ------- | ----------------------------------------------------------------------------------------- |
| `reason` | string  | `SessionEndInput.reason` (`clear`, `resume`, `logout`, `prompt_input_exit`, `other`, ...) |
| `normal` | boolean | the session ended normally, so unsettled calls of it were abandoned, not in flight        |

## Tracker kinds, written by hook mode (`claude-tool-tracker hook`)

`tool_use_id` and `agent_id` come from the hook input (`agent_id` only inside a subagent).

### `tracker.pre_tool` (`PreToolUse`)

| Field             | Type    | Meaning                                                                                 |
| ----------------- | ------- | --------------------------------------------------------------------------------------- |
| `permission_mode` | string? | the live mode: `default`, `acceptEdits`, `bypassPermissions`, `plan`, `dontAsk`, `auto` |
| `agent_type`      | string? | the agent type name, in a subagent or an `--agent` session                              |
| `tool_name`       | string  | the tool                                                                                |
| `cwd`             | string? | the hook's working directory                                                            |

### `tracker.post_tool` (`PostToolUse`, `PostToolUseFailure`)

Never the raw `tool_response`.

| Field             | Type     | Meaning                                                                             |
| ----------------- | -------- | ----------------------------------------------------------------------------------- |
| `hook_event`      | string   | `PostToolUse` or `PostToolUseFailure`                                               |
| `tool_name`       | string   | the tool                                                                            |
| `permission_mode` | string?  | as above                                                                            |
| `agent_type`      | string?  | as above                                                                            |
| `duration_ms`     | number?  | Claude Code's tool execution time; excludes permission-prompt and hook time         |
| `is_error`        | boolean? | `true` for a failure event; for a success, `tool_response.is_error` when it has one |
| `exit_code`       | number?  | `tool_response.exit_code` (or `exitCode`) when it is a number                       |
| `response_bytes`  | number?  | size of the raw `tool_response` JSON (success only)                                 |
| `error`           | string?  | the failure text (`PostToolUseFailure.error`)                                       |
| `is_interrupt`    | boolean? | `PostToolUseFailure.is_interrupt`                                                   |

### `tracker.permission_offer` (`PermissionRequest`)

The hook input declares no `tool_use_id`; the envelope omits it unless the runtime supplies one.

| Field             | Type    | Meaning                                                         |
| ----------------- | ------- | --------------------------------------------------------------- |
| `tool_name`       | string  | the tool                                                        |
| `permission_mode` | string? | as above                                                        |
| `agent_type`      | string? | as above                                                        |
| `tool_input`      | object? | the tool input the dialog shows (for correlation)               |
| `suggestions`     | object? | `permission_suggestions` as given (array of `PermissionUpdate`) |

### `tracker.denied` (`PermissionDenied`)

| Field             | Type    | Meaning                                                       |
| ----------------- | ------- | ------------------------------------------------------------- |
| `tool_name`       | string  | the tool                                                      |
| `reason`          | string  | the denial reason (carries the auto-mode classifier's reason) |
| `permission_mode` | string? | as above                                                      |
| `agent_type`      | string? | as above                                                      |

## CETA kinds (`source` `ceta`)

Written by CETA's `PreToolUse` process, in Go, for the call it is deciding, with that call's
`tool_use_id` (and `agent_id`). Verdict words are CETA's own, not the mod's.

### `ceta.verdict`

| Field             | Type    | Meaning                                                                                                                         |
| ----------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------- |
| `verdict`         | string  | the engine's verdict: `approve`, `ask`, `reject` or `abstain`                                                                   |
| `delivered`       | string  | the verdict actually delivered (differs when an unattended session turns an ask into a reject, `hookio.NeverAskWhenUnattended`) |
| `reason`          | string? | the reason text                                                                                                                 |
| `rule`            | string? | the deciding rule                                                                                                               |
| `sandbox_enabled` | boolean | whether the sandbox was enabled for the call                                                                                    |

### `ceta.trace`

| Field   | Type   | Meaning                                                             |
| ------- | ------ | ------------------------------------------------------------------- |
| `trace` | object | the rule trace, whenever CETA computed one (CETA's own trace shape) |

### `ceta.rule_errors`

| Field    | Type   | Meaning                                                                 |
| -------- | ------ | ----------------------------------------------------------------------- |
| `errors` | object | the per-rule failure snapshot ADR 0043 flushes today (CETA's own shape) |

### `ceta.command_class`

| Field   | Type   | Meaning                                                                              |
| ------- | ------ | ------------------------------------------------------------------------------------ |
| `class` | string | for Bash, CETA's normalized command class (the key `report --group-by command` uses) |

## Oversized events

Any kind may carry `data._truncated` (the original line size in bytes) and, as a last resort,
`data._dropped: true` with no other fields: see "Maximum line size and shortening" in the README.
