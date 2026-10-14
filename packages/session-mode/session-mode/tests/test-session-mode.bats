#!/usr/bin/env bats
# bats file_tags=type:unit

if [[ -n ${TEST_SUPPORT:-} ]]; then
  source "$TEST_SUPPORT/test_helper.bash"
else
  source "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper.bash"
fi

# The record file this session's CLAUDE_SESSION_ID/SESSION_MODE_STATE_DIR
# (set in test_helper's setup) resolves to.
record_file() { echo "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.session-mode.json"; }

@test "help exits 0 and shows usage" {
  create_cmd_wrapper session-mode
  run "$TEST_DIR/run_session-mode" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: session-mode"* ]]
}

@test "missing SUBCOMMAND is a usage error" {
  create_cmd_wrapper session-mode
  run "$TEST_DIR/run_session-mode"
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing SUBCOMMAND"* ]]
}

@test "unknown SUBCOMMAND is a usage error" {
  create_cmd_wrapper session-mode
  run "$TEST_DIR/run_session-mode" bogus
  [ "$status" -eq 1 ]
  [[ "$output" == *"unknown subcommand"* ]]
}

# =====================================================================================
# start
# =====================================================================================

@test "start: fresh record is created with state=running" {
  create_cmd_wrapper session-mode
  run "$TEST_DIR/run_session-mode" start drain-beads --detail "P1 only"
  [ "$status" -eq 0 ]
  local f
  f="$(record_file)"
  [ -f "$f" ]
  [ "$(jq -r '.kind' "$f")" = "drain-beads" ]
  [ "$(jq -r '.state' "$f")" = "running" ]
  [ "$(jq -r '.detail' "$f")" = "P1 only" ]
  [ "$(jq -r '.started_at' "$f")" = "$(jq -r '.updated_at' "$f")" ]
}

@test "start: missing KIND is a usage error" {
  create_cmd_wrapper session-mode
  run "$TEST_DIR/run_session-mode" start
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing KIND"* ]]
}

@test "start: malformed KIND is rejected" {
  create_cmd_wrapper session-mode
  run "$TEST_DIR/run_session-mode" start "Not_A_Slug"
  [ "$status" -eq 1 ]
  [[ "$output" == *"invalid kind"* ]]
}

@test "start: idempotent same-kind refresh keeps state and started_at, updates detail" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads --detail "first"
  local f started
  f="$(record_file)"
  started="$(jq -r '.started_at' "$f")"
  sleep 1
  run "$TEST_DIR/run_session-mode" start drain-beads --detail "second"
  [ "$status" -eq 0 ]
  [ "$(jq -r '.state' "$f")" = "running" ]
  [ "$(jq -r '.started_at' "$f")" = "$started" ]
  [ "$(jq -r '.detail' "$f")" = "second" ]
}

@test "start: idempotent same-kind refresh preserves a previously-set handoff_bead_id" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads --detail "first"
  "$TEST_DIR/run_session-mode" set-status running --handoff-bead tc-m08w3
  run "$TEST_DIR/run_session-mode" start drain-beads --detail "second"
  [ "$status" -eq 0 ]
  local f
  f="$(record_file)"
  [ "$(jq -r '.handoff_bead_id' "$f")" = "tc-m08w3" ]
}

@test "start: same kind but previously finished resets to running with a fresh started_at" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads
  local f old_started
  f="$(record_file)"
  old_started="$(jq -r '.started_at' "$f")"
  "$TEST_DIR/run_session-mode" set-status finished
  sleep 1
  run "$TEST_DIR/run_session-mode" start drain-beads
  [ "$status" -eq 0 ]
  [ "$(jq -r '.state' "$f")" = "running" ]
  [ "$(jq -r '.started_at' "$f")" != "$old_started" ]
}

@test "start: a different kind already active is a conflict (exit 3), record untouched" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads --detail "keep-me"
  run "$TEST_DIR/run_session-mode" start unblock-human-beads
  [ "$status" -eq 3 ]
  local f
  f="$(record_file)"
  [ "$(jq -r '.kind' "$f")" = "drain-beads" ]
  [ "$(jq -r '.detail' "$f")" = "keep-me" ]
}

@test "start --force overrides a different kind's active record" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads --detail "old"
  run "$TEST_DIR/run_session-mode" start unblock-human-beads --force --detail "new"
  [ "$status" -eq 0 ]
  local f
  f="$(record_file)"
  [ "$(jq -r '.kind' "$f")" = "unblock-human-beads" ]
  [ "$(jq -r '.detail' "$f")" = "new" ]
  [ "$(jq -r '.state' "$f")" = "running" ]
}

