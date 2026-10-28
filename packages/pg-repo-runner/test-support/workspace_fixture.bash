# shellcheck shell=bash
# Fixture of the runner C suites (bead pg2-xg9ppr, F8): a miniature workspace of
# three tiny repos (real git, each a clone of a bare local remote under a real
# pn-workspace.toml and pn-workspace.lock.json), a build command the test
# controls, and the pn the runner runs.
#
# Two tiers of pn. When a real `pn` that has `workforest add --no-hooks` and a nix
# are on PATH, the REAL pn creates, builds in and removes the sets (its trust gate,
# its wsid registry, its worktrees; the build command is a script of this fixture, so
# no nix evaluation happens). Where there is none (a nix build sandbox), a small
# stand-in does the same set operations with real git. RR_TEST_PN=fake forces the
# stand-in. Tests that are only meaningful against the real pn call ws_require_real_pn.
#
# Needs from the caller: gfh_setup done, TEST_DIR (= $GFH_WORK), SCRIPTS_DIR.

WS_REPOS=(nix-alpha nix-beta nix-gamma)

# ws_make_repo DIR NAME: a clone of a fresh bare remote with one commit pushed.
ws_make_repo() {
  local dir="$1" name="$2"
  gfh_init_bare "$TEST_DIR/remotes/$name.git"
  gfh_clone "$TEST_DIR/remotes/$name.git" "$dir" "pg-repo-runner"
  echo "$name" >"$dir/f"
  git -C "$dir" add f
  git -C "$dir" commit -q -m "seed $name"
  git -C "$dir" push -q origin main
}

# ws_commit_in DIR: a new local commit on main (never pushed).
ws_commit_in() {
  echo "$RANDOM$RANDOM" >>"$1/f"
  git -C "$1" add f
  git -C "$1" commit -q -m "change"
}

# ws_write_toml: pn-workspace.toml and the lock the real pn reads (hand-written: no
# nix evaluation is needed because the fixture has no flake edges).
ws_write_toml() {
  local k
  {
    printf "[workspace]\nid = 'rr-fixture'\nterminal = 'nix-gamma'\n"
    printf "build_command = '%s/build.sh'\n" "$TEST_DIR"
    printf "apply_command = 'sudo darwin-rebuild switch --flake {terminal_nix_dir}#{hostname}'\n"
    printf "activate_command = 'sudo {out}/sw/bin/darwin-rebuild activate'\n\n[repos]\n"
    for k in "${WS_REPOS[@]}"; do
      printf "[repos.%s]\nurl = '%s/remotes/%s.git'\nbranch = 'main'\n" "$k" "$TEST_DIR" "${k#nix-}"
    done
  } >"$WS/pn-workspace.toml"
  {
    printf '{"terminal":"nix-gamma","order":["nix-alpha","nix-beta","nix-gamma"],"repos":{'
    printf '"nix-alpha":{"flake_path":"flake.nix","remote_url":"%s/remotes/alpha.git"},' "$TEST_DIR"
    printf '"nix-beta":{"flake_path":"flake.nix","remote_url":"%s/remotes/beta.git"},' "$TEST_DIR"
    printf '"nix-gamma":{"flake_path":"flake.nix","remote_url":"%s/remotes/gamma.git"}' "$TEST_DIR"
    printf '},"edges":[]}\n'
  } >"$WS/pn-workspace.lock.json"
}

# ws_write_config [BUILD-FLAGS-OFF-FOR...]: the runner's allow-list; build is on for
# every repo except the keys given. push is false for EVERY repo, in every test.
# SURFACE is "off" (default) or a tracker dir.
# shellcheck disable=SC2120  # the suites pass the keys to opt out; the fixture itself passes none
ws_write_config() {
  local surface="${WS_SURFACE:-off}" k off=" $* "
  {
    cat <<EOF
workspace_root = "$WS"
kill_switch = "$TEST_DIR/config-kill"

[thresholds]
max_load = 11.0
nice_level = 10
quiet_window = 900
EOF
    for k in "${WS_REPOS[@]}"; do
      printf '\n[repos.%s]\nverify = false\nsync = false\npush = false\n' "$k"
      if [[ $off == *" $k "* ]]; then printf 'build = false\n'; else printf 'build = true\n'; fi
    done
    if [[ $surface != off ]]; then
      printf '\n[surface]\nenabled = true\ntracker_dir = "%s"\nnotify = true\n' "$surface"
      printf '\n[surface.repo_labels]\nnix-gamma = "terminal-label"\n'
    fi
  } >"$TEST_DIR/config.toml"
}

