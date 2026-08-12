#!/usr/bin/env bash
# probe-pg2-fkmg4.sh — reproduction / verification probe for bead pg2-fkmg4
# (`git branch` verdict by SAFETY, not by flag: Abstain every spelling where
# git's OWN guard has been removed, Approve every spelling where it remains).
#
# Builds the hook binary from the CURRENT worktree source and asks it for a
# verdict on each probe command, printing `<decision>  <command>`.
#
# ASKLOG ISOLATION: XDG_DATA_HOME is pointed at a throwaway directory so probe
# rows land in a scratch asks.db and never reach the real corpus.
#
# THE SAFE/UNSAFE BOUNDARY IS GIT'S OWN, straight out of `git branch -h`:
#
#   -d, --[no-]delete   delete fully merged branch          GUARDED
#   -D                  delete branch (even if not merged)  = -d + --force
#   -m, --[no-]move     move/rename a branch                GUARDED
#   -M                  move/rename, even if target exists  = -m + --force
#   -c, --[no-]copy     copy a branch                       GUARDED
#   -C                  copy, even if target exists         = -c + --force
#   -f, --[no-]force    force creation, move/rename, deletion
#
# So the lowercase forms are the ones git itself refuses in the destructive
# case; the uppercase forms are that form FUSED with --force, and an explicit
# -f/--force removes the guard from any of them (INCLUDING plain creation:
# `git branch -f <existing> <start>` silently MOVES an existing ref).
#
# MEASURED AGAINST REAL GIT (2026-07-31, throwaway repos, recorded on the bead):
#
#   git branch -M old keepme  -> clobbered the existing target: keepme went
#                                bdfdb1f -> bad17ef.
#   git branch -C a keepme    -> overwrote the existing branch.
#   git branch -m old keepme  -> "fatal: a branch named 'keepme' already
#                                exists" — git's guard HELD.
#   git branch -d unmerged    -> "error: the branch 'unmerged' is not fully
#                                merged" — git's guard HELD.
#
# RE-MEASURED 2026-08-12 (git 2.54.0, throwaway repo, `keepme` at 82cd0ea and
# `probe` at 6eeb755) for the two claims this bead rests on that the bead itself
# did not measure — force CREATION, and the `--no-` negation:
#
#   git branch keepme probe            -> "fatal: a branch named 'keepme'
#                                         already exists"  (guard HELD)
#   git branch -f keepme probe         -> accepted; keepme MOVED 82cd0ea ->
#                                         6eeb755. One flag turns a refusal into
#                                         a silent ref rewrite.
#   git branch --no-force newname probe-> accepted: `--no-force` IS a real
#                                         spelling git parses.
#   git branch --no-force keepme <old> -> "fatal: a branch named 'keepme'
#                                         already exists" — the negation really
#                                         turns force OFF, so reading it as force
#                                         would gate the STRICTER command.
#
# EXPECTED AFTER THE FIX: every row in an UNSAFE block answers `abstain` (the
# hook emits `{}`, so Claude Code prompts in `default` mode — the operator
# accepted the auto-mode consequence), and every row in a SAFE block answers
# `allow`.
set -euo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-fkmg4-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)

export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

probe() {
  local cmd="$1"
  local out decision
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-fkmg4-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  decision="$(printf '%s' "$out" |
    jq -r '.hookSpecificOutput.permissionDecision // .permissionDecision // "abstain"')"
  printf '%-7s %s\n' "$decision" "$cmd"
}

# ---------------------------------------------------------------------------
# THE TEN VERBATIM ROWS THE BEAD RECORDED (measured 2026-07-31, permission_mode
# "default"). Seven were `ALLOW` and are the defect; `-D` was the only unsafe
# spelling caught, and the last three are correct and must not move.
# ---------------------------------------------------------------------------
echo "=== THE TEN VERBATIM ROWS THE BEAD RECORDED ==="
probe 'git branch -D foo'
probe 'git branch -Df foo'
probe 'git branch -fD foo'
probe 'git branch --delete --force foo'
probe 'git branch --delet --forc foo'
probe 'git branch -M old new'
probe 'git branch -C a b'
probe 'git branch -d merged'
probe 'git branch -m old new'
probe 'git branch'

echo
echo "=== UNSAFE: the FUSED uppercase forms, bare and clustered ==="
probe 'git branch -D foo'
probe 'git branch -M old new'
probe 'git branch -C a b'
probe 'git branch -Dv foo'
probe 'git branch -vM old new'
probe 'git branch -rC a b'
probe 'git branch -Dt foo'
probe 'git branch -Dft foo'

