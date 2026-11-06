#!/usr/bin/env bash
# Ambient-repo guard around the unit-test run of the `run-unit-tests` hook.
#
# Why: the hook runs inside a `git commit` / pre-land hook, so git has exported
# GIT_DIR (and often GIT_INDEX_FILE, GIT_WORK_TREE) into the test processes. A
# test that runs a bare `git init` or `git config` then writes into the
# AMBIENT repo instead of its fixture, and the canonical clone's shared
# .git/config gains a stray core.worktree (recurring incident: the canonical
# clone breaks `git rev-parse --show-toplevel`, makes it look dirty on a
# worktree's branch, and stops landing). Fixing tests one at a time never ended
# it, so this guard works at the one place every test run passes through.
#
# Usage: run-unit-tests-guard.sh <command> [<arg>...]
#   1. resolves the ambient repo's common git dir while the inherited GIT_*
#      variables are still in place (that is the config a leaked write would hit);
#   2. snapshots <common-dir>/config;
#   3. runs <command> with EVERY GIT_* variable removed, so no test can inherit
#      GIT_DIR/GIT_WORK_TREE (the cause), naming what it removed on stderr;
#   4. fails (exit 97) if <common-dir>/config changed (the symptom, e.g. a test
#      that writes the real config by absolute path), even when <command> passed.
# Exits with <command>'s own status otherwise. Not inside a git repo: runs
# <command> scrubbed and skips the config check.
set -euo pipefail

if [ "$#" -eq 0 ]; then
  echo "run-unit-tests-guard: usage: run-unit-tests-guard.sh <command> [<arg>...]" >&2
  exit 2
fi

common=""
if c="$(git rev-parse --git-common-dir 2>/dev/null)"; then
  common="$(cd "$c" 2>/dev/null && pwd -P)" || common=""
fi

before=""
if [ -n "$common" ] && [ -f "$common/config" ]; then
  before="$(cat "$common/config")"
fi

scrubbed=()
while IFS= read -r name; do
  [ -n "$name" ] && scrubbed+=("$name")
done < <(env | sed -n 's/^\(GIT_[A-Za-z0-9_]*\)=.*/\1/p')
if [ "${#scrubbed[@]}" -gt 0 ]; then
  echo "run-unit-tests-guard: removing inherited ${scrubbed[*]} from the test environment" >&2
fi

rc=0
if [ "${#scrubbed[@]}" -gt 0 ]; then
  env "${scrubbed[@]/#/-u}" "$@" || rc=$?
else
  "$@" || rc=$?
fi

if [ -n "$common" ] && [ -f "$common/config" ]; then
  after="$(cat "$common/config")"
  if [ "$after" != "$before" ]; then
    echo "run-unit-tests-guard: the unit tests changed the ambient repo's $common/config:" >&2
    diff <(printf '%s\n' "$before") <(printf '%s\n' "$after") >&2 || true
    echo "run-unit-tests-guard: a test wrote outside its fixture; find it and give it a hermetic repo (git -C <tempdir>, no GIT_* in env)." >&2
    exit 97
  fi
fi
exit "$rc"
