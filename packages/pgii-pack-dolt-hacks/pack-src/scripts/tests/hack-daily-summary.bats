#!/usr/bin/env bats
# Unit tests for ../hack-daily-summary.sh.
# All external CLIs (gc, gh, claude, git, jq) are stubbed via PATH.
# Stubs write their args to $CALLS_LOG so tests can assert what was invoked.

setup() {
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  export GC_CITY="$TEST_DIR/gc"
  export CURSOR_FILE="$GC_CITY/.gc/state/daily-summary-last-run"
  export EVENTS_FILE="$GC_CITY/.gc/events.jsonl"
  export TRACE_FILE="$GC_CITY/.gc/runtime/control-dispatcher-trace.log"
  export CITY_TOML="$GC_CITY/city.toml"
  export CALLS_LOG="$TEST_DIR/calls.log"
  export STUB_FIXTURES="$BATS_TEST_DIRNAME/fixtures"

  mkdir -p "$GC_CITY/.gc/state" "$GC_CITY/.gc/runtime" "$TEST_DIR/bin"
  : > "$CALLS_LOG"

  # Generic stub maker: prints fixture content based on first arg, logs all argv.
  make_stub() {
    local name="$1"; local body="$2"
    cat > "$TEST_DIR/bin/$name" <<EOF
#!/usr/bin/env bash
echo "$name \$*" >> "$CALLS_LOG"
$body
EOF
    chmod +x "$TEST_DIR/bin/$name"
  }
  export -f make_stub

  PATH="$TEST_DIR/bin:$PATH"
  export PATH

  # Source the script with main() suppressed so individual functions are
  # callable from tests. The script's `main "$@"` final line is the only
  # call site; setting SOURCED_FOR_TEST short-circuits it.
  export SOURCED_FOR_TEST=1
}

teardown() {
  rm -rf "$TEST_DIR"
}

# Loader: source the script in a way that does not invoke main().
load_script() {
  # The script ends with `main "$@"`. We sed it out for sourcing.
  local tmp="$TEST_DIR/script-sourceable.sh"
  sed '$d' "$BATS_TEST_DIRNAME/../hack-daily-summary.sh" > "$tmp"
  # shellcheck disable=SC1090
  source "$tmp"
}

@test "script: invocable and exits 0 with minimal stubs" {
  cat > "$TEST_DIR/bin/gc" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  bd)     echo "[]" ;;
  rig)    echo '{"rigs":[]}' ;;
  events) : ;;
  agent)  echo "NAME STATE" ;;
  mail)   : ;;
esac
EOF
  chmod +x "$TEST_DIR/bin/gc"
  cat > "$TEST_DIR/bin/git" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
  chmod +x "$TEST_DIR/bin/git"
  cp "$STUB_FIXTURES/city.toml" "$CITY_TOML"
  cp "$STUB_FIXTURES/trace.log" "$TRACE_FILE"

  run bash "$BATS_TEST_DIRNAME/../hack-daily-summary.sh"
  [ "$status" -eq 0 ]
}

@test "read_cursor: missing file returns now - 86400" {
  load_script
  local before=$(date +%s)
  local result; result="$(read_cursor)"
  local after=$(date +%s)
  [ "$result" -ge $((before - 86400)) ]
  [ "$result" -le $((after - 86400 + 1)) ]
}

@test "read_cursor: malformed file falls back to now - 86400" {
  echo "garbage" > "$CURSOR_FILE"
  load_script
  local before=$(date +%s)
  local result; result="$(read_cursor)"
  [ "$result" -ge $((before - 86400 - 1)) ]
}

@test "read_cursor: gap older than 7 days is clamped" {
  local eight_days_ago=$(( $(date +%s) - 8*86400 ))
  echo "$eight_days_ago" > "$CURSOR_FILE"
  load_script
  local now=$(date +%s)
  local result; result="$(read_cursor)"
  [ "$result" -ge $((now - 604801)) ]
  [ "$result" -le $((now - 604799)) ]
}

@test "read_cursor: valid recent cursor is returned verbatim" {
  local ts=$(( $(date +%s) - 3600 ))
  echo "$ts" > "$CURSOR_FILE"
  load_script
  [ "$(read_cursor)" = "$ts" ]
}

@test "write_cursor: writes ts atomically and creates parent dir" {
  rm -rf "$GC_CITY/.gc/state"
  load_script
  write_cursor 1700000000
  [ "$(cat "$CURSOR_FILE")" = "1700000000" ]
  [ ! -e "$CURSOR_FILE.tmp" ]   # tmp file cleaned up
}