echo
echo "=== UNSAFE: an explicit force removes the guard from ANY of them ==="
probe 'git branch -f other main'
probe 'git branch --force other main'
probe 'git branch --forc other main'
probe 'git branch -d --force foo'
probe 'git branch --delete -f foo'
probe 'git branch -df foo'
probe 'git branch -fd foo'
probe 'git branch -f --delet foo'
probe 'git branch -m -f old new'
probe 'git branch --move --force old new'
probe 'git branch -c -f a b'
probe 'git branch --copy --force a b'
probe 'git branch --d --f foo'
probe 'git branch --force=x other main'

echo
echo "=== UNSAFE: flag AFTER the operand (position must not matter) ==="
probe 'git branch foo -D'
probe 'git branch old new -M'
probe 'git branch foo --delete --force'
probe 'git branch other main -f'

echo
echo "=== SAFE: git's own guard is still in place ==="
probe 'git branch -d merged'
probe 'git branch --delete merged'
probe 'git branch --delet merged'
probe 'git branch -d a b'
probe 'git branch -m old new'
probe 'git branch --move old new'
probe 'git branch -c a b'
probe 'git branch --copy a b'
probe 'git branch new-branch'
probe 'git branch new-branch origin/main'

echo
echo "=== SAFE: read / list forms ==="
probe 'git branch'
probe 'git branch --list'
probe 'git branch -a'
probe 'git branch -r'
probe 'git branch -v'
probe 'git branch -vv'
probe 'git branch --show-current'
probe 'git branch --contains HEAD'
probe 'git branch --merged main'
probe 'git branch --no-merged main'
probe "git branch --format='%(refname:short)'"
probe 'git branch --sort=-committerdate'
probe 'git branch --set-upstream-to=origin/main foo'
probe 'git branch --unset-upstream foo'
probe 'git branch --edit-description foo'

echo
echo "=== SAFE: the --no- NEGATION TRAP — a negation is not the flag ==="
probe 'git branch --no-force other main'
probe 'git branch --no-delete foo'
probe 'git branch --no-move old new'
probe 'git branch --no-copy a b'
probe 'git branch --no-contains HEAD'

echo
echo "=== SAFE: end-of-options terminator — a dashed token after -- is a NAME ==="
probe 'git branch -- -D'
probe 'git branch -- -M'
probe 'git branch -- -C'
probe 'git branch -- -f'
probe 'git branch -- --delete --force'

echo
echo "=== SAFE: case sensitivity — -d/-m/-c differ from -D/-M/-C, -F is not -f ==="
probe 'git branch -d foo'
probe 'git branch -m old new'
probe 'git branch -c a b'
probe 'git branch -dF foo'

echo
echo "=== SAFE: a GLUED short VALUE is not a cluster of flag letters ==="
probe 'git branch -uorigin/DEV foo'
probe 'git branch -udrafts/x foo'
probe 'git branch -tdirect foo'
probe 'git branch -uorigin/MAIN foo'
probe 'git branch -uorigin/CI foo'
probe 'git branch -uorigin/feature-docs foo'

echo
echo "=== SAFE: a branch NAME is an operand, never scanned for flag letters ==="
probe 'git branch -d DEV-123'
probe 'git branch -m Cool-Feature Mint'
probe 'git branch CI-1494'

echo
echo "=== SCOPE GUARD: no OTHER git subcommand's verdict may move ==="
probe 'git reset --hard HEAD~1'
probe 'git reset --soft HEAD~1'
probe 'git clean -fdx'
probe 'git push --force origin main'
probe 'git push origin main'
probe 'git push -f origin main'
probe 'git rebase --interactiv'
probe 'git remote -v add upstream https://example.invalid/x.git'
probe 'git remote -v'
probe 'git config core.hooksPath /tmp/h'
probe 'git config --get user.email'
probe 'git tag v1'
probe 'git log --oneline -5'
probe 'git commit -m "wip"'
probe 'git checkout -b feat'

echo
echo "=== REGRESSION: text-vs-parsed (the flag as an ARGUMENT, never a flag) ==="
probe 'git commit -m "git branch -M is now abstained (pg2-fkmg4)"'
probe 'bd comment pg2-fkmg4 -m "git branch -C measured ALLOW"'

echo
echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"
