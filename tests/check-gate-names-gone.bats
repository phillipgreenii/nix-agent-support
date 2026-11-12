#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for scripts/check-gate-names-gone.sh (the ALR-1 check) and
# scripts/check-public-repo-denylist.sh (the public-repo check). Deterministic logic
# (global rule V-3): the REAL scripts run against throwaway fixture trees.

bats_require_minimum_version 1.5.0

ALR="$BATS_TEST_DIRNAME/../scripts/check-gate-names-gone.sh"
PUB="$BATS_TEST_DIRNAME/../scripts/check-public-repo-denylist.sh"
REPO_ROOT="$BATS_TEST_DIRNAME/.."

setup() {
  A="$BATS_TEST_TMPDIR/repo-a"
  B="$BATS_TEST_TMPDIR/repo-b"
  mkdir -p "$A/docs" "$A/src" "$B/src"
  ALLOW="$BATS_TEST_TMPDIR/allow.txt"
  printf '# allowlist\n\nrepo-a/docs/old.md\nrepo-b/legacy/**\n' >"$ALLOW"
}

alr() { run --separate-stderr "$ALR" --allowlist "$ALLOW" "$@"; }

@test "a clean tree passes" {
  echo "nothing to see" >"$A/src/x.txt"
  alr repo-a="$A" repo-b="$B"
  [ "$status" -eq 0 ]
}

@test "each banned token is a hit printed as path:line:token" {
  local t
  for t in LOW_DISK_USAGE BEAD_SERVER_DOWN SYSTEM_PAUSE non_blocking_gates "gate list" "gate set" pg_router_gate_foo pg_router_gates_active disk-watchdog disk-check-tick; do
    printf 'ok\nuses %s here\n' "$t" >"$A/src/x.txt"
    alr repo-a="$A"
    [ "$status" -eq 1 ]
    [[ "$output" == "repo-a/src/x.txt:2:$t" ]] || { echo "token $t: got '$output'" >&2; return 1; }
  done
}

@test "delegate list and bd gate list are not hits" {
  printf 'a delegate list of tools\nbd gate list shows gates\nrun bd gate set x\nthe pn:applied gate\n' >"$A/src/x.txt"
  alr repo-a="$A"
  [ "$status" -eq 0 ]
}

@test "gate list and gate set are matched as whole words only" {
  printf 'a mitigate list\nthe gate listing\nbigate set\n' >"$A/src/x.txt"
  alr repo-a="$A"
  [ "$status" -eq 0 ]
  printf 'The Gate List is gone\n' >"$A/src/x.txt"
  alr repo-a="$A"
  [ "$status" -eq 1 ]
}

@test "an allowlisted file passes, an unlisted file with the same token fails, per repo" {
  echo "SYSTEM_PAUSE" >"$A/docs/old.md"
  alr repo-a="$A" repo-b="$B"
  [ "$status" -eq 0 ]
  echo "SYSTEM_PAUSE" >"$A/docs/new.md"
  alr repo-a="$A" repo-b="$B"
  [ "$status" -eq 1 ]
  [[ "$output" == "repo-a/docs/new.md:1:SYSTEM_PAUSE" ]]
  rm "$A/docs/new.md"
  # the allowlist is read per repo: repo-a's entry does not excuse repo-b
  mkdir -p "$B/docs"
  echo "SYSTEM_PAUSE" >"$B/docs/old.md"
  alr repo-a="$A" repo-b="$B"
  [ "$status" -eq 1 ]
  [[ "$output" == "repo-b/docs/old.md:1:SYSTEM_PAUSE" ]]
}

@test "a glob entry excuses everything under it" {
  mkdir -p "$B/legacy/deep"
  echo "disk-watchdog" >"$B/legacy/deep/x.md"
  alr repo-a="$A" repo-b="$B"
  [ "$status" -eq 0 ]
}

