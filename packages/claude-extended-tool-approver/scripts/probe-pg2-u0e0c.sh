#!/usr/bin/env bash
# probe-pg2-u0e0c.sh — reproduction / verification probe for bead pg2-u0e0c
# (`git clean` returns Abstain for EVERY spelling; the flag-aware row design is
# REJECTED, not deferred — operator ruling 2026-07-30, pg2-4yy4r item 3).
#
# Builds the hook binary from the CURRENT worktree source and asks it for a
# verdict on each probe command, printing `<decision>  <command>`.
#
# ASKLOG ISOLATION: XDG_DATA_HOME is pointed at a throwaway directory so probe
# rows land in a scratch asks.db and never reach the real corpus.
#
# WHY A UNIFORM VERDICT AND NOT A FLAG-AWARE SPLIT. The design this replaces
# (approve a no-force / `-n` / `--dry-run` clean, abstain `-f…`) was refuted
# because the flag test is the bug surface:
#
#   - `-fdx` is ONE token, so an exact-token `-f` test sorts the MOST
#     destructive spelling into the "no force given" branch and APPROVES it.
#   - git's parse-options accepts any UNAMBIGUOUS PREFIX of a long option, so
#     `--force` is also `--forc`, `--for`, `--fo`, `--f`. Every spelling a
#     matcher misses is a silent misclassification, toward Approve.
#
# A uniform verdict has no flag test, so neither defect can exist. The `-n` /
# `--dry-run` rows below are deliberately Abstain too, NOT Approve: the
# provably-safe read-only spellings were not carved out, so a dry run PROMPTS in
# `default` mode.
#
# WHAT THE ABSTAIN COSTS, AND THAT THE OPERATOR ACCEPTED IT. Abstain emits `{}`,
# which hands the verdict to Claude Code: auto-approve mode, then settings
# pre-authorization, then the prompt. So `git clean -fdx` still prompts in
# `default` mode but is AUTO-APPROVED, unprompted, in an auto-approving mode —
# raised explicitly (irreversible deletion of untracked files, which can include
# uncommitted work and un-ignored `.env` files, and the inconsistency with
# force-push -> Reject and `reset --hard` -> Ask) and REAFFIRMED. Revisiting it
# is a new ruling with its own bead, not a change to make here.
#
# WHY `{}` AND NOT `allow` — THE CLAIM THE CHAIN ROWS RE-MEASURE. Flipping an
# Ask to an Abstain can yield `allow` when a LATER rule re-approves the leaf.
# It does not here, for two independent reasons, and the last block probes both:
#
#   - `git` is absent from safecmds' approve lists (`alwaysSafe`,
#     `safeReadCmds`, `safeWriteCmds`) and appears ONLY in `hasSubcommands`, so
#     no later rule approves a bare `git` leaf.
#   - Abstain outranks Approve in the MostRestrictive fold (pg2-t4uyx), so an
#     APPROVING sibling leaf in a compound (`git clean -fdx && echo done`)
#     cannot green-light the whole expression.
#
# EXPECTED AFTER THE FIX: every `git clean` row answers `abstain`, in every
# spelling, and the SCOPE-GUARD block is unchanged — this bead moves the `clean`
# verdict and nothing else.
set -euo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-u0e0c-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)

export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

# probe prints the DECISION. Rows whose point is the emitted BYTES use emit.
probe() {
  local cmd="$1"
  local out decision
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-u0e0c-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  decision="$(printf '%s' "$out" |
    jq -r '.hookSpecificOutput.permissionDecision // .permissionDecision // "abstain"')"
  printf '%-7s %s\n' "$decision" "$cmd"
}

emit() {
  local cmd="$1"
  local out
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-u0e0c-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  printf '%-40s %s\n' "${out:-<empty>}" "$cmd"
}

