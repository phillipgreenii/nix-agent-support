#!/usr/bin/env bash
# probe-pg2-xjt1s.sh — verification probe for bead pg2-xjt1s: the PERSISTENT
# `export …; git …` route, which all three of the git rule's env screens used to miss
# because each read only its own leaf's prefix.
#
# TWO INSTRUMENTS, and neither is sufficient alone:
#
#   PART A — REAL BASH. Which cross-leaf assignment spellings actually reach a CHILD
#     PROCESS's environment. The screens are about the environment git INHERITS, so a
#     spelling that never leaves the shell is not a hazard and screening it would be a
#     false prompt. This is what says `export` YES and a plain `NAME=v;` NO — and what
#     makes `set -a` the one case where the plain spelling has to be screened too.
#
#   PART B — THE HOOK. The verdict for each spelling. `{}` (abstain) is NOT `allow`, and
#     that distinction is the entire movement this bead makes.
#
# WHAT TO LOOK FOR IN PART B. Each block prints the INLINE-PREFIX spelling immediately
# before the EXPORT spelling of the same assignment, because the acceptance criterion is
# a RELATION: the pair must MATCH. Before this bead the inline row was `{}`/`ask` while
# the export row was `allow`.
#
# ASKLOG ISOLATION: XDG_DATA_HOME is pointed at a throwaway directory so probe rows land
# in a scratch asks.db and never reach the real corpus (pg2-cbihz). Part A runs its
# assignments inside `bash -c` subprocesses and touches nothing outside them.
set -uo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-xjt1s-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

############################  PART A — REAL BASH  #############################
bash --version | head -1
echo
echo '=== A. does the spelling reach a CHILD PROCESS environment? ==='

child='printf "%s\n" "${MARKERVAR:-<unset-in-child>}"'
show="/usr/bin/env bash -c '$child'"

reaches() {
  local label="$1" script="$2" out
  out="$(bash -c "$script" 2>&1)"
  printf '  %-50s -> %s\n' "$label" "${out:-<empty>}"
}

reaches 'NAME=v; child' "MARKERVAR=v; $show"
reaches 'NAME=v && child' "MARKERVAR=v && $show"
reaches 'export NAME=v; child' "export MARKERVAR=v; $show"
reaches 'NAME=v; export NAME; child' "MARKERVAR=v; export MARKERVAR; $show"
reaches 'declare NAME=v; child' "declare MARKERVAR=v; $show"
reaches 'declare -x NAME=v; child' "declare -x MARKERVAR=v; $show"
reaches 'typeset -x NAME=v; child' "typeset -x MARKERVAR=v; $show"
reaches 'declare -gx NAME=v; child' "declare -gx MARKERVAR=v; $show"
reaches 'set -a; NAME=v; child' "set -a; MARKERVAR=v; $show"
reaches 'set -o allexport; NAME=v; child' "set -o allexport; MARKERVAR=v; $show"
reaches 'NAME=v child   (prefix only)' "MARKERVAR=v $show"
reaches 'export NAME=v | cat; child' "export MARKERVAR=v | cat; $show"
reaches '(export NAME=v); child' "(export MARKERVAR=v); $show"
reaches 'export NAME=v; unset NAME; child' "export MARKERVAR=v; unset MARKERVAR; $show"
reaches 'export NAME=v; export -n NAME; child' "export MARKERVAR=v; export -n MARKERVAR; $show"
reaches 'readonly NAME=v; child' "readonly MARKERVAR=v; $show"

############################  PART B — THE HOOK  ##############################
set -eo pipefail
bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)
export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

probe() {
  local cmd="$1"
  local out label
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-xjt1s-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  label="$(printf '%s' "$cmd" | tr '\n' '~')"
  printf '%-84s -> %s\n' "$label" "$(printf '%s' "$out" | jq -c '.hookSpecificOutput.permissionDecision // .permissionDecision // "abstain"')"
}

# pair <assignment> <git args> — inline prefix, then the export spelling. MUST match.
pair() {
  probe "$1 git $2"
  probe "export $1; git $2"
}

