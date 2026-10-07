#!/usr/bin/env bash
# validate-nix-wrapper-adoption.sh — live adoption harness for the always-on
# "run nix through pg-nix-log-wrapped" rule (bead pg2-kqrrs.11, epic pg2-kqrrs).
#
# WHAT IT DOES: runs N fresh `claude -p` sessions (default 5) with the FIXED
# prompt "run `nix flake check` in <repo>", captures each session's full
# stream-json transcript (--verbose, so every tool call is in it), and counts
# the runs whose transcript shows the agent invoking `pg-nix-log-wrapped`.
# PASS when at least MIN_PASS runs (default 4) did.
#
# THE RULE UNDER TEST lives in two sources (both delivered by home-manager, so
# it only reaches a fresh session after the operator-run `pn workspace apply`):
#   - always-on MUST rule: phillipg-nix-ziprecruiter
#     modules/workspace-root-CLAUDE.md.txt, installed to
#     <workspace root>/CLAUDE.md by modules/workspace.nix (home.file ... source);
#   - subagent-brief extension of L-3: phillipgreenii-nix-agent-support
#     home/programs/agent-rules/pgii-agent-rules.md, composed into
#     ~/.claude/CLAUDE.md by home/programs/agent-rules/default.nix.
# Running this BEFORE that apply measures the old rule set and will fail; that
# is expected, not a harness bug.
#
# WHAT COUNTS AS "ADOPTED": the primary criterion is strict, a Bash tool_use
# (main loop or subagent) whose `command` contains `pg-nix-log-wrapped`, so a
# transcript that merely quotes the rule in prose does not count. The looser
# whole-transcript `grep -c pg-nix-log-wrapped` is also reported per run for
# context (it is the bead's literal wording) but does not decide the verdict.
#
# DELIBERATELY NON-HERMETIC and NOT a checks.* gate (same stance as
# validate-hook-router-live.sh): it makes real Claude API requests (token
# spend) and runs a real `nix flake check`, so it is run by hand, never
# automatically. Its logic is unit-tested against a fake `claude` stub in
# tests/validate-nix-wrapper-adoption.bats.
#
# REQUIRES: `claude` and `jq` on PATH (override the binary with CLAUDE_BIN).
#
# Environment:
#   CLAUDE_BIN                 claude executable (default: claude)
#   PG_HARNESS_CLAUDE_ARGS     extra args for claude -p, word-split
#                              (default: --permission-mode bypassPermissions,
#                              matching validate-hook-router-live.sh)
#   PG_HARNESS_TIMEOUT         per-run seconds when `timeout`/`gtimeout` exists
#                              (default: 1800)
#
# EXIT: 0 pass; 1 fewer than MIN_PASS runs adopted the wrapper (or a prerequisite
# failed); 2 usage error.
#
# Usage: scripts/validate-nix-wrapper-adoption.sh [--repo DIR] [--runs N]
#                                                 [--min-pass N] [--out-dir DIR]
#
# Post-apply invocation (operator):
#   scripts/validate-nix-wrapper-adoption.sh --runs 5 --min-pass 4

set -euo pipefail

