#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Script-level tests for pg-repo-runner (bead pg2-zj2v7m, F1): the real script as
# a subprocess, against a real temporary workspace layout (a pn-workspace.toml and
# real git clones of bare local remotes under it), the real lease (live and dead
# processes), the real verdict store. The load average is injected through the
# documented PG_REPO_RUNNER_LOADAVG override; nothing here pushes, and every repo
# in the fixture config has push = false.

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  if [[ -z ${GFH_LIB:-} ]]; then
    echo "GFH_LIB is not set: run under the workspace .envrc (direnv) or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
    return 1
  fi
  # shellcheck disable=SC1090,SC1091
  source "$GFH_LIB"
  gfh_save_env SCRIPTS_DIR SCRIPT_UNDER_TEST TEST_SUPPORT GFH_LIB
  gfh_setup "pg-repo-runner"
  gfh_restore_env

  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    RUNNER=("$SCRIPT_UNDER_TEST")
  else
    RUNNER=(bash -euo pipefail "$SCRIPTS_DIR/pg-repo-runner.sh")
  fi

  TEST_DIR="$GFH_WORK"
  WS="$TEST_DIR/ws"
  mkdir -p "$WS" "$TEST_DIR/remotes" "$TEST_DIR/elsewhere"

  make_repo "$WS/nix-alpha" alpha
  make_repo "$WS/nix-beta" beta
  make_repo "$WS/nix-gamma" gamma
  make_repo "$TEST_DIR/elsewhere/nix-link-target" link
  ln -s "$TEST_DIR/elsewhere/nix-link-target" "$WS/nix-link"
  mkdir -p "$WS/nix-plain"
  ALPHA_SHA="$(git -C "$WS/nix-alpha" rev-parse HEAD)"

  # The workspace: every repo key the fixture knows. nix-ghost is deliberately
  # NOT here (it is in the runner's allow-list only).
  cat >"$WS/pn-workspace.toml" <<'EOF'
[workspace]
id = 'fixture'

[repos]
[repos.nix-alpha]
url = 'x'
branch = 'main'
[repos.nix-beta]
url = 'x'
branch = 'main'
[repos.nix-gamma]
url = 'x'
branch = 'main'
[repos.nix-link]
url = 'x'
branch = 'main'
[repos.nix-plain]
url = 'x'
branch = 'main'
EOF

  write_config "$WS"

  export XDG_STATE_HOME="$TEST_DIR/state"
  export PG_REPO_RUNNER_CONFIG="$TEST_DIR/config.toml"
  export PG_REPO_RUNNER_LOADAVG=0.50
  STATE="$XDG_STATE_HOME/pg-repo-runner"
  SLEEPER=""
}

teardown() {
  if [[ -n ${SLEEPER:-} ]]; then
    kill "$SLEEPER" 2>/dev/null || true
  fi
  gfh_teardown
}

# make_repo DIR NAME: a clone of a fresh bare remote, with one commit pushed.
make_repo() {
  local dir="$1" name="$2"
  gfh_init_bare "$TEST_DIR/remotes/$name.git"
  gfh_clone "$TEST_DIR/remotes/$name.git" "$dir" "pg-repo-runner"
  echo "$name" >"$dir/f"
  git -C "$dir" add f
  git -C "$dir" commit -q -m "seed $name"
  git -C "$dir" push -q origin main
}

# write_config ROOT [EXTRA-TOP-LEVEL-LINES]: the allow-list. verify and sync are
# on for most repos; push is false for EVERY repo, in every test.
write_config() {
  cat >"$TEST_DIR/config.toml" <<EOF
workspace_root = "$1"
kill_switch = "$TEST_DIR/config-kill"
${2:-}
[thresholds]
max_load = 11.0
nice_level = 10

[repos.nix-alpha]
verify = true
sync = true
push = false

[repos.nix-beta]
verify = false
sync = false
push = false

[repos.nix-link]
verify = true
sync = true
push = false

[repos.nix-plain]
verify = true
sync = true
push = false

[repos.nix-ghost]
verify = true
sync = true
push = false
EOF
}

# role ROLE [ITEM]: run a role for an item (default: nix-alpha at its tip).
role() {
  local item="${2-nix-alpha@$ALPHA_SHA}"
  if [[ -n $item ]]; then
    PG_ROUTER_ITEM_ID="$item" run "${RUNNER[@]}" "$1"
  else
    run env -u PG_ROUTER_ITEM_ID "${RUNNER[@]}" "$1"
  fi
}

