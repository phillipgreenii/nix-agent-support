#!/bin/bash
# Regenerates events/ and the two fixture databases. Needs jq, sqlite3 and a built
# claude-tool-tracker (CTT=/path/to/claude-tool-tracker). Run from this directory.
# tracker.db: all event files ingested in one run. tracker_reingest.db: the same events
# ingested in a different order (sess-b first), so every call_id differs while
# (session_id, tool_use_id) stays the same.
set -eu
CTT="${CTT:?set CTT to the claude-tool-tracker binary}"
here="$(cd "$(dirname "$0")" && pwd)"
D="${here:?}/events"
mkdir -p "$D"
rm -f "${D:?}"/*.jsonl
ev() { # file id ts source kind session tuid data
  if [ -n "$7" ]; then
    jq -nc --arg id "$2" --argjson ts "$3" --arg src "$4" --arg kind "$5" --arg s "$6" --arg tu "$7" --argjson d "$8" \
      '{schema:"tool-events/1",event_id:$id,ts:$ts,source:$src,kind:$kind,session_id:$s,tool_use_id:$tu,data:$d}' >>"$D/$1"
  else
    jq -nc --arg id "$2" --argjson ts "$3" --arg src "$4" --arg kind "$5" --arg s "$6" --argjson d "$8" \
      '{schema:"tool-events/1",event_id:$id,ts:$ts,source:$src,kind:$kind,session_id:$s,data:$d}' >>"$D/$1"
  fi
}
F=tool-events.sess-a.tracker.jsonl
C=tool-events.sess-a.ceta.jsonl
ev $F a01 1790000000001 tracker tracker.call_start sess-a tu-a1 '{"tool":"Bash","input":{"command":"git status"},"cwd":"/nonexistent/example-repo"}'
ev $F a02 1790000000002 tracker tracker.pre_tool sess-a tu-a1 '{"permission_mode":"default","tool_name":"Bash","cwd":"/nonexistent/example-repo"}'
ev $C a03 1790000000003 ceta ceta.verdict sess-a tu-a1 '{"verdict":"approve","delivered":"approve","reason":"read-only","rule":"git-readonly","sandbox_enabled":true}'
ev $C a04 1790000000004 ceta ceta.command_class sess-a tu-a1 '{"class":"git status"}'
ev $C a05 1790000000005 ceta ceta.trace sess-a tu-a1 '{"trace":[{"rule_order":1,"rule_name":"git-readonly","decision":"approve","reason":"read-only"}]}'
ev $F a06 1790000000006 tracker tracker.check sess-a tu-a1 '{"decision":"allow","hook":"PreToolUse","reason":"read-only"}'
ev $F a07 1790000000007 tracker tracker.post_tool sess-a tu-a1 '{"hook_event":"PostToolUse","tool_name":"Bash","permission_mode":"default","duration_ms":12,"is_error":false}'
ev $F a08 1790000000008 tracker tracker.call_end sess-a tu-a1 '{"outcome":"ok"}'
ev $F a11 1790000000011 tracker tracker.call_start sess-a tu-a2 '{"tool":"Bash","input":{"command":"rm -rf build"},"cwd":"/nonexistent/example-repo"}'
ev $F a12 1790000000012 tracker tracker.pre_tool sess-a tu-a2 '{"permission_mode":"default","tool_name":"Bash","cwd":"/nonexistent/example-repo"}'
ev $C a13 1790000000013 ceta ceta.verdict sess-a tu-a2 '{"verdict":"ask","delivered":"ask","reason":"destructive","rule":"rm-guard","sandbox_enabled":false}'
ev $C a14 1790000000014 ceta ceta.command_class sess-a tu-a2 '{"class":"rm"}'
ev $F a15 1790000000015 tracker tracker.check sess-a tu-a2 '{"decision":"ask","hook":"PreToolUse","reason":"destructive"}'
ev $F a16 1790000000016 tracker tracker.post_tool sess-a tu-a2 '{"hook_event":"PostToolUseFailure","tool_name":"Bash","permission_mode":"default","is_error":true,"error":"boom"}'
ev $F a17 1790000000017 tracker tracker.call_end sess-a tu-a2 '{"outcome":"error","error":"boom"}'
ev $F a18 1790000000018 tracker tracker.session_end sess-a "" '{"reason":"other","normal":true}'

F=tool-events.sess-b.tracker.jsonl
C=tool-events.sess-b.ceta.jsonl
ev $F b01 1790000000101 tracker tracker.call_start sess-b tu-b1 '{"tool":"Bash","input":{"command":"curl example.invalid"},"cwd":"/"}'
ev $F b02 1790000000102 tracker tracker.pre_tool sess-b tu-b1 '{"permission_mode":"dontAsk","tool_name":"Bash","cwd":"/nonexistent/example-repo"}'
ev $C b03 1790000000103 ceta ceta.verdict sess-b tu-b1 '{"verdict":"ask","delivered":"reject","reason":"network","rule":"net-guard","sandbox_enabled":true}'
ev $F b04 1790000000104 tracker tracker.check sess-b tu-b1 '{"decision":"deny","hook":"PreToolUse","reason":"network"}'
ev $F b05 1790000000105 tracker tracker.call_end sess-b tu-b1 '{"outcome":"refused","error":"network"}'
ev $F b11 1790000000111 tracker tracker.call_start sess-b tu-b2 '{"tool":"Write","input":{"file_path":"/nonexistent/example-repo/x.txt","content":"hi"},"cwd":"/nonexistent/example-repo"}'
ev $F b12 1790000000112 tracker tracker.pre_tool sess-b tu-b2 '{"permission_mode":"default","tool_name":"Write","cwd":"/nonexistent/example-repo"}'
ev $F b13 1790000000113 tracker tracker.check sess-b tu-b2 '{"decision":"allow","reason":"rule"}'
ev $F b14 1790000000114 tracker tracker.call_end sess-b tu-b2 '{"outcome":"ok"}'
echo 'not json' >>"$D/$F"

ev tool-events.sess-c.ceta.jsonl c01 1790000000201 ceta ceta.verdict sess-c tu-c1 '{"verdict":"abstain","delivered":"abstain","sandbox_enabled":false}'

tmp="$(mktemp -d)"
built="$tmp/built"
mkdir -p "$built" "$tmp/events2"
"$CTT" ingest --db "$built/tracker.db" --events-dir "$D" >/dev/null
cp "$D"/*sess-b* "$tmp/events2/"
"$CTT" ingest --db "$built/tracker_reingest.db" --events-dir "$tmp/events2" >/dev/null
cp "$D"/* "$tmp/events2/"
"$CTT" ingest --db "$built/tracker_reingest.db" --events-dir "$tmp/events2" >/dev/null
# The fixtures are committed as SQL text (a binary .db does not survive the
# trailing-whitespace hook); the tests load them into a temp database.
for f in tracker tracker_reingest; do
  sqlite3 "$built/$f.db" .dump | sed -e 's/[[:space:]]*$//' >"$here/$f.sql"
done
