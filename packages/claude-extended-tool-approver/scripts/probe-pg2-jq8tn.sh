#!/usr/bin/env bash
# probe-pg2-jq8tn.sh — verification probe for bead pg2-jq8tn: inside a $(...)/backtick
# command substitution, `git -C <path> rev-parse HEAD` put `-C` where
# cmdparse.classifySubstitutionCommand's `tokens[1]` lookup expects the git SUBCOMMAND,
# so the lookup missed and the whole body was refused even though `rev-parse` (and
# `status`, `rev-list`, `symbolic-ref`, `merge-base`, `describe`, `ls-tree`) are already
# admitted read subcommands.
#
# THE FIX: cmdparse.stripGitDashC (internal/cmdparse/parser.go) consumes zero or more
# leading `-C <path>` pairs, exposing the real subcommand to the SAME tokens[1]-exact
# lookup, then folds SubstitutionCleared (the subcommand admission) with
# readerArgsClearance(paths) (the pg2-zpct4 path-readability screen already applied to
# every fileReaderSubstitutions argv path) via minClearance — most-restrictive-wins. The
# result for an absolute -C path is SubstitutionDelegated, never an outright
# SubstitutionCleared: this seam declines to rule on path readability, and the engine's
# substitution recursion (which reaches internal/rules/git's own -C/zone handling) is
# authoritative for such a body, in both directions. SCOPED TO `-C` ONLY — no other
# leading git global option (`-c`, `--git-dir=`, `--work-tree=`, `--namespace=`,
# `--exec-path[=]`, `-p`/`--paginate`/...) is touched, per THE pg2-a5r9r RULING's
# tokens[1]-exactness (gitReadSubcommands' comment block in parser.go).
#
# THIS IS A LESS-RESTRICTIVE CHANGE. Before the fix, a substitution body on which
# classifySubstitutionCommand returned SubstitutionRefused was floored by the engine's
# foldSubstitutionScan at NoOpinion (abstain) EVEN IF full-engine recursion on the same
# body would approve it. After the fix such a body returns SubstitutionDelegated
# instead, so the floor no longer applies and the full recursion's verdict — which
# reaches internal/rules/git's own already-correct `-C` chdir/zone handling — is used
# as-is. The floor can only ever move a row from NoOpinion toward something LESS
# restrictive (Approve, or a NoOpinion/Ask/Reject the recursion independently reaches on
# its own authority) — see engine.go's foldSubstitutionScan comment on why it is keyed
# on SubstitutionRefused specifically and not on "not cleared".
#
# TWO SECTIONS:
#
#  1. THE HOOK ANSWERS, from a binary built out of THIS worktree, for every fixture the
#     bead's acceptance criteria name (the two measured examples, repeated -C, every
#     OTHER leading global option pinned as UNCHANGED, the malformed -C spellings, the
#     non-admitted-subcommand controls, and the write-flag control).
#  2. THE CORPUS REPLAY: an ENGINE A/B between the pre-fix tree (main) and this
#     worktree, over the production asklog, filtered to Bash rows whose command
#     contains `$(git -C` or `` `git -C `` (a syntactic superset of every row this fix
#     could possibly move — any row without that literal substring cannot contain a
#     `-C` inside a git command substitution body at all, so it is provably identical on
#     both trees; TestCorpusVerdictReplay's own construction, not an assumption).
#
# ISOLATION (bead pg2-cbihz's rule, matching probe-pg2-68w11.sh / probe-pg2-4ak2k.sh):
# the production asklog is NEVER opened read-write. An APFS clone (`cp -c`,
# copy-on-write) makes a private snapshot file first; `cmd_evaluate`/`baseline`/`compare`
# (which open asklog.NewStore(asklog.DefaultDBPath()) read-write) are NOT used anywhere
# in this script — only TestCorpusVerdictReplay, which redirects XDG_DATA_HOME to a temp
# dir and reads a JSONL extract taken from the (already read-only-intended) snapshot
# copy, never the live db handle.
set -uo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-jq8tn-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver) || exit 1
export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

probe_cwd="$pkg_root"

# decision <command> [cwd] — the permissionDecision word alone; "abstain" for {}.
decision() {
  local cmd="$1" cwd="${2:-$probe_cwd}"
  jq -cn --arg c "$cmd" --arg w "$cwd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-jq8tn-probe",cwd:$w,permission_mode:"auto",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null | jq -r '.hookSpecificOutput.permissionDecision // "abstain"'
}

