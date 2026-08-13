#!/usr/bin/env bash
# probe-pg2-ft2hl.sh — verification probe for bead pg2-ft2hl: the `declare` / `typeset`
# spelling of the in-command assignment pg2-wq3ki taught the chain to read
# (`declare WT=/abs/worktree && git -C "$WT" commit`), and the spellings that are
# DECLINED or refused because their own semantics rewrite the value.
#
# TWO HALVES, because the bead's claim has two.
#
#  1. WHAT BASH ACTUALLY DOES WITH EACH ASSIGNMENT BUILTIN. This is the half that
#     DECIDES THE SCOPE. Every refusal in
#     internal/cmdparse/incommandvars.go's assignmentBuiltinReads / declWrites is
#     justified by one of these measurements, and each is a case where the value the
#     command WRITES DOWN is not the value bash ends up holding — so reading it would be
#     a CONFIDENTLY WRONG answer, which is worse than the prompt it saves.
#  2. WHAT THE HOOK ANSWERS, from a binary built out of THIS worktree, with the RAW
#     emitted output printed rather than a decision word — a withdrawn Approve
#     serializes to the empty object `{}` and a decision-only probe cannot tell `{}`
#     from a missing key (the same reason probe-pg2-wq3ki.sh prints it raw).
#
# ISOLATION: the git half runs entirely inside a throwaway repo under a mktemp
# directory — never a real checkout. The hook half points XDG_DATA_HOME at a throwaway
# directory so probe rows land in a scratch asks.db and never reach the real corpus.
# NOTHING here runs `ceta evaluate`/`baseline`/`compare`: all three open the shared
# production asklog READ-WRITE (bead pg2-cbihz).
#
# THE CORPUS REPLAY IS NOT RUN HERE — it needs a SECOND tree to diff against, so it
# cannot live in a one-tree script. The recipe is recorded at the bottom of
# scripts/probe-pg2-wq3ki.sh and applies unchanged; what a replay for THIS bead must
# look for is recorded at the bottom of this file.
set -uo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-ft2hl-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
canonical="$work/canonical"
worktree="$canonical/.worktrees/feat"
mkdir -p "$canonical"
git -C "$canonical" init -q -b main
git -C "$canonical" config user.email probe@invalid
git -C "$canonical" config user.name probe
git -C "$canonical" commit -q --allow-empty -m init
git -C "$canonical" worktree add -q -b feat "$worktree"

echo "git: $(git --version)"
echo "bash: $BASH_VERSION"
echo "canonical clone (on primary): $canonical"
echo "linked worktree (on feat):    $worktree"
echo

echo '=== 1. THE BASH MEASUREMENTS THAT DECIDE WHICH SPELLINGS MAY BE READ ==='
# Each row prints the value bash actually holds AFTER the builtin, next to the text the
# command wrote down. A mismatch is a spelling this seam must NOT read.
measure() { printf '  %-46s -> [%s]\n' "$1" "$(bash -c "$2" 2>&1 | tr '\n' ' ')"; }

echo '--- 1a. READ: the unflagged form is a plain shell-variable assignment'
measure 'declare WT=/abs/path' 'declare WT=/abs/path; echo "$WT"'
measure 'typeset WT=/abs/path' 'typeset WT=/abs/path; echo "$WT"'
measure 'WT=/first; declare WT' 'WT=/first; declare WT; echo "$WT"' # a naked name is a NO-OP

echo '--- 1b. REFUSED: a FLAG makes the stored value something else entirely'
measure 'declare -i N=5+5' 'declare -i N=5+5; echo "$N"'                     # ARITHMETIC
measure 'declare -l L=ABC' 'declare -l L=ABC; echo "$L"'                     # case-FOLDED
measure 't=/real; declare -n ref=t' 't=/real; declare -n ref=t; echo "$ref"' # an ALIAS
measure 'WT=/first; declare -u WT; WT=/second' \
  'WT=/first; declare -u WT; WT=/second; echo "$WT"'                   # the NEXT assignment folds
measure 'declare -a arr=(/a /b)' 'declare -a arr=(/a /b); echo "$arr"' # the FIRST ELEMENT
measure 'arr=(/a /b)' 'arr=(/a /b); echo "$arr"'                       # …plain spelling too
measure 'WT="(/a /b)"' 'WT="(/a /b)"; echo "$WT"'                      # …but QUOTED is a scalar

echo '--- 1c. DECLINED by operator ruling: local and readonly'
measure 'local WT=/x (outside a function)' 'local WT=/x; echo "rc=$?"' # a bash ERROR
measure 'readonly WT=/x; WT=/y' 'readonly WT=/x; WT=/y; echo "$WT"'    # the reassignment FAILS