echo
echo '=== B1. THE CONFIG-SOURCE FAMILY (pg2-a12rl): pairs MUST MATCH ==='
pair 'GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil' 'status'
pair 'GIT_CONFIG_GLOBAL=/tmp/evil.cfg' 'status'
pair 'GIT_CONFIG_PARAMETERS=core.pager=EVIL' 'log'

echo
echo '=== B2. THE PROGRAM-NAMING FAMILY (pg2-6c85x): pairs MUST MATCH ==='
pair 'GIT_PAGER=/tmp/evil' 'log'
pair 'GIT_EXTERNAL_DIFF=/tmp/evil' 'diff'
pair 'GIT_SSH_COMMAND=/tmp/evil' 'fetch origin'
pair 'GIT_ASKPASS=/tmp/evil' 'fetch origin'

echo
echo '=== B3. THE REDIRECT TWIN (GIT_DIR / GIT_WORK_TREE): pairs MUST MATCH ==='
pair 'GIT_DIR=/other' 'commit -m msg'
pair 'GIT_WORK_TREE=/other' 'commit -m msg'
pair 'GIT_DIR=/other' 'log'

echo
echo '=== B4. EVERY SEPARATOR the bead names ==='
probe 'export GIT_PAGER=/tmp/evil; git log'
probe 'export GIT_PAGER=/tmp/evil && git log'
probe "$(printf 'export GIT_PAGER=/tmp/evil\ngit log')" # the NEWLINE separator; `~` in the label is the newline
probe 'export GIT_PAGER=/tmp/evil; echo one; echo two; git log'

echo
echo '=== B5. FAIL-CLOSED: the effect cannot be determined -> MUST NOT be allow ==='
probe 'export GIT_PAGER=$X; git log'
probe 'export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=$K GIT_CONFIG_VALUE_0=$V; git status'
probe 'export GIT_PAGER; git log'
probe 'GIT_PAGER=/tmp/evil; export GIT_PAGER; git log'
probe 'declare -x GIT_PAGER=/tmp/evil; git log'
probe 'typeset -x GIT_PAGER=/tmp/evil; git log'
probe 'declare GIT_PAGER=/tmp/evil; git log'
probe 'declare -x GIT_EDITOR=true; git commit --amend'
probe 'set -a; GIT_PAGER=/tmp/evil; git log'
probe 'set -o allexport; GIT_CONFIG_GLOBAL=/tmp/evil.cfg; git status'
probe 'git log; export GIT_PAGER=/tmp/evil; git log'

echo
echo '=== B6. BOUNDARY: measured NOT to reach git -> MUST stay allow ==='
probe 'GIT_CONFIG_COUNT=1; git status'
probe 'GIT_PAGER=/tmp/evil; git log'
probe 'GIT_PAGER=/tmp/evil && git log'
probe 'GIT_DIR=/other; git commit -m msg'
probe 'export GIT_PAGER=/tmp/evil | cat; git log'
probe 'git log; export GIT_PAGER=/tmp/evil'
probe 'export FOO=bar; git status'
probe 'export GIT_PAGERX=/tmp/evil; git log'
probe 'export git_pager=/tmp/evil; git log'
probe 'export GIT_SSH_VARIANT=ssh; git fetch origin'

echo
echo '=== B7. THE pg2-6qh3p EDITOR CARVE-OUT reaches the export route too ==='
probe 'GIT_EDITOR=true git commit --amend'
probe 'export GIT_EDITOR=true; git commit --amend'
probe 'export GIT_EDITOR=:; git rebase --skip'
probe 'export GIT_EDITOR=/tmp/evil; git commit --amend'

echo
echo '=== B8. THE ONE PREDICATE DELIBERATELY NOT WIDENED (a relaxation, own ruling) ==='
probe 'GIT_SEQUENCE_EDITOR=: git rebase -i main'
probe 'export GIT_SEQUENCE_EDITOR=: ; git rebase -i main'

echo
echo '=== B9. TEXT IS NOT AN OPERATION (pg2-5b901), now over EXPRESSION scope ==='
probe 'git commit -m "screen the export GIT_CONFIG_COUNT=1 route (pg2-xjt1s)"'
probe 'echo "export GIT_PAGER=/tmp/evil" > notes.txt; git status'
