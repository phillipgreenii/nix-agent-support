#!/usr/bin/env bats
# bats file_tags=type:unit
# Logic tests for scripts/validate-nix-wrapper-adoption.sh (bead pg2-kqrrs.11).
#
# The harness itself is live and non-hermetic (real `claude -p`, real nix, API
# spend), so it is never run here. These tests drive its counting/verdict logic
# against a FAKE `claude` stub on PATH: no real claude, no network, no nix.
#
# The stub emits stream-json shaped like `claude -p --output-format stream-json
# --verbose`. Run N (1-based, tracked in a counter file) is "adopted" when N is
# listed in STUB_ADOPT_RUNS; "prose" runs (STUB_PROSE_RUNS) only MENTION the
# wrapper in assistant text, which must NOT count.

SCRIPT="$BATS_TEST_DIRNAME/../scripts/validate-nix-wrapper-adoption.sh"

setup() {
  TEST_DIR="$(mktemp -d)"
  export HOME="$TEST_DIR/home"
  mkdir -p "$HOME" "$TEST_DIR/bin" "$TEST_DIR/repo"
  export STUB_DIR="$TEST_DIR/stub"
  mkdir -p "$STUB_DIR"
  : >"$STUB_DIR/count"

  cat >"$TEST_DIR/bin/claude" <<'STUB'
#!/usr/bin/env bash
dir="$STUB_DIR"
n=$(($(cat "$dir/count" 2>/dev/null | wc -l) + 1))
echo "$n" >>"$dir/count"
printf '%s\n' "$*" >"$dir/args.$n"
pwd >"$dir/cwd.$n"
echo '{"type":"system","subtype":"init"}'
echo 'not json at all'
if [[ " ${STUB_ADOPT_RUNS:-} " == *" $n "* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"pg-nix-log-wrapped nix flake check"}}]}}'
elif [[ " ${STUB_PROSE_RUNS:-} " == *" $n "* ]]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"I should use pg-nix-log-wrapped but will not."},{"type":"tool_use","name":"Bash","input":{"command":"nix flake check"}}]}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"nix flake check"}}]}}'
fi
echo '{"type":"result","subtype":"success"}'
[[ " ${STUB_FAIL_RUNS:-} " == *" $n "* ]] && exit 3
exit 0
STUB
  chmod +x "$TEST_DIR/bin/claude"
  export PATH="$TEST_DIR/bin:$PATH"
}

teardown() {
  rm -rf "$TEST_DIR"
}

@test "passes when 5 of 5 runs call the wrapper" {
  STUB_ADOPT_RUNS="1 2 3 4 5" run "$SCRIPT" --repo "$TEST_DIR/repo" --out-dir "$TEST_DIR/out"
  [ "$status" -eq 0 ]
  [[ "$output" == *"adopted in 5 of 5"* ]]
  [[ "$output" == *PASS* ]]
}

@test "passes at the threshold: 4 of 5" {
  STUB_ADOPT_RUNS="1 2 4 5" run "$SCRIPT" --repo "$TEST_DIR/repo" --out-dir "$TEST_DIR/out"
  [ "$status" -eq 0 ]
  [[ "$output" == *"adopted in 4 of 5"* ]]
}

@test "fails below the threshold: 3 of 5" {
  STUB_ADOPT_RUNS="1 2 3" run "$SCRIPT" --repo "$TEST_DIR/repo" --out-dir "$TEST_DIR/out"
  [ "$status" -eq 1 ]
  [[ "$output" == *"only 3 of 5"* ]]
}

@test "prose-only mention of the wrapper does not count as adoption" {
  STUB_ADOPT_RUNS="1 2 3" STUB_PROSE_RUNS="4 5" run "$SCRIPT" --repo "$TEST_DIR/repo" --out-dir "$TEST_DIR/out"
  [ "$status" -eq 1 ]
  [[ "$output" == *"run 4: not adopted"* ]]
  # the looser whole-transcript grep still sees the prose mention
  [[ "$output" == *"loose mentions=1"* ]]
}

