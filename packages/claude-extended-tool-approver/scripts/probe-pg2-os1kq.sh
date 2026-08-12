#!/usr/bin/env bash
# probe-pg2-os1kq.sh — reproduction / verification probe for bead pg2-os1kq
# (git long-flag tests were EXACT-TOKEN, but git's parse-options accepts any
# UNIQUE PREFIX of a long option, so every such test was bypassable by
# shortening the flag by one character — silently toward Approve).
#
# Builds the hook binary from the CURRENT worktree source and asks it for a
# verdict on each probe command, printing `<decision>  <command>`.
#
# ASKLOG ISOLATION: XDG_DATA_HOME is pointed at a throwaway directory so probe
# rows land in a scratch asks.db and never reach the real corpus.
#
# MEASURED AGAINST REAL GIT 2.54.0, 2026-07-30 (throwaway repos, one fresh repo
# per reset spelling so no prior reset confounds the reading):
#
#   git reset --hard|--har|--ha|--h HEAD~1  -> "HEAD is now at <sha> base", the
#                                              worktree file reverted: the HARD
#                                              reset was PERFORMED in all four.
#   git rebase --interactive|--interactiv|--intera|--inte|--int|--in
#                                           -> flag PARSED (`--i` alone is
#                                              "ambiguous option: i").
#   git push --force-with-lease|--force-with-leas|--force-with|--force-w
#                                           -> flag PARSED ("No configured push
#                                              destination"); `--force-` and
#                                              shorter are ambiguous.
#   git clean --force|--forc|--for|--fo|--f  -> all accepted (evidence only;
#                                              bead pg2-u0e0c owns `git clean`).
set -euo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-os1kq-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)

export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

probe() {
  local cmd="$1"
  local out decision
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-os1kq-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  decision="$(printf '%s' "$out" |
    jq -r '.hookSpecificOutput.permissionDecision // .permissionDecision // "abstain"')"
  printf '%-7s %s\n' "$decision" "$cmd"
}

# ---------------------------------------------------------------------------
# THE DEFECT. Every row here is a command real git 2.54.0 accepts and executes,
# measured above. The full spelling of each was already gated; only the
# abbreviation was approved. Expected AFTER the fix: no `allow` in this block,
# and the reset rows must not carry a reason claiming the reset is soft.
# ---------------------------------------------------------------------------
echo "=== DEFECT: --hard abbreviations (full spelling asks) ==="
probe 'git reset --hard HEAD~1'
probe 'git reset --har HEAD~1'
probe 'git reset --ha HEAD~1'
probe 'git reset --h HEAD~1'

echo
echo "=== DEFECT: --force-with-lease abbreviations (cross-branch must deny) ==="
probe 'git push --force origin main'
probe 'git push --force-with-lease origin main:other'
probe 'git push --force-with-leas origin main:other'
probe 'git push --force-with origin main:other'
probe 'git push --force-w origin main:other'

echo
echo "=== DEFECT: --interactive abbreviations (editor requirement) ==="
probe 'git rebase --interactive'
probe 'git rebase --interactiv'
probe 'git rebase --intera'
probe 'git rebase --int'
probe 'git rebase --in'

echo
echo "=== THE SIX VERBATIM ROWS THE BEAD RECORDED ==="
probe 'git reset --hard HEAD~1'
probe 'git reset --har  HEAD~1'
probe 'git reset --ha   HEAD~1'
probe 'git push --force origin main'
probe 'git push --force-with-leas origin main'
probe 'git rebase --interactiv'

echo
echo "=== --interactive with an automated editor stays approvable ==="
probe 'GIT_SEQUENCE_EDITOR=true git rebase --interactive HEAD~1'
probe 'GIT_SEQUENCE_EDITOR=true git rebase --interactiv HEAD~1'
probe 'GIT_SEQUENCE_EDITOR=true git rebase --int HEAD~1'

echo
echo "=== --force-with-lease abbreviations, SAME branch to origin: still allow ==="
probe 'git push --force-with-lease origin main'
probe 'git push --force-with-leas origin main'
probe 'git push --force-w origin main'
probe 'git push --force-w origin main:main'

echo
echo "=== --force-with-lease abbreviations, non-origin named remote: still ask ==="
probe 'git push --force-with-lease upstream main'
probe 'git push --force-w upstream main'

echo
echo "=== --force-with-lease abbreviations to a URL: still deny (ordering) ==="
probe 'git push --force-with-lease https://example.invalid/x.git main'
probe 'git push --force-w https://example.invalid/x.git main'

echo
echo "=== END-OF-OPTIONS TERMINATOR: a token after -- is an OPERAND ==="
probe 'git reset -- --hard'
probe 'git reset -- --har'
probe 'git rebase -- --interactiv'
probe 'git push origin main -- --force-w'

echo
echo "=== force / --mirror / --delete abbreviations (pg2-bohpm, must deny) ==="
probe 'git push --force origin main'
probe 'git push --forc origin main'
probe 'git push --for origin main'
probe 'git push -f origin main'
probe 'git push -fu origin main'
probe 'git push origin +main'
probe 'git push --mirror origin'
probe 'git push --mirro origin'
probe 'git push --m origin'
probe 'git push --delete origin main'
probe 'git push --delet origin main'
probe 'git push --de origin main'
probe 'git push -d origin main'
probe 'git push origin :main'

