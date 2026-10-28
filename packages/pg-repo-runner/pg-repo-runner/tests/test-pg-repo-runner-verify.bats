#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Runner B of the async-runners design (bead pg2-j0itft, F2): the `tips` source
# and the `verify` role of pg-repo-runner. The real script is run as a subprocess
# against a real temporary workspace (a pn-workspace.toml and real git clones of
# bare local remotes under it) and the real verdict store.
#
# Two tiers of nix. The classification tests (green, red, superseded, signal
# exit, duplicate, duration) run the script with PG_REPO_RUNNER_NIX_BIN naming a
# small recorder script that stands in for the nix binary, because what they pin
# is what the RUNNER does with nix's exit status and argv, and a signal exit
# cannot be produced on demand by nix itself. The "real nix" tests at the end run
# `nix flake check` for real on a tiny fixture flake (real git, real snapshot
# URL); they skip with a reason where no usable nix is on PATH (inside a nix build
# sandbox). Nothing here pushes, and every repo in the fixture config has
# push = false.

bats_require_minimum_version 1.5.0

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
  make_repo "$WS/nix-ghost" ghost
  make_repo "$TEST_DIR/elsewhere/nix-link-target" link
  ln -s "$TEST_DIR/elsewhere/nix-link-target" "$WS/nix-link"
  ALPHA_SHA="$(git -C "$WS/nix-alpha" rev-parse HEAD)"

  # nix-ghost is a clone on disk but deliberately NOT a pn-workspace.toml key.
  write_workspace nix-alpha nix-beta nix-link
  write_config "$WS"

  export XDG_STATE_HOME="$TEST_DIR/state"
  export PG_REPO_RUNNER_CONFIG="$TEST_DIR/config.toml"
  export PG_REPO_RUNNER_LOADAVG=0.50
  STATE="$XDG_STATE_HOME/pg-repo-runner"
  SLEEPER=""
  make_nix_shim
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

# commit_in DIR: a new local commit (never pushed).
commit_in() {
  echo "$RANDOM$RANDOM" >>"$1/f"
  git -C "$1" add f
  git -C "$1" commit -q -m "change"
}

# write_workspace KEY...: pn-workspace.toml with these repo keys, branch main.
write_workspace() {
  local k
  {
    printf "[workspace]\nid = 'fixture'\n\n[repos]\n"
    for k in "$@"; do
      printf "[repos.%s]\nurl = 'x'\nbranch = 'main'\n" "$k"
    done
  } >"$WS/pn-workspace.toml"
}

# write_config ROOT: verify is on for alpha, link, ghost and flk; off for beta.
# push is false for EVERY repo, in every test.
write_config() {
  cat >"$TEST_DIR/config.toml" <<EOF
workspace_root = "$1"
kill_switch = "$TEST_DIR/config-kill"

[thresholds]
max_load = 11.0
nice_level = 10

[repos.nix-alpha]
verify = true
sync = false
push = false

[repos.nix-beta]
verify = false
sync = false
push = false

[repos.nix-link]
verify = true
sync = false
push = false

[repos.nix-ghost]
verify = true
sync = false
push = false

[repos.nix-flk]
verify = true
sync = false
push = false
EOF
}

# make_nix_shim: the recorder that stands in for nix in the classification tests.
# It appends its argv (one line per run) to $TEST_DIR/nix.argv, sleeps
# $SHIM_SLEEP seconds, then ends as $SHIM_MODE says: "exit" (with $SHIM_EXIT, the
# default 0) or "kill" (SIGKILL on itself, so its parent sees 137).
make_nix_shim() {
  cat >"$TEST_DIR/nix-shim" <<EOF
#!/usr/bin/env bash
echo "\$*" >>"$TEST_DIR/nix.argv"
[[ -z \${SHIM_SLEEP:-} ]] || sleep "\$SHIM_SLEEP"
echo "shim output line"
if [[ \${SHIM_MODE:-exit} == kill ]]; then
  kill -KILL \$\$
fi
exit "\${SHIM_EXIT:-0}"
EOF
  chmod +x "$TEST_DIR/nix-shim"
  export PG_REPO_RUNNER_NIX_BIN="$TEST_DIR/nix-shim"
}