@test "start --force resets a same-kind finished record to running with a fresh started_at" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads
  local f old_started
  f="$(record_file)"
  old_started="$(jq -r '.started_at' "$f")"
  "$TEST_DIR/run_session-mode" set-status finished
  sleep 1
  run "$TEST_DIR/run_session-mode" start drain-beads --force
  [ "$status" -eq 0 ]
  [ "$(jq -r '.state' "$f")" = "running" ]
  [ "$(jq -r '.started_at' "$f")" != "$old_started" ]
}

# =====================================================================================
# session id resolution (CLAUDE_SESSION_ID / CLAUDE_CODE_SESSION_ID fallback)
# =====================================================================================

@test "start: CLAUDE_SESSION_ID unset but CLAUDE_CODE_SESSION_ID set still resolves (fallback)" {
  create_cmd_wrapper session-mode
  run env -u CLAUDE_SESSION_ID CLAUDE_CODE_SESSION_ID="code-sess-1" "$TEST_DIR/run_session-mode" start drain-beads
  [ "$status" -eq 0 ]
  local f
  f="$SESSION_MODE_STATE_DIR/code-sess-1.session-mode.json"
  [ -f "$f" ]
  [ "$(jq -r '.kind' "$f")" = "drain-beads" ]
  [ "$(jq -r '.state' "$f")" = "running" ]
}

@test "start: neither CLAUDE_SESSION_ID nor CLAUDE_CODE_SESSION_ID set fails cleanly, no file written" {
  create_cmd_wrapper session-mode
  run env -u CLAUDE_SESSION_ID -u CLAUDE_CODE_SESSION_ID "$TEST_DIR/run_session-mode" start drain-beads
  [ "$status" -ne 0 ]
  [[ "$output" == *"CLAUDE_SESSION_ID/CLAUDE_CODE_SESSION_ID is not set"* ]]
  [ ! -d "$SESSION_MODE_STATE_DIR" ] || [ -z "$(ls -A "$SESSION_MODE_STATE_DIR")" ]
}

# =====================================================================================
# set-status
# =====================================================================================

@test "set-status: updates the state and updated_at, keeps kind/detail/started_at" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads --detail "P1 only"
  local f started
  f="$(record_file)"
  started="$(jq -r '.started_at' "$f")"
  sleep 1
  run "$TEST_DIR/run_session-mode" set-status stopping
  [ "$status" -eq 0 ]
  [ "$(jq -r '.state' "$f")" = "stopping" ]
  [ "$(jq -r '.kind' "$f")" = "drain-beads" ]
  [ "$(jq -r '.detail' "$f")" = "P1 only" ]
  [ "$(jq -r '.started_at' "$f")" = "$started" ]
  [ "$(jq -r '.updated_at' "$f")" != "$started" ]
}

@test "set-status: invalid state is a usage error" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads
  run "$TEST_DIR/run_session-mode" set-status bogus
  [ "$status" -eq 1 ]
  [[ "$output" == *"invalid state"* ]]
}

@test "set-status: no record found exits 2" {
  create_cmd_wrapper session-mode
  run "$TEST_DIR/run_session-mode" set-status finished
  [ "$status" -eq 2 ]
  [[ "$output" == *"no session-mode record found"* ]]
}

# --- set-status --handoff-bead (bead tc-m08w3) ---

@test "set-status --handoff-bead: sets handoff_bead_id on a record that had none" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads
  local f
  f="$(record_file)"
  [ "$(jq -r '.handoff_bead_id // empty' "$f")" = "" ]
  run "$TEST_DIR/run_session-mode" set-status finished --handoff-bead tc-m08w3
  [ "$status" -eq 0 ]
  [ "$(jq -r '.handoff_bead_id' "$f")" = "tc-m08w3" ]
  [ "$(jq -r '.state' "$f")" = "finished" ]
}

@test "set-status without --handoff-bead preserves a previously-set handoff_bead_id" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads
  "$TEST_DIR/run_session-mode" set-status stopping --handoff-bead tc-m08w3
  local f
  f="$(record_file)"
  [ "$(jq -r '.handoff_bead_id' "$f")" = "tc-m08w3" ]
  # A later set-status call with no --handoff-bead (a plain state transition,
  # unrelated to the handoff bead) MUST NOT clear the field.
  run "$TEST_DIR/run_session-mode" set-status finished
  [ "$status" -eq 0 ]
  [ "$(jq -r '.handoff_bead_id' "$f")" = "tc-m08w3" ]
}

