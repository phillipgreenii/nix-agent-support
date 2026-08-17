#!/usr/bin/env bash
# probe-pg2-68w11.sh — verification probe for bead pg2-68w11: primarycommit's and
# primarypush's identical `autoApprovingModes` maps wrongly included "auto". The
# operator confirmed directly (2026-08-14, 2026-08-15) that Claude Code's `auto`
# permission mode PROMPTS on an Ask verdict from this hook — it does NOT silently
# accept it the way `bypassPermissions` measurably does (pg2-2t9wz). So hard-Rejecting
# a primary commit/push in `auto` mode was based on a wrong premise; it should Ask.
#
# THE FIX, and the trap a naive read of the bead would fall into:
#
#   - Just deleting "auto" from the map is NOT enough. Both rules use ONE boolean to
#     decide TWO different branches: `findingUnresolved` (already correct: the
#     non-silently-accepting branch already returned Ask, so removing "auto" from
#     that map alone fixes this branch for free) and `findingPrimary` (the ordinary
#     "a plain commit/push lands on the canonical primary" case), whose NON-accepting
#     branch was `return hookio.NotApplicable()` — deferring to the generic `git` rule
#     behind it, which then judges the command on its own and can reach APPROVE with
#     NO PROMPT AT ALL. That deferral is deliberate for default/plan/acceptEdits/empty
#     (R-6: "a human is directing this"), but "auto" is an UNATTENDED mode — extending
#     that same trust to it is a MUCH bigger, unauthorized widening than "Reject
#     becomes Ask": it was measured below (section 2) and is NOT what this bead
#     authorizes.
#   - The fix therefore keeps "auto" in a (renamed, still exported) GatedModes set —
#     primary commits/pushes in `auto` mode still get a DECISIVE verdict, never a
#     deferral — and only changes WHICH decisive verdict: AutoApprovingModes (the
#     strict subset that silently accepts an Ask: bypassPermissions, dontAsk) keeps
#     the hard Reject; the rest of GatedModes ("auto") now gets Ask instead.
#
# THE MEASUREMENT METHOD is the engine A/B replay from
# docs/engine-ab-replay-runbook.md: build the pre-fix and post-fix trees out-of-repo,
# replay the SAME corpus snapshot through both via internal/setup's
# TestCorpusVerdictReplay (the EvaluateHook path, XDG_DATA_HOME redirected — see that
# file's doc comment for the full read-only discipline), and diff the two verdict
# streams by row index. This is an ENGINE A/B, not a logged-vs-current replay: it is
# INTERNALLY SELF-PROVING when correct — the runbook's tell-tale is that the ENTIRE
# delta is accounted for by "reject -> ask" attributed to primary-commit/primary-push,
# and NOTHING else moves.
#
# SCOPE ARGUMENT for why replaying only permission_mode='auto' rows is sufficient
# (not a shortcut that hides something): GatedModes and AutoApprovingModes are the
# SAME two maps for every key except "auto" — bypassPermissions and dontAsk are
# unchanged in both, and every other mode string (default, plan, acceptEdits, "",
# any unrecognized value) was already absent from the OLD map and is still absent
# from BOTH new maps. So for any row whose permission_mode != "auto", every map
# lookup this bead touches returns the IDENTICAL boolean before and after — the two
# engines cannot produce a different verdict for such a row, BY CONSTRUCTION. Section
# 3 below still spot-checks bypassPermissions/default/plan against the live corpus to
# confirm that construction argument empirically, rather than resting on it alone.
#
# ISOLATION (bead pg2-cbihz's rule): the production asklog is NEVER opened read-write.
# An APFS clone (`cp -c`, near-instant, copy-on-write) makes a private snapshot file
# first; `cmd_evaluate`/`baseline`/`compare` (which open
# asklog.NewStore(asklog.DefaultDBPath()) read-write) are NOT used anywhere in this
# script — only TestCorpusVerdictReplay, which redirects XDG_DATA_HOME to a temp dir
# and takes a read-only JSONL extract, not the live db handle.
set -uo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-68w11-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

asklog_src="${PG68W11_ASKLOG:-$HOME/.local/share/claude-extended-tool-approver/asks.db}"
if [[ ! -f $asklog_src ]]; then
  echo "no asklog at $asklog_src — nothing to replay against. Set PG68W11_ASKLOG to override." >&2
  exit 1
fi

echo "=== 0. SNAPSHOT the production asklog (never opened read-write by this script) ==="
snapshot="$work/asks-snapshot.db"
if cp -c "$asklog_src" "$snapshot" 2>/dev/null; then
  echo "  APFS clone (cp -c): $snapshot"
else
  echo "  APFS clone unavailable on this filesystem; falling back to a full copy" >&2
  cp "$asklog_src" "$snapshot"