# ws_make_build_command: the build command pn runs. It records what it saw and ends
# with the status in $TEST_DIR/build.exit.
ws_make_build_command() {
  echo 0 >"$TEST_DIR/build.exit"
  cat >"$TEST_DIR/build.sh" <<EOF
#!/usr/bin/env bash
d="$TEST_DIR"
echo x >>"\$d/build.count"
{
  echo "cwd=\$(pwd -P)"
  echo "root=\${PN_WORKSPACE_ROOT:-}"
  echo "nice=\$(ps -o ni= -p \$\$ | tr -d ' ')"
  for r in nix-alpha nix-beta nix-gamma; do echo "head.\$r=\$(git -C "\$PN_WORKSPACE_ROOT/\$r" rev-parse HEAD)"; done
  echo "lsroot=\$(ls "\$PN_WORKSPACE_ROOT" | tr '\n' ' ')"
} >"\$d/build.record"
exit "\$(cat "\$d/build.exit")"
EOF
  chmod +x "$TEST_DIR/build.sh"
}

# ws_make_fake_pn: the stand-in for pn where there is no real one. workforest add,
# remove and prune are real git worktree operations over the repos of the lock;
# build runs the workspace's build command.
ws_make_fake_pn() {
  cat >"$TEST_DIR/fake-pn" <<'EOF'
#!/usr/bin/env bash
set -u
root="${PN_WORKSPACE_ROOT:?}"
repos() { jq -r '.order[]' "$root/pn-workspace.lock.json"; }
[[ ${1:-} == workspace ]] || exit 2
shift
case "${1:-} ${2:-}" in
"workforest add")
  branch="$3" start="$4"
  mkdir -p "$root/.workforests/$branch"
  for r in $(repos); do
    git -C "$root/$r" worktree add -q -b "$branch" "$root/.workforests/$branch/$r" "$start" >&2 || exit 1
  done
  cp "$root/pn-workspace.toml" "$root/pn-workspace.lock.json" "$root/.workforests/$branch/"
  ;;
"workforest remove")
  branch="$3"
  for r in $(repos); do
    git -C "$root/$r" worktree remove --force "$root/.workforests/$branch/$r" 2>/dev/null || true
  done
  rm -f "$root/.workforests/$branch/pn-workspace.toml" "$root/.workforests/$branch/pn-workspace.lock.json"
  rmdir "$root/.workforests/$branch"
  ;;
"workforest prune")
  for r in $(repos); do git -C "$root/$r" worktree prune; done
  ;;
"build "*)
  cd "$root/$(jq -r .terminal "$root/pn-workspace.lock.json")" || exit 1
  exec "$(tq -o json -f "$root/pn-workspace.toml" .workspace.build_command | jq -r .)"
  ;;
*) exit 2 ;;
esac
EOF
  chmod +x "$TEST_DIR/fake-pn"
}