echo "ceta built from: $pkg_root"
echo "probe cwd:       $probe_cwd"
echo

echo "=== 1. FIXTURES (this worktree's binary) ==="
printf '  %-8s %s\n' DECISION COMMAND

echo "  -- the bead's own measured examples: substitution no longer floored at abstain --"
decision_row() { printf '  %-8s %s\n' "$(decision "$1")" "$1"; }
decision_row 'echo $(git -C /Users/phillipg/phillipg_mbp rev-parse HEAD)'
decision_row 'echo $(git -C /Users/phillipg/phillipg_mbp status --porcelain)'
decision_row 'echo `git -C /Users/phillipg/phillipg_mbp rev-parse HEAD`'

echo "  -- repeated -C: still resolves via the union-screened path, not refused --"
decision_row 'echo $(git -C /Users/phillipg/phillipg_mbp -C .worktrees/pg2-jq8tn rev-parse HEAD)'

echo "  -- every OTHER leading global option must still refuse (THE pg2-a5r9r RULING) --"
decision_row 'echo $(git -c core.pager=id rev-parse HEAD)'
decision_row 'echo $(git --git-dir=/tmp/x rev-parse HEAD)'
decision_row 'echo $(git --work-tree=/tmp/x rev-parse HEAD)'
decision_row 'echo $(git --namespace=foo rev-parse HEAD)'
decision_row 'echo $(git --exec-path=/tmp/x rev-parse HEAD)'
decision_row 'echo $(git -p rev-parse HEAD)'
decision_row 'echo $(git --paginate rev-parse HEAD)'

echo "  -- malformed / edge spellings of -C itself --"
decision_row 'echo $(git -C rev-parse)'
decision_row 'echo $(git -C)'
decision_row 'echo $(git -C /tmp/x)'

echo "  -- -C stripped correctly, but the subcommand behind it is NOT admitted --"
decision_row 'echo $(git -C /Users/phillipg/phillipg_mbp branch)'
decision_row 'echo $(git -C /Users/phillipg/phillipg_mbp log)'
decision_row 'echo $(git -C /Users/phillipg/phillipg_mbp diff)'
decision_row 'echo $(git -C /Users/phillipg/phillipg_mbp show HEAD)'

echo "  -- write-flag screening still fires through a -C prefix --"
decision_row 'echo $(git -C /Users/phillipg/phillipg_mbp commit -am msg)'
echo

echo "=== 2. THE CORPUS REPLAY: ENGINE A/B against the production asklog ==="
asklog_src="${PGJQ8TN_ASKLOG:-$HOME/.local/share/claude-extended-tool-approver/asks.db}"
if [[ ! -f $asklog_src ]]; then
  echo "no asklog at $asklog_src — skipping the corpus replay. Set PGJQ8TN_ASKLOG to override." >&2
  exit 0
fi

echo "--- 2.0 SNAPSHOT the production asklog (never opened read-write by this script) ---"
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

echo "--- 2.1 BUILD both trees (base = main, post = this worktree) ---"
base_ref="${PGJQ8TN_BASE_REF:-main}"
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
echo "  files that differ (expected: internal/cmdparse/parser.go + its test + this script):"
diff -rq "$base_src" "$pkg_root" --exclude=.git 2>&1 | sed -n 's/^Files .*base-src\/\(.*\) and .*/    \1/p'
echo

(cd "$base_src" && GOFLAGS=-mod=mod go test -c -o "$work/setup-pre.test" ./internal/setup) || exit 1
(cd "$pkg_root" && go test -c -o "$work/setup-post.test" ./internal/setup) || exit 1
echo "  built: setup-pre.test (base=$base_ref), setup-post.test (this worktree)"
echo

echo "--- 2.2 EXTRACT candidate rows (a superset: any row LACKING the substring cannot" \
  "be affected) ---"
snap_out="$work/replay-rows-gitc.jsonl"
sqlite3 -noheader "$snapshot" "
  PRAGMA case_sensitive_like=ON;
  SELECT json_object('command', c, 'cwd', w, 'permission_mode', COALESCE(pm,'')) FROM (
    SELECT DISTINCT json_extract(tool_input_json,'\$.command') AS c, cwd AS w,
      permission_mode AS pm FROM tool_decisions
    WHERE excluded=0 AND tool_name='Bash'
      AND (json_extract(tool_input_json,'\$.command') LIKE '%\$(git -C%'
           OR json_extract(tool_input_json,'\$.command') LIKE '%\`git -C%')
      AND json_extract(tool_input_json,'\$.command') IS NOT NULL
  );" >"$snap_out"
