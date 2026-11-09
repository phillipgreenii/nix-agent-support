# Claude Code mods: findings guide

The key words MUST, MUST NOT, SHOULD, SHOULD NOT and MAY in this page are to be read as in RFC 2119.

This guide records what the display-layer spikes learned about Claude Code mods (a mod is a plugin
that draws or extends the Claude Code interface through the `$` host facade), so a later agent does
not repeat the spike. The `claude-code-mods` skill points here.

- Claude Code version the findings were observed on: 2.1.314.
- Date of this guide: 2026-11-06 (the spike observations are dated 2026-11-05 unless a finding says
  otherwise).
- Each finding is marked **verified live** (observed on Claude Code 2.1.314) or **docs only** (taken
  from the documentation and the shipped type definitions, not observed). Where a finding mixes the
  two, the sentence that is docs only says so.
- This guide MUST be updated whenever a later phase learns something new. New spike results go under
  "Spike results" at the end.

## F1: the mod status line

Verified live. A mod's `$.ui.status` is one plain-text row per plugin, prefixed by the engine with a
warning glyph and the plugin's manifest `name`. Newlines draw as a replacement glyph; ANSI escapes
show literally; long text is cut with an ellipsis; there is no ordering control; an updated line
moves to the bottom of the mod lines. The `displayName` and `title` manifest keys are ignored.

Consequence: the status line is for short, focused alerts only, and one plugin SHOULD own all of
them as one line it fits itself.

## F2: the shell statusLine setting

Verified live. The shell `statusLine` setting accepts only `{ type: "command", command: <string> }`;
`null`, `""` and `{}` are rejected; an empty `command` string loads and draws no shell row. In this
repo a null option is a deliberate no-op that leaves an already-written `statusLine` in the user
settings file.

Consequence: the shell line is retired by setting the `statusLine` option to
`{ type = "command"; command = ""; }`, which the option already accepts. This is now the claude-settings
default, and the shell part scripts and their module are removed. Writing the empty command
overwrites any stale `statusLine` the old module left, so no delete filter was added.

## F3: the band above the prompt

Verified live. The band above the prompt is one site. Two plugins returning their own trees do not
both draw: the first answering plugin wins. A plugin that awaits `next(e)` and nests the result
composes (two stacked rows, both buttons worked). The band supports color, background color, bold,
dim, emoji and Nerd Font glyphs, several rows, and re-wraps with width. The band hook sees the live
width.

Consequence: one plugin owns the band and draws other bundles' rows from pushed data; it also nests
`next(e)` so a band from another plugin is not lost.

## F4: collapsing the band

Verified live. The engine's own collapse control hides the whole band; a plugin's toggle cannot undo
it.

Consequence: an alert MUST NOT live only in the band.

## F5: panes, tabs and modals

Verified live, except where noted. Several panes can be open at once; they become tabs; the last
opened is active. A pane opened `holdToasts` holds every toast until it closes. A modal pane takes
focus; Tab and arrows walk its buttons; Esc and a dismiss button close it. Tabs were tested with two
panes from one plugin; behavior across plugins, and a modal beside the Focus pane, is untested.

Consequence: modals and the Focus pane are panes.

## F6: config, prompt fill and submit

Verified live. `$.config.set({ key: "model", value })` switches the model and the display follows.
There is no effort row. `$.prompt.fill` puts text in the prompt box without sending.
`$.prompt.submit` from a command does not start a turn until the next user input.

Consequence: the model button applies directly; the effort and wrap-up buttons pre-fill the prompt.

## F7: environment variables

Partly verified live. `$.env.set` on fixed string-literal names is visible to later child processes
and to `!` shell commands; a headless run set and unset both a made-up name and `AWS_PROFILE` with
no denial, and an interactive `!` command saw a made-up name. `AWS_PROFILE` was not tried
interactively. A model-run Bash call was not directly confirmed (open item below).

Consequence: an assume action can set a fixed list of variables.

## F8: render hook and links

Verified live. A render hook can rewrite assistant-message markdown so ids become links; a click
opened them.

Consequence: transcript links are feasible.

## F9: session usage, measure events and repo

Partly verified live. `$.session.usage()` gives context window at start and, after the first model
response, tokens and percent; it gives rate limits and cost. Only the five-hour limit was seen live;
the seven-day kind and "a whole-point move" are docs only, and the list was empty in one session. A
`session.measure` event fires at start and after each turn and names what changed (verified for
context, rate limits and cost). Effort is readable from the `turn.step` event after the first model
request. `$.session.repo()` gives root, remote and `internal`, plus a `name` that was null in every
run; no branch. `$.session.messages()` entries carry only role, text and tool uses.