nix_runs() {
  if [[ -f $TEST_DIR/nix.argv ]]; then wc -l <"$TEST_DIR/nix.argv" | tr -d ' '; else echo 0; fi
}

# verify [ITEM]: run the verify role (default: nix-alpha at its tip).
verify() {
  local item="${1-nix-alpha@$ALPHA_SHA}"
  PG_ROUTER_ITEM_ID="$item" run "${RUNNER[@]}" verify
}

verdict_file() { # REPO SHA
  printf '%s/verdicts/%s/%s.check.json' "$STATE" "$1" "$2"
}

assert_no_lease() {
  [ ! -e "$STATE/lease" ] && [ ! -L "$STATE/lease" ]
}

# hold_lease: a live process that is not this test owns the lease.
hold_lease() {
  sleep 300 >/dev/null 2>&1 3>&- &
  SLEEPER=$!
  mkdir -p "$STATE"
  ln -s "$SLEEPER:$(ps -o lstart= -p "$SLEEPER" | tr -s ' ')" "$STATE/lease"
}

# ---- the check verdicts ----------------------------------------------------

@test "a passing check is a green verdict, run on an immutable snapshot of the tip" {
  verify
  [ "$status" -eq 0 ]
  local f
  f="$(verdict_file nix-alpha "$ALPHA_SHA")"
  [ -f "$f" ]
  [ "$(jq -r .result "$f")" = green ]
  [ "$(jq -r .kind "$f")" = check ]
  [ "$(jq -r .inputs "$f")" = locked ]
  [ "$(jq -r .exit "$f")" = 0 ]
  [ "$(jq -r .repo "$f")" = nix-alpha ]
  [ "$(jq -r .sha "$f")" = "$ALPHA_SHA" ]
  [ "$(nix_runs)" -eq 1 ]
  # the exact command: flake check, no lock write, the commit as a git+file snapshot
  local real
  real="$(realpath "$WS")"
  [ "$(cat "$TEST_DIR/nix.argv")" = "flake check --no-write-lock-file git+file://$real/nix-alpha?rev=$ALPHA_SHA" ]
  # its output is kept where the verdict says
  [ -s "$(jq -r .logPath "$f")" ]
  grep -q 'shim output line' "$(jq -r .logPath "$f")"
  assert_no_lease
}

@test "a failing check is a red verdict, the run still exits 0, and a red is not retried" {
  local code f
  for code in 1 2 100; do
    rm -rf "$STATE/verdicts" "$TEST_DIR/nix.argv"
    SHIM_EXIT=$code verify
    [ "$status" -eq 0 ] || {
      echo "exit $code: run exited $status: $output"
      return 1
    }
    f="$(verdict_file nix-alpha "$ALPHA_SHA")"
    [ "$(jq -r .result "$f")" = red ]
    [ "$(jq -r .exit "$f")" = "$code" ]
    [ "$(nix_runs)" -eq 1 ]
  done
  # the same event again: the red stays and nix is not run a second time
  verify
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 1 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = red ]
}

@test "a signal exit is a malfunction, not a red: exit 1, no verdict, and the next dispatch runs again" {
  SHIM_MODE="kill" verify
  [ "$status" -eq 1 ]
  [ ! -e "$(verdict_file nix-alpha "$ALPHA_SHA")" ]
  [[ $output == *"signal 9"* ]]
  grep -q 'exit=1' "$STATE/run.log"
  assert_no_lease
  # an explicit 128+n exit is the same malfunction
  rm -f "$TEST_DIR/nix.argv"
  SHIM_EXIT=143 verify
  [ "$status" -eq 1 ]
  [ ! -e "$(verdict_file nix-alpha "$ALPHA_SHA")" ]
  # pg-router's retry: the item is dispatched again and now the check can finish
  verify
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = green ]
  [ "$(nix_runs)" -eq 2 ]
}