@test "gather_beads: emits a Bead activity section with counts" {
  cat > "$TEST_DIR/bin/gc" <<'EOF'
#!/usr/bin/env bash
echo "gc $*" >> "$CALLS_LOG"
if [[ "$1" == "bd" ]]; then
  shift
  if   [[ "$*" == *"--closed-after"*  ]]; then cat "$STUB_FIXTURES/bd-closed.json";  exit 0
  elif [[ "$*" == *"--created-after"* ]]; then cat "$STUB_FIXTURES/bd-created.json"; exit 0
  elif [[ "$*" == *"--status=open"*   ]]; then echo "[]";                            exit 0
  fi
fi
EOF
  chmod +x "$TEST_DIR/bin/gc"

  load_script
  local out="$TEST_DIR/intermediate.md"
  gather_beads 1747800000 "$out"

  grep -q "## Bead activity" "$out"
  grep -q "Closed: 3" "$out"
  grep -q "Created: 2" "$out"
  grep -qE "order:dolt-health|PR #123" "$out"
}

@test "gather_beads: tolerates bd failure with a marker line" {
  cat > "$TEST_DIR/bin/gc" <<'EOF'
#!/usr/bin/env bash
echo "gc $*" >> "$CALLS_LOG"
exit 1
EOF
  chmod +x "$TEST_DIR/bin/gc"

  load_script
  local out="$TEST_DIR/intermediate.md"
  gather_beads 1747800000 "$out"

  grep -q "## Bead activity" "$out"
  grep -qE "section failed|error" "$out"
}

@test "gather_prs: walks rigs with github remotes, aggregates state" {
  cat > "$TEST_DIR/bin/gc" <<'EOF'
#!/usr/bin/env bash
echo "gc $*" >> "$CALLS_LOG"
if [[ "$1" == "rig" && "$2" == "list" ]]; then cat "$STUB_FIXTURES/gc-rig-list.json"; exit 0; fi
EOF
  chmod +x "$TEST_DIR/bin/gc"

  cat > "$TEST_DIR/bin/git" <<'EOF'
#!/usr/bin/env bash
echo "git $*" >> "$CALLS_LOG"
# Pretend only the second rig (zr-monorepo) has a github origin.
if [[ "$*" == *"/x/zr-mono"*"remote.origin.url"* ]]; then
  echo "git@github.com:owner/repo.git"
  exit 0
fi
exit 1
EOF
  chmod +x "$TEST_DIR/bin/git"

  cat > "$TEST_DIR/bin/gh" <<'EOF'
#!/usr/bin/env bash
echo "gh $*" >> "$CALLS_LOG"
cat "$STUB_FIXTURES/gh-prs.json"
EOF
  chmod +x "$TEST_DIR/bin/gh"

  load_script
  local out="$TEST_DIR/intermediate.md"
  gather_prs 1747800000 "$out"

  grep -q "## PR activity" "$out"
  grep -q "Merged: 1" "$out"
  grep -q "Open: 2" "$out"
  grep -q "owner/repo" "$out"
  # The non-github rig must not produce a gh call.
  ! grep -q "gh pr list --repo /x/gc" "$CALLS_LOG"
}

@test "gather_prs: no github remotes → still emits the header and a note" {
  cat > "$TEST_DIR/bin/gc" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == "rig" && "$2" == "list" ]]; then cat "$STUB_FIXTURES/gc-rig-list.json"; exit 0; fi
EOF
  chmod +x "$TEST_DIR/bin/gc"
  cat > "$TEST_DIR/bin/git" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
  chmod +x "$TEST_DIR/bin/git"

  load_script
  local out="$TEST_DIR/intermediate.md"
  gather_prs 1747800000 "$out"

  grep -q "## PR activity" "$out"
  grep -qE "No rigs with GitHub remotes" "$out"
}

