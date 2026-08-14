#!/usr/bin/env bash
# probe-pg2-zpct4.sh — verification probe for bead pg2-zpct4: the two PATH MODELS are
# reconciled, so capturing a read into an env-var value can no longer clear a path the
# bare read refuses.
#
# THE HOLE (pre-existing, found by pg2-d0ja3's provenance fuzz target, measured identical
# on the base commit a064a73e — not a regression):
#
#	cat /etc/shadow                 abstain   safe-commands refuses it via readPathIssue
#	X=$(cat /etc/shadow) echo hi    ALLOW     the static substitution allowlist cleared it
#
# cmdparse's static safe-substitution allowlist screened argv through
# `secretpath.IsSecret`, which does not classify `/etc/shadow`; safe-commands'
# readPathIssue asks `patheval` whether the path is in a ZONE this session may read, and it
# is not. A cleared body classifies ExpansionSafeCmd, which SKIPS the substitution
# recursion — so the weaker model stood in place of the stronger one, and the captured
# value was then available to the surviving leaf.
#
# THE FIX IS A RECONCILIATION, NOT A SECOND DENY-LIST. Path IDENTIFICATION is now one
# shared predicate (cmdparse.LooksLikePath, which safecmds' looksLikePath delegates to);
# path READABILITY has one authority (`patheval`, via readPathIssue), and the static seam
# DECLINES to answer it — a body naming a path returns SubstitutionDelegated and the
# authoritative model rules on it through recursion. `internal/secretpath` is untouched:
# that map is consumed by every rule, so an entry there would be a repo-wide policy change.
#
# SIX SECTIONS:
#
#  1. THE HOLE, CLOSED — the bead's headline row plus every sibling the same mechanism held.
#  2. THE RELATION, COMPUTED LIVE — for a matrix of readers x paths, the captured spelling
#     is never less restrictive than the bare one. This is the section that survives
#     retuning either model, and it is computed rather than asserted from a table.
#  3. FAIL-CLOSED — a path NEITHER model can place reaches no Approve by any spelling.
#  4. NOT WIDENED, NOT NARROWED — in-zone reads still clear, on both spellings, because
#     delegation defers to the authority instead of guessing.
#  5. pg2-xl79d's INCUMBENT DESIGN IS UNCHANGED — a DYNAMIC operand still clears. That
#     asymmetry is recorded, deliberate, and out of scope here.
#  6. NOT CLOSED BY THIS — `jq -f PATH` auto-approves a deny-listed key in the BARE
#     spelling too, so reconciling made the two spellings AGREE while both stay wrong.
#     That is bead pg2-wrxg6, in safecmds.
#
# WHAT `{}` (abstain) MEANS: claude-code decides — auto-approve mode, then settings
# pre-authorization, then the prompt. So `abstain` is NOT a gate in `auto` mode. See
# docs/adr/0043-ceta-rule-verdict-vocabulary.md's Decision.
#
# ISOLATION: the binary is built from the CURRENT worktree and XDG_DATA_HOME points at a
# throwaway directory, so probe rows land in a scratch asks.db and NEVER reach the shared
# production corpus (bead pg2-cbihz — `evaluate`/`baseline`/`compare` open it read-write,
# and this probe uses none of them).
set -uo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-zpct4-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver) || exit 1
export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

# probe_cwd is a REAL directory: the path evaluator classifies against a live filesystem,
# so a synthetic cwd would change every verdict below.
probe_cwd="$pkg_root"

# decision <command> — the decision word alone; `abstain` for the empty object.
decision() {
  jq -cn --arg c "$1" --arg w "$probe_cwd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-zpct4-probe",cwd:$w,permission_mode:"auto",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null | jq -r '.hookSpecificOutput.permissionDecision // "abstain"'
}

# rank <decision> — the RESTRICTIVENESS order hookio.MostRestrictive folds over
# (Approve < NoOpinion < Ask < Reject). Section 2 compares ranks, never words, so the
# relation is checked rather than eyeballed.
rank() {
  case "$1" in
  allow) echo 0 ;;
  abstain) echo 1 ;;
  ask) echo 2 ;;
  deny) echo 3 ;;
  *) echo 9 ;;
  esac
}

# pair <body> — bare verdict + captured verdict + the relation verdict for one body.
pair() {
  local body="$1" db dc verdict
  db="$(decision "$body")"
  dc="$(decision "X=\$($body) echo hi")"
  verdict=OK
  [ "$(rank "$dc")" -lt "$(rank "$db")" ] && verdict='VIOLATION <-- captured is LOOSER'
  printf '  %-8s %-8s %-28s %s\n' "$db" "$dc" "$verdict" "$body"
}

echo "ceta built from: $pkg_root"
echo "probe cwd:       $probe_cwd"
echo