@test "the signal classifier is exactly 129 to 192: 128 and 193 are reds" {
  SHIM_EXIT=128 verify
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = red ]
  rm -rf "$STATE/verdicts"
  SHIM_EXIT=129 verify
  [ "$status" -eq 1 ]
  rm -rf "$STATE/verdicts"
  SHIM_EXIT=192 verify
  [ "$status" -eq 1 ]
  rm -rf "$STATE/verdicts"
  SHIM_EXIT=193 verify
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = red ]
}

@test "a tip that moved before the run is a superseded verdict and nix is never run" {
  commit_in "$WS/nix-alpha"
  local new f
  new="$(git -C "$WS/nix-alpha" rev-parse HEAD)"
  [ "$new" != "$ALPHA_SHA" ]
  verify "nix-alpha@$ALPHA_SHA"
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 0 ]
  f="$(verdict_file nix-alpha "$ALPHA_SHA")"
  [ "$(jq -r .result "$f")" = superseded ]
  [ "$(jq -r .kind "$f")" = check ]
  grep -q 'superseded' "$STATE/run.log"
  # the same stale event again is absorbed by the stored verdict
  verify "nix-alpha@$ALPHA_SHA"
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 0 ]
  # the newer tip has its own event and is checked normally
  verify "nix-alpha@$new"
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 1 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$new")")" = green ]
}

@test "a duplicate event whose verdict is stored does not run nix, even under load or a held lease" {
  verify
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 1 ]
  local before
  before="$(cat "$(verdict_file nix-alpha "$ALPHA_SHA")")"
  verify
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 1 ]
  # the stored verdict ends the run before the load check: no "busy" answer
  PG_REPO_RUNNER_LOADAVG=99 verify
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 1 ]
  # ... and before the lease
  hold_lease
  verify
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 1 ]
  [ "$(cat "$(verdict_file nix-alpha "$ALPHA_SHA")")" = "$before" ]
  grep -q 'a check verdict for nix-alpha@.* is already stored' "$STATE/run.log"
}

@test "the measured duration and the sampled load are recorded in the verdict" {
  SHIM_SLEEP=2 PG_REPO_RUNNER_LOADAVG=3.25 verify
  [ "$status" -eq 0 ]
  local f
  f="$(verdict_file nix-alpha "$ALPHA_SHA")"
  [ "$(jq '.durationS >= 2 and .durationS < 30' "$f")" = true ]
  [ "$(jq -r .loadStart "$f")" = 3.25 ]
  [ "$(jq -r .loadEnd "$f")" = 3.25 ]
  [ "$(jq '(.finishedAt | fromdateiso8601) - (.startedAt | fromdateiso8601) >= 2' "$f")" = true ]
  grep -q 'check end: exit=0 after [0-9]*s (load 3.25)' "$STATE/run.log"
  # status --json carries it
  run "${RUNNER[@]}" status --json --repo nix-alpha
  [ "$(jq -r '.[0].durationS >= 2' <<<"$output")" = true ]
}

@test "an abbreviated sha resolves and the verdict is keyed by the full sha" {
  verify "nix-alpha@${ALPHA_SHA:0:10}"
  [ "$status" -eq 0 ]
  [ -f "$(verdict_file nix-alpha "$ALPHA_SHA")" ]
  [[ "$(cat "$TEST_DIR/nix.argv")" == *"?rev=$ALPHA_SHA" ]]
}

@test "a sha that is not a commit of the repo is skipped with exit 0: no nix, no verdict" {
  verify "nix-alpha@0123456789abcdef0123456789abcdef01234567"
  [ "$status" -eq 0 ]
  [ "$(nix_runs)" -eq 0 ]
  [ ! -e "$(verdict_file nix-alpha 0123456789abcdef0123456789abcdef01234567)" ]
  grep -q 'is not a commit of nix-alpha' "$STATE/run.log"
}

@test "verify needs a sha: a bare repo key is a usage error, exit 2" {
  verify nix-alpha
  [ "$status" -eq 2 ]
  [[ $output == *"verify needs an item id"* ]]
  [ "$(nix_runs)" -eq 0 ]
}

