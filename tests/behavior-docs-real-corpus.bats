#!/usr/bin/env bats
# bats file_tags=type:integration
# Set DISCOVERY of tests/behavior-docs-real-corpus.sh (bead pg2-ksssh). The runner
# used to hard-code five sets, so four sets authored after it sat outside every
# conformance gate and the next one would have joined them silently. It now finds
# every `*/docs/behavior/README.md`; this file pins what "finds" means, against the
# REAL runner and real directories (no mock of the tool under test).
#
# Env (set by the nix check `test-behavior-docs-real-corpus-discovery`; export them
# by hand to run this file alone):
#   CORPUS_RUNNER     path to tests/behavior-docs-real-corpus.sh
#   CORPUS_REPO_ROOT  the repo (or flake source) root holding the real sets and the
#                     claude-marketplace/behavior-docs-conformance evaluator scripts

setup() {
  if [[ -z ${CORPUS_RUNNER:-} || -z ${CORPUS_REPO_ROOT:-} ]]; then
    echo "CORPUS_RUNNER and CORPUS_REPO_ROOT must be set" >&2
    return 1
  fi
  FIX="$BATS_TEST_TMPDIR/root"
  mkdir -p "$FIX"
}

# mkset <repo-relative set dir> — a set is a docs/behavior directory with a README.md
mkset() {
  mkdir -p "$FIX/$1"
  printf '# %s\n' "$1" >"$FIX/$1/README.md"
}

@test "a docs/behavior/README.md directory is a set, listed repo-relative" {
  mkset "behavior-docs/docs/behavior"
  mkset "packages/zeta/docs/behavior"
  run bash "$CORPUS_RUNNER" --list-sets "$FIX"
  [ "$status" -eq 0 ]
  [ "$output" = $'behavior-docs/docs/behavior\npackages/zeta/docs/behavior' ]
}

@test "a docs/behavior directory without a README.md is not a set" {
  mkset "packages/real/docs/behavior"
  mkdir -p "$FIX/packages/readmeless/docs/behavior"
  printf '# x\n' >"$FIX/packages/readmeless/docs/behavior/invariants.md"
  run bash "$CORPUS_RUNNER" --list-sets "$FIX"
  [ "$status" -eq 0 ]
  [ "$output" = "packages/real/docs/behavior" ]
}

@test "dot-directories hold COPIES of the tree and are not scanned" {
  mkset "packages/real/docs/behavior"
  mkset ".worktrees/some-bead/packages/real/docs/behavior"
  mkset ".git/modules/x/docs/behavior"
  run bash "$CORPUS_RUNNER" --list-sets "$FIX"
  [ "$status" -eq 0 ]
  [ "$output" = "packages/real/docs/behavior" ]
}

@test "the order is byte order whatever the caller's locale" {
  # C order: uppercase before lowercase, '-' (0x2d) before '_' (0x5f). A locale-collated
  # sort interleaves these, which is how the same finding serialized two ways.
  mkset "packages/a_b/docs/behavior"
  mkset "packages/a-b/docs/behavior"
  mkset "packages/B/docs/behavior"
  mkset "packages/a/docs/behavior"
  LC_ALL=en_US.UTF-8 run bash "$CORPUS_RUNNER" --list-sets "$FIX"
  [ "$status" -eq 0 ]
  expected=$(printf '%s\n' packages/a_b/docs/behavior packages/a-b/docs/behavior packages/B/docs/behavior packages/a/docs/behavior | LC_ALL=C sort)
  [ "$expected" = $'packages/B/docs/behavior\npackages/a-b/docs/behavior\npackages/a/docs/behavior\npackages/a_b/docs/behavior' ]
  [ "$output" = "$expected" ]
}

@test "every set the repo holds is discovered, including those once left out" {
  run bash "$CORPUS_RUNNER" --list-sets "$CORPUS_REPO_ROOT"
  [ "$status" -eq 0 ]
  for s in \
    behavior-docs/docs/behavior \
    packages/ccpool/docs/behavior \
    packages/pa-monitor/docs/behavior \
    packages/pg-router/docs/behavior \
    packages/pg-connector/docs/behavior \
    packages/pg-router-ccpool-handler/docs/behavior \
    packages/pg-router-source-pg-connector/docs/behavior \
    packages/work-report/docs/behavior \
    docs/behavior/pg-decider \
    docs/behavior/pg-desk \
    docs/behavior/pg-task-focus \
    packages/pg-connector/docs/behavior/github; do
    printf '%s\n' "$output" | grep -Fx -- "$s"
  done
}

@test "a directory directly under docs/behavior with a README.md is a set too" {
  # pg-decider, pg-desk and pg-task-focus live at docs/behavior/<name>, and
  # pg-connector's github set at packages/pg-connector/docs/behavior/github, beside
  # (not instead of) the set at its parent (bead pg2-nz2f0).
  mkset "behavior-docs/docs/behavior"
  mkset "docs/behavior/alpha"
  mkset "docs/behavior/beta"
  mkset "packages/pkg/docs/behavior"
  mkset "packages/pkg/docs/behavior/sub"
  run bash "$CORPUS_RUNNER" --list-sets "$FIX"
  [ "$status" -eq 0 ]
  [ "$output" = $'behavior-docs/docs/behavior\ndocs/behavior/alpha\ndocs/behavior/beta\npackages/pkg/docs/behavior\npackages/pkg/docs/behavior/sub' ]
}

@test "a set nested deeper than one level under docs/behavior is not discovered" {
  # The rule is one level: a README two levels down is a sub-document of the set
  # above it, not a set, so a fixture tree under a set cannot become a gate operand.
  mkset "packages/pkg/docs/behavior"
  mkset "packages/pkg/docs/behavior/sub/deeper"
  mkset "docs/other/behavior/x"
  run bash "$CORPUS_RUNNER" --list-sets "$FIX"
  [ "$status" -eq 0 ]
  [ "$output" = "packages/pkg/docs/behavior" ]
}

@test "dot-directories are pruned for the one-level sets as well" {
  mkset "docs/behavior/real"
  mkset ".worktrees/some-bead/docs/behavior/real"
  run bash "$CORPUS_RUNNER" --list-sets "$FIX"
  [ "$status" -eq 0 ]
  [ "$output" = "docs/behavior/real" ]
}

@test "discovery finding no set at all is loud, never a vacuous pass" {
  mkdir -p "$FIX/claude-marketplace"
  ln -s "$CORPUS_REPO_ROOT/claude-marketplace/behavior-docs-conformance" \
    "$FIX/claude-marketplace/behavior-docs-conformance"
  run bash "$CORPUS_RUNNER" "$FIX"
  [ "$status" -eq 2 ]
  [[ $output == *"no behavior-docs set discovered"* ]]
}

@test "a repo whose method set is missing is loud: every seam hangs off it" {
  mkdir -p "$FIX/claude-marketplace"
  ln -s "$CORPUS_REPO_ROOT/claude-marketplace/behavior-docs-conformance" \
    "$FIX/claude-marketplace/behavior-docs-conformance"
  mkset "packages/only/docs/behavior"
  run bash "$CORPUS_RUNNER" "$FIX"
  [ "$status" -eq 2 ]
  [[ $output == *"the method set is the root of every seam"* ]]
}