@test "set-status --handoff-bead can update an already-set handoff_bead_id (the update path)" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads
  "$TEST_DIR/run_session-mode" set-status stopping --handoff-bead tc-old0
  run "$TEST_DIR/run_session-mode" set-status finished --handoff-bead tc-new1
  [ "$status" -eq 0 ]
  local f
  f="$(record_file)"
  [ "$(jq -r '.handoff_bead_id' "$f")" = "tc-new1" ]
}

@test "set-status --handoff-bead requires a value" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads
  run "$TEST_DIR/run_session-mode" set-status finished --handoff-bead
  [ "$status" -eq 1 ]
  [[ "$output" == *"requires a value"* ]]
}

# =====================================================================================
# show
# =====================================================================================

@test "show: prints the raw record when present" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads --detail "P1 only"
  run "$TEST_DIR/run_session-mode" show
  [ "$status" -eq 0 ]
  [[ "$output" == *'"kind":"drain-beads"'* ]]
}

@test "show: absent record exits 2" {
  create_cmd_wrapper session-mode
  run "$TEST_DIR/run_session-mode" show
  [ "$status" -eq 2 ]
}

@test "show: pre-existing zero-byte file behaves like absent (exit 2, no crash)" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  : >"$(record_file)"
  run "$TEST_DIR/run_session-mode" show
  [ "$status" -eq 2 ]
}

# =====================================================================================
# hook session-end
# =====================================================================================

_hook_payload() {
  printf '{"session_id":"%s","transcript_path":"%s"}' "$1" "$2"
}

@test "hook session-end: a running record is marked finished" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads
  local f payload
  f="$(record_file)"
  payload="$(_hook_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl")"
  run bash -c "printf '%s' '$payload' | '$TEST_DIR/run_session-mode' hook session-end"
  [ "$status" -eq 0 ]
  [ "$(jq -r '.state' "$f")" = "finished" ]
}

@test "hook session-end: preserves handoff_bead_id while marking finished" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads
  "$TEST_DIR/run_session-mode" set-status stopping --handoff-bead tc-m08w3
  local f payload
  f="$(record_file)"
  payload="$(_hook_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl")"
  run bash -c "printf '%s' '$payload' | '$TEST_DIR/run_session-mode' hook session-end"
  [ "$status" -eq 0 ]
  [ "$(jq -r '.state' "$f")" = "finished" ]
  [ "$(jq -r '.handoff_bead_id' "$f")" = "tc-m08w3" ]
}

@test "hook session-end: a stopping record is marked finished" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads
  "$TEST_DIR/run_session-mode" set-status stopping
  local f payload
  f="$(record_file)"
  payload="$(_hook_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl")"
  run bash -c "printf '%s' '$payload' | '$TEST_DIR/run_session-mode' hook session-end"
  [ "$status" -eq 0 ]
  [ "$(jq -r '.state' "$f")" = "finished" ]
}

@test "hook session-end: an already-finished record stays finished" {
  create_cmd_wrapper session-mode
  "$TEST_DIR/run_session-mode" start drain-beads
  "$TEST_DIR/run_session-mode" set-status finished
  local f updated payload
  f="$(record_file)"
  updated="$(jq -r '.updated_at' "$f")"
  payload="$(_hook_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl")"
  run bash -c "printf '%s' '$payload' | '$TEST_DIR/run_session-mode' hook session-end"
  [ "$status" -eq 0 ]
  [ "$(jq -r '.state' "$f")" = "finished" ]
  [ "$(jq -r '.updated_at' "$f")" = "$updated" ]
}

@test "hook session-end: no record file is a no-op, exit 0" {
  create_cmd_wrapper session-mode
  local payload
  payload="$(_hook_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl")"
  run bash -c "printf '%s' '$payload' | '$TEST_DIR/run_session-mode' hook session-end"
  [ "$status" -eq 0 ]
  [ ! -e "$(record_file)" ]
}

@test "hook session-end: a zero-byte record file is a no-op, exit 0" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  : >"$(record_file)"
  local payload
  payload="$(_hook_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl")"
  run bash -c "printf '%s' '$payload' | '$TEST_DIR/run_session-mode' hook session-end"
  [ "$status" -eq 0 ]
  [ ! -s "$(record_file)" ]
}