n_rows="$(wc -l <"$snap_out" | tr -d ' ')"
echo "  $n_rows distinct (command, cwd, permission_mode) rows contain \$(git -C or \`git -C"
echo

pre_out="$work/verdicts-pre.tsv"
post_out="$work/verdicts-post.tsv"
CETA_REPLAY_SNAPSHOT="$snap_out" CETA_REPLAY_OUT="$pre_out" \
  "$work/setup-pre.test" -test.run TestCorpusVerdictReplay -test.timeout 15m 2>/dev/null |
  grep REPLAY | sed 's/^/  pre:  /'
CETA_REPLAY_SNAPSHOT="$snap_out" CETA_REPLAY_OUT="$post_out" \
  "$work/setup-post.test" -test.run TestCorpusVerdictReplay -test.timeout 15m 2>/dev/null |
  grep REPLAY | sed 's/^/  post: /'
echo

echo "--- 2.3 THE TRANSITION TABLE (every distinct transition, none silently capped) ---"
transitions="$(join -t $'\t' -j 1 <(sort -k1,1 "$pre_out") <(sort -k1,1 "$post_out") |
  awk -F'\t' '$2!=$4 {print $2"("$3") -> "$4"("$5")"}' | sort | uniq -c | sort -rn)"
if [[ -z $transitions ]]; then
  echo "  (no transitions — no row in this candidate set moved)"
else
  echo "$transitions"
fi
echo
rank() { case "$1" in approve) echo 0 ;; abstain) echo 1 ;; ask) echo 2 ;; reject) echo 3 ;; *) echo 9 ;; esac }
n_more_restrictive=0
n_less_restrictive=0
if [[ -n $transitions ]]; then
  while IFS= read -r line; do
    [[ -z $line ]] && continue
    cnt="$(awk '{print $1}' <<<"$line")"
    from="$(sed -E 's/^ *[0-9]+ ([a-z]+)\(.*/\1/' <<<"$line")"
    to="$(sed -E 's/.*-> ([a-z]+)\(.*/\1/' <<<"$line")"
    fr="$(rank "$from")"
    tr_="$(rank "$to")"
    if [[ $tr_ -gt $fr ]]; then
      n_more_restrictive=$((n_more_restrictive + cnt))
    elif [[ $tr_ -lt $fr ]]; then
      n_less_restrictive=$((n_less_restrictive + cnt))
    fi
  done <<<"$transitions"
fi
echo "  transitions toward MORE restrictive: $n_more_restrictive  (MUST be 0)"
echo "  transitions toward LESS restrictive: $n_less_restrictive  (the bead's expected class)"
if [[ $n_more_restrictive -ne 0 ]]; then
  echo "  *** VIOLATION: a row became MORE restrictive — do not land ***"
fi
echo

echo "--- 2.4 SUPPORTING SQL COUNTS (row scope, independent of the engine replay) ---"
sqlite3 -noheader "$snapshot" "
  PRAGMA case_sensitive_like=ON;
  SELECT 'total rows (all-time):        ' || COUNT(*) FROM tool_decisions
    WHERE excluded=0 AND tool_name='Bash'
      AND (json_extract(tool_input_json,'\$.command') LIKE '%\$(git -C%'
           OR json_extract(tool_input_json,'\$.command') LIKE '%\`git -C%');
  SELECT 'total rows (last 30 days):    ' || COUNT(*) FROM tool_decisions
    WHERE excluded=0 AND tool_name='Bash'
      AND (json_extract(tool_input_json,'\$.command') LIKE '%\$(git -C%'
           OR json_extract(tool_input_json,'\$.command') LIKE '%\`git -C%')
      AND created_at >= datetime('now','-30 days');
  SELECT 'distinct bodies (all-time):   ' || COUNT(DISTINCT json_extract(tool_input_json,'\$.command'))
    FROM tool_decisions
    WHERE excluded=0 AND tool_name='Bash'
      AND (json_extract(tool_input_json,'\$.command') LIKE '%\$(git -C%'
           OR json_extract(tool_input_json,'\$.command') LIKE '%\`git -C%');
"
echo

echo "asklog isolation: this script never opened $asklog_src read-write; all replay ran"
echo "against the snapshot at (now deleted) $snapshot, via TestCorpusVerdictReplay with"
echo "XDG_DATA_HOME redirected to a temp dir."
