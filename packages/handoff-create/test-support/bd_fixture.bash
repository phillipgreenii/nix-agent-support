# shellcheck shell=bash
# Real-bd throwaway-database fixture for handoff-create's bats suites (bead
# pg2-2xfbi). Replicates the approach of the sibling repo's daily-focus
# bd_fixture.bash (per-module copy, not a cross-repo dependency).
#
# ISOLATION INVARIANTS (machine invariant; every one is asserted, not assumed):
#   - the database lives in a fresh temp dir and is bd EMBEDDED mode
#     (`bd init -p <prefix>` in a temp dir writes dolt_mode: embedded), so no
#     dolt server is involved: this file never starts one and never touches
#     port 25252 or the shared beads-dolt data;
#   - every BEADS_*/BD_* variable inherited from the caller is dropped, so a
#     stray BEADS_DIR / server-host variable cannot point bd at the real tracker;
#   - before ANY bd write a test calls bd_fixture_assert_isolated, which fails
#     unless `bd where` resolves inside the temp dir and the metadata says
#     embedded.
#
# Every file that loads this helper shares ONE database across its tests
# (bd_fixture_setup_file), so its tests MUST run serially within the file.
export BATS_NO_PARALLELIZE_WITHIN_FILE=true

# Real git-commit hooks export GIT_DIR/GIT_INDEX_FILE/GIT_WORK_TREE and the
# commit author identity into hook subprocesses; a fixture git or bd call
# inheriting them would act on the OUTER repo (or stamp the wrong owner).
scrub_git_env() {
  unset GIT_DIR GIT_INDEX_FILE GIT_WORK_TREE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES
  unset GIT_AUTHOR_NAME GIT_AUTHOR_EMAIL GIT_AUTHOR_DATE GIT_COMMITTER_NAME GIT_COMMITTER_EMAIL GIT_COMMITTER_DATE
}

# Drop every inherited bd/beads variable, then pin the two that keep the run
# local and quiet.
scrub_bd_env() {
  local v
  for v in $(compgen -e); do
    case "$v" in
    BEADS_* | BD_*) unset "$v" ;;
    esac
  done
  export BEADS_DOLT_AUTO_START=0
  export BD_BACKUP_ENABLED=0
}

bd_fixture_setup_file() {
  scrub_git_env
  scrub_bd_env
  TDIR="$(mktemp -d)"
  TDIR="$(cd "$TDIR" && pwd -P)"
  export TDIR
  export HOME="$TDIR"
  (cd "$TDIR" && bd init --prefix tst >/dev/null 2>&1)
  [ -d "$TDIR/.beads" ] || {
    echo "bd init failed" >&2
    return 1
  }
}

bd_fixture_setup() {
  scrub_git_env
  scrub_bd_env
  export HOME="$TDIR"
  cd "$TDIR" || return 1
}

bd_fixture_teardown_file() {
  # bd's async post-command write can race a bare rm -rf; retry briefly.
  for _ in 1 2 3 4 5; do
    rm -rf "$TDIR" 2>/dev/null && return 0
  done
  rm -rf "$TDIR"
}

# bd_fixture_assert_isolated: fail unless the bd the NEXT command would use is
# the throwaway embedded database inside $TDIR. Call before any bd write.
bd_fixture_assert_isolated() {
  local where mode
  where="$(bd -C "$TDIR" where 2>&1 | head -1)"
  case "$where" in
  "$TDIR"/.beads) ;;
  *)
    echo "ISOLATION FAILURE: bd where resolved to '$where', not inside $TDIR" >&2
    return 1
    ;;
  esac
  mode="$(jq -r '.dolt_mode // ""' "$TDIR/.beads/metadata.json")"
  [ "$mode" = "embedded" ] || {
    echo "ISOLATION FAILURE: dolt_mode is '$mode', not embedded" >&2
    return 1
  }
}

# Registers (or clears) the custom `handoff` type in the throwaway database.
bd_fixture_register_handoff_type() { bd -C "$TDIR" config set types.custom handoff >/dev/null; }
bd_fixture_unregister_types() { bd -C "$TDIR" config set types.custom "" >/dev/null; }

# bd_fixture_show_json <id>: the normalized single issue object, whichever
# envelope this bd build/env emits (J-1 prelude).
bd_fixture_show_json() {
  bd -C "$TDIR" show "$1" --json |
    jq -c '(if type=="object" and has("data") then .data else . end) | (if type=="array" then .[0] else . end)'
}

# bd_fixture_count: number of beads (any status) in the throwaway database.
bd_fixture_count() {
  bd -C "$TDIR" list --status all -n 0 --json |
    jq '(if type=="object" and has("data") then .data else . end) | length'
}

# bd_call_log_install [fail-create-message]: puts a `bd` shim first on PATH
# that logs every call's argv (one line per call) to $BD_CALL_LOG, then execs
# the real bd. With an argument, a `create` call instead prints that message
# on stderr and exits 1 (a create failure other than an unregistered type).
bd_call_log_install() {
  local real_bd
  real_bd="$(command -v bd)"
  BD_SHIM_DIR="$(mktemp -d)"
  BD_CALL_LOG="$BD_SHIM_DIR/calls.log"
  export BD_CALL_LOG BD_SHIM_FAIL_CREATE="${1:-}"
  cat >"$BD_SHIM_DIR/bd" <<SHIM
#!/usr/bin/env bash
printf '%s\n' "\${*//\$'\n'/\\\\n}" >>"$BD_CALL_LOG"
for a in "\$@"; do
  if [ "\$a" = create ] && [ -n "\$BD_SHIM_FAIL_CREATE" ]; then
    echo "\$BD_SHIM_FAIL_CREATE" >&2
    exit 1
  fi
done
exec "$real_bd" "\$@"
SHIM
  chmod +x "$BD_SHIM_DIR/bd"
  PATH="$BD_SHIM_DIR:$PATH"
  export PATH
}
