#!/usr/bin/env bash
# probe-pg2-6qh3p.sh — verification probe for bead pg2-6qh3p: the INERT-VALUE EDITOR
# CARVE-OUT (operator ruling on pg2-agprs, 2026-08-13; bd memory
# `ceta-editor-carveout-ruling`).
#
# THE RULING: the EXACT literal values `true` and `:` are allowed; every other value
# is screened; and it applies to the WHOLE editor family in BOTH spellings —
# GIT_EDITOR, GIT_SEQUENCE_EDITOR, `git -c core.editor=`, `git -c sequence.editor=`.
#
# Builds the hook binary from the CURRENT worktree source and prints the RAW emitted
# hook output, because this bead moves verdicts in BOTH directions and only the
# emitted output distinguishes them:
#
#   - LESS RESTRICTIVE (the friction the ruling removes): the inert-value rows must
#     emit `permissionDecision: "allow"`. 65 of pg2-6c85x's 97 newly-prompting rows
#     were `GIT_EDITOR=true git rebase --continue/--skip` (~0.43/day).
#   - MORE RESTRICTIVE (the hole the ruling closes): GIT_SEQUENCE_EDITOR with a REAL
#     PROGRAM must emit `{}`. It was MEASURED running a marker on
#     `.git/rebase-merge/git-rebase-todo` (scripts/probe-pg2-6c85x.sh, git 2.54.0) and
#     was DECLINED there, so the env spelling was APPROVED while its argv twin
#     `git -c sequence.editor=<prog>` was screened — an env-route bypass of an argv
#     screen.
#
# THE THREE CONSTRAINTS the ruling imposes, each with its own section below:
#   (a) the carve-out reaches the ARGV spellings too — otherwise the env spelling
#       becomes LESS restrictive than argv, breaking pg2-6c85x's relation;
#   (b) the allowance is EXACT-TOKEN, never a prefix/substring/regex;
#   (c) a non-literal value (a variable, a substitution) fails CLOSED.
#
# ASKLOG ISOLATION: XDG_DATA_HOME is pointed at a throwaway directory so probe rows
# land in a scratch asks.db and never reach the real corpus.
set -euo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-6qh3p-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)

export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

probe() {
  local cmd="$1"
  local out
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-6qh3p-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  printf '%-64s -> %s\n' "$cmd" "$out"
}

echo "=== BASELINE: the same commands with NO editor assignment ==="
probe 'git rebase --continue'
probe 'git rebase --skip'
probe 'git commit --amend'
probe 'git rebase -i main'

echo
echo "=== LESS RESTRICTIVE: the inert values, ENV spelling -> must be allow ==="
probe 'GIT_EDITOR=true git rebase --continue'
probe 'GIT_EDITOR=true git rebase --skip'
probe 'GIT_EDITOR=: git rebase --continue'
probe 'GIT_EDITOR=true git commit --amend'
probe 'GIT_EDITOR=: git commit --amend'
probe 'GIT_SEQUENCE_EDITOR=: git rebase -i main'
probe 'GIT_SEQUENCE_EDITOR=true git rebase -i main'
probe 'env GIT_EDITOR=true git rebase --continue'

echo
echo "=== CONSTRAINT (a): the ARGV twins get the SAME carve-out -> must be allow ==="
probe 'git -c core.editor=true rebase --continue'
probe 'git -c core.editor=: commit --amend'
probe 'git -c sequence.editor=true rebase --continue'
probe 'git -c sequence.editor=: log'

echo
echo "=== MORE RESTRICTIVE: a REAL program, both spellings -> must be {} ==="
probe 'GIT_EDITOR=/tmp/evil git commit --amend'
probe 'GIT_SEQUENCE_EDITOR=/tmp/evil git rebase -i main'
probe "GIT_SEQUENCE_EDITOR=\"sed -i 's/^pick /fixup /'\" git rebase -i HEAD~1"
probe 'git -c core.editor=/tmp/evil commit --amend'
probe 'git -c sequence.editor=/tmp/evil rebase -i main'

echo
echo "=== CONSTRAINT (b): EXACT-TOKEN — every near-miss must be {} ==="
probe 'GIT_EDITOR=truex git commit --amend'
probe 'GIT_EDITOR="true " git commit --amend'
probe 'GIT_EDITOR=/bin/true git commit --amend'
probe 'GIT_EDITOR=":;evil" git commit --amend'
probe 'GIT_EDITOR=TRUE git commit --amend'
probe 'GIT_EDITOR="true" git commit --amend'
probe 'GIT_EDITOR= git commit --amend'
probe 'git -c core.editor=truex commit --amend'
probe 'git -c core.editor=TRUE commit --amend'

echo
echo "=== CONSTRAINT (c): a non-literal value fails CLOSED -> must be {} ==="
probe 'GIT_EDITOR=$X git commit --amend'
probe 'GIT_EDITOR=$(echo true) git commit --amend'
probe 'GIT_EDITOR=${EDITOR:-true} git commit --amend'
probe 'GIT_SEQUENCE_EDITOR=$X git rebase -i main'

echo
echo "=== CONTAINMENT: every OTHER program-naming variable stays VALUE-BLIND -> {} ==="
probe 'GIT_PAGER=true git log'
probe 'GIT_PAGER=cat git log'
probe 'GIT_EXTERNAL_DIFF=true git diff'
probe 'GIT_SSH_COMMAND=true git fetch origin'
probe 'GIT_ASKPASS=true git fetch origin'
probe 'git -c core.pager=true log'

echo
echo "=== NO DECISIVE VERDICT IS WEAKENED by an inert editor value ==="
probe 'git tag v1'
probe 'GIT_EDITOR=true git tag v1'
probe 'git -c core.editor=true tag v1'
probe 'git push --force origin main'
probe 'GIT_EDITOR=true git push --force origin main'
probe 'git -c sequence.editor=: push --force origin main'
probe 'GIT_EDITOR=true git config core.hooksPath /tmp/h'

echo
echo "=== THE REBASE ARM: still requires the editor to be PRESENT ==="
probe 'git rebase -i main'
probe 'git rebase --interactive HEAD~3'
echo "    (the argv twin is MORE restrictive here: the arm reads the ENV prefix —"
echo "     recorded in isInertEditorValue, not a defect of this bead)"
probe 'git -c sequence.editor=: rebase -i main'

echo
echo "=== pg2-5bph1 INTERACTION (MEASURED, reported, NOT fixed here) ==="
echo "    The substitution shape DOES move, and in the direction the ruling authorizes."
echo "    Measured against a binary built from HEAD vs this worktree, 2026-08-13:"
echo
echo '      out=$(GIT_EDITOR=true git rebase --continue)'
echo '        HEAD  -> ask  "env var value contains an unevaluated/unsafe expression: out"'
echo "        after -> {}"
echo
echo "    The git rule sees NO git leaf for this shape (cmdparse yields one command-less"
echo "    leaf whose value is the whole substitution), so the git rule did not change the"
echo "    verdict directly. The env-vars rule reaches INTO the substitution, and the inner"
echo "    leaf now clears, so the decisive Ask is no longer raised. It is NOT a blanket"
echo "    clearing of the shape: a NON-inert editor still escalates, and the no-editor"
echo "    baseline was already {}. Rows below, in that order."
probe 'out=$(git rebase --continue)'
probe 'out=$(GIT_EDITOR=true git rebase --continue)'
probe 'out=$(GIT_EDITOR=: git rebase --continue)'
probe 'out=$(GIT_EDITOR=/tmp/evil git rebase --continue)'
probe 'out=$(GIT_SEQUENCE_EDITOR=/tmp/evil git rebase -i main)'
