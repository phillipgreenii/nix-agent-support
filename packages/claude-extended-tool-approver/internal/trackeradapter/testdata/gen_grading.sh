#!/bin/bash
# Regenerates grading-events/ and tracker_grading.sql: one call for each row of the
# settlement grading table, plus a converted ask, a call that ran and errored, a
# refused call whose denial surfaced as an error, a call CETA settled, and an
# unresolved call. Needs jq, sqlite3 and a built claude-tool-tracker
# (CTT=/path/to/claude-tool-tracker). Run from this directory.
set -eu
CTT="${CTT:?set CTT to the claude-tool-tracker binary}"
here="$(cd "$(dirname "$0")" && pwd)"
D="${here:?}/grading-events"
mkdir -p "$D"
rm -f "${D:?}"/*.jsonl
F="tool-events.sess-g.tracker.jsonl"
C="tool-events.sess-g.ceta.jsonl"
ev() { # file id ts source kind tuid data
  if [ -n "$6" ]; then
    jq -nc --arg id "$2" --argjson ts "$3" --arg src "$4" --arg kind "$5" --arg tu "$6" --argjson d "$7" \
      '{schema:"tool-events/1",event_id:$id,ts:$ts,source:$src,kind:$kind,session_id:"sess-g",tool_use_id:$tu,data:$d}' >>"$D/$1"
  else
    jq -nc --arg id "$2" --argjson ts "$3" --arg src "$4" --arg kind "$5" --argjson d "$7" \
      '{schema:"tool-events/1",event_id:$id,ts:$ts,source:$src,kind:$kind,session_id:"sess-g",data:$d}' >>"$D/$1"
  fi
}
# call N TOOL INPUT-JSON MODE ENGINE DELIVERED CHECK OUTCOME
#   ENGINE/DELIVERED "-" writes no ceta.verdict; CHECK "-" writes no tracker.check;
#   OUTCOME "-" writes no call_end; OUTCOME error|refused add an error text.
call() {
  local n="$1" tool="$2" input="$3" mode="$4" engine="$5" delivered="$6" check="$7" outcome="$8"
  local tu="tu-g$n" base=$((1790000001000 + n * 100))
  ev "$F" "g${n}a" $((base + 1)) tracker tracker.call_start "$tu" \
    "$(jq -nc --arg t "$tool" --argjson i "$input" '{tool:$t,input:$i,cwd:"/nonexistent/example-repo"}')"
  ev "$F" "g${n}b" $((base + 2)) tracker tracker.pre_tool "$tu" \
    "$(jq -nc --arg m "$mode" --arg t "$tool" '{permission_mode:$m,tool_name:$t,cwd:"/nonexistent/example-repo"}')"
  if [ "$engine" != "-" ]; then
    ev "$C" "g${n}c" $((base + 3)) ceta ceta.verdict "$tu" \
      "$(jq -nc --arg e "$engine" --arg d "$delivered" '{verdict:$e,delivered:$d,reason:"fixture",sandbox_enabled:false}')"
    ev "$C" "g${n}d" $((base + 4)) ceta ceta.trace "$tu" \
      "$(jq -nc --arg e "$engine" '{trace:[{rule_order:1,rule_name:"fixture-rule",decision:$e,reason:"fixture"}]}')"
  fi
  if [ "$check" != "-" ]; then
    ev "$F" "g${n}e" $((base + 5)) tracker tracker.check "$tu" \
      "$(jq -nc --arg c "$check" '{decision:$c,hook:"PreToolUse",reason:"fixture"}')"
  fi
  case "$outcome" in
  ok) ev "$F" "g${n}f" $((base + 8)) tracker tracker.call_end "$tu" '{"outcome":"ok"}' ;;
  error)
    ev "$F" "g${n}f" $((base + 7)) tracker tracker.post_tool "$tu" '{"hook_event":"PostToolUseFailure","tool_name":"x","permission_mode":"default","is_error":true,"error":"boom"}'
    ev "$F" "g${n}g" $((base + 8)) tracker tracker.call_end "$tu" '{"outcome":"error","error":"boom"}'
    ;;
  refused) ev "$F" "g${n}f" $((base + 8)) tracker tracker.call_end "$tu" '{"outcome":"refused","error":"declined"}' ;;
  esac
}
B='{"command":"git push origin main"}'
call 1 Bash "$B" default ask ask ask ok
call 2 Bash "$B" default ask ask ask ok
call 3 Write '{"file_path":"/nonexistent/example-repo/x.txt","content":"hi"}' default ask ask ask ok
call 4 Bash '{"command":"docker run example"}' default ask ask ask refused
call 5 Bash '{"command":"curl example.invalid"}' dontAsk ask reject deny refused
call 6 Bash '{"command":"make build"}' auto abstain abstain ask ok
call 7 Bash '{"command":"make build"}' auto abstain abstain ask refused
call 8 Bash '{"command":"kubectl get pods"}' default abstain abstain ask ok
call 9 Bash '{"command":"kubectl delete pod x"}' default abstain abstain ask refused
call 10 Bash '{"command":"ls"}' default abstain abstain allow ok
call 11 Bash '{"command":"ls"}' bypassPermissions abstain abstain allow ok
call 12 Bash '{"command":"go test ./..."}' default ask ask ask error
call 13 Bash '{"command":"make build"}' default ask ask ask refused
call 14 Bash '{"command":"npm test"}' default ask ask ask -
call 15 Bash '{"command":"git status"}' default approve approve allow ok
call 16 Bash '{"command":"rm -rf /"}' default reject reject deny refused
ev "$F" g99 1790000009999 tracker tracker.session_end "" '{"reason":"other","normal":true}'

tmp="$(mktemp -d)"
"$CTT" ingest --db "$tmp/tracker_grading.db" --events-dir "$D" >/dev/null
sqlite3 "$tmp/tracker_grading.db" .dump | sed -e 's/[[:space:]]*$//' >"$here/tracker_grading.sql"
