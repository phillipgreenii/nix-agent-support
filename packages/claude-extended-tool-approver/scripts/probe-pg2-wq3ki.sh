#!/usr/bin/env bash
# probe-pg2-wq3ki.sh — verification probe for bead pg2-wq3ki: a `git commit` whose
# target directory the command ITSELF writes down
# (`WT=/abs/worktree && git -C "$WT" commit`) resolves and stops costing a gate, while
# every neighbouring spelling keeps the verdict it had.
#
# THREE HALVES, because the bead's claim has three.
#
#  1. WHAT THE SHELL AND GIT ACTUALLY DO WITH AN EMPTY/UNSET VALUE. This is the half
#     that DECIDES THE SCOPE, and it is the reason the `$(…)` derivation is DECLINED
#     rather than implemented (see internal/rules/primarycommit/dirresolve.go's DECLINED
#     section). A derivation that could be WRONG is only admissible if being wrong is
#     fail-safe, and it is NOT: `git -C ""` is a NO-OP that runs in the current
#     directory, and unquoted `cd $UNSET` is `cd` with no argument, i.e. $HOME. In the
#     layout this rule protects the current directory IS the canonical clone on its
#     primary branch. Only the quoted `cd "$EMPTY"` form fails closed (bash refuses a
#     null directory), and one form out of three is not a fail-safe.
#  2. WHAT THE HOOK ANSWERS, from a binary built out of THIS worktree, with the RAW
#     emitted output printed rather than a decision word — a withdrawn Approve
#     serializes to the empty object `{}` and a decision-only probe cannot tell `{}`
#     from a missing key.
#  3. WHETHER THE RESOLVED SPELLING AGREES WITH THE LITERAL ONE. This is the whole
#     safety argument for a bead that is deliberately LESS restrictive: the resolution
#     must not invent a permission, it must only make `git -C "$WT" commit` reach the
#     verdict `git -C /abs/worktree commit` already reached. Each pair below is printed
#     side by side so a disagreement is visible rather than argued.
#
# ISOLATION: the git half runs entirely inside a throwaway repo under a mktemp
# directory — never a real checkout. The hook half points XDG_DATA_HOME at a throwaway
# directory so probe rows land in a scratch asks.db and never reach the real corpus.
# NOTHING here runs `ceta evaluate`/`baseline`/`compare`: all three open the shared
# production asklog READ-WRITE (bead pg2-cbihz).
#
# THE CORPUS REPLAY IS NOT RUN HERE — it needs a SECOND tree to diff against, so it
# cannot live in a one-tree script. The recipe, and the measured transition set, are
# recorded at the bottom of this file.
set -uo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-wq3ki-probe.XXXXXX")"
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

echo '=== 1. THE EMPTY-VALUE MEASUREMENT THAT DECLINES THE $(…) DERIVATION ==='
echo '--- 1a. quoted `cd "$EMPTY"`: FAILS, so an && chain short-circuits (fail-safe)'
(
  cd "$worktree" || exit
  E=""
  # shellcheck disable=SC2164 # the cd FAILING, and the shell staying put, IS the measurement
  cd "$E" 2>&1 | sed 's/^/    /'
  printf '    exit=%s pwd=%s\n' "$?" "$PWD"
)
echo '--- 1b. UNQUOTED `cd $UNSET`: succeeds and lands in $HOME (NOT fail-safe)'
# `set +u` is part of the measurement, not a workaround: an agent's Bash call runs
# without `set -u`, so an unset variable expands to nothing rather than erroring.
(
  set +u
  cd "$worktree" || exit
  unset U
  # shellcheck disable=SC2086,SC2164 # the unquoted form, and where it lands, ARE the measurement
  cd $U
  printf '    exit=%s pwd=%s (HOME=%s)\n' "$?" "$PWD" "$HOME"
)
echo '--- 1c. `git -C ""`: a NO-OP — the command runs in the CURRENT directory (NOT fail-safe)'
(
  cd "$worktree" || exit
  printf '    git -C "" rev-parse --show-toplevel -> %s (exit=%s)\n' "$(git -C "" rev-parse --show-toplevel)" "$?"
  git -C "" commit -q --allow-empty -m "landed via an empty -C"
  printf '    commit landed in: %s\n' "$(git -C "" rev-parse --show-toplevel)"
  printf '    HEAD of that repo: %s\n' "$(git log --oneline -1)"
)
echo "--- 1d. and what a FAILED substitution assigns, which is what 1a-1c then receive:"
(
  cd "$work" || exit # not a repo at all
  V="$(git rev-parse --show-toplevel 2>/dev/null)"
  printf '    outside a work tree: V=[%s] (empty), git rev-parse exit was nonzero\n' "$V"
)
echo '    => a $(…) derivation that is wrong is wrong in the direction of the CWD,'
echo "       which in this layout is the canonical clone on primary. Hence DECLINED."
echo

# ---------------------------------------------------------------------------
bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)
export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

# probe <mode> <cwd> <command>
# The fixture lives under a mktemp directory whose name is noise, so both the echoed
# command and the reason are printed with that prefix collapsed to <CANON>/<WT>. The
# hook still receives the real absolute paths — only the DISPLAY is shortened, and the
# raw decision object is printed verbatim.
probe() {
  local mode="$1" cwd="$2" cmd="$3" out shown
  out="$(jq -cn --arg c "$cmd" --arg w "$cwd" --arg m "$mode" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-wq3ki-probe",cwd:$w,permission_mode:$m,tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  shown="$(printf '%s' "$cmd" | tr '\n' ';' | sed -e "s|$worktree|<WT>|g" -e "s|$canonical|<CANON>|g")"
  out="$(printf '%s' "$out" | sed -e "s|$worktree|<WT>|g" -e "s|$canonical|<CANON>|g")"
  printf '  [%-17s] %s\n      -> %s\n' "$mode" "$shown" "${out:0:220}"
}