# The kernel admitted the run: it reached the role body. The sync role runs for
# real (a clean, in-sync clone: exit 0); build is still a stub (exit 2).
assert_admitted() {
  grep -q ' admitted$' "$STATE/run.log"
  if [[ $output == *"is not implemented yet"* ]]; then
    [ "$status" -eq 2 ]
  else
    [ "$status" -eq 0 ]
    grep -q ' sync: nix-alpha in sync$' "$STATE/run.log"
  fi
}

# The kernel stopped before the role body.
assert_not_admitted() {
  if [[ -f $STATE/run.log ]]; then
    ! grep -q ' admitted$' "$STATE/run.log"
  fi
}

assert_no_lease() {
  [ ! -e "$STATE/lease" ] && [ ! -L "$STATE/lease" ]
}

start_sleeper() {
  sleep 300 >/dev/null 2>&1 3>&- &
  SLEEPER=$!
  SLEEPER_OWNER="$SLEEPER:$(ps -o lstart= -p "$SLEEPER" | tr -s ' ')"
}

write_verdict() { # REPO SHA KIND RESULT
  (
    # shellcheck disable=SC1091
    source "$SCRIPTS_DIR/pg-repo-runner.bash"
    rr_init_state
    rr_verdict_write "$1" "$2" "$3" "$4" locked 2026-10-28T00:00:00Z 2026-10-28T00:05:00Z 0 "$RR_RUN_LOG"
  )
}

# ---- usage ----------------------------------------------------------------

@test "--help prints usage on stdout and exits 0" {
  run "${RUNNER[@]}" --help
  [ "$status" -eq 0 ]
  [[ $output == *"Usage: pg-repo-runner COMMAND"* ]]
  [[ $output == *"PG_REPO_RUNNER_LOADAVG"* ]]
}

@test "no command, an unknown command and an unknown option exit 2" {
  run "${RUNNER[@]}"
  [ "$status" -eq 2 ]
  run "${RUNNER[@]}" frobnicate
  [ "$status" -eq 2 ]
  run "${RUNNER[@]}" --frobnicate
  [ "$status" -eq 2 ]
  run "${RUNNER[@]}" verify extra
  [ "$status" -eq 2 ]
}

@test "workspace-tips is a stub that exits 2, not implemented" {
  run "${RUNNER[@]}" workspace-tips
  [ "$status" -eq 2 ]
  [[ $output == *"not implemented"* ]]
}

# ---- resolve: the allow-list ----------------------------------------------

@test "a repo in pn-workspace.toml but NOT in the allow-list is refused" {
  role sync "nix-gamma@$ALPHA_SHA"
  [ "$status" -eq 2 ]
  [[ $output == *"not in the allow-list"* ]]
  assert_not_admitted
  assert_no_lease
}

@test "a repo in the allow-list but NOT a pn-workspace.toml repo key is refused" {
  role sync "nix-ghost@$ALPHA_SHA"
  [ "$status" -eq 2 ]
  [[ $output == *"not a repo key of pn-workspace.toml"* ]]
  assert_not_admitted
}

@test "a missing or malformed PG_ROUTER_ITEM_ID is a usage error" {
  role sync ""
  [ "$status" -eq 2 ]
  [[ $output == *"PG_ROUTER_ITEM_ID is not set"* ]]
  local bad
  for bad in "../nix-alpha" "nix-alpha/../nix-beta" "/etc" "nix-alpha@" "nix-alpha@not-hex"; do
    role sync "$bad"
    [ "$status" -eq 2 ] || {
      echo "not refused: $bad"
      return 1
    }
    [[ $output == *"malformed PG_ROUTER_ITEM_ID"* ]]
  done
}

@test "a repo key without a sha resolves too" {
  role sync nix-alpha
  assert_admitted
}

# ---- path check -----------------------------------------------------------

@test "a key whose directory is a symlink out of the workspace is refused" {
  role sync "nix-link@$ALPHA_SHA"
  [ "$status" -eq 2 ]
  [[ $output == *"path check failed"* ]]
  assert_not_admitted
}

@test "a key whose directory is not a git repository is refused" {
  role sync "nix-plain@$ALPHA_SHA"
  [ "$status" -eq 2 ]
  [[ $output == *"path check failed"* ]]
}

@test "a key whose directory does not exist is refused" {
  rm -rf "$WS/nix-alpha"
  role sync
  [ "$status" -eq 2 ]
  [[ $output == *"path check failed"* ]]
}

@test "a key directory that is not itself the top level of its repository is refused" {
  # the workspace root is inside a repository, so git finds a top level for
  # nix-plain, but it is the root's, not the key's own path.
  gfh_init_repo "$WS" "pg-repo-runner"
  role sync "nix-plain@$ALPHA_SHA"
  [ "$status" -eq 2 ]
  [[ $output == *"path check failed"* ]]
}