@test "hook session-end: no session_id in payload is a no-op, exit 0" {
  create_cmd_wrapper session-mode
  run bash -c "printf '%s' '{}' | '$TEST_DIR/run_session-mode' hook session-end"
  [ "$status" -eq 0 ]
}

@test "hook session-end: derives the state dir from transcript_path when SESSION_MODE_STATE_DIR is unset" {
  create_cmd_wrapper session-mode
  # A record placed directly at a transcript-derived dir (SESSION_MODE_STATE_DIR
  # unset for this call), never through `start`.
  local other="$TEST_DIR/elsewhere"
  mkdir -p "$other"
  local other_record="$other/$CLAUDE_SESSION_ID.session-mode.json"
  printf '{"kind":"drain-beads","state":"running","started_at":"x","updated_at":"x"}' >"$other_record"
  local payload
  payload="$(_hook_payload "$CLAUDE_SESSION_ID" "$other/$CLAUDE_SESSION_ID.jsonl")"
  run env -u SESSION_MODE_STATE_DIR bash -c "printf '%s' '$payload' | '$TEST_DIR/run_session-mode' hook session-end"
  [ "$status" -eq 0 ]
  [ "$(jq -r '.state' "$other_record")" = "finished" ]
}

# =====================================================================================
# hook post-compact
# =====================================================================================
#
# The hook writes <transcript_dir>/<session_id>.compaction.json beside the
# transcript. It derives the directory from transcript_path itself, ignores
# SESSION_MODE_STATE_DIR, NEVER creates the directory, prints nothing and
# always exits 0.

# compaction_file [DIR] — the marker path for this session (default: the
# test's SESSION_MODE_STATE_DIR, used as the transcript directory).
compaction_file() { echo "${1:-$SESSION_MODE_STATE_DIR}/$CLAUDE_SESSION_ID.compaction.json"; }

# _compact_payload SID TRANSCRIPT TRIGGER_JSON_FRAGMENT — emits session_id,
# transcript_path and, when the 3rd arg is non-empty, that raw fragment
# (e.g. '"trigger":"auto"') as an extra field.
_compact_payload() {
  if [[ -n ${3:-} ]]; then
    printf '{"session_id":"%s","transcript_path":"%s",%s}' "$1" "$2" "$3"
  else
    _hook_payload "$1" "$2"
  fi
}

_run_post_compact() {
  # $1 = payload; extra env prefix words via $2.. (e.g. env -u X)
  local payload="$1"
  shift
  run "$@" bash -c "printf '%s' '$payload' | '$TEST_DIR/run_session-mode' hook post-compact"
}

@test "hook post-compact: trigger manual writes a marker beside the transcript" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  local before after f payload
  f="$(compaction_file)"
  payload="$(_compact_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl" '"trigger":"manual"')"
  before="$(date +%s)"
  _run_post_compact "$payload" env
  after="$(date +%s)"
  [ "$status" -eq 0 ]
  [ -f "$f" ]
  [ "$(wc -l <"$f" | tr -d ' ')" = "1" ]
  [ "$(jq -r '.trigger' "$f")" = "manual" ]
  [ "$(jq -r '.ts | type' "$f")" = "number" ]
  [ "$(jq -r '.ts' "$f")" -ge "$before" ]
  [ "$(jq -r '.ts' "$f")" -le "$after" ]
  # exactly the two contract fields, no count and no summary
  [ "$(jq -c 'keys' "$f")" = '["trigger","ts"]' ]
}

@test "hook post-compact: trigger auto writes a marker" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  local payload
  payload="$(_compact_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl" '"trigger":"auto"')"
  _run_post_compact "$payload" env
  [ "$status" -eq 0 ]
  [ "$(jq -r '.trigger' "$(compaction_file)")" = "auto" ]
}

@test "hook post-compact: a second call overwrites the marker" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  local f payload
  f="$(compaction_file)"
  printf '{"ts":1,"trigger":"manual"}\n' >"$f"
  payload="$(_compact_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl" '"trigger":"auto"')"
  _run_post_compact "$payload" env
  [ "$status" -eq 0 ]
  [ "$(jq -r '.trigger' "$f")" = "auto" ]
  [ "$(jq -r '.ts' "$f")" -gt 1 ]
  [ "$(wc -l <"$f" | tr -d ' ')" = "1" ]
}

