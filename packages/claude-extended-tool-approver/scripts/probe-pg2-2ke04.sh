#!/usr/bin/env bash
# probe-pg2-2ke04.sh — reproduction / verification probe for bead pg2-2ke04
# (a deny-listed credential was ALLOWED via one variable hop on a READ).
#
# THE DEFECT. safecmds.argsHaveDynamicExpansion — the guard that refuses an
# argument whose path resolves only at runtime ($VAR / $(...) / backtick) — was
# wired to safeWriteCmds ONLY. Every READ command (cat/head/tail/less/more/wc/
# diff/sort/uniq/awk/jq/tq/xxd/strings, plus sed/yq/gofmt/grep/rg reads) skipped
# it, because looksLikePath matches only literal /, ./, ../ and ~/ — so a `$F`
# argument is not path-like, no zone check runs, and the leaf auto-approves:
#
#   cat /Users/phillipg/.ssh/id_rsa        -> deny   (correct)
#   F=/Users/phillipg/.ssh/id_rsa; cat $F  -> allow  (the bypass)
#   F=/Users/phillipg/.ssh/id_rsa; rm  $F  -> abstain (write: guard fired)
#
# A credential READ is an exfiltration primitive, so the read path warrants the
# guard at least as much as the write path.
#
# Builds the hook binary from the CURRENT worktree source and asks it for a
# verdict on each probe command, printing `<decision>  <command>`.
#
# ASKLOG ISOLATION: XDG_DATA_HOME is pointed at a throwaway directory so probe
# rows land in a scratch asks.db and never reach the real corpus.
#
# MEASURED PROMPT-VOLUME COST of the fix — full-corpus replay through
# `claude-extended-tool-approver evaluate --format=json`, before vs after,
# 333,349 rows graded in both runs, 2026-07-31:
#
#   allow   -> abstain   3,455
#   abstain -> ask         250
#   allow   -> ask         142
#                        -----
#   changed              3,847   (1.15% of the corpus)
#   toward allow             0   <-- the invariant
#
# 3,597 rows lose an `allow`: 14.96% of the 24,045 corpus rows that both approved
# and contain a `$` or a backtick.
set -euo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-2ke04-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)

export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

probe() {
  local cmd="$1"
  local out decision
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-2ke04-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  decision="$(printf '%s' "$out" |
    jq -r '.hookSpecificOutput.permissionDecision // .permissionDecision // "abstain"')"
  printf '%-7s %s\n' "$decision" "$cmd"
}

# ---------------------------------------------------------------------------
# THE DEFECT, VERBATIM from the bead's "Verified live" block. Every row here
# measured `allow` on main @ 9c52f66b. Expected AFTER the fix: no `allow`.
# ---------------------------------------------------------------------------
echo "=== DEFECT: single variable hop on a READ (bead's verbatim rows) ==="
probe 'cat /Users/phillipg/.ssh/id_rsa'
probe 'F=/Users/phillipg/.ssh/id_rsa; cat $F'
probe 'F=/Users/phillipg/.ssh/id_rsa; head $F'
probe 'F=/Users/phillipg/.ssh/id_rsa; xxd $F'
probe 'F=/Users/phillipg/.aws/credentials; cat $F'
probe 'FOO=~/.ssh/id_rsa; cat $FOO'

echo
echo "=== CONTROL: write path unchanged (guard already fired here) ==="
probe 'F=/Users/phillipg/.ssh/id_rsa; rm $F'
probe 'F=/Users/phillipg/.ssh/id_rsa; cp $F /tmp/x'

# ---------------------------------------------------------------------------
# WHOLE safeReadCmds LIST. The map in safecmds.go is the authority; this block
# enumerates every key so no member is left unguarded. (The Go test
# TestSafecmds_EverySafeReadCmdGatesDynamicPath iterates the map itself, so a NEW
# member added later cannot escape without failing the suite.)
# ---------------------------------------------------------------------------
echo
echo "=== WHOLE safeReadCmds LIST: one variable hop, deny-listed path ==="
for rc in cat head tail less more wc diff sort uniq awk jq tq xxd strings; do
  probe "F=/Users/phillipg/.ssh/id_rsa; $rc \$F"