@test "gather_agents: counts crashes/idle_kills/quarantines by agent_name" {
  cat > "$TEST_DIR/bin/gc" <<'EOF'
#!/usr/bin/env bash
echo "gc $*" >> "$CALLS_LOG"
if [[ "$1" == "events" ]]; then cat "$STUB_FIXTURES/gc-events-session.jsonl"; exit 0; fi
if [[ "$1" == "agent" && "$2" == "list" ]]; then
  echo "NAME              STATE"
  echo "zr.pr-reviewer    awake"
  echo "zr.pr-triage      sleeping"
  exit 0
fi
EOF
  chmod +x "$TEST_DIR/bin/gc"

  load_script
  local out="$TEST_DIR/intermediate.md"
  gather_agents 1747800000 "$out"

  grep -q "## Agent health" "$out"
  grep -q "Crashes: 2" "$out"
  grep -q "Idle kills: 1" "$out"
  grep -q "Quarantines: 1" "$out"
  grep -q "zr.pr-reviewer" "$out"
}

@test "gather_agents: empty event stream emits zero counts" {
  cat > "$TEST_DIR/bin/gc" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == "events" ]]; then exit 0; fi
if [[ "$1" == "agent" && "$2" == "list" ]]; then echo "NAME STATE"; exit 0; fi
EOF
  chmod +x "$TEST_DIR/bin/gc"

  load_script
  local out="$TEST_DIR/intermediate.md"
  gather_agents 1747800000 "$out"

  grep -q "Crashes: 0" "$out"
  grep -q "Idle kills: 0" "$out"
  grep -q "Quarantines: 0" "$out"
}

@test "gather_anomalies: detects overridden orders that fired" {
  cp "$STUB_FIXTURES/city.toml"  "$CITY_TOML"
  cp "$STUB_FIXTURES/trace.log"  "$TRACE_FILE"

  load_script
  local out="$TEST_DIR/intermediate.md"
  gather_anomalies 1747800000 "$out"

  grep -q "## Order" "$out"
  grep -q "mol-dog-jsonl" "$out"
  grep -qE "fired.*2 time|fired 2 time" "$out"   # HACK 12 regression count
}

@test "gather_anomalies: clean trace produces no anomalies" {
  cat > "$CITY_TOML" <<'EOF'
[[orders.overrides]]
name = "mol-dog-jsonl"
enabled = false
EOF
  # Trace with only allowed (non-overridden) orders firing.
  cat > "$TRACE_FILE" <<'EOF'
2026-05-20T02:00:00Z agent=control-dispatcher order.fired subject=hack-archive-and-compact
EOF

  load_script
  local out="$TEST_DIR/intermediate.md"
  gather_anomalies 1747800000 "$out"

  grep -q "## Order" "$out"
  grep -qE "No anomalies|none detected" "$out"
}

@test "gather_anomalies: handles back-to-back overrides without blank lines" {
  cat > "$CITY_TOML" <<'EOF'
[[orders.overrides]]
name = "alpha"
enabled = false
[[orders.overrides]]
name = "beta"
enabled = false
EOF
  cat > "$TRACE_FILE" <<'EOF'
2026-05-20T01:00:00Z agent=control-dispatcher order.fired subject=alpha
2026-05-20T02:00:00Z agent=control-dispatcher order.fired subject=beta
EOF

  load_script
  local out="$TEST_DIR/intermediate.md"
  gather_anomalies 1747800000 "$out"

  grep -q "alpha" "$out"
  grep -q "beta"  "$out"
}

@test "polish_with_llm: returns claude stdout on success" {
  cat > "$TEST_DIR/bin/claude" <<'EOF'
#!/usr/bin/env bash
echo "claude $*" >> "$CALLS_LOG"
cat   # echo stdin as if Haiku returned it
echo
echo "**Polished narrative.**"
EOF
  chmod +x "$TEST_DIR/bin/claude"

  load_script
  local intermediate="$TEST_DIR/in.md"
  echo "raw bullets" > "$intermediate"

  local got; got="$(polish_with_llm "$intermediate")"
  echo "$got" | grep -q "raw bullets"
  echo "$got" | grep -q "Polished narrative"
}

@test "polish_with_llm: claude missing → returns intermediate verbatim" {
  rm -f "$TEST_DIR/bin/claude"
  # Tighten PATH so the host's system claude (homebrew/nix profile/npm) is NOT
  # discovered by `command -v claude`. Keep /bin and /usr/bin for `cat`,
  # which polish_with_llm needs for the fallback branch.
  PATH="$TEST_DIR/bin:/bin:/usr/bin"
  load_script
  local intermediate="$TEST_DIR/in.md"
  printf 'fallback contents\n' > "$intermediate"

  local got; got="$(polish_with_llm "$intermediate")"
  [ "$got" = "fallback contents" ]
}

