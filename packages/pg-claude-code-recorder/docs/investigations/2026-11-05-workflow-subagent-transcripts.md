# Workflow subagent transcripts: parse them, and what happens to the old snapshots

## Finding

Files written by the Workflow tool live under
`projects/<project>/<uuid>/subagents/workflows/wf_<id>/`: `agent-<id>.jsonl`
(transcripts, same format as other subagent transcripts), `agent-<id>.meta.json`
and `journal.jsonl`. `Classify` tested for a `workflows` directory anywhere in the
path before the subagent cases, so all of them were `unparsed` snapshots: grown
transcripts were copied whole and never parsed, and the two parser sources
accepted only the 5-component subagent shape.

## Change

- `Classify`: the `workflows` rule now applies only when the path has no
  `subagents` component. Run files (`<uuid>/workflows/wf_*.json`) and saved
  scripts (`<uuid>/workflows/scripts/*.js`) stay unparsed.
- `claudetranscript` and `claudesubagentmeta` match the 7-component shape
  `projects/<p>/<uuid>/subagents/workflows/wf_<id>/agent-<id>.{jsonl,meta.json}`
  and emit a `workflow_id` field (the `wf_<id>` directory name) next to
  `agent_id`. The parser version is not bumped: the field exists only on new
  records, and a bump would re-parse the whole index.
- `journal.jsonl` is NOT parsed (decision). It holds `started`/`result` control
  lines per agent (10 files, about 115 KB for the largest, 44 lines); each
  `result` repeats text already present in the agent transcripts. It stays an
  unparsed snapshot.

## Migration decision: leave the old rows, start new ones (no re-ingest job)

How the existing code handles a path whose classification changes:

- Append: `resolveAppend` only considers file rows with `mode = append`. The old
  row is `snapshot`, so it is never continued; `createFile` creates a new append
  row and `closeOtherHolders` closes the old row's path. The new row copies the
  whole file from byte 0, so no history is lost: the old row keeps every stored
  snapshot version and path history, it just no longer holds the live path.
- Snapshot to snapshot with a new source (the `.meta.json` files): the old
  `ingestSnapshot` continued the existing row, whose `source` stayed `unparsed`
  forever, and the parser selects a source by the row's `source`, so the meta
  files would never have been parsed. `ingestSnapshot` now continues a row only
  when its source equals the current classification; otherwise it creates a new
  row (version 1) and closes the old row's path, the same as the append case.

Count of affected files, from the read-only evidence in the originating
investigation (2026-11-04): 492 `agent-*.jsonl` + 492 `agent-*.meta.json` = 984
files change source (the 10 `journal.jsonl` and the 11 run files / 6 scripts do
not). Each is re-ingested the next time the watcher or a sweep sees its path;
no migration command is needed. Cost: the 492 transcripts are copied once more
(a second copy of those bytes beside the old snapshot versions, which stay until
retention removes them). The live state directory was not touched.

Test: `TestSourceChangeStartsANewFileAndClosesTheOldPath` (real files, real
SQLite) pins both cases.