# ---------------------------------------------------------------------------
# THE WIDENING, 2026-07-30: `git branch` FORCE-DELETE. Two root causes, both in
# isDestructive — a clustered short (`-Df`, `-fD`) that an exact-token `-D` test
# cannot see, and the LONG-FORM EQUIVALENT `--delete --force`, which is `-D`
# spelled out and which no amount of short-flag matching finds.
#
# MEASURED AGAINST REAL GIT 2.54.0, 2026-07-30, one FRESH repo per spelling with
# a genuinely UNMERGED branch. Every row below that this gate must catch answered
# `Deleted branch unmerged (was <sha>)` and the branch was gone:
#
#   -D / -Df / -fD / --delete --force / --delet --forc
#   -d --force / --delete -f / -f --delet
#
# And the two that must STAY approved really are not the destructive case:
#
#   git branch -d unmerged      -> "error: the branch 'unmerged' is not fully
#                                   merged" — git REFUSES; branch still present.
#   git branch -f other main    -> accepted, a force-MOVE/create; the unmerged
#                                   branch is untouched.
#
# `git branch --d --f unmerged` is NOT a spelling git accepts — it answers
# `error: ambiguous option: f (could be --force or --format)` — but the open
# prefix matcher gates it anyway, which is the fail-safe direction.
#
# SUPERSEDED, 2026-07-31 — READ THIS BEFORE JUDGING THE `git branch` ROWS BELOW.
# Operator ruling pg2-4yy4r item 5, implemented by pg2-fkmg4, replaced this bead's
# force-DELETE classification with git's own safe/unsafe boundary, so TWO of the
# expectations written above and below are no longer this repo's policy:
#
#   * the gated rows now answer `abstain`, not `ask` — the verdict level moved
#     down, to Claude Code's prompt;
#   * `git branch -f other main`, `-M old new` and `-C a b` now answer `abstain`
#     too. They are NOT "not the destructive case": -M and -C were measured
#     CLOBBERING an existing branch, and -f is a force CREATION that silently
#     MOVES an existing ref.
#
# The rows that must still answer `allow` are the GUARDED spellings only — `-d`,
# `-m`, `-c` alone, plain creation, the read/list flags, a `--no-` negation, and
# anything after `--`. scripts/probe-pg2-fkmg4.sh is the current probe for this
# subcommand; the block below is kept as this bead's historical reproduction.
echo
echo "=== WIDENING: git branch force-delete (must NOT allow, in any spelling) ==="
probe 'git branch -D foo'
probe 'git branch -Df foo'
probe 'git branch -fD foo'
probe 'git branch --delete --force foo'
probe 'git branch --delet --forc foo'
probe 'git branch -d foo'

echo
echo "=== WIDENING: further conjunction mixtures and orderings ==="
probe 'git branch -d --force foo'
probe 'git branch --delete -f foo'
probe 'git branch -f --delet foo'
probe 'git branch --d --f foo'
probe 'git branch foo -D'
probe 'git branch foo --delete --force'
probe 'git branch -r -D origin/foo'
probe 'git branch -Dt foo'
probe 'git branch -Dft foo'

echo
echo "=== WIDENING: kept their allow in 2026-07-30; see the SUPERSEDED note above ==="
probe 'git branch -d foo'
probe 'git branch --delete foo'
probe 'git branch --delet foo'
probe 'git branch -f other main'
probe 'git branch --force other main'
probe 'git branch -M old new'
probe 'git branch -C old new'
probe 'git branch -m old new'
probe 'git branch'
probe 'git branch -a'
probe 'git branch --list'
probe 'git branch -uorigin/DEV foo'
probe 'git branch -udrafts/x foo'
probe 'git branch -tdirect foo'
probe 'git branch -- -D'

echo
echo "=== REGRESSION: pg2-abb65 / pg2-8imjo / pg2-szadj verdicts ==="
probe 'git push https://example.invalid/x.git main'
probe 'git push git@example.invalid:evil/x.git main'
probe 'git push --repo=https://example.invalid/x.git main'
probe 'git push /tmp/dst.git main'
probe 'git push file:///tmp/dst.git main'
probe 'git push origin main'
probe 'git remote -v add upstream https://example.invalid/x.git'
probe 'git remote add upstream https://example.invalid/x.git'
probe 'git remote -v'
probe 'git remote get-url origin'
probe 'git config core.hooksPath /tmp/h'
probe 'git config remote.origin.url https://evil.invalid/x.git'
probe 'git config --get user.email'
probe 'git config --list'
probe 'git config -f .git/config --get core.fsmonitor'
probe 'git config user.email a@b.c'
probe 'git config x y'
probe 'git config --unset clean.requireForce'
probe 'git config set core.hooksPath /tmp/h'

echo
echo "=== OUT OF SCOPE (pg2-u0e0c owns git clean): uniform ask, all spellings ==="
probe 'git clean'
probe 'git clean --force'
probe 'git clean --forc'
probe 'git clean --for'
probe 'git clean --f'
probe 'git clean -fdx'

echo
echo "=== REGRESSION: ordinary traffic keeps its verdict ==="
probe 'git branch -D feat'
probe 'git reset HEAD~1'
probe 'git reset --soft HEAD~1'
probe 'git reset --mixed HEAD~1'
probe 'git rebase main'
probe 'git rebase --continue'
probe 'git log --oneline -5'
probe 'git status --porcelain'
probe 'git commit -m "wip"'
probe 'git add -A'
probe 'git checkout -b feat'
probe 'git -c core.pager=EVIL log'

echo
echo "=== REGRESSION: text-vs-parsed (the flag as an ARGUMENT, never a flag) ==="
probe 'git commit -m "git reset --har is now gated (pg2-os1kq)"'
probe 'bd comment pg2-os1kq -m "git push --force-with-leas measured allow"'

echo
echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"