@test "polish_with_llm: claude returns empty → returns intermediate verbatim" {
  cat > "$TEST_DIR/bin/claude" <<'EOF'
#!/usr/bin/env bash
exit 0   # success but no output
EOF
  chmod +x "$TEST_DIR/bin/claude"

  load_script
  local intermediate="$TEST_DIR/in.md"
  printf 'fallback contents\n' > "$intermediate"

  local got; got="$(polish_with_llm "$intermediate")"
  [ "$got" = "fallback contents" ]
}

@test "deliver: invokes gc mail send with subject + message; reports gc exit" {
  cat > "$TEST_DIR/bin/gc" <<'EOF'
#!/usr/bin/env bash
echo "gc $*" >> "$CALLS_LOG"
EOF
  chmod +x "$TEST_DIR/bin/gc"

  load_script
  run deliver "hello body"
  [ "$status" -eq 0 ]
  grep -q 'gc mail send operator' "$CALLS_LOG"
  grep -qE 'Daily summary [0-9]{4}-[0-9]{2}-[0-9]{2}' "$CALLS_LOG"
}

@test "deliver: propagates gc mail send failure" {
  cat > "$TEST_DIR/bin/gc" <<'EOF'
#!/usr/bin/env bash
exit 7
EOF
  chmod +x "$TEST_DIR/bin/gc"

  load_script
  run deliver "body"
  [ "$status" -ne 0 ]
}

@test "main: end-to-end with all stubs writes mail and advances cursor" {
  # Compose a single gc stub that handles all subcommands.
  cat > "$TEST_DIR/bin/gc" <<'EOF'
#!/usr/bin/env bash
echo "gc $*" >> "$CALLS_LOG"
case "$1" in
  bd)       case "$*" in
              *--closed-after*)  cat "$STUB_FIXTURES/bd-closed.json" ;;
              *--created-after*) cat "$STUB_FIXTURES/bd-created.json" ;;
              *--status=open*)   echo "[]" ;;
            esac ;;
  rig)      cat "$STUB_FIXTURES/gc-rig-list.json" ;;
  events)   cat "$STUB_FIXTURES/gc-events-session.jsonl" ;;
  agent)    echo "NAME STATE" ;;
  mail)     : ;;
esac
EOF
  chmod +x "$TEST_DIR/bin/gc"

  cat > "$TEST_DIR/bin/git" <<'EOF'
#!/usr/bin/env bash
exit 1   # no rig has a github remote — keeps test focused
EOF
  chmod +x "$TEST_DIR/bin/git"

  cat > "$TEST_DIR/bin/claude" <<'EOF'
#!/usr/bin/env bash
echo "## Polished summary"
echo
cat
EOF
  chmod +x "$TEST_DIR/bin/claude"

  cp "$STUB_FIXTURES/city.toml" "$CITY_TOML"
  cp "$STUB_FIXTURES/trace.log" "$TRACE_FILE"
  # Set cursor to two hours ago.
  echo $(( $(date +%s) - 7200 )) > "$CURSOR_FILE"

  run bash "$BATS_TEST_DIRNAME/../hack-daily-summary.sh"
  [ "$status" -eq 0 ]
  grep -q 'gc mail send operator' "$CALLS_LOG"
  # Cursor advanced past the original value.
  local cur; cur="$(cat "$CURSOR_FILE")"
  [ "$cur" -gt $(( $(date +%s) - 60 )) ]
}

@test "main: gc mail send failure does NOT advance cursor" {
  local original_ts=1700000000
  echo "$original_ts" > "$CURSOR_FILE"

  cat > "$TEST_DIR/bin/gc" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == "mail" ]]; then exit 7; fi
case "$1" in
  bd)     echo "[]" ;;
  rig)    cat "$STUB_FIXTURES/gc-rig-list.json" ;;
  events) : ;;
  agent)  echo "NAME STATE" ;;
esac
EOF
  chmod +x "$TEST_DIR/bin/gc"

  cat > "$TEST_DIR/bin/git" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
  chmod +x "$TEST_DIR/bin/git"

  cp "$STUB_FIXTURES/city.toml" "$CITY_TOML"
  cp "$STUB_FIXTURES/trace.log" "$TRACE_FILE"

  run bash "$BATS_TEST_DIRNAME/../hack-daily-summary.sh"
  [ "$status" -ne 0 ]
  [ "$(cat "$CURSOR_FILE")" = "$original_ts" ]
}