@test "a workspace root that is itself a symlink is fine: the real paths must agree" {
  ln -s "$WS" "$TEST_DIR/ws-link"
  write_config "$TEST_DIR/ws-link"
  role sync
  assert_admitted
}

# ---- per-repo role flags --------------------------------------------------

@test "a role that is off for the repo is skipped with exit 0, before any admission" {
  role verify "nix-beta@$ALPHA_SHA"
  [ "$status" -eq 0 ]
  grep -q 'skipped: verify is not enabled for' "$STATE/run.log"
  assert_not_admitted
  assert_no_lease
  role sync "nix-beta@$ALPHA_SHA"
  [ "$status" -eq 0 ]
}

# ---- kill switch ----------------------------------------------------------

@test "the state-dir push.disabled file yields exit 9" {
  mkdir -p "$STATE"
  touch "$STATE/push.disabled"
  role sync
  [ "$status" -eq 9 ]
  [[ $output == *"kill switch present"* ]]
  assert_not_admitted
  assert_no_lease
}

@test "the config's kill file yields exit 9, and removing it admits again" {
  touch "$TEST_DIR/config-kill"
  role sync
  [ "$status" -eq 9 ]
  assert_not_admitted
  rm "$TEST_DIR/config-kill"
  role sync
  assert_admitted
}

# ---- load check -----------------------------------------------------------

@test "a load at or above the threshold yields exit 9" {
  PG_REPO_RUNNER_LOADAVG=36.16 role sync
  [ "$status" -eq 9 ]
  [[ $output == *"not below"* ]]
  assert_not_admitted
  assert_no_lease
  PG_REPO_RUNNER_LOADAVG=11.00 role sync
  [ "$status" -eq 9 ]
}

@test "a load just below the threshold is admitted" {
  PG_REPO_RUNNER_LOADAVG=10.99 role sync
  assert_admitted
}

@test "without a configured threshold the core count is the threshold" {
  write_config "$WS"
  sed -i.bak '/^max_load/d' "$TEST_DIR/config.toml"
  PG_REPO_RUNNER_NCPU=4 PG_REPO_RUNNER_LOADAVG=4.00 role sync
  [ "$status" -eq 9 ]
  PG_REPO_RUNNER_NCPU=4 PG_REPO_RUNNER_LOADAVG=3.99 role sync
  assert_admitted
}

@test "the environment threshold overrides the config" {
  PG_REPO_RUNNER_MAX_LOAD=0.25 role sync
  [ "$status" -eq 9 ]
}

@test "an unreadable load average is a malfunction, exit 1" {
  PG_REPO_RUNNER_LOADAVG=lots role sync
  [ "$status" -eq 1 ]
  [[ $output == *"cannot read the 1-minute load average"* ]]
}

# ---- single-flight lease --------------------------------------------------

@test "a run while another process holds the lease yields exit 9 and leaves the lease alone" {
  start_sleeper
  mkdir -p "$STATE"
  ln -s "$SLEEPER_OWNER" "$STATE/lease"
  role sync
  [ "$status" -eq 9 ]
  [[ $output == *"lease is held"* ]]
  assert_not_admitted
  [ "$(readlink "$STATE/lease")" = "$SLEEPER_OWNER" ]
  # every role shares the one lease
  role sync
  [ "$status" -eq 9 ]
  role build ""
  [ "$status" -eq 9 ]
}

@test "a lease whose owner is dead is reclaimed, the run is admitted and the lease is released" {
  sleep 0.2 &
  local dead=$!
  wait "$dead"
  mkdir -p "$STATE"
  ln -s "$dead:Thu Jan 1 00:00:00 1970" "$STATE/lease"
  role sync
  assert_admitted
  assert_no_lease
  [ -z "$(find "$STATE" -maxdepth 1 -name '.reclaim.*')" ]
}

@test "a lease whose pid was recycled by another process is reclaimed" {
  start_sleeper
  mkdir -p "$STATE"
  ln -s "$SLEEPER:Thu Jan 1 00:00:00 1970" "$STATE/lease"
  role sync
  assert_admitted
  assert_no_lease
}

@test "the lease is released after an admitted run, so the next run is admitted too" {
  role sync
  assert_admitted
  assert_no_lease
  role sync
  assert_admitted
}

# ---- the role stubs and the run log ---------------------------------------

@test "sync runs for real and build reaches its stub once admitted" {
  role sync
  assert_admitted
  [ "$status" -eq 0 ]
  # build is workspace-wide: no repo item is needed
  role build ""
  [ "$status" -eq 2 ]
  [[ $output == *"role 'build' is not implemented yet"* ]]
}