fi
check="$(sqlite3 "$snapshot" 'PRAGMA quick_check;')"
echo "  quick_check: $check"
if [[ $check != ok ]]; then
  echo "  snapshot failed integrity check — aborting rather than measuring against a" >&2
  echo "  possibly-torn copy." >&2
  exit 1
fi
span="$(sqlite3 "$snapshot" "SELECT MIN(created_at) || ' .. ' || MAX(created_at) FROM tool_decisions WHERE excluded=0;")"
echo "  corpus span (excluded=0 rows): $span"
echo

echo "=== 1. BUILD both trees (base = the branch fork point, post = this worktree) ==="
base_ref="${PG68W11_BASE_REF:-main}"
base_src="$work/base-src"
mkdir -p "$base_src"
if ! git -C "$pkg_root" archive "$base_ref" | tar -x -C "$base_src"; then
  echo "git archive $base_ref FAILED" >&2
  exit 1
fi
if [[ ! -f $base_src/go.mod ]]; then
  echo "exported base tree has no go.mod at its root — layout assumption broken" >&2
  exit 1
fi

echo "  diff cmd/ (must be IDENTICAL — only the rule packages are meant to differ):"
if diff -rq "$base_src/cmd" "$pkg_root/cmd" >/tmp/pg2-68w11-cmddiff.$$ 2>&1; then
  echo "    cmd/ IDENTICAL"
else
  echo "    *** cmd/ DIFFERS — the A/B is no longer isolated to the rule packages ***"
  cat /tmp/pg2-68w11-cmddiff.$$
fi
rm -f /tmp/pg2-68w11-cmddiff.$$
echo "  files that differ overall (expected: gh.go comment + the two rule packages + their tests):"
diff -rq "$base_src" "$pkg_root" --exclude=.git 2>&1 | sed -n 's/^Files .*base-src\/\(.*\) and .*/    \1/p'
echo

(cd "$base_src" && GOFLAGS=-mod=mod go test -c -o "$work/setup-pre.test" ./internal/setup) || exit 1
(cd "$pkg_root" && go test -c -o "$work/setup-post.test" ./internal/setup) || exit 1
echo "  built: setup-pre.test (base=$base_ref), setup-post.test (this worktree)"
echo

echo "=== 2. THE TRAP, measured: a naive 'just remove auto from the map' fix ==="
echo "    (documented here for the record, NOT re-run live — it required a THIRD tree"
echo '    that no longer exists in this checkout. The transition table it produced,'
echo "    measured during this bead's development against this same snapshot:"
echo '      153 reject(primary-commit) -> approve(engine)   <- the generic git rule'
echo '                                                          approved with NO prompt'
echo '       55 reject(primary-commit) -> abstain(engine)   <- {} is auto-accepted too'
echo '        8 reject(primary-commit) -> abstain(-)'
echo '        5 reject(primary-commit) -> abstain(safe-commands)'
echo '        2 reject(primary-commit) -> abstain(git)'
echo '       24 reject(primary-commit) -> ask(primary-commit)  <- the only correct class'
echo "    216 of 240 non-'ask' movements were toward approve/abstain — the naive fix was"
echo "    caught by THIS SECTION's construction argument and fixed before landing (see"
echo "    the GatedModes vs AutoApprovingModes split in primarycommit.go)."
echo

echo "=== 3. EXTRACT distinct auto-mode Bash rows and REPLAY both trees ==="
snap_out="$work/replay-rows-auto.jsonl"
sqlite3 -noheader "$snapshot" "SELECT json_object('command', c, 'cwd', w,
    'permission_mode', COALESCE(pm,'')) FROM (
    SELECT DISTINCT json_extract(tool_input_json,'\$.command') AS c, cwd AS w,
      permission_mode AS pm FROM tool_decisions
    WHERE excluded=0 AND tool_name='Bash' AND permission_mode='auto'
      AND json_extract(tool_input_json,'\$.command') IS NOT NULL);" >"$snap_out"
n_auto="$(wc -l <"$snap_out" | tr -d ' ')"
echo "  $n_auto distinct (command, cwd, 'auto') rows extracted"

pre_out="$work/verdicts-pre.tsv"
post_out="$work/verdicts-post.tsv"
CETA_REPLAY_SNAPSHOT="$snap_out" CETA_REPLAY_OUT="$pre_out" \
  "$work/setup-pre.test" -test.run TestCorpusVerdictReplay -test.timeout 15m 2>/dev/null |
  grep REPLAY | sed 's/^/  pre:  /'
CETA_REPLAY_SNAPSHOT="$snap_out" CETA_REPLAY_OUT="$post_out" \
  "$work/setup-post.test" -test.run TestCorpusVerdictReplay -test.timeout 15m 2>/dev/null |
  grep REPLAY | sed 's/^/  post: /'
echo