log() { printf '[validate-nix-wrapper-adoption] %s\n' "$*" >&2; }
fail() {
  printf '[validate-nix-wrapper-adoption] FAIL: %s\n' "$*" >&2
  exit 1
}
usage_error() {
  printf '[validate-nix-wrapper-adoption] usage error: %s\n' "$*" >&2
  exit 2
}

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
runs=5
min_pass=4
out_dir=""

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    # Print the leading comment block (everything before `set -euo`).
    awk 'NR == 1 { next } /^set -euo/ { exit } { sub(/^# ?/, ""); print }' "${BASH_SOURCE[0]}"
    exit 0
    ;;
  --repo)
    [[ $# -ge 2 ]] || usage_error "--repo needs a value"
    repo="$2"
    shift
    ;;
  --runs)
    [[ $# -ge 2 ]] || usage_error "--runs needs a value"
    runs="$2"
    shift
    ;;
  --min-pass)
    [[ $# -ge 2 ]] || usage_error "--min-pass needs a value"
    min_pass="$2"
    shift
    ;;
  --out-dir)
    [[ $# -ge 2 ]] || usage_error "--out-dir needs a value"
    out_dir="$2"
    shift
    ;;
  *) usage_error "unknown argument: $1" ;;
  esac
  shift
done

[[ $runs =~ ^[1-9][0-9]*$ ]] || usage_error "--runs must be a positive integer, got '$runs'"
[[ $min_pass =~ ^[0-9]+$ ]] || usage_error "--min-pass must be a non-negative integer, got '$min_pass'"
((min_pass <= runs)) || usage_error "--min-pass ($min_pass) exceeds --runs ($runs)"
[[ -d $repo ]] || usage_error "--repo is not a directory: $repo"

claude_bin="${CLAUDE_BIN:-claude}"
command -v "$claude_bin" >/dev/null 2>&1 || fail "required tool '$claude_bin' not found on PATH"
command -v jq >/dev/null 2>&1 || fail "required tool 'jq' not found on PATH"

if [[ -z $out_dir ]]; then
  out_dir="$(mktemp -d "${TMPDIR:-/tmp}/validate-nix-wrapper-adoption.XXXXXX")"
else
  mkdir -p "$out_dir"
fi

# Optional per-run wall-clock cap; absent on stock macOS, so only when found.
timeout_cmd=()
for t in timeout gtimeout; do
  if command -v "$t" >/dev/null 2>&1; then
    timeout_cmd=("$t" "${PG_HARNESS_TIMEOUT:-1800}")
    break
  fi
done

# Word-split on purpose: an operator-supplied flag string.
# shellcheck disable=SC2206
claude_args=(${PG_HARNESS_CLAUDE_ARGS:---permission-mode bypassPermissions})

# The fixed prompt (bead acceptance text), with the repo path filled in.
prompt="run \`nix flake check\` in $repo"

# Strict criterion: any Bash tool_use whose command names the wrapper.
# `fromjson?` skips any non-JSON line instead of aborting the stream.
adopted_filter='fromjson?
  | select(.type == "assistant")
  | .message.content[]?
  | select(.type == "tool_use" and .name == "Bash")
  | (.input.command // "")
  | select(contains("pg-nix-log-wrapped"))'

log "repo=$repo runs=$runs min_pass=$min_pass out_dir=$out_dir"
log "prompt: $prompt"

passes=0
for ((i = 1; i <= runs; i++)); do
  transcript="$out_dir/run-$i.jsonl"
  log "run $i/$runs: claude -p ..."
  # A failed or timed-out session still leaves a (partial) transcript worth
  # judging: a run that never reached nix simply does not count as adopted.
  (cd "$repo" && "${timeout_cmd[@]}" "$claude_bin" -p "$prompt" \
    --output-format stream-json --verbose "${claude_args[@]}") \
    >"$transcript" 2>"$out_dir/run-$i.stderr" ||
    log "run $i: claude exited non-zero (judging the partial transcript)"

  strict="$(jq -R -r "$adopted_filter" "$transcript" 2>/dev/null | wc -l | tr -d ' ')"
  loose="$(grep -c 'pg-nix-log-wrapped' "$transcript" || true)"
  if ((strict > 0)); then
    passes=$((passes + 1))
    log "run $i: ADOPTED (wrapped Bash calls=$strict, loose mentions=$loose)"
  else
    log "run $i: not adopted (wrapped Bash calls=0, loose mentions=$loose)"
  fi
done

log "adopted in $passes of $runs runs (need >= $min_pass); transcripts in $out_dir"
if ((passes >= min_pass)); then
  log "PASS"
  exit 0
fi
fail "only $passes of $runs runs used pg-nix-log-wrapped (need >= $min_pass)"