@test "a claude exit failure still judges the partial transcript" {
  STUB_ADOPT_RUNS="1 2 3 4 5" STUB_FAIL_RUNS="5" run "$SCRIPT" --repo "$TEST_DIR/repo" --out-dir "$TEST_DIR/out"
  [ "$status" -eq 0 ]
  [[ "$output" == *"run 5: claude exited non-zero"* ]]
}

@test "uses the fixed prompt, stream-json, and runs inside the repo" {
  STUB_ADOPT_RUNS="1 2 3 4 5" run "$SCRIPT" --repo "$TEST_DIR/repo" --out-dir "$TEST_DIR/out"
  [ "$status" -eq 0 ]
  [ "$(wc -l <"$STUB_DIR/count" | tr -d ' ')" -eq 5 ]
  grep -qF -- "-p run \`nix flake check\` in $TEST_DIR/repo" "$STUB_DIR/args.1"
  grep -qF -- "--output-format stream-json --verbose" "$STUB_DIR/args.1"
  [ "$(cat "$STUB_DIR/cwd.1")" = "$(cd "$TEST_DIR/repo" && pwd -P)" ] ||
    [ "$(cat "$STUB_DIR/cwd.1")" = "$TEST_DIR/repo" ]
  [ -s "$TEST_DIR/out/run-5.jsonl" ]
}

@test "--runs and --min-pass are honoured" {
  STUB_ADOPT_RUNS="1 2" run "$SCRIPT" --repo "$TEST_DIR/repo" --out-dir "$TEST_DIR/out" --runs 2 --min-pass 2
  [ "$status" -eq 0 ]
  [ "$(wc -l <"$STUB_DIR/count" | tr -d ' ')" -eq 2 ]
}

@test "default args use a narrow allowlist and never bypass permissions" {
  STUB_ADOPT_RUNS="1" run "$SCRIPT" --repo "$TEST_DIR/repo" --out-dir "$TEST_DIR/out" --runs 1 --min-pass 1
  [ "$status" -eq 0 ]
  grep -qF -- "--permission-mode dontAsk" "$STUB_DIR/args.1"
  grep -qF -- "--allowedTools Bash(pg-nix-log-wrapped *),Bash(nix *)" "$STUB_DIR/args.1"
  if grep -qF -- "bypassPermissions" "$STUB_DIR/args.1"; then false; fi
  if grep -qF -- "dangerously-skip-permissions" "$STUB_DIR/args.1"; then false; fi
}

@test "PG_HARNESS_CLAUDE_ARGS replaces the default permission args" {
  PG_HARNESS_CLAUDE_ARGS="--model sonnet" STUB_ADOPT_RUNS="1" run "$SCRIPT" --repo "$TEST_DIR/repo" --out-dir "$TEST_DIR/out" --runs 1 --min-pass 1
  [ "$status" -eq 0 ]
  grep -qF -- "--model sonnet" "$STUB_DIR/args.1"
  if grep -qF -- "bypassPermissions" "$STUB_DIR/args.1"; then false; fi
}

@test "usage errors exit 2 and never call claude" {
  run "$SCRIPT" --runs 0
  [ "$status" -eq 2 ]
  run "$SCRIPT" --runs 3 --min-pass 4
  [ "$status" -eq 2 ]
  run "$SCRIPT" --bogus
  [ "$status" -eq 2 ]
  run "$SCRIPT" --repo "$TEST_DIR/nonexistent"
  [ "$status" -eq 2 ]
  [ ! -s "$STUB_DIR/count" ]
}

@test "missing claude binary is a prerequisite failure, not a usage error" {
  CLAUDE_BIN="no-such-claude-binary" run "$SCRIPT" --repo "$TEST_DIR/repo"
  [ "$status" -eq 1 ]
  [[ "$output" == *"not found on PATH"* ]]
}