echo "=== 2. THE FIX: a target the command establishes literally no longer gates ==="
for mode in default bypassPermissions; do
  probe "$mode" "$canonical" "WT=$worktree && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "WT=$worktree; git -C \"\${WT}\" commit -m x"
  probe "$mode" "$canonical" "WT=$worktree && cd \"\$WT\" && git commit -m x"
  probe "$mode" "$canonical" "export WT=$worktree && git -C \"\$WT\" commit -m x"
done
echo

echo "=== 3. NOT WIDENED: a target the command does NOT establish keeps its verdict ==="
for mode in default bypassPermissions; do
  probe "$mode" "$canonical" 'git -C "$WT" commit -m x'
  probe "$mode" "$canonical" 'WT=$(git rev-parse --show-toplevel) && git -C "$WT" commit -m x'
  probe "$mode" "$canonical" 'WT=$(mktemp -d) && cd "$WT" && git commit -m x'
  probe "$mode" "$canonical" "WT=$worktree git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "WT=$worktree | cat && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "WT=$worktree && WT=\$(mktemp -d) && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "WT=$worktree && git -C \"\${WT:-/tmp}\" commit -m x"
done
echo

echo "=== 4. NOT LAUNDERED: resolving to the CANONICAL clone on primary still denies ==="
echo "    (the resolution makes the rule KNOW it is a primary commit — the opposite of an escape)"
for mode in default bypassPermissions auto dontAsk; do
  probe "$mode" "$canonical" "WT=$canonical && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "WT=$canonical && cd \"\$WT\" && git commit -m x"
done
echo

echo "=== 5. THE SAFETY ARGUMENT: the resolved spelling AGREES with the literal one ==="
echo "    Each pair is (variable spelling, literal spelling). They MUST match."
for mode in default bypassPermissions; do
  probe "$mode" "$canonical" "WT=$worktree && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "git -C $worktree commit -m x"
  probe "$mode" "$canonical" "WT=$canonical && git -C \"\$WT\" commit -m x"
  probe "$mode" "$canonical" "git -C $canonical commit -m x"
  probe "$mode" "$canonical" "WT=$worktree && cd \"\$WT\" && git commit -m x"
  probe "$mode" "$canonical" "cd $worktree && git commit -m x"
done
echo

echo "=== 6. SCOPE: a non-commit subcommand is none of this rule's business ==="
probe default "$canonical" "WT=$worktree && git -C \"\$WT\" status"
probe default "$canonical" "WT=$worktree && git -C \"\$WT\" push origin feat"
echo

echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"

# ===========================================================================
# THE CORPUS REPLAY (run once per tree, then diff — it cannot be one script)
#
# READ-ONLY extraction from the shared production asklog, then the harness in
# internal/setup/replay_test.go, which redirects XDG_DATA_HOME itself:
#
#   sqlite3 "file:$HOME/.local/share/claude-extended-tool-approver/asks.db?immutable=1" \
#     "VACUUM INTO '/scratch/snap.db';"
#   sqlite3 -noheader /scratch/snap.db "SELECT json_object('command', c, 'cwd', w,
#       'permission_mode', COALESCE(pm,'')) FROM (
#       SELECT DISTINCT json_extract(tool_input_json,'\$.command') AS c, cwd AS w,
#         permission_mode AS pm FROM tool_decisions
#       WHERE excluded=0 AND tool_name='Bash'
#         AND json_extract(tool_input_json,'\$.command') IS NOT NULL);" > /scratch/replay-rows.jsonl
#   go test -c -o /scratch/setup.test ./internal/setup     # once per tree
#   CETA_REPLAY_SNAPSHOT=/scratch/replay-rows.jsonl CETA_REPLAY_OUT=/scratch/verdicts-<tree>.tsv \
#     /scratch/setup.test -test.run TestCorpusVerdictReplay -test.timeout 6h -test.v
#   join -t $'\t' -j 1 <(sort -k1,1 before.tsv) <(sort -k1,1 after.tsv) \
#     | awk -F'\t' '$2!=$4 {print $2"("$3") -> "$4"("$5")"}' | sort | uniq -c
#
# MEASURED 2026-08-13, 194233 distinct (command, cwd, mode) rows — 133637 replayable,
# 60596 skipped for a working directory that no longer exists (the path evaluator
# classifies against a real filesystem, so no verdict exists for those; the replayable
# subset is NOT the whole corpus).
#
#   92  ask(primary-commit)     -> abstain(engine/-)
#   48  ask(primary-commit)     -> approve(engine)
#    3  reject(primary-commit)  -> approve(engine)
#    1  reject(primary-commit)  -> abstain(engine)
#    1  approve(engine)         -> abstain(-)          <- MORE restrictive
#
# 144 of the 145 are the authorized shape: a literal absolute assignment in the SAME
# command, then `git -C "$VAR" commit` or `cd "$VAR" … git commit` — the workforest /
# drain-beads idiom the bead was filed for. The 4 `reject` rows were auto-mode commits
# into a linked worktree, i.e. exactly the hard denies the bead reports. The single
# `approve -> abstain` row is the ENGINE's `cd` expansion: `WT=/Volumes/…/worktrees/…;
# cd "$WT"; git -C "$WT" diff …` used to be judged against the FICTIONAL cwd
# `<cwd>/$WT`, which happened to sit inside the project root and clear the path check;
# judged against the real worktree it no longer does — and it now agrees with the
# LITERAL spelling of the same command, which already answered `abstain` on both trees.
# That agreement was measured for all three interesting classes (section 5's pairs, and
# the same pairs replayed on both trees).
