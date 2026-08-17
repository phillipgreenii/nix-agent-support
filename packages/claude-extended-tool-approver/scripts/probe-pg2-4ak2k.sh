#!/usr/bin/env bash
# probe-pg2-4ak2k.sh — verification probe for bead pg2-4ak2k: a leaf now carries a
# SUBSHELL SCOPE PATH (cmdparse.ParsedCommand.SubshellScope), and
# cmdparse.InCommandVars uses it to stop treating an assignment made INSIDE a `( … )`
# that has already CLOSED as still bound in the consuming leaf's scope.
#
#   ( WT=/abs/worktree ) ; git -C "$WT" commit -m x
#
# is a command that is ALREADY BROKEN at runtime (bash discards the subshell's `WT`
# the moment `)` closes, so `$WT` is empty and `git -C ""` runs a no-op in the CURRENT
# directory — pg2-wq3ki's own measurement of why that is not inert). Before this bead
# the seam believed the written-down value anyway; after it, the same command reaches
# the UNRESOLVED verdict every OTHER unreadable target already gets.
#
# TWO HALVES, because the bead's claim has two.
#
#  1. THE HOOK ANSWERS, from a binary built out of THIS worktree, for the exact three
#     shapes the bead's acceptance criteria name: the newly-closed case (must now be
#     unresolved), the same-subshell case (must still resolve), and the
#     enclosing-scope case (a top-level assignment, consumed inside a nested subshell,
#     must still resolve) — plus a fourth, sibling-subshells, that a bare depth
#     counter could not have told apart from the same-subshell case: both are at
#     nesting depth 1, but they are DIFFERENT scopes, and only a scope PATH (not a
#     depth number) can tell that.
#  2. THE CORPUS REPLAY: an ENGINE A/B between the pre-fix tree (main) and this
#     worktree, over the WHOLE corpus (not filtered to one permission_mode — this
#     defect is not mode-specific, it is about which leaves establish a variable at
#     all). The bead's own measurement claim is that ZERO rows move, because the shape
#     — an assignment inside a subshell that CLOSES before the consuming leaf runs —
#     essentially never appears in real traffic. This section CONFIRMS that rather
#     than assuming it.
#
# ISOLATION: half 1 runs entirely inside a throwaway repo under a mktemp directory,
# with XDG_DATA_HOME pointed at a scratch dir so probe rows never reach the real
# corpus. Half 2 NEVER opens the production asklog read-write: it is APFS-cloned
# (`cp -c`) into a private snapshot, integrity-checked, and only that snapshot is
# read from — `ceta evaluate`/`baseline`/`compare` (which open the live db read-write)
# are not used anywhere in this script, only TestCorpusVerdictReplay, which redirects
# XDG_DATA_HOME to a temp dir per bead pg2-cbihz's rule.
set -uo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-4ak2k-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
canonical="$work/canonical"
worktree="$canonical/.worktrees/feat"
mkdir -p "$canonical"
git -C "$canonical" init -q -b main
git -C "$canonical" config user.email probe@invalid
git -C "$canonical" config user.name probe
git -C "$canonical" commit -q --allow-empty -m init
git -C "$canonical" worktree add -q -b feat "$worktree" >/dev/null

echo "git: $(git --version)"
echo "bash: $BASH_VERSION"
echo "canonical clone (on primary): $canonical"
echo "linked worktree (on feat):    $worktree"
echo

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)
export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

# probe <mode> <cwd> <command>
probe() {
  local mode="$1" cwd="$2" cmd="$3" out shown
  out="$(jq -cn --arg c "$cmd" --arg w "$cwd" --arg m "$mode" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-4ak2k-probe",cwd:$w,permission_mode:$m,tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  shown="$(printf '%s' "$cmd" | tr '\n' ';' | sed -e "s|$worktree|<WT>|g" -e "s|$canonical|<CANON>|g")"
  out="$(printf '%s' "$out" | sed -e "s|$worktree|<WT>|g" -e "s|$canonical|<CANON>|g")"
  printf '  [%-17s] %s\n      -> %s\n' "$mode" "$shown" "${out:0:220}"
}

echo "=== 1. AC1: assignment in a subshell that ALREADY CLOSED -> now UNRESOLVED ==="
echo "    (before this bead: silently resolved to the worktree, hiding a broken command)"
for mode in default bypassPermissions auto; do
  probe "$mode" "$canonical" "( WT=$worktree ) ; git -C \"\$WT\" commit -m x"
done
echo

echo "=== 2. AC2: assignment AND consumption in the SAME subshell -> still resolves ==="
for mode in default bypassPermissions; do
  probe "$mode" "$canonical" "( WT=$worktree && cd \"\$WT\" && git commit -m x )"
done
echo

echo "=== 3. AC (implied by 'enclosing scope'): top-level assignment, consumed INSIDE a nested subshell -> still resolves ==="
for mode in default bypassPermissions; do
  probe "$mode" "$canonical" "WT=$worktree; (git -C \"\$WT\" commit -m x)"