done

echo
echo "=== OTHER READ SURFACES that share the read zone check ==="
probe 'F=/Users/phillipg/.ssh/id_rsa; sed -n 1p $F'
probe 'F=/Users/phillipg/.ssh/id_rsa; yq . $F'
probe 'F=/Users/phillipg/.ssh/id_rsa; gofmt -l $F'
probe 'F=/Users/phillipg/.ssh/id_rsa; grep x $F'
probe 'F=/Users/phillipg/.ssh/id_rsa; rg x $F'
probe 'F=/Users/phillipg/.ssh/id_rsa; jar tf $F'
probe 'F=/Users/phillipg/.ssh/id_rsa; bash -n $F'

echo
echo "=== KNOWN REMAINING HOLE (out of scope: needs dataflow, option 2) ==="
# The path never appears as an ARGUMENT of the reading command — it arrives on
# stdin. Refusing it needs the value followed ACROSS the pipe, which is the
# intra-command-dataflow capability pg2-2ke04 option 2 / pg2-553z3 weigh. Expected
# to stay `allow` both before and after this fix; recorded so it is not mistaken
# for a regression.
probe 'F=/Users/phillipg/.ssh/id_rsa; echo $F | xargs cat'

# RESOLUTION (pg2-qmtsn, 2026-08-18): CLOSED WITHOUT A FIX. The bead gave an
# explicit off-ramp — measure how often a credential path reaches a reader
# only via a pipe, and close with no code change if that is ~never. It is.
#
# MEASUREMENT. A parsed-structure detector (cmdparse.Parse leaves grouped by
# PipelineID/PipelineIndex; cmdparse.InCommandVars to resolve a bare `$F`
# reference to the literal value an earlier leaf assigned; secretpath.IsSecret
# for the deny-list check, GATED by cmdparse.LooksLikePath so it never runs on
# a raw prose/heredoc argument — the pg2-5b901 / pg2-ia640.5 failure mode this
# bead's own description warns against; cmdparse.SkipMessageArgs /
# SkipGrepPattern to exclude bd/git/gh message text and grep/rg search
# patterns from candidacy) was run once, read-only, against a full snapshot
# COPY of the live asklog corpus (the live db cannot be safely full-table-
# scanned under immutable=1 — see docs/engine-ab-replay-runbook.md's "Asklog
# read access" section; `PRAGMA quick_check` on the snapshot reported `ok`).
# The reader side was restricted to the xargs-wrapped shape specifically
# (`producer | xargs <reader>` with the reader's own args containing no path
# operand): a bare `producer | cat` with no xargs never opens a file at all —
# cat with zero args just streams stdin bytes back out, so it leaks nothing
# beyond what the producer already would. xargs (or an equivalent explicit
# construct, e.g. `find -exec`) is what actually turns piped TEXT into a
# file-open ARGUMENT.
#
#   Corpus: 364,823 non-excluded rows (all tools), 238,473 Bash rows,
#           2026-03-13T20:47:33Z .. 2026-08-18T19:05:04Z (~5 months).
#   MATCHED rows (secret-mentioning producer -> arg-less xargs-reader
#     across a pipe): 0 / 238,473.
#   Mechanism sanity check (detector is not silently dead): the underlying
#     xargs-reader shape itself, WITHOUT the secrecy filter, fires 1,686
#     times across 1,573 unique commands (mostly `find ... | xargs grep -l
#     ...` / `find ... | head -1 | xargs cat` idioms) — so the zero above is
#     a real absence of the secret-mentioning subset, not a detector that
#     never matches real commands. Detector correctness was also unit-checked
#     directly against this file's own canonical example
#     (`F=/Users/phillipg/.ssh/id_rsa; echo $F | xargs cat`), which it flags.
#   Supplementary reconnaissance (a broad `LIKE` text scan, NOT the primary
#     measurement — used only to sanity-check no adjacent idiom was missed):
#     rows containing both a credential-looking substring (id_rsa,
#     id_ed25519, .aws/credentials, .credentials) AND a pipe AND one of
#     xargs/-exec/"while read": 4 total in the whole corpus, and all 4 are
#     meta — `bd comment`/`bd create` bodies discussing THIS bead or an
#     unrelated grep-flag bead — not attempted exploit commands.
#
# DECISION: leave the hole. Real dataflow (following a value across a pipe
# into the next command's stdin) is strictly more machinery than pg2-2ke04's
# already-deferred intra-command dataflow (option 2), and the corpus gives
# zero evidence it would ever fire. Building it now would be unjustified
# complexity for a shape that has not occurred once in ~5 months of this
# operator's real usage. pg2-2ke04 option 2 and pg2-553z3 (KEEP STRICT,
# operator ruling 2026-07-30 — see that bead) remain separately deferred for
# the same reason: no measured volume justifies the shared intra-command
# dataflow primitive they and this bead would need. If a future corpus
# measurement finds this shape starting to appear, re-open with the specific
# rows as evidence rather than reopening on principle.