@test "hook post-compact: absent trigger writes unknown" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  local payload
  payload="$(_compact_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl")"
  _run_post_compact "$payload" env
  [ "$status" -eq 0 ]
  [ "$(jq -r '.trigger' "$(compaction_file)")" = "unknown" ]
}

@test "hook post-compact: numeric trigger writes unknown" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  local payload
  payload="$(_compact_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl" '"trigger":7')"
  _run_post_compact "$payload" env
  [ "$status" -eq 0 ]
  [ "$(jq -r '.trigger' "$(compaction_file)")" = "unknown" ]
}

@test "hook post-compact: empty-string trigger writes unknown" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  local payload
  payload="$(_compact_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl" '"trigger":""')"
  _run_post_compact "$payload" env
  [ "$status" -eq 0 ]
  [ "$(jq -r '.trigger' "$(compaction_file)")" = "unknown" ]
}

@test "hook post-compact: an unrecognized trigger string writes unknown" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  local payload
  payload="$(_compact_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl" '"trigger":"bogus"')"
  _run_post_compact "$payload" env
  [ "$status" -eq 0 ]
  [ "$(jq -r '.trigger' "$(compaction_file)")" = "unknown" ]
}

@test "hook post-compact: compact_summary is not stored" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  local payload
  payload="$(_compact_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl" '"trigger":"auto","compact_summary":"secret summary"')"
  _run_post_compact "$payload" env
  [ "$status" -eq 0 ]
  [[ "$(cat "$(compaction_file)")" != *"secret summary"* ]]
}

@test "hook post-compact: payload {} (no session_id) writes nothing, exit 0" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  _run_post_compact '{}' env
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ -z "$(ls -A "$SESSION_MODE_STATE_DIR")" ]
}

@test "hook post-compact: non-JSON input writes nothing, exit 0" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  _run_post_compact 'this is not json' env
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ -z "$(ls -A "$SESSION_MODE_STATE_DIR")" ]
}

@test "hook post-compact: missing transcript_path writes nothing, exit 0" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  _run_post_compact "{\"session_id\":\"$CLAUDE_SESSION_ID\",\"trigger\":\"auto\"}" env
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ -z "$(ls -A "$SESSION_MODE_STATE_DIR")" ]
}

@test "hook post-compact: a nonexistent transcript dir writes nothing and creates NO directory" {
  create_cmd_wrapper session-mode
  local missing="$TEST_DIR/no-such-dir/deeper" payload
  payload="$(_compact_payload "$CLAUDE_SESSION_ID" "$missing/$CLAUDE_SESSION_ID.jsonl" '"trigger":"auto"')"
  # SESSION_MODE_STATE_DIR unset, as in production: the PWD-derived fallback
  # under $HOME/.claude/projects must NOT be materialized.
  _run_post_compact "$payload" env -u SESSION_MODE_STATE_DIR
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  [ ! -e "$TEST_DIR/no-such-dir" ]
  [ ! -e "$HOME/.claude/projects" ]
  [ ! -e "$HOME/.claude" ]
  # and nothing under the (isolated) HOME at all
  [ -z "$(ls -A "$HOME")" ]
}

@test "hook post-compact: prints nothing to stdout or stderr on success" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  local payload out
  payload="$(_compact_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl" '"trigger":"auto"')"
  out="$(printf '%s' "$payload" | "$TEST_DIR/run_session-mode" hook post-compact 2>&1)"
  [ -z "$out" ]
  [ -f "$(compaction_file)" ]
}

@test "hook post-compact: marker file mode is 0600" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  local payload mode
  payload="$(_compact_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl" '"trigger":"auto"')"
  _run_post_compact "$payload" env
  [ "$status" -eq 0 ]
  mode="$(stat -c '%a' "$(compaction_file)" 2>/dev/null || stat -f '%Lp' "$(compaction_file)")"
  [ "$mode" = "600" ]
}

@test "hook post-compact: SESSION_MODE_STATE_DIR set elsewhere does NOT redirect the write" {
  create_cmd_wrapper session-mode
  local tdir="$TEST_DIR/transcripts" payload
  mkdir -p "$tdir"
  # SESSION_MODE_STATE_DIR (from setup) points at a different, even
  # nonexistent, directory: the marker must land beside the transcript.
  payload="$(_compact_payload "$CLAUDE_SESSION_ID" "$tdir/$CLAUDE_SESSION_ID.jsonl" '"trigger":"auto"')"
  _run_post_compact "$payload" env
  [ "$status" -eq 0 ]
  [ -f "$(compaction_file "$tdir")" ]
  [ ! -e "$SESSION_MODE_STATE_DIR" ]
}