echo "=== 1. THE HOLE, CLOSED. Every row was 'allow' in the captured spelling on a064a73e ==="
printf '  %-8s %-8s %-28s %s\n' BARE CAPTURED RELATION BODY
pair 'cat /etc/shadow'
pair 'cat /etc/passwd'
pair 'tail -1 /etc/passwd'
pair 'head -1 /etc/shadow'
pair 'grep -c x /etc/shadow'
pair 'wc -l /etc/shadow'
pair 'wc -l < /etc/shadow'
pair 'jq -r .x /etc/shadow'
pair 'yq .a /etc/shadow'
pair 'tq -f /etc/shadow .a'
pair '[ -f /etc/shadow ]'
pair 'cat /Users/phillipg/.aws/credentials'
pair 'wc -l < /Users/phillipg/.aws/credentials'
pair 'cat /'
pair 'cat /var/log/system.log'

echo
echo "=== 2. THE RELATION, computed live over readers x paths ==="
echo "    (the claim is an INEQUALITY, not a verdict: capturing a read never gates it LESS.)"
printf '  %-8s %-8s %-28s %s\n' BARE CAPTURED RELATION BODY
for reader in 'cat' 'head -1' 'wc -l' 'grep -c x' 'jq -r .x'; do
  # SC2088 disabled DELIBERATELY: the tilde MUST NOT expand here. These strings are
  # COMMAND TEXT handed to ceta, which receives exactly what Claude Code would send it and
  # does its own tilde resolution through patheval's cleanPath. Expanding the tilde in this
  # script would test a DIFFERENT path than the one under test and would silently stop
  # exercising the `~`-prefix branch of LooksLikePath.
  # shellcheck disable=SC2088
  for path in '/etc/shadow' '/Users/otheruser/notes.txt' '~someuser/notes.txt' \
    '.env' '/Users/phillipg/.ssh/id_rsa' '~/.ssh/config' \
    "$probe_cwd/go.mod" './go.mod' 'go.mod'; do
    pair "$reader $path"
  done
done

echo
echo "=== 3. FAIL-CLOSED: a path NEITHER model can place reaches no Approve, any spelling ==="
for p in '/etc/shadow' '/etc/master.passwd' '/Users/otheruser/notes.txt' '~someuser/notes.txt'; do
  for spelling in "cat $p" "X=\$(cat $p) echo hi" "echo \$(cat $p)" "X=\$(wc -l < $p) echo hi"; do
    printf '  %-8s %s\n' "$(decision "$spelling")" "$spelling"
  done
done

echo
echo "=== 4. NOT WIDENED, NOT NARROWED: in-zone reads still clear on BOTH spellings ==="
echo "    (delegation defers to the authority; it does not guess, so an approvable read is"
echo "     still approved — by the model that actually knows the zone.)"
for c in \
  'cat go.mod' \
  'X=$(cat go.mod) echo hi' \
  'cat ./go.mod' \
  'X=$(cat ./go.mod) echo hi' \
  'echo $(cat ./go.mod)' \
  'X=$(wc -l < ./go.mod) echo hi' \
  'X=$(jq -r .x f.json) echo hi' \
  'X=$(cat /tmp/x.json) echo hi' \
  'X=$(readlink /etc/shadow) echo hi' \
  'X=$(basename /etc/shadow) echo hi' \
  'X=$(git rev-parse HEAD) echo hi' \
  'echo $(date)'; do
  printf '  %-8s %s\n' "$(decision "$c")" "$c"
done

echo
echo "=== 5. pg2-xl79d's INCUMBENT DESIGN, unchanged: a DYNAMIC operand still clears ==="
echo '    A $VAR operand is not a path this seam resolved, so there is nothing to delegate.'
echo "    The bare spelling refuses it (pg2-2ke04) while the captured spelling clears it —"
echo "    a pre-existing asymmetry pg2-xl79d recorded as deliberate. NOT closed here: doing"
echo "    so would withdraw the 37-row relief pg2-xl79d landed, and it is a different lever."
printf '  %-8s %-8s %-28s %s\n' BARE CAPTURED RELATION BODY
pair 'cat "$f"'
pair 'jq -r .x "$f"'
pair 'wc -l < "$f"'

echo
echo "=== 6. NOT CLOSED BY THIS: jq -f drops its PATH operand in safecmds (pg2-wrxg6) ==="
echo "    The BARE spelling auto-approves a deny-listed key, so reconciling made the two"
echo "    spellings AGREE while both stay wrong. The fix belongs to safecmds' jq branch."
pair 'jq -f /Users/phillipg/.ssh/id_rsa .'
printf '  %-8s %s\n' "$(decision 'echo $(jq -f /Users/phillipg/.ssh/id_rsa .)')" 'echo $(jq -f /Users/phillipg/.ssh/id_rsa .)'
echo "    (the command-position row is 'ask' only because IsSecret REFUSES rather than"
echo "     delegates — see the pg2-wrxg6 note on fileReaderSubstitutions for why that"
echo "     over-refusal is kept deliberately.)"

echo
echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"