@test "the canonical working tree is never read or written: a dirty tree changes neither the run nor the tree" {
  echo dirty >>"$WS/nix-alpha/f"
  echo new >"$WS/nix-alpha/untracked"
  local before
  before="$(git -C "$WS/nix-alpha" status --porcelain)"
  verify
  [ "$status" -eq 0 ]
  [ "$(git -C "$WS/nix-alpha" status --porcelain)" = "$before" ]
  [ "$(git -C "$WS/nix-alpha" rev-parse HEAD)" = "$ALPHA_SHA" ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = green ]
  # nothing was pushed
  [ "$(git -C "$TEST_DIR/remotes/alpha.git" rev-parse main)" = "$ALPHA_SHA" ]
}

@test "the primary branch comes from pn-workspace.toml, not from the checked-out branch" {
  git -C "$WS/nix-alpha" branch trunk
  git -C "$WS/nix-alpha" checkout -q trunk
  commit_in "$WS/nix-alpha"
  local trunk_tip
  trunk_tip="$(git -C "$WS/nix-alpha" rev-parse HEAD)"
  git -C "$WS/nix-alpha" checkout -q main
  # main is the workspace branch: the trunk commit is not its tip, so it is superseded
  verify "nix-alpha@$trunk_tip"
  [ "$(jq -r .result "$(verdict_file nix-alpha "$trunk_tip")")" = superseded ]
  [ "$(nix_runs)" -eq 0 ]
  # and when the workspace names trunk, trunk's tip is the one that is checked
  write_workspace nix-beta nix-link
  printf "[repos.nix-alpha]\nurl = 'x'\nbranch = 'trunk'\n" >>"$WS/pn-workspace.toml"
  rm -rf "$STATE/verdicts"
  verify "nix-alpha@$trunk_tip"
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$trunk_tip")")" = green ]
}

@test "the kernel still gates verify: kill switch, load and a held lease answer exit 9 with no nix" {
  mkdir -p "$STATE"
  touch "$STATE/push.disabled"
  verify
  [ "$status" -eq 9 ]
  rm "$STATE/push.disabled"
  PG_REPO_RUNNER_LOADAVG=50 verify
  [ "$status" -eq 9 ]
  hold_lease
  verify
  [ "$status" -eq 9 ]
  [ "$(nix_runs)" -eq 0 ]
  [ ! -e "$(verdict_file nix-alpha "$ALPHA_SHA")" ]
}

@test "a repo with verify off, one outside the workspace file and a symlinked one are not checked" {
  verify "nix-beta@$(git -C "$WS/nix-beta" rev-parse HEAD)"
  [ "$status" -eq 0 ]
  verify "nix-ghost@$(git -C "$WS/nix-ghost" rev-parse HEAD)"
  [ "$status" -eq 2 ]
  verify "nix-link@$(git -C "$WS/nix-link" rev-parse HEAD)"
  [ "$status" -eq 2 ]
  [ "$(nix_runs)" -eq 0 ]
}

@test "an unconfigured nix binary is a malfunction, exit 1, and stores no verdict" {
  # the built script injects its own store nix, so only a raw run proves the guard
  if [[ -n ${SCRIPT_UNDER_TEST:-} ]]; then
    skip "the built script carries its own nix path"
  fi
  PG_REPO_RUNNER_NIX_BIN="" RR_NIX_BIN="" verify
  [ "$status" -eq 1 ]
  [ ! -e "$(verdict_file nix-alpha "$ALPHA_SHA")" ]
}

# ---- the tips source --------------------------------------------------------

tips() { run --separate-stderr "${RUNNER[@]}" tips; }

@test "tips emits one JSONL record per verify-enabled repo whose tip has no verdict" {
  tips
  [ "$status" -eq 0 ]
  # nix-alpha only: nix-beta is verify=false, nix-ghost is not a workspace key,
  # nix-link is a symlink, nix-flk is in neither the workspace file nor on disk
  [ "$(wc -l <<<"$output" | tr -d ' ')" -eq 1 ]
  [ "$(jq -r .id <<<"$output")" = "nix-alpha@$ALPHA_SHA" ]
  [ "$(jq -r .type <<<"$output")" = repo-tip ]
  [ "$(jq -r .title <<<"$output")" = "nix-alpha tip" ]
  [ "$(jq -r .metadata.repo <<<"$output")" = nix-alpha ]
  [ "$(jq -r .metadata.sha <<<"$output")" = "$ALPHA_SHA" ]
  [ "$(jq -r .metadata.branch <<<"$output")" = main ]
  # the default retry window is an hour
  [ "$(jq '(.expiresAt | fromdateiso8601) - (.at | fromdateiso8601)' <<<"$output")" = 3600 ]
  # the skipped entries are explained in the run log, not on stdout
  grep -q "tips: skipped 'nix-ghost'" "$STATE/run.log"
  grep -q "tips: skipped 'nix-link'" "$STATE/run.log"
}