@test "only git-tracked and unignored files of a git checkout are searched" {
  git -C "$A" init -q
  printf 'ignored.txt\n' >"$A/.gitignore"
  echo "SYSTEM_PAUSE" >"$A/ignored.txt"
  alr repo-a="$A"
  [ "$status" -eq 0 ]
  echo "SYSTEM_PAUSE" >"$A/src/tracked.txt"
  alr repo-a="$A"
  [ "$status" -eq 1 ]
}

@test "usage errors exit 2" {
  run --separate-stderr "$ALR"
  [ "$status" -eq 2 ]
  run --separate-stderr "$ALR" --allowlist "$BATS_TEST_TMPDIR/missing" "$A"
  [ "$status" -eq 2 ]
}

@test "the real allowlist keeps the real tree clean (own repo)" {
  # outside a git checkout (the nix sandbox) the scan would also see this test's own
  # scratch files; the nix check scans the repo source separately, before bats runs
  git -C "$REPO_ROOT" rev-parse --show-toplevel >/dev/null 2>&1 || skip "not a git checkout"
  run --separate-stderr "$ALR" nix-agent-support="$REPO_ROOT"
  [ "$status" -eq 0 ] || { echo "$output $stderr" >&2; return 1; }
}

# --- public-repo check ---

pub() { run --separate-stderr "$PUB" --root "$A"; }

@test "public-repo: an absent denylist prints skipped, exits 0 and never says passed" {
  echo "Acme Corp" >"$A/src/x.txt"
  PG_PUBLIC_REPO_DENYLIST="$BATS_TEST_TMPDIR/none.txt" pub
  [ "$status" -eq 0 ]
  [ "$output" = "skipped: no denylist on this machine" ]
  [[ "$output" != *passed* ]]
}

@test "public-repo: an XDG_CONFIG_HOME denylist is read when the variable is unset" {
  mkdir -p "$BATS_TEST_TMPDIR/xdg/pg-public-repo"
  printf 'acmecorp\n' >"$BATS_TEST_TMPDIR/xdg/pg-public-repo/denylist.txt"
  echo "ships to AcmeCorp" >"$A/src/x.txt"
  env -u PG_PUBLIC_REPO_DENYLIST XDG_CONFIG_HOME="$BATS_TEST_TMPDIR/xdg" "$PUB" --root "$A" && return 1
  run --separate-stderr env -u PG_PUBLIC_REPO_DENYLIST XDG_CONFIG_HOME="$BATS_TEST_TMPDIR/xdg" "$PUB" --root "$A"
  [ "$status" -eq 1 ]
}

@test "public-repo: a listed token fails case-insensitively and the token is not printed" {
  printf '# employer names\n\nAcmeCorp\n' >"$BATS_TEST_TMPDIR/deny.txt"
  printf 'one\nships to acmecorp today\n' >"$A/src/x.txt"
  PG_PUBLIC_REPO_DENYLIST="$BATS_TEST_TMPDIR/deny.txt" pub
  [ "$status" -eq 1 ]
  [ "$output" = "src/x.txt:2" ]
  [[ "$output$stderr" != *cme* ]]
}

@test "public-repo: a clean tree passes and says passed" {
  printf 'AcmeCorp\n' >"$BATS_TEST_TMPDIR/deny.txt"
  echo "generic text" >"$A/src/x.txt"
  PG_PUBLIC_REPO_DENYLIST="$BATS_TEST_TMPDIR/deny.txt" pub
  [ "$status" -eq 0 ]
  [[ "$output" == passed:* ]]
}

@test "public-repo: a denylist with only comments is skipped, not a pass" {
  printf '# nothing\n\n' >"$BATS_TEST_TMPDIR/deny.txt"
  PG_PUBLIC_REPO_DENYLIST="$BATS_TEST_TMPDIR/deny.txt" pub
  [ "$status" -eq 0 ]
  [ "$output" = "skipped: no denylist on this machine" ]
}