echo "=== 4. THE TRANSITION TABLE (auto-mode rows only — see the scope argument above) ==="
transitions="$(join -t $'\t' -j 1 <(sort -k1,1 "$pre_out") <(sort -k1,1 "$post_out") |
  awk -F'\t' '$2!=$4 {print $2"("$3") -> "$4"("$5")"}' | sort | uniq -c | sort -rn)"
echo "$transitions"
echo
n_reject_to_ask="$(printf '%s\n' "$transitions" | awk '$2 ~ /^reject\(/ && $0 ~ /-> ask\(/ {s+=$1} END {print s+0}')"
n_toward_approve_or_abstain="$(printf '%s\n' "$transitions" | awk '$0 ~ /-> (approve|abstain)\(/ {s+=$1} END {print s+0}')"
echo "  reject -> ask transitions:            $n_reject_to_ask"
echo "  transitions toward approve/abstain:    $n_toward_approve_or_abstain  (MUST be 0)"
if [[ $n_toward_approve_or_abstain -ne 0 ]]; then
  echo "  *** VIOLATION: a row became MORE permissive than Ask — do not land ***"
fi
echo

echo "=== 5. CROSS-CHECK: bypassPermissions/default/plan rows must be BYTE-IDENTICAL ==="
echo "    (the construction argument in the header — spot-checked empirically here)"
other_snap="$work/replay-rows-other.jsonl"
sqlite3 -noheader "$snapshot" "SELECT json_object('command', c, 'cwd', w,
    'permission_mode', COALESCE(pm,'')) FROM (
    SELECT DISTINCT json_extract(tool_input_json,'\$.command') AS c, cwd AS w,
      permission_mode AS pm FROM tool_decisions
    WHERE excluded=0 AND tool_name='Bash'
      AND permission_mode IN ('bypassPermissions','default','plan')
      AND json_extract(tool_input_json,'\$.command') IS NOT NULL);" >"$other_snap"
other_pre="$work/verdicts-other-pre.tsv"
other_post="$work/verdicts-other-post.tsv"
CETA_REPLAY_SNAPSHOT="$other_snap" CETA_REPLAY_OUT="$other_pre" \
  "$work/setup-pre.test" -test.run TestCorpusVerdictReplay -test.timeout 15m 2>/dev/null |
  grep REPLAY | sed 's/^/  pre:  /'
CETA_REPLAY_SNAPSHOT="$other_snap" CETA_REPLAY_OUT="$other_post" \
  "$work/setup-post.test" -test.run TestCorpusVerdictReplay -test.timeout 15m 2>/dev/null |
  grep REPLAY | sed 's/^/  post: /'
if diff -q <(sort -k1,1n "$other_pre") <(sort -k1,1n "$other_post") >/dev/null; then
  echo "  IDENTICAL — confirms no other mode's verdicts moved"
else
  echo "  *** DIFFERS — the change leaked outside 'auto' mode ***"
  diff <(sort -k1,1n "$other_pre") <(sort -k1,1n "$other_post") | head -20
fi
echo

echo "asklog isolation: this script never opened $asklog_src read-write; all replay ran"
echo "against the snapshot at (now deleted) $snapshot, via TestCorpusVerdictReplay with"
echo "XDG_DATA_HOME redirected to a temp dir."

# ===========================================================================
# MEASURED 2026-08-17, snapshot spanning 2026-03-13T20:47:33Z .. 2026-08-17T17:52:59Z
# (740,651,008-byte production asks.db, APFS-cloned; PRAGMA quick_check: ok).
#
# 30653 distinct (command, cwd, 'auto') Bash rows; 26762 replayable, 3891 skipped for a
# working directory that no longer exists (both trees skip the identical set — the
# path evaluator classifies against a real filesystem, so this is not part of either
# engine's own behavior).
#
#   247  reject(primary-commit) -> ask(primary-commit)
#     2  reject(primary-push)   -> ask(primary-push)
#     1  reject(primary-commit) -> ask(git)         <- same leaf, a SIBLING rule's ask
#     1  reject(primary-commit) -> ask(env-vars)        wins the tie inside a compound
#                                                        command once primary-commit's
#                                                        own contribution stops being
#                                                        the UNIQUE reject
#   ---
#   251 total reject -> ask; 0 toward approve or abstain.
#
# Pre:  abstain=7846 approve=18152 ask=450 reject=314
# Post: abstain=7846 approve=18152 ask=701 reject= 63
#   (7846-7846=0, 18152-18152=0, 701-450=251, 314-63=251 — self-proving: the entire
#   delta is the intended reject->ask class and nothing else moved.)
#
# Section 5 (bypassPermissions/default/plan, 901 distinct rows, 496 replayable):
# byte-identical pre vs post, confirming the construction argument that only "auto"
# mode's verdicts could possibly differ.
# ===========================================================================