# ---------------------------------------------------------------------------
# THE SIX VERBATIM ROWS THE BEAD RECORDED (measured 2026-07-30 against a
# throwaway binary carrying this one-line change, permission_mode "default").
# Each answered `{}`; re-measured here because a reading is valid only for the
# instant it was taken.
# ---------------------------------------------------------------------------
echo "=== THE SIX VERBATIM ROWS THE BEAD RECORDED ==="
probe 'git clean'
probe 'git clean -n'
probe 'git clean -fdx'
probe 'git clean --force'
probe 'git clean -fdx && echo done'
probe 'cd /tmp && git clean -fdx'

echo
echo "=== EVERY SPELLING IN THE ACCEPTANCE CRITERIA ==="
probe 'git clean'
probe 'git clean -n'
probe 'git clean --dry-run'
probe 'git clean -f'
probe 'git clean -fdx'
probe 'git clean -df'
probe 'git clean --force'
probe 'git clean --forc'
probe 'git clean --for'
probe 'git clean --fo'
probe 'git clean --f'

echo
echo "=== MORE SPELLINGS: clusters, separated shorts, operands, terminator ==="
probe 'git clean -fd'
probe 'git clean -xdf'
probe 'git clean -f -d -x'
probe 'git clean -ffdx'
probe 'git clean -nd'
probe 'git clean --dry-ru'
probe 'git clean -f build/'
probe 'git clean -e node_modules -fdx'
probe 'git clean -x -f -- src/'
probe 'git clean -i'
probe 'git clean -q -fdx'
probe 'git clean --exclude=node_modules -fdx'

echo
echo "=== CONTEXT-CARRYING FORMS: -C chdir, pre-subcommand options, env ==="
probe 'git -C /tmp clean -fdx'
probe 'git -C ../other clean -fdx'
probe 'git -c clean.requireForce=false clean'
# The `.git/`-path DENY below is the `git-directory` rule, whose Reject outranks this
# arm's Abstain in the MostRestrictive fold — NOT a `clean` verdict. The two rows after
# it drop the literal `.git` and measure `{}`, which is what shows the deny keys on the
# PATH rather than on the redirection.
probe 'GIT_DIR=/other/.git git clean -fdx'
probe 'GIT_DIR=/other git clean -fdx'
probe 'GIT_WORK_TREE=/other git clean -fdx'

echo
echo "=== CHAIN / BOUNDARY: the emitted BYTES, which is where 'allow' would show"
echo '=== (want {} — anything with permissionDecision "allow" is the defect) ==='
emit 'git clean -fdx'
emit 'git clean -fdx && echo done'
emit 'echo start && git clean -fdx'
emit 'git clean -fdx; git status'
emit 'git clean -fdx || true'
emit 'cd /tmp && git clean -fdx'

echo
echo "=== THE ONE LEAF A LATER RULE DOES APPROVE: the HELP forms ==="
echo "=== (safecmds.isHelpRequest keys on hasSubcommands['git']; a man-page read,"
echo "===  so allow is CORRECT — but it is why the blanket claim needs this row) ==="
probe 'git clean --help'
probe 'git help clean'
probe 'git clean -h'

echo
echo "=== SCOPE GUARD: no OTHER subcommand's verdict may move ==="
probe 'git reset --hard HEAD~1'
probe 'git reset --soft HEAD~1'
probe 'git branch -D feat'
probe 'git branch -d merged'
probe 'git push --force origin main'
probe 'git push origin main'
probe 'git rebase --interactiv'
probe 'git remote -v add upstream https://example.invalid/x.git'
probe 'git remote -v'
probe 'git config clean.requireForce false'
probe 'git config --get user.email'
probe 'git tag v1'
probe 'git log --oneline -5'
probe 'git commit -m "wip"'
probe 'git checkout -b feat'

echo
echo "=== REGRESSION: text-vs-parsed (the spelling as an ARGUMENT, not a flag) ==="
probe 'git commit -m "git clean is now abstained for every spelling (pg2-u0e0c)"'
probe 'bd comment pg2-u0e0c -m "git clean -fdx measured {}"'

echo
echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"