echo
echo '=== SPELLINGS: $(...) and backtick, not only $VAR ==='
probe 'cat $(echo /Users/phillipg/.ssh/id_rsa)'
probe 'cat `echo /Users/phillipg/.ssh/id_rsa`'
probe 'cat ${HOME}/.ssh/id_rsa'
probe 'head -1 $(printf %s /Users/phillipg/.aws/credentials)'
probe 'xxd $(echo ~/.ssh/id_rsa)'

echo
echo "=== QUOTED and CONCATENATED spellings ==="
probe 'F=/Users/phillipg/.ssh/id_rsa; cat "$F"'
probe 'D=/Users/phillipg/.ssh; cat $D/id_rsa'
probe 'cat $HOME/.ssh/id_rsa'

# ---------------------------------------------------------------------------
# TEXT-vs-PARSED (the pg2-5b901 failure mode). The guard keys on PARSED args,
# so text that merely QUOTES a gated command is not itself gated.
# ---------------------------------------------------------------------------
echo
echo "=== REGRESSION: text-vs-parsed (the bypass as PROSE, never an operand) ==="
probe 'git commit -m "cat $F no longer auto-approves (pg2-2ke04)"'
probe 'bd comment pg2-2ke04 -m "F=/Users/phillipg/.ssh/id_rsa; cat $F measured allow"'
probe 'echo "cat $F"'

echo
echo "=== REGRESSION: static read paths keep their verdict ==="
probe 'cat /tmp/x'
probe 'cat ./README.md'
probe 'head -20 /tmp/log.txt'
probe 'wc -l /tmp/x'
probe 'jq . /tmp/x.json'
probe 'grep -rn foo /tmp'
probe 'sed -n 1p /tmp/x'
probe 'cat'
probe 'ls -la /tmp'
probe 'echo hi'

echo
echo '=== PROGRAM-OPERAND ROLE: code containing a literal $ is NOT gated ==='
probe "awk '{print \$1}' /tmp/x"
probe "awk -F'\\t' '{print \$2}' /tmp/x"
probe "awk -v n=1 '{print \$n}' /tmp/x"
probe "sed 's/x\$//' /tmp/x"
probe "jq '.count = \$c' /tmp/x.json"
probe "echo '{}' | jq --arg a b '{a:\$a}'"

echo
echo "=== EXPECTED COST: benign dynamic reads now abstain (measured by replay) ==="
probe 'for f in *.go; do cat "$f"; done'
probe 'cat $HOME/.zshrc'
probe 'wc -l $f'
# ls/find/du/stat/file/lsof stay APPROVED: browsingCmds expose names, sizes and
# timestamps, never file CONTENT, so a dynamic path there is not an exfiltration
# primitive. Deliberately unchanged by this bead.
probe 'ls $d'
probe 'find $d -name x'

echo
echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"