Consequence: context, limits, cost, model and effort need no external collector. Session name has no
documented mod source (see F13 for the session registry).

## F10: streaming hooks and plugin-added nouns

Partly verified live. A hook on a streaming event (`turn.step`) MUST be an async generator
delegating with `yield* next(e)`. A method on a plugin-added noun takes exactly one object argument
and returns a Promise (verified live). That only plain data crosses plugin boundaries is docs only,
and a callback across the boundary was not tried. A hook that throws is skipped alone; a failed hot
reload keeps the previous version loaded.

Consequence: the `pgd` noun is data in, Promise out, one object per call. Contributions are data,
not callbacks.

## F11: the recorder and session titles

Verified live (2026-11-05). The recorder (`pg-claude-code-recorder`) indexes transcript
`custom-title` records by session; its `query` command returned them with the session id and time.
The field that holds the title text was not found: `customTitle` was rejected as an unknown field.

Consequence: superseded by F13: session name is read from the session registry, not the recorder.

## F13: the session registry

Observed on Claude Code 2.1.314 (2026-11-06). Undocumented Claude Code state; it MAY change in any
release, so this entry MUST be re-checked on an upgrade. Claude Code keeps one file per running
session, `~/.claude/sessions/<pid>.json`, for headless and interactive sessions alike. Fields:
`sessionId`, `name`, `nameSource` and `nameSince`; a name change rewrites the entry (`name`,
`nameSource`, `formerNames`, `nameSince`). `nameSource` takes `derived` (the name comes from the
working directory), `auto`, `user` (`-n <name>` or a rename) or `collision`. A live `/rename` was not
run. `$.fs.list(dir)` answers the entries of a directory (`name`, `kind`, `size`, `mtimeMs`,
`isLink`).

Consequence: session name is read from the registry entry whose `sessionId` equals `$.session.id()`,
shown when `nameSource` is not `derived`, with no recorder, no `ps` and no transcript read.

## F12: processes, files and invalidation

Partly docs only. `$.process.run` runs a command without a shell (verified live). `$.fs` reads files
(docs only). `$.ui.invalidate` redraws, capped at 10 per second (docs only).

Consequence: file reads and the recorder CLI are the data paths; invalidation is cheap but MUST
still be coalesced.

## Open items

These are carried forward from the design and MUST stay here until a spike or a later phase answers
them.

- Whether `$.fs` reads `.git/HEAD` and the `.git` worktree pointer file as expected.
- `reviewDecision` from `gh pr view` on a real PR, and its git-lock impact.
- Whether a contributed button press can run an action (data cannot carry a callback; a command name
  or event may be needed).
- A model-run Bash call seeing a variable from `$.env.set`.
- Whether the plugin store is safe under concurrent writers from many sessions, and how fresh one
  session's write is for another.
- Whether OTLP over `$.http.fetch` is allowed under the managed web policy, and what first-class mod
  telemetry would take.
- Whether a rename can be detected by hooking the built-in `/rename` command, which would let the
  name refresh on demand and not on a timer.
- Whether the `PromptHint` or `SessionMode` sites accept a colored multi-row tree (not needed for
  this design).
- The assume expiry source and whether the four variables suffice, the wrap-up command text, and the
  alert thresholds. The plugin name `pgd` (operator ruling 2026-11-06) is accepted for now and MAY
  change later; bead links are deferred until a target exists.

## Spike results

Entries from the spikes (the OTLP route and the recorder title field) and from later phases are
appended below, newest last. Each entry names the Claude Code version it was observed on and the
date.

### OTLP over http fetch

- result: allowed. A headless run (`claude --plugin-dir <throwaway mod dir> -p "..."`) with a throwaway
  mod whose `session.start` hook called `$.http.fetch` (POST, `content-type: application/json`, a
  minimal OTLP/HTTP JSON body with one gauge) to a loopback-only listener at
  `http://127.0.0.1:<port>/v1/metrics`. The listener received the POST with a parseable JSON body and
  the fetch returned status 200. No policy error was raised.
- version: 2.1.314
- date: 2026-11-06
- consequence: the exporter's `exportMetrics()` MAY post OTLP over `$.http.fetch` to the configured
  `metrics.endpoint`; no emitter command is needed. Only loopback was tested; a non-loopback endpoint
  is still subject to the organization's web policy.