@test "every run is explained in the run log under the state directory" {
  role sync
  PG_REPO_RUNNER_LOADAVG=99 role sync
  [ -f "$STATE/run.log" ]
  grep -q "role=sync item=nix-alpha@$ALPHA_SHA admitted" "$STATE/run.log"
  grep -q 'exit=0' "$STATE/run.log"
  grep -q 'exit=9 busy: 1-minute load 99 is not below ' "$STATE/run.log"
}

# ---- malfunctions ---------------------------------------------------------

@test "a missing config is a malfunction, exit 1" {
  rm "$TEST_DIR/config.toml"
  role sync
  [ "$status" -eq 1 ]
  [[ $output == *"config is not readable"* ]]
}

@test "a config that is not TOML is a malfunction, exit 1" {
  echo 'this = is = not toml' >"$TEST_DIR/config.toml"
  role sync
  [ "$status" -eq 1 ]
}

@test "a config without a usable workspace_root is a malfunction, exit 1" {
  write_config "$TEST_DIR/no-such-dir"
  role sync
  [ "$status" -eq 1 ]
  [[ $output == *"workspace_root"* ]]
}

# ---- verdicts and status --------------------------------------------------

@test "status on an empty store says so, reads no config and exits 0" {
  rm "$TEST_DIR/config.toml"
  run "${RUNNER[@]}" status
  [ "$status" -eq 0 ]
  [[ $output == *"state dir: $STATE"* ]]
  [[ $output == *"kill switch: absent"* ]]
  [[ $output == *"lease: free"* ]]
  [[ $output == *"verdicts: none"* ]]
  run "${RUNNER[@]}" status --json
  [ "$output" = '[]' ]
}

@test "status prints a verdict that was written atomically" {
  write_verdict nix-alpha "$ALPHA_SHA" check green
  # the write left exactly the verdict: no temp file anywhere in the store
  [ "$(find "$STATE/verdicts" -type f | wc -l | tr -d ' ')" = 1 ]
  [ -f "$STATE/verdicts/nix-alpha/$ALPHA_SHA.check.json" ]
  run "${RUNNER[@]}" status
  [ "$status" -eq 0 ]
  [[ $output == *"verdicts:"* ]]
  [[ $output == *"nix-alpha"*"${ALPHA_SHA:0:12}"*"check"*"green"*"locked"*"2026-10-28T00:05:00Z"* ]]
}

@test "status --json prints the full records and --repo filters" {
  write_verdict nix-alpha "$ALPHA_SHA" check green
  write_verdict nix-beta abcdef1 build red
  run "${RUNNER[@]}" status --json
  [ "$status" -eq 0 ]
  [ "$(jq length <<<"$output")" = 2 ]
  run "${RUNNER[@]}" status --json --repo nix-beta
  [ "$(jq -r '.[0] | [.repo, .sha, .kind, .result, .inputs, .exit] | join(" ")' <<<"$output")" = "nix-beta abcdef1 build red locked 0" ]
  [ "$(jq length <<<"$output")" = 1 ]
  run "${RUNNER[@]}" status --repo "../x"
  [ "$status" -eq 2 ]
  run "${RUNNER[@]}" status --nope
  [ "$status" -eq 2 ]
}

@test "status shows a held lease, a stale lease and a kill file" {
  start_sleeper
  mkdir -p "$STATE"
  ln -s "$SLEEPER_OWNER" "$STATE/lease"
  touch "$STATE/push.disabled"
  run "${RUNNER[@]}" status
  [[ $output == *"lease: held by $SLEEPER_OWNER"* ]]
  [[ $output == *"kill switch: $STATE/push.disabled"* ]]
  rm "$STATE/lease"
  ln -s "$SLEEPER:Thu Jan 1 00:00:00 1970" "$STATE/lease"
  run "${RUNNER[@]}" status
  [[ $output == *"lease: stale ("* ]]
}

# ---- no repo is enabled for push -----------------------------------------

@test "the fixture allow-list enables push for no repo, and a sync pass never moves a remote ref" {
  [ "$(tq -o json -f "$TEST_DIR/config.toml" . | jq '[.repos[].push] | map(select(. == true)) | length')" = 0 ]
  local before
  before="$(git -C "$TEST_DIR/remotes/alpha.git" rev-parse main)"
  role sync
  assert_admitted
  [ "$(git -C "$TEST_DIR/remotes/alpha.git" rev-parse main)" = "$before" ]
  [ "$(git -C "$WS/nix-alpha" rev-parse HEAD)" = "$ALPHA_SHA" ]
  [ -z "$(git -C "$WS/nix-alpha" status --porcelain)" ]
}