# ws_setup_pn: picks the tier, exports the variables pn needs, and sets PN_REAL (the
# pn the runner uses, or the one a wrapper delegates to) and PN_TIER (real|fake).
ws_setup_pn() {
  export PG_NIX_LOG_DISABLE=1 OTEL_SDK_DISABLED=true
  export XDG_STATE_HOME="$TEST_DIR/state" XDG_CONFIG_HOME="$TEST_DIR/config" XDG_DATA_HOME="$TEST_DIR/data"
  mkdir -p "$XDG_STATE_HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME"
  PN_TIER=fake
  if [[ ${RR_TEST_PN:-} != fake ]] && command -v pn >/dev/null 2>&1 && command -v nix >/dev/null 2>&1 &&
    pn workspace workforest add --help 2>&1 | grep -q -- '--no-hooks'; then
    PN_TIER=real
    PN_REAL="$(command -v pn)"
    unset PG_REPO_RUNNER_PN_BIN
    # the one-time TOFU grant of the canonical workspace, as the operator gives it;
    # workforest add propagates it to the sets it creates
    PN_WORKSPACE_ROOT="$WS" pn workspace allow >/dev/null 2>&1
  else
    ws_make_fake_pn
    PN_REAL="$TEST_DIR/fake-pn"
    export PG_REPO_RUNNER_PN_BIN="$PN_REAL"
  fi
}

ws_require_real_pn() {
  [[ $PN_TIER == real ]] || skip "needs the real pn (none with --no-hooks on PATH, or RR_TEST_PN=fake)"
}

# ws_fixture_setup: the whole fixture. Needs WS (= $TEST_DIR/ws) created by the caller's
# TEST_DIR and gfh_setup already done.
ws_fixture_setup() {
  WS="$TEST_DIR/ws"
  mkdir -p "$WS" "$TEST_DIR/remotes"
  local k
  for k in "${WS_REPOS[@]}"; do ws_make_repo "$WS/$k" "${k#nix-}"; done
  ws_write_toml
  ws_make_build_command
  WS_SURFACE="${WS_SURFACE:-off}"
  ws_write_config
  export PG_REPO_RUNNER_CONFIG="$TEST_DIR/config.toml"
  export PG_REPO_RUNNER_LOADAVG=0.50
  export PG_REPO_RUNNER_EPOCH=1793000000
  STATE="$TEST_DIR/state/pg-repo-runner"
  ws_setup_pn
}

# ws_tips: the sorted "repo=sha" lines of the canonical primary tips.
ws_tips() {
  local k
  for k in "${WS_REPOS[@]}"; do printf '%s=%s\n' "$k" "$(git -C "$WS/$k" rev-parse refs/heads/main)"; done | LC_ALL=C sort
}

# ws_hash: the tuple hash the design names, computed independently of the runner.
ws_hash() { ws_tips | sha256sum | cut -d' ' -f1; }

# ws_state: a digest of every byte of the canonical clones (.git and the working
# tree, index included) plus `git status --porcelain`, the ref list and the worktree
# list. Reads only.
ws_state() {
  local k
  for k in "${WS_REPOS[@]}"; do
    echo "== $k"
    (
      cd "$WS/$k" || exit 1
      find . -type f -exec sha256sum {} + | LC_ALL=C sort -k2
      find . -type d | LC_ALL=C sort
      find . -type l -exec sh -c 'for f; do echo "$f -> $(readlink "$f")"; done' _ {} + | LC_ALL=C sort
    )
    GIT_OPTIONAL_LOCKS=0 git -C "$WS/$k" status --porcelain
    git -C "$WS/$k" for-each-ref
    git -C "$WS/$k" worktree list --porcelain
  done | sha256sum | cut -d' ' -f1
}

# ws_sets: the reserved-prefix set directories and branches left anywhere, one per line.
ws_sets() {
  local k
  find "$WS/.workforests" -mindepth 1 -maxdepth 1 -name 'runner-build-*' -exec basename {} \; 2>/dev/null || true
  for k in "${WS_REPOS[@]}"; do
    git -C "$WS/$k" for-each-ref --format='%(refname:short)' 'refs/heads/runner-build-*'
  done
}

# ws_build [HASH]: one build run for the tuple HASH (default: the current one).
ws_build() {
  local h="${1:-$(ws_hash)}"
  PG_ROUTER_ITEM_ID="workspace@$h" run "${RUNNER[@]}" build
}

# ws_verdict_file [HASH]
ws_verdict_file() { printf '%s/verdicts/workspace/%s.build.json' "$STATE" "${1:-$(ws_hash)}"; }