@test "tips: the retry window is configurable, and stdout is nothing but JSON lines" {
  PG_REPO_RUNNER_RETRY_WINDOW=120 tips
  [ "$(jq '(.expiresAt | fromdateiso8601) - (.at | fromdateiso8601)' <<<"$output")" = 120 ]
  [ -z "$stderr" ]
  sed -i.bak 's/^nice_level = 10/nice_level = 10\nretry_window = 900/' "$TEST_DIR/config.toml"
  tips
  [ "$(jq '(.expiresAt | fromdateiso8601) - (.at | fromdateiso8601)' <<<"$output")" = 900 ]
}

@test "tips: a tip with a verdict is not emitted, a new tip is, and the id carries the sha" {
  verify
  tips
  [ "$status" -eq 0 ]
  [ -z "$output" ]
  commit_in "$WS/nix-alpha"
  local new again
  new="$(git -C "$WS/nix-alpha" rev-parse HEAD)"
  tips
  [ "$(jq -r .id <<<"$output")" = "nix-alpha@$new" ]
  # emitted again for the same tip: the same id (the duplicate pg-router absorbs)
  again="$output"
  tips
  [ "$(jq -r .id <<<"$output")" = "$(jq -r .id <<<"$again")" ]
}

@test "tips: a signal exit stores no verdict, so the tip stays offered" {
  SHIM_MODE="kill" verify
  [ "$status" -eq 1 ]
  tips
  [ "$(jq -r .id <<<"$output")" = "nix-alpha@$ALPHA_SHA" ]
}

@test "tips reads the primary branch from pn-workspace.toml and ignores a checked-out feature branch" {
  git -C "$WS/nix-alpha" checkout -q -b feature
  commit_in "$WS/nix-alpha"
  tips
  [ "$(jq -r .id <<<"$output")" = "nix-alpha@$ALPHA_SHA" ]
}

@test "tips does not take the lease or consult the load: it only reads refs and the store" {
  hold_lease
  local owner
  owner="$(readlink "$STATE/lease")"
  PG_REPO_RUNNER_LOADAVG=99 tips
  [ "$status" -eq 0 ]
  [ "$(jq -r .id <<<"$output")" = "nix-alpha@$ALPHA_SHA" ]
  [ "$(readlink "$STATE/lease")" = "$owner" ]
}

@test "tips: a tip item is exactly what verify consumes" {
  tips
  local id
  id="$(jq -r .id <<<"$output")"
  verify "$id"
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-alpha "$ALPHA_SHA")")" = green ]
}

@test "tips refuses arguments, and a broken config is a malfunction" {
  run "${RUNNER[@]}" tips extra
  [ "$status" -eq 2 ]
  rm "$TEST_DIR/config.toml"
  run "${RUNNER[@]}" tips
  [ "$status" -eq 1 ]
}

@test "tips and verify: no repo has push enabled in the fixture, and nothing reaches a remote" {
  [ "$(tq -o json -f "$TEST_DIR/config.toml" . | jq '[.repos[].push] | map(select(. == true)) | length')" = 0 ]
  local before
  before="$(git -C "$TEST_DIR/remotes/alpha.git" rev-parse main)"
  tips
  verify
  [ "$(git -C "$TEST_DIR/remotes/alpha.git" rev-parse main)" = "$before" ]
}

# ---- real nix, on a fixture flake -------------------------------------------