done
echo

echo "=== 4. NOT LAUNDERED: a same-subshell resolution to the CANONICAL clone still hard-denies ==="
for mode in default bypassPermissions; do
  probe "$mode" "$canonical" "( WT=$canonical && git -C \"\$WT\" commit -m x )"
done
echo

echo "=== 5. UNCHANGED, and still the closing case's twin: SIBLING subshells never share scope ==="
echo "    (a bare depth counter could not tell this from case 2 — both are depth 1 — but the"
echo "     two subshells are DIFFERENT scopes, so the second's read must still be unresolved)"
probe default "$canonical" "(WT=$worktree); (git -C \"\$WT\" commit -m x)"
echo

echo "=== 6. Pipeline-stage exclusion (a different, already-solved mechanism) is untouched ==="
probe default "$canonical" "WT=$worktree | cat && git -C \"\$WT\" commit -m x"
echo

echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"
echo

# ===========================================================================
# THE CORPUS REPLAY — an ENGINE A/B between main (pre-fix) and this worktree
# (post-fix), over the WHOLE corpus (not one permission_mode: this defect is not
# mode-specific).
asklog_src="${PG4AK2K_ASKLOG:-$HOME/.local/share/claude-extended-tool-approver/asks.db}"
if [[ ! -f $asklog_src ]]; then
  echo "no asklog at $asklog_src — nothing to replay against. Set PG4AK2K_ASKLOG to override." >&2
  exit 1
fi

echo "=== 7. SNAPSHOT the production asklog (never opened read-write by this script) ==="
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

echo "=== 8. BUILD both trees (base = main, post = this worktree) ==="
base_ref="${PG4AK2K_BASE_REF:-main}"
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
echo "  files that differ from $base_ref (expected: cmdparse's 3 files + git.go's comment + this script):"
diff -rq "$base_src" "$pkg_root" --exclude=.git 2>&1 | sed -n 's/^Files .*base-src\/\(.*\) and .*/    \1/p'
echo

(cd "$base_src" && GOFLAGS=-mod=mod go test -c -o "$work/setup-pre.test" ./internal/setup) || exit 1
(cd "$pkg_root" && go test -c -o "$work/setup-post.test" ./internal/setup) || exit 1
echo "  built: setup-pre.test (base=$base_ref), setup-post.test (this worktree)"
echo

echo "=== 9. EXTRACT every distinct (command, cwd, permission_mode) Bash row and REPLAY both trees ==="
snap_out="$work/replay-rows.jsonl"
sqlite3 -noheader "$snapshot" "SELECT json_object('command', c, 'cwd', w,
    'permission_mode', COALESCE(pm,'')) FROM (
    SELECT DISTINCT json_extract(tool_input_json,'\$.command') AS c, cwd AS w,
      permission_mode AS pm FROM tool_decisions
    WHERE excluded=0 AND tool_name='Bash'
      AND json_extract(tool_input_json,'\$.command') IS NOT NULL);" >"$snap_out"
n_rows="$(wc -l <"$snap_out" | tr -d ' ')"
echo "  $n_rows distinct (command, cwd, permission_mode) rows extracted"

pre_out="$work/verdicts-pre.tsv"
post_out="$work/verdicts-post.tsv"
CETA_REPLAY_SNAPSHOT="$snap_out" CETA_REPLAY_OUT="$pre_out" \
  "$work/setup-pre.test" -test.run TestCorpusVerdictReplay -test.timeout 30m 2>/dev/null |
  grep REPLAY | sed 's/^/  pre:  /'
CETA_REPLAY_SNAPSHOT="$snap_out" CETA_REPLAY_OUT="$post_out" \
  "$work/setup-post.test" -test.run TestCorpusVerdictReplay -test.timeout 30m 2>/dev/null |
  grep REPLAY | sed 's/^/  post: /'
echo

echo "=== 10. THE TRANSITION TABLE (the whole corpus — this defect is not mode-specific) ==="
transitions="$(join -t $'\t' -j 1 <(sort -k1,1 "$pre_out") <(sort -k1,1 "$post_out") |
  awk -F'\t' '$2!=$4 {print $2"("$3") -> "$4"("$5")"}' | sort | uniq -c | sort -rn)"
if [[ -z $transitions ]]; then
  echo "  (no rows moved — every row's verdict is byte-identical on both trees)"
else
  echo "$transitions"
fi
echo
n_moved="$(printf '%s\n' "$transitions" | awk 'NF>0 {s+=$1} END {print s+0}')"
echo "  total moved rows: $n_moved  (the bead's own measurement expects 0)"
if [[ $n_moved -ne 0 ]]; then
  echo "  *** rows moved — report them precisely rather than reconciling silently ***"
fi
echo

echo "asklog isolation: this script never opened $asklog_src read-write; all replay ran"
echo "against the snapshot at (now deleted) $snapshot, via TestCorpusVerdictReplay with"
echo "XDG_DATA_HOME redirected to a temp dir."