@test "hook post-compact: leaves no temp dotfile behind" {
  create_cmd_wrapper session-mode
  mkdir -p "$SESSION_MODE_STATE_DIR"
  local payload
  payload="$(_compact_payload "$CLAUDE_SESSION_ID" "$SESSION_MODE_STATE_DIR/$CLAUDE_SESSION_ID.jsonl" '"trigger":"auto"')"
  _run_post_compact "$payload" env
  [ "$status" -eq 0 ]
  [ "$(ls -A "$SESSION_MODE_STATE_DIR")" = "$CLAUDE_SESSION_ID.compaction.json" ]
}

@test "hook: unknown event usage mentions post-compact" {
  create_cmd_wrapper session-mode
  run "$TEST_DIR/run_session-mode" hook bogus
  [ "$status" -eq 1 ]
  [[ "$output" == *"post-compact"* ]]
}

@test "help documents hook post-compact" {
  create_cmd_wrapper session-mode
  run "$TEST_DIR/run_session-mode" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"hook post-compact"* ]]
}

# =====================================================================================
# CLI record-directory resolution without $SESSION_MODE_STATE_DIR (pg2-3pg4v)
# =====================================================================================
#
# Regression coverage for pg2-3pg4v: resolve_file() (used by start/set-status/
# show) must locate the record via the session's actual, FIXED transcript
# file -- found by globbing for the session id -- rather than re-deriving a
# directory from $PWD on every call, which drifts as Bash tool calls `cd`/
# `git -C` around mid-session.

@test "start: locates the record dir via the session's transcript file, not \$PWD, when SESSION_MODE_STATE_DIR is unset" {
  create_cmd_wrapper session-mode
  local transcript_dir="$HOME/.claude/projects/-some-other-encoded-dir"
  mkdir -p "$transcript_dir"
  : >"$transcript_dir/$CLAUDE_SESSION_ID.jsonl"
  local unrelated_cwd="$TEST_DIR/unrelated"
  mkdir -p "$unrelated_cwd"
  run bash -c "cd '$unrelated_cwd' && env -u SESSION_MODE_STATE_DIR '$TEST_DIR/run_session-mode' start drain-beads"
  [ "$status" -eq 0 ]
  local f="$transcript_dir/$CLAUDE_SESSION_ID.session-mode.json"
  [ -f "$f" ]
  [ "$(jq -r '.kind' "$f")" = "drain-beads" ]
}

@test "start then set-status resolve to the SAME record even after \$PWD changes mid-session (pg2-3pg4v regression)" {
  create_cmd_wrapper session-mode
  local transcript_dir="$HOME/.claude/projects/-fixed-project-dir"
  mkdir -p "$transcript_dir"
  : >"$transcript_dir/$CLAUDE_SESSION_ID.jsonl"

  local start_cwd="$TEST_DIR/repo-a" other_cwd="$TEST_DIR/repo-b"
  mkdir -p "$start_cwd" "$other_cwd"

  # Before the fix, this second call -- from a DIFFERENT cwd than the first
  # -- would re-derive a different $PWD-based directory and fail with "no
  # session-mode record found", even though the record from `start` exists.
  ( cd "$start_cwd" && env -u SESSION_MODE_STATE_DIR "$TEST_DIR/run_session-mode" start drain-beads )
  run bash -c "cd '$other_cwd' && env -u SESSION_MODE_STATE_DIR '$TEST_DIR/run_session-mode' set-status finished"
  [ "$status" -eq 0 ]
  local f="$transcript_dir/$CLAUDE_SESSION_ID.session-mode.json"
  [ -f "$f" ]
  [ "$(jq -r '.state' "$f")" = "finished" ]
}

@test "start: falls back to the \$PWD-derived dir when no transcript exists for this session yet" {
  create_cmd_wrapper session-mode
  local cwd="$TEST_DIR/repo-c"
  mkdir -p "$cwd"
  run bash -c "cd '$cwd' && env -u SESSION_MODE_STATE_DIR '$TEST_DIR/run_session-mode' start drain-beads"
  [ "$status" -eq 0 ]
  local encoded_cwd expected_dir
  encoded_cwd="$(printf '%s' "$cwd" | tr -c 'A-Za-z0-9' '-')"
  expected_dir="$HOME/.claude/projects/$encoded_cwd"
  [ -f "$expected_dir/$CLAUDE_SESSION_ID.session-mode.json" ]
}