echo '--- 1d. REFUSED: a PREFIX assignment makes declare ephemeral — but not export'
measure 'WT=/first; WT=/x declare WT=/y' 'WT=/first; WT=/x declare WT=/y; echo "$WT"'
measure 'WT=/first; OTHER=/x declare WT=/y' 'WT=/first; OTHER=/x declare WT=/y; echo "$WT"'
measure 'WT=/first; WT=/x export WT=/y' 'WT=/first; WT=/x export WT=/y; echo "$WT"'
echo '    => the SAME-name prefix discards the write and a DIFFERENT-name prefix keeps it;'
echo '       this seam does not model which, so it reads neither. `export` is a POSIX'
echo '       SPECIAL builtin, so assignments before it persist — hence it is unaffected.'
echo

# ---------------------------------------------------------------------------
bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)
export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

# probe <mode> <cwd> <command> — the fixture lives under a mktemp directory whose name is
# noise, so both the echoed command and the emitted object are printed with that prefix
# collapsed to <CANON>/<WT>. The hook still receives the real absolute paths.
probe() {
  local mode="$1" cwd="$2" cmd="$3" out shown
  out="$(jq -cn --arg c "$cmd" --arg w "$cwd" --arg m "$mode" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-ft2hl-probe",cwd:$w,permission_mode:$m,tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  shown="$(printf '%s' "$cmd" | tr '\n' ';' | sed -e "s|$worktree|<WT>|g" -e "s|$canonical|<CANON>|g")"
  out="$(printf '%s' "$out" | sed -e "s|$worktree|<WT>|g" -e "s|$canonical|<CANON>|g")"
  printf '  [%-17s] %s\n      -> %s\n' "$mode" "$shown" "${out:0:200}"
}

echo "=== 2. THE FIX: the declare/typeset spelling of a resolved target no longer gates ==="
for mode in default bypassPermissions; do
  probe "$mode" "$canonical" "declare WT=$worktree && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "typeset WT=$worktree && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "declare WT=$worktree && cd \"\$WT\" && git commit -m x"
done
echo

echo "=== 3. NOT WIDENED: every spelling whose value bash rewrites keeps its verdict ==="
for mode in default bypassPermissions; do
  probe "$mode" "$canonical" "local WT=$worktree && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "readonly WT=$worktree && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "nameref WT=$worktree && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "declare -i WT=$worktree && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "declare -n WT=$worktree && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "declare -- WT=$worktree && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "WT=/x declare WT=$worktree && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "declare WT=$worktree | cat && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" 'declare WT=$(mktemp -d) && git -C "$WT" commit -m x'
done
echo

echo "=== 4. REVOKED, not kept: an unreadable reassignment takes the earlier literal away ==="
echo "    (before this bead the declare leaf was SKIPPED, so /first survived a value bash had changed)"
for mode in default bypassPermissions; do
  probe "$mode" "$canonical" "WT=$worktree && declare -i WT=5+5 && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "WT=$worktree && readonly WT=/elsewhere && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "WT=($worktree /other) && git -C \"\$WT\" commit -m x"
done
echo

echo "=== 5. NOT LAUNDERED: resolving to the CANONICAL clone on primary still denies ==="
for mode in default bypassPermissions auto dontAsk; do
  probe "$mode" "$canonical" "declare WT=$canonical && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "typeset WT=$canonical && cd \"\$WT\" && git commit -m x"
done
echo

echo "=== 6. AGREEMENT: the declare spelling answers as the PLAIN spelling does ==="
echo "    Each pair is (declare spelling, plain spelling). The declare leaf is not in"
echo "    safe-commands' allowlist — deliberately, since a \`declare -x LD_PRELOAD=…\` is an"
echo "    env-assignment vector the env-var guard cannot see — so it contributes an ABSTAIN"
echo "    where a bare assignment contributes nothing. That difference is RESTRICTIVE."
for mode in default bypassPermissions; do
  probe "$mode" "$canonical" "declare WT=$worktree && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "WT=$worktree && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "declare WT=$canonical && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "WT=$canonical && git -C \"\$WT\" commit -m x"
done
echo

echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"

# ===========================================================================
# WHAT A FULL CORPUS REPLAY MUST LOOK FOR (recipe: scripts/probe-pg2-wq3ki.sh)
#
# Toward ALLOW, and ONLY here: rows whose command contains an unflagged
# `declare NAME=<literal>` / `typeset NAME=<literal>` followed in the SAME command by
# `git -C "$NAME" commit…` or `cd "$NAME" … git commit` — the same workforest /
# drain-beads idiom pg2-wq3ki relieved, one spelling over.
#
# Toward ABSTAIN/ASK/REJECT is permitted anywhere, and TWO new sources of it are
# EXPECTED rather than suspicious:
#
#   - a plain `NAME=(a b)` array assignment consumed as `"$NAME"`. Before this bead the
#     seam bound the parenthesised TEXT; bash binds the FIRST ELEMENT, so the binding was
#     confidently wrong and is now refused (literalAssignedValue's array guard).
#   - a `local`/`readonly`/`nameref`/flagged-`declare` reassignment of a name an earlier
#     leaf bound literally. Before this bead the decl leaf was skipped and the earlier
#     literal SURVIVED it; it is now revoked.
#
# A row moving toward allow that is NOT the first shape is a finding: the relief is
# confined to the `declare`/`typeset` spelling this bead authorizes.