# require_real_nix: skip unless a usable nix is on PATH (not inside a nix build
# sandbox). Sets NIX_SYSTEM and points PG_REPO_RUNNER_NIX_BIN at the real nix.
require_real_nix() {
  local nix
  nix="$(command -v nix)" || skip "no nix on PATH (a nix build sandbox has none)"
  NIX_SYSTEM="$(timeout 60 "$nix" eval --impure --raw --expr builtins.currentSystem 2>/dev/null)" ||
    skip "nix is on PATH but cannot evaluate here (no usable store or daemon)"
  export PG_REPO_RUNNER_NIX_BIN="$nix"
}

# write_flake DIR EXITCODE: a flake with one derivation check whose builder
# exits EXITCODE (0: the check passes).
write_flake() {
  cat >"$1/flake.nix" <<EOF
{
  description = "pg-repo-runner fixture";
  outputs = { self }: {
    checks.$NIX_SYSTEM.fixture = derivation {
      name = "fixture-check";
      system = "$NIX_SYSTEM";
      builder = "/bin/sh";
      args = [ "-c" "echo fixture-builder-ran; echo ok > \$out; exit $2" ];
    };
  };
}
EOF
}

# make_flake_repo EXITCODE: a workspace repo nix-flk whose tip is the flake.
make_flake_repo() {
  make_repo "$WS/nix-flk" flk
  write_workspace nix-alpha nix-beta nix-link nix-flk
  write_flake "$WS/nix-flk" "$1"
  git -C "$WS/nix-flk" add flake.nix
  git -C "$WS/nix-flk" commit -q -m "flake"
  FLK_SHA="$(git -C "$WS/nix-flk" rev-parse HEAD)"
}

@test "real nix: a passing flake check is green, with the duration recorded" {
  require_real_nix
  make_flake_repo 0
  verify "nix-flk@$FLK_SHA"
  [ "$status" -eq 0 ] || {
    echo "$output"
    cat "$STATE/run.log"
    return 1
  }
  local f
  f="$(verdict_file nix-flk "$FLK_SHA")"
  [ "$(jq -r .result "$f")" = green ]
  [ "$(jq -r .exit "$f")" = 0 ]
  [ "$(jq '.durationS >= 0' "$f")" = true ]
  [ -f "$(jq -r .logPath "$f")" ]
  # a duplicate event does not run nix again: the stored verdict is byte-identical
  local before
  before="$(cat "$f")"
  verify "nix-flk@$FLK_SHA"
  [ "$status" -eq 0 ]
  [ "$(cat "$f")" = "$before" ]
}

@test "real nix: a failing flake check is red, the run exits 0, and the failure is in the kept log" {
  require_real_nix
  make_flake_repo 1
  verify "nix-flk@$FLK_SHA"
  [ "$status" -eq 0 ] || {
    echo "$output"
    cat "$STATE/run.log"
    return 1
  }
  local f
  f="$(verdict_file nix-flk "$FLK_SHA")"
  [ "$(jq -r .result "$f")" = red ]
  [ "$(jq '.exit != 0 and .exit <= 128' "$f")" = true ]
  grep -qi 'fail\|error' "$(jq -r .logPath "$f")"
}

@test "real nix: the check reads the committed snapshot, never the working tree" {
  require_real_nix
  make_flake_repo 0
  # an uncommitted change that would turn the check red, and an untracked file
  write_flake "$WS/nix-flk" 1
  echo junk >"$WS/nix-flk/untracked"
  local before
  before="$(git -C "$WS/nix-flk" status --porcelain)"
  verify "nix-flk@$FLK_SHA"
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-flk "$FLK_SHA")")" = green ]
  [ "$(git -C "$WS/nix-flk" status --porcelain)" = "$before" ]
}

@test "real nix: a tip that moved before the run is superseded and starts no nix" {
  require_real_nix
  make_flake_repo 0
  commit_in "$WS/nix-flk"
  verify "nix-flk@$FLK_SHA"
  [ "$status" -eq 0 ]
  [ "$(jq -r .result "$(verdict_file nix-flk "$FLK_SHA")")" = superseded ]
  [ ! -d "$STATE/logs/nix-flk" ] || [ -z "$(ls -A "$STATE/logs/nix-flk")" ]
}
