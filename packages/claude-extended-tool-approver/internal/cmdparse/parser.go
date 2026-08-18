package cmdparse

// Supported shell syntax:
//   - Simple commands: cmd arg1 arg2
//   - Compound commands: cmd1 && cmd2, cmd1 || cmd2, cmd1 ; cmd2, cmd1 | cmd2,
//     cmd1 & cmd2 (bare '&' background separator; '&>', '>&', '2>&1' preserved)
//   - Quoting: double quotes (with backslash escapes), single quotes (literal)
//   - Environment prefixes: FOO=bar cmd
//   - Redirections: any [FD]OP[TARGET] where FD is empty, a descriptor number, or
//     bash's `{varname}` open-and-assign form, and OP is one of <, >, >>, >|, >&,
//     <>, &>, &>>; plus fd duplication/close N>&M / N>&- (dropped: no file target)
//     and herestrings (<<<)
//   - Heredocs (<<, <<-, quoted or unquoted delimiter): the BODY is an opaque extent
//     lifted out before splitting, never leaves and never args — see heredoc.go
//   - Command substitution: $(cmd), `cmd`
//   - Process substitution: <(cmd), >(cmd)
//   - Subshell grouping: ( cmd1; cmd2 )
//   - Inline comments: cmd # comment
//   - Loops: for VAR in LIST; do CMD; done  /  while COND; do CMD; done
//
// Unsupported (falls through as Abstain — safe default):
//   - Brace expansion: {a,b,c}
//   - Array syntax: ${arr[@]}
//   - Coproc: coproc cmd
//   - Cross-token quote concatenation: 'it'\''s'

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/secretpath"
)

// THE pg2-xl79d WIDENING: THE TWO LISTS BELOW WERE AN ACCIDENT, NOT A RISK MODEL.
//
// MEASURED, 2026-08-13: replaying 8,560 logged rows (2026-08-06..13) through the
// DEPLOYED binary produced 104 asks, 51 of them from the env-var rule, and 37 of those
// 51 carried ONE reason — envvars' post-recursion fallback (`env var value contains an
// unevaluated/unsafe expression: <NAME>`) on an ordinary local capture. The names were
// script locals (`out`, `n`, `st`, `total`, `REV`, `ALL_CSV`, …); nothing phantom, no
// regression of pg2-8cp08/pg2-hfnrr/pg2-3ggxm. `jq` alone appeared in 27 of the 37.
//
// The cause is an INCONSISTENCY between two mechanisms that are each correct alone. A
// body on THIS static list classifies ExpansionSafeCmd and never reaches the fallback;
// any other body must be POSITIVELY CLEARED by full-engine recursion, and the recursion
// correctly refuses a dynamically-expanded path arg (pg2-2ke04) and a dynamic redirect
// source (pg2-2u5jf). Composed, that produced (deployed binary, cwd
// `/Users/phillipg/phillipg_mbp`, `permission_mode=auto`):
//
//	X=$(cat "$f") echo hi        ->  allow    cat IS on the list
//	X=$(grep -c x "$f") echo hi  ->  allow    grep IS on the list
//	X=$(jq -r .x "$f") echo hi   ->  ASK      jq was not
//	X=$(wc -l < "$f") echo hi    ->  ASK      wc IS on the list; the REDIRECT was refused
//	X=$(seq 1 3) echo hi         ->  ASK      seq was on neither list
//	X=$(jq -r .x f.json) echo hi ->  allow    the SAME jq, with a LITERAL path
//
// There is no risk model under which capturing `cat "$f"` is safe and capturing
// `jq -r .x "$f"` is not. Capturing a command's output into a shell variable neither
// writes nor exfiltrates, and the underlying read is gated (or not) identically whether
// or not it is captured — the last row is the proof, since it is the same program on the
// same list-membership question, differing only in a quote. So the entries below are the
// read shapes whose LITERAL-path forms ALREADY cleared, admitted so the two spellings
// agree.
//
// WHAT THIS DELIBERATELY DOES NOT DO, because each is a different lever with its own
// bead:
//
//   - It does NOT touch the envvars rule, argsHaveDynamicExpansion / the pg2-2ke04
//     dynamic-path refusal, the pg2-2u5jf dynamic-redirect floor, or any NAME list. In
//     particular it is NOT "lever (b)" — gating the fallback on the assignment's NAME.
//     pg2-5huwx verified that reopens the fbbf3ade hole, because engine.go's
//     StripLeadingEnvAssignments keeps the value body away from this static floor on the
//     leaf path, leaving envvars as the ONLY guard there.
//   - It does NOT relax the SOLE-SIMPLE-COMMAND shape test. `curl -s … | sh` is on no
//     list and is not one command, so it still recurses to a non-Approve and still asks;
//     so does `rm -rf /etc`, which no list holds. Bodies carrying control flow inside a
//     pipeline (`c=$(find … | while read … | wc -l | tr …)`) stay refused too — those
//     need the AST-subtree recursion of pg2-1019a / pg2-x9452, not an allowlist entry,
//     and they were 1-2 rows.
//   - It does NOT gate a DYNAMIC operand. The secretpath screens here are LITERAL-text
//     screens, and `X=$(cat "$f")` has always cleared with `$f` unresolved; that
//     exposure is the incumbent design (the recursion path is where pg2-2ke04 gates it),
//     and stating it plainly is better than implying these entries close it.
//
// The CLASS fix — that a recursive verdict cannot distinguish "no rule knew this
// command" from "a rule refused it", which is why this list needs hand-extension at all
// — is its own bead, filed alongside pg2-xl79d.

// THE pg2-zpct4 RECONCILIATION: THIS SEAM NO LONGER RULES ON PATH READABILITY.
//
// MEASURED on the base commit a064a73e (cwd = this package, `permission_mode=auto`, one
// probe per row through the built binary). Every pair is the SAME read written two ways:
//
//	cat /etc/shadow                                  abstain    X=$(cat /etc/shadow) echo hi                   ALLOW
//	cat /etc/passwd                                  abstain    X=$(cat /etc/passwd) echo hi                   ALLOW
//	tail -1 /etc/passwd                              abstain    X=$(tail -1 /etc/passwd) echo hi               ALLOW
//	grep -c x /etc/shadow                            abstain    X=$(grep -c x /etc/shadow) echo hi             ALLOW
//	head -1 /etc/shadow                              abstain    X=$(head -1 /etc/shadow) echo hi               ALLOW
//	jq -r .x /etc/shadow                             abstain    X=$(jq -r .x /etc/shadow) echo hi              ALLOW
//	wc -l < /etc/shadow                              abstain    X=$(wc -l < /etc/shadow) echo hi               ALLOW
//	cat /Users/phillipg/.aws/credentials             abstain    X=$(cat …/.aws/credentials) echo hi            ALLOW
//
// TWO PATH MODELS DISAGREED, and the WEAKER one was the one a captured read reached. The
// screen here was `secretpath.IsSecret`, which classifies a small deny-list of secret
// DIRS and BASENAMES and knows nothing of `/etc/shadow`; the bare spelling goes through
// `internal/rules/safecmds`' readPathIssue, which asks `patheval` whether the path is in a
// ZONE this session may read at all. `/etc/shadow` is in no readable zone, so the bare
// read abstains — and capturing it into an env-var value cleared it, because
// ExpansionSafeCmd means the body is never recursed and this screen therefore STANDS IN
// PLACE OF the whole path model (the same structural trap the `test` / `[` note below
// records for a deny-listed credential).
//
// THE FIX IS A RECONCILIATION, NOT A SECOND DENY-LIST. Adding `/etc/shadow` here would
// leave the disagreement in place for the next path, so the two questions are separated
// and each is given ONE owner:
//
//   - PATH IDENTIFICATION — "is this token a filesystem path at all?" — is now ONE
//     predicate, LooksLikePath, which lives here and which safecmds' looksLikePath
//     delegates to. It is a purely LEXICAL question, so a static parser can own it, and a
//     single definition is what keeps the two seams from drifting apart again.
//   - PATH READABILITY — "may this session read that path?" — has ONE AUTHORITY,
//     `patheval.PathEvaluator`, reached through safecmds' readPathIssue. It is
//     CONFIG-DEPENDENT (project root, cwd, extra roots, sandbox settings, container
//     mounts), and this package is a pure static seam with no config, so it CANNOT answer
//     that question and MUST NOT pretend to.
//
// So this seam DECLINES. A body whose read names a path returns SubstitutionDelegated
// rather than a clearance, and the authoritative model rules on it through the engine's
// substitution recursion. Delegation is not approval: SubstitutionRefused is the ZERO
// value, `patheval.PathUnknown.CanRead()` is false, and readPathIssue refuses anything it
// cannot place — so a path NEITHER model can classify cannot reach Approve by this route.
//
// WHY `secretpath.IsSecret` STAYS, rather than being replaced by the delegation: it
// classifies tokens LooksLikePath does not. A bare basename (`.env`, `id_rsa`) has no `/`,
// `./`, `../` or `~` prefix, so it is not path-SHAPED — which also means readPathIssue's
// zone check never runs on it either. The two screens are a UNION here, never a
// substitution of one for the other; `X=$(cat .env) echo hi` asks today because of this
// screen and must keep asking.
//
// WHAT DELEGATION COSTS, stated because it is a real movement and not zero:
//
//   - env-value position is unchanged for an in-zone read. `X=$(cat /tmp/x.json) echo hi`
//     stops classifying ExpansionSafeCmd and instead goes through envvars' recursion,
//     which approves the read and clears the value — same `allow`, one more hop.
//   - COMMAND position loses a DECISIVE allow for a path-bearing read unless the
//     recursion approves it, which is exactly the authority moving. The engine's
//     substitution floor is therefore keyed on SubstitutionRefused rather than on "not
//     cleared", so a DELEGATED body is governed by the model that owns the question
//     instead of being floored for asking it — see engine.foldSubstitutionScan.
//   - A jq/yq FILTER that happens to look like a secret path (`jq -r .env f.json`) is
//     REFUSED here rather than delegated, because IsSecret is a classification this seam
//     owns. That is fail-CLOSED over-refusal on a filter, and the note on
//     fileReaderSubstitutions' KNOWN over-refusal still applies to it.
//
// NOT CLOSED BY THIS, and reported rather than implied: `jq -f`, `--rawfile`,
// `--slurpfile` and `--argfile` name a PATH as the flag's operand, and safecmds' jq branch
// drops that operand — so `jq -f /Users/phillipg/.ssh/id_rsa .` auto-approves in the BARE
// spelling too (measured `allow` on a064a73e). Delegation makes the captured spelling
// AGREE with the bare one, which is the relation this bead asserts, and both remain wrong
// until the bare spelling is fixed. That is bead pg2-wrxg6, in safecmds, not here.

// SubstitutionClearance is the THREE-VALUED answer this seam gives about a command
// substitution's body. Two values were not enough (pg2-zpct4): a `false` that meant "I
// statically refuse this" and a `false` that meant "another model owns this question"
// were indistinguishable, so the engine's floor had to treat a DELEGATION as a REFUSAL.
//
// The order is LEAST-CLEARED FIRST and it is load-bearing twice over: SubstitutionRefused
// is the ZERO value, so a code path that forgets to set one fails CLOSED, and
// minClearance can fold two answers by taking the minimum.
type SubstitutionClearance int

const (
	// SubstitutionRefused: this seam withholds clearance on its own authority — a write
	// spelling, a body on no list, a shape it does not model, an unparseable body, a
	// deny-listed secret path. The engine's floor keeps such a body at or above
	// NoOpinion EVEN IF full-engine recursion would approve it, which is what stops
	// `$(git show HEAD)`'s textconv/external-diff RCE surface from being unlocked by a
	// rule that only sees "git, read-only".
	SubstitutionRefused SubstitutionClearance = iota
	// SubstitutionDelegated: the body is a modelled READ, and the only outstanding
	// question is whether its PATH may be read — which `patheval` owns and this package
	// cannot answer. Recursion's verdict is authoritative for such a body, in both
	// directions.
	SubstitutionDelegated
	// SubstitutionCleared: statically safe with NO outstanding question. This is the
	// only value IsSafeSubstitutionBody reports as true, so ExpansionSafeCmd — which
	// skips recursion entirely — is reachable only from here.
	SubstitutionCleared
)

// minClearance folds two clearances by taking the LEAST cleared of the two.
func minClearance(a, b SubstitutionClearance) SubstitutionClearance {
	if b < a {
		return b
	}
	return a
}

// LooksLikePath reports whether an argument is SHAPED like a filesystem path.
//
// It is the SINGLE definition of that question for the whole repo (pg2-zpct4).
// `internal/rules/safecmds`' looksLikePath delegates to it, so the static substitution
// seam and the rule that owns path readability can no longer disagree about WHICH tokens
// are paths — a disagreement that let a captured read clear a path the bare read refused.
// It lives here because the question is purely LEXICAL: no config, no filesystem, no zone
// model, which is precisely what makes it safe for a static parser to own.
//
// It is deliberately NOT a readability judgement. A token this returns true for is a
// token whose readability `patheval` must rule on; see THE pg2-zpct4 RECONCILIATION above.
//
// The two tilde clauses are measured bypasses, not defensiveness:
//
//   - A bare "~" is the home directory just as much as "~/": the path evaluator's
//     cleanPath expands both to $HOME. Without matching it, a bare "~" arg (e.g.
//     `rm -rf ~`) was never classified and slipped through as safe (tc-sfpto).
//   - A "~user" argument (tilde + username, no slash — "~someuser", "~someuser/x") is
//     ALSO a home path: cleanPath resolves it via an os/user lookup to that user's home.
//     Without matching it, `rm -rf ~someuser` slipped through as safe — the tc-fielf
//     gap, the same shape as the bare-"~" tc-sfpto miss. Any "~" prefix except bare "~"
//     is path-shaped; the len check keeps bare "~" going through the clause above.
func LooksLikePath(arg string) bool {
	return arg == "~" ||
		strings.HasPrefix(arg, "/") ||
		strings.HasPrefix(arg, "./") ||
		strings.HasPrefix(arg, "../") ||
		strings.HasPrefix(arg, "~/") ||
		(strings.HasPrefix(arg, "~") && len(arg) > 1) // ~user / ~user/... (tc-fielf)
}

// safeCmdSubstitutions: commands that never mutate and never read a file's CONTENT,
// safe inside $(...) regardless of arguments.
//
// "never read a file" is about CONTENT, not about touching the filesystem: `readlink`
// and `realpath` resolve a name, and `test`/`[` stat one. What unites the list is that
// no member can emit another file's bytes, so nothing here needs the secretpath screen
// the reader list below applies.
var safeCmdSubstitutions = map[string]bool{
	"mktemp": true, "date": true, "whoami": true, "id": true,
	"pwd": true, "basename": true, "dirname": true,
	"readlink": true, "realpath": true, "uname": true,
	"echo": true, "printf": true,
	// pg2-xl79d addition. `seq` is admitted UNCONDITIONALLY because it touches no
	// filesystem path in any spelling: its operands are numbers and its only flags are
	// formatting (`-f`, `-s`, `-w`), so there is no path for a screen to inspect. It is
	// also the one member of envvars' measured EXHAUSTION half that a rule genuinely can
	// model, which is why it is relieved HERE rather than by withdrawing that Ask —
	// envvars.go records why the exhaustion half as a whole must keep it (it also
	// contains `bash -c`).
	//
	// `test` / `[` are NOT here, and the reason is measured — see
	// fileReaderSubstitutions.
	"seq": true,
}

// fileReaderSubstitutions: read-only commands whose PATH ARGS are DISPOSITIONED rather
// than cleared — a deny-listed secretpath REFUSES (so `$(cat .env)` still forces a
// prompt), and a path-shaped operand DELEGATES to the model that owns readability
// (pg2-zpct4). See readerArgsClearance and THE pg2-zpct4 RECONCILIATION.
//
// Membership means "this command can NAME A PATH and does not write", not
// specifically "it prints file content" — `ls` was always a metadata-only member and
// `test`/`[` join it below. The DISPOSITION is what earns membership, so the safe default
// for a new entry is HERE rather than in safeCmdSubstitutions; see the `test` comment
// for the deny-consuming movement that settles it.
var fileReaderSubstitutions = map[string]bool{
	"cat": true, "grep": true, "head": true, "tail": true, "wc": true, "ls": true,
	// pg2-xl79d additions: the structured-data readers. Each is on
	// `internal/rules/safecmds`' safeReadCmds/read path already — i.e. its BARE
	// invocation is approved by that rule after a zone check — so admitting it here
	// makes the CAPTURED spelling agree with the bare one, which is the whole of this
	// widening. Each also carries a per-command write-spelling fact, verified against
	// the installed binary's own `--help` on 2026-08-13 rather than assumed, because a
	// blanket rule about `-i` or `-o` would be WRONG for two of the three:
	//
	//   - `jq` is stdout-only: it has no in-place flag and no output-file flag, so no
	//     invocation of it writes. (`--rawfile`/`--slurpfile`/`--argfile` READ a file;
	//     their path operand is a bare token, so the screen below sees it.)
	//   - `yq` DOES write, two ways — `-i`/`--inplace` edits in place and
	//     `-s`/`--split-exp`/`--split-exp-file` writes one file per result — and
	//     hasWriteFlag's MutatingFlags["yq"] half screens both (pg2-1wt3b widened
	//     that shared map directly; substitutionWriteFlags carries no yq entry
	//     anymore — see its own doc).
	//   - `tq` (cryptaliagy/tomlq) has NO write spelling at all. Its `-o`/`--output` is
	//     an output FORMAT (`toml`/`json`), not a file, and its `-i`/`--input` is an
	//     input FORMAT, not in-place. So the two flag letters that mean "write" for
	//     other tools mean "format" here, which is exactly why the write vocabulary is
	//     per-command (hasWriteFlag's MutatingFlags/substitutionWriteFlags union) and
	//     never a shared letter test.
	//
	// ACCEPTED RESIDUE, stated because it is the same one pipesink.go's MutatingFlags
	// records for `awk`/`sed`: the FILTER/EXPRESSION text is not audited. jq cannot open
	// a file for writing from a filter at all, and yq's writes are the flags above, so
	// the residue here is narrower than for awk — but it is not zero and is not claimed
	// to be.
	//
	// KNOWN over-refusal, deliberate: this screen is path-shaped, so a jq/yq FILTER that
	// happens to look like a secret path (`jq -r .env f.json`) is refused by the static
	// list. That is fail-CLOSED. Do not "fix" it by teaching this seam the program-operand
	// role: that duplicates a rule's flag grammar in the parser, which is what ADR 0039's
	// I9 keeps out of here.
	//
	// pg2-zpct4 MEASURED WHY THE IsSecret HALF MUST STAY A REFUSAL rather than joining the
	// path DELEGATION, which would have removed this over-refusal. A delegation makes
	// recursion authoritative in BOTH directions, and recursion is measurably WRONG for
	// one deny-listed spelling: safecmds' jq branch drops the PATH operand of `-f` /
	// `--rawfile` / `--slurpfile`, so `jq -f /Users/phillipg/.ssh/id_rsa .` recurses to
	// `allow` (bead pg2-wrxg6). With IsSecret refusing, `echo $(jq -f …/.ssh/id_rsa .)` is
	// `ask`; delegating it would have made that row `allow` — a widening on a deny-listed
	// key. So the price of this over-refusal is one fail-closed prompt on a filter, and
	// what it buys is that a deny-listed operand cannot be cleared by a rule that failed to
	// see it.
	"jq": true, "yq": true, "tq": true,
	// `test` / `[` — pg2-xl79d, and THEY ARE ON THE SCREENED LIST FOR A MEASURED REASON.
	// They belong to the metadata-only class: they stat rather than read, they write
	// nothing, and they emit NOTHING ON STDOUT (their whole result is an exit status), so
	// a `$(test -f "$f")` capture yields the empty string whatever the file is. On that
	// reasoning they were first placed in safeCmdSubstitutions, beside the equally
	// metadata-only `readlink`/`realpath`/`basename` — and the probe measured a
	// DENY -> ALLOW movement:
	//
	//	X=$(test -f /Users/phillipg/.ssh/id_rsa) echo hi   deny -> ALLOW   (unscreened)
	//	X=$([ -f /Users/phillipg/.ssh/id_rsa ]) echo hi    deny -> ALLOW   (unscreened)
	//
	// `deny` is this repo's strongest verdict and no widening may consume one. The cause
	// is structural, not specific to `test`: ExpansionSafeCmd means the body is NEVER
	// recursed, so the deny-listed-credential Reject that fires through recursion is
	// bypassed, and only the screen on THIS list stands in its place. An unscreened
	// entry therefore CONVERTS a Reject into an Approve, while a screened one cannot.
	// So the rule for any future addition is: an entry that can NAME A PATH goes on the
	// screened list even when its disclosure channel is provably empty. (Ranked against
	// the alternative — withholding `test`/`[` entirely — the screened admission clears
	// the cohort rows and moves no deny; that is strictly better.)
	//
	// pg2-zpct4 GENERALISED that structural argument and it is the reason this list's
	// screen became a DISPOSITION. `secretpath.IsSecret` was never the whole path model,
	// only the part this package can evaluate; the deny-listed credential above is the
	// case where the two models happened to AGREE, and `/etc/shadow` is the case where
	// they did not. A screened entry can no longer clear a path at all — it delegates —
	// so the addition rule now reads: an entry that can name a path goes HERE, and its
	// paths are ruled on by `patheval`, never by this file.
	//
	// The trailing `]` of the `[` spelling is just another argument to the screen
	// (`IsSecret("]")` is false), so no bracket-specific handling is needed. `[[ … ]]` is
	// a *syntax.TestClause rather than a CallExpr, so soleSimpleCommandLeaf refuses it
	// and no entry here can change that.
	"test": true, "[": true,
}

// substitutionWriteFlags SUPPLEMENTS the shared MutatingFlags vocabulary
// (pipesink.go) for this seam. MutatingFlags is the single source of truth and is
// consulted FIRST — see hasWriteFlag — so an entry here exists only where a write
// spelling is missing from it.
//
// `yq -s`/`--split-exp`/`--split-exp-file` USED TO be that case (pg2-xl79d,
// 2026-08-13): MutatingFlags["yq"] carried only the `-i` family, the gap was
// shared by `internal/rules/safecmds`' isYqInPlace, and pg2-xl79d deliberately
// screened it HERE rather than widening the shared map, because widening
// MutatingFlags is a MORE-restrictive change for every OTHER consumer of it
// (cmdparse.StageWritesInput and its callers) and owes its own replay
// consideration — not something to fold in as a side effect of fixing this seam.
//
// pg2-1wt3b did that replay and widened MutatingFlags["yq"] directly (both
// isYqInPlace and this seam now consume it), so the supplement below is EMPTY:
// there is currently no yq write spelling MutatingFlags misses. The map and
// hasWriteFlag stay in place as the extension point this seam's contract
// promises — a FUTURE write spelling need only be added here if it must clear
// this substitution seam before its own MutatingFlags widening has had its
// separate replay.
var substitutionWriteFlags = map[string]map[string]bool{}

// hasWriteFlag reports whether args carry a flag that turns cmd into a WRITER, under
// the union of the shared MutatingFlags vocabulary and this seam's supplement. Both
// halves go through HasAnyFlag, so the glued spellings (`-i=true`, `--split-exp=x`)
// cannot hide behind an `=`.
//
// It is applied to EVERY branch of classifySubstitutionCommand rather than only to the
// reader branch, so that a member added to either list later inherits the screen
// instead of needing whoever adds it to remember. It is a no-op for every incumbent:
// of MutatingFlags' four keys (`find`, `sort`, `yq`, `tree`) only `yq` is on a
// substitution list at all.
func hasWriteFlag(cmd string, args []string) bool {
	return HasAnyFlag(args, MutatingFlags[cmd]) || HasAnyFlag(args, substitutionWriteFlags[cmd])
}

// gitReadSubcommands: git subcommands that only read metadata (no diff/show/log —
// those honor textconv/external-diff, an RCE surface a hook cannot neutralize).
//
// ADMISSION TEST (pg2-mgs91 audit; criterion 3 restated by pg2-a5r9r). An entry must
// satisfy 1, 2, 4 and 5. Criterion 3 is RETAINED AS A NUMBERED SLOT but disqualifies
// nothing on its own — see THE pg2-a5r9r RULING below for the evidence and for why the
// slot is kept rather than renumbered away. They are written out so a later reader
// RE-DERIVES an entry rather than inheriting it, and so a candidate's rejection does not
// have to be re-argued from scratch:
//
//  1. It emits no object CONTENT. Content emission is what reaches the
//     clean/textconv/`diff.external` filter chain, and every link in that chain is a
//     PROGRAM NAMED BY CONFIG — so a command that merely "reads" executes
//     attacker-controlled code. This is the original one-line rationale above,
//     unchanged; it is criterion 1 because it disposes of most candidates.
//
//     TWO CORRECTIONS from pg2-a5r9r, both measured on git 2.54.0, 2026-08-13.
//     (a) The original text named `.gitattributes` + `.git/config` as ONE
//     attacker-controlled unit, and that overstates it. A `.gitattributes` naming an
//     UNDEFINED diff driver executes NOTHING — git falls back to the builtin diff — so
//     `.gitattributes` travels with a clone but is INERT by itself, and the program's
//     NAME has to come from a config source, which a clone does not carry (see the
//     RULING). The RCE leg is therefore the SAME reachability class as criterion 3's,
//     not a stricter one. (b) The criterion survives that anyway, on a SECOND leg that
//     needs no config at all: DISCLOSURE. `git show HEAD:.env` printed the blob's bytes
//     with system and global config neutralised, and in a SUBSTITUTION those bytes
//     become the OUTER command's argv — the audit-unit problem IsSafeSubstitutionBody's
//     DECLINED PIPELINE note states for stdin. Criterion 3 has no such second leg, and
//     that asymmetry is the whole of the pg2-a5r9r ruling.
//
//  2. No FLAG turns it into a content path or names a program. The lookup below keys
//     on `tokens[1]` ALONE, so admitting a subcommand admits EVERY spelling of it. A
//     subcommand with one dangerous flag is inadmissible at this granularity — it
//     would need a flag-aware predicate this one is not.
//
//  3. Does it consult the INDEX? Index refresh compares the index against the worktree,
//     which is the path that honors `core.fsmonitor` — a config value naming a program
//     git EXECUTES, the same class as criterion 1's first leg. THE ANSWER DOES NOT
//     DISQUALIFY, and the absolute wording this slot used to carry ("It consults no
//     INDEX") was the defect pg2-a5r9r was filed for: two incumbents fail it. Keep
//     asking the question — it is worth knowing — but decide on 1, 2, 4 and 5.
//     THE SLOT IS NOT RENUMBERED, deliberately: the "fails (3)" / "fails (1) and (2)"
//     citations below and in substitution_test.go's audit rows would all shift by one,
//     which is churn that makes the history harder to follow for no gain.
//
//  4. It contacts no REMOTE. Egress is a different risk class from a local read:
//     the destination is itself config-controlled (`url.*.insteadOf`, remote
//     helpers, `core.sshCommand`), so the command both runs a config-named program
//     and chooses where the bytes go.
//
//  5. It writes nothing and moves no ref. SCOPED TO REPOSITORY CONTENT — objects, refs
//     and the working tree — and NOT to the index's stat cache. The distinction is
//     load-bearing, because read literally this criterion would disqualify the same two
//     incumbents criterion 3 does: an index REFRESH really does rewrite `.git/index`
//     (measured on git 2.54.0, 2026-08-13 — `git status` and `git describe --dirty` each
//     REPLACED the file's inode, while `git rev-parse HEAD` and `git ls-tree HEAD` left
//     it untouched). It does not disqualify them, because the refresh re-records stat
//     data for entries it does not change: no object, no ref and no tracked file moves.
//     Apply this one to CONTENT, not to files under `.git/`.
//
// REMOVAL IS THE ESCALATION PATH and needs no new mechanism — the same posture the
// README states for the consumer `approvedCommands` list. Delete the entry and the
// body falls back to the prompt. Remove one when (1), (2), (4) or (5) stops holding
// for it — a git release adding a content-emitting `--format` atom, a filter-honoring
// flag, or a remote dependency it did not have. NOT on (3) alone: an index dependency
// a subcommand newly acquires is not a removal trigger, per THE pg2-a5r9r RULING
// below. Removal is also MORE restrictive, so it owes the replay that ruling ran.
//
// DECLINED CANDIDATES, recorded so the audit is not re-run and so nobody reads their
// absence as an oversight:
//
//   - `show`, `log`, `diff-tree` — fail (1) and (2). All three emit diffs or content
//     and honor textconv and `diff.external`. This is the incumbent rationale.
//   - `cat-file` — fails (1) and (2) hardest. `--textconv` and `--filters` run the
//     filter programs BY NAME, and `-p HEAD:.env` prints a secret's bytes from a
//     `<rev>:<path>` spec that the fileReaderSubstitutions secretpath screen below
//     structurally cannot see (it screens argv PATHS).
//   - `ls-files` — DECLINED, BUT ITS RECORDED GROUND DOES NOT HOLD, said plainly rather
//     than reconciled with an invented distinction (pg2-a5r9r). It was declined for
//     failing (3): its stat-comparing spellings (`-m`, `-d`, `-o` with exclusions)
//     compare the index against the worktree, so `core.fsmonitor` is reachable, and (2)
//     means admitting the subcommand admits those spellings. All of that is TRUE —
//     `git ls-files -m` was measured executing an fsmonitor marker — and none of it
//     disqualifies, for exactly the reasons the RULING below gives for `status`. It
//     stays out because RE-ADMITTING it is a LESS-restrictive change that owes its own
//     corpus replay, which is the same rule that kept `status` in. Not acted on here;
//     recorded as a follow-up. It emits only path names, so it fails neither leg of (1).
//   - `for-each-ref` — fails (2). The `--format` atom set includes the
//     `%(contents…)` family, which prints an object's own bytes, and a ref MAY point
//     at a blob; the subcommand-only key cannot separate "print refnames" from
//     "print an object". (`--shell`/`--python`/`--perl`/`--tcl` are output QUOTING
//     modes, not interpreters — those are not the objection.)
//   - `ls-remote` — fails (4).
//
// THE pg2-a5r9r RULING: `status` AND `describe --dirty` STAY, AND CRITERION 3 IS THE
// DEFECT. pg2-mgs91 recorded them as a KNOWN INCUMBENT EXCEPTION — the same property
// that declined `ls-files`, held by two members of the list — and left the criterion
// stated as absolute. pg2-a5r9r settled which of the two was wrong, and it is the
// criterion.
//
// THE FACTUAL CLAIM WAS RIGHT. Measured on git 2.54.0, 2026-08-13, with `core.fsmonitor`
// pointed at a marker script that appends and exits 1: `git status`, `git describe
// --dirty` and `git ls-files -m` each EXECUTED it, while `git describe` (no `--dirty`),
// `git rev-parse HEAD` and `git ls-tree HEAD` did not. So the exposure is real, and
// `--dirty` really does come along free — the lookup below keys on `tokens[1]` alone.
// What was wrong is treating it as a reason THIS list can act on:
//
//   - THIS LIST IS NOT THE CONTROL FOR THAT SINK, and removing an entry does not make it
//     one. gitReadSubcommands is the SUBSTITUTION-BODY floor only; a BARE `git status`
//     is approved by the git rule's own readOnlySubcommands
//     (`internal/rules/git/git.go`), which also holds `describe`, `ls-files`, `show` and
//     `log`. MEASURED through the real binary, this worktree, 2026-08-13, against a
//     variant built with `status` and `describe` DELETED from this map: `git status`,
//     `git status --porcelain`, `git describe --dirty` and `git ls-files -m` each
//     answered `allow` on BOTH sides. Exactly TWO shapes moved, both substitutions —
//     `echo "$(git status --porcelain)"` and `echo "$(git describe --dirty)"`, `allow`
//     -> `abstain`. So removal costs two prompting shapes and closes nothing: the same
//     subcommand, unwrapped, still runs the fsmonitor program.
//   - THE CONFIG THAT ARMS THE SINK IS NOT SHIPPABLE BY A REPO. `git clone` does not
//     transfer `.git/config` — measured: a clone of a repo with both `core.fsmonitor`
//     and `diff.<driver>.textconv` set carried NEITHER key — so a hostile upstream can
//     plant neither. That is the same reachability class as criterion 1's RCE leg (see
//     correction (a) there), which is why criterion 3 cannot be the stricter of the two.
//   - THE SOURCES AN AGENT CAN NAME ARE SCREENED WHERE THEY BELONG, not here. At THIS
//     seam the `tokens[1]`-exact key refuses `git -c core.fsmonitor=… status`, and
//     soleSimpleCommandLeaf's `len(call.Assigns) > 0` refusal covers the env spelling
//     `GIT_CONFIG_COUNT=… git status`; both measured `abstain` on both sides. At the top
//     level `hasGitConfigInjection` defers a pre-subcommand `-c` as "a known RCE class",
//     and `gatedConfigKeys` classes `core.fsmonitor` as a configSink — the SAME class
//     and the SAME Ask as `diff.external` and `diff.<driver>.textconv`, which is the
//     repo's own statement that criteria 1 and 3 name one hazard, not two.
//   - CETA'S THREAT MODEL DOES NOT TREAT THE REPO AS HOSTILE, so nothing else in the
//     package is relying on this criterion. `patheval` reports the project/CWD
//     `PathReadWrite`; ADR 0041's Context names that trust as the cause of a
//     false-allow rather than as a mistake to undo; ADR 0040's Context bounds its own
//     scope with "this is not a remote-attacker vector". `primarycommit`'s resolver
//     reads repo-local `.git/config` and TRUSTS it to name the primary branch. This
//     criterion was the only place in CETA asserting the opposite.
//
// ONE GAP FOUND HERE AND CLOSED WHERE IT BELONGS (pg2-a12rl). At the TOP level the env
// spelling `GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=… git
// status` measured `allow` on BOTH sides while this list was ruled on — `hasRedirectEnvVar`
// screened only `GIT_DIR` and `GIT_WORK_TREE`, and git honors the `GIT_CONFIG_*` triple as
// command-line-equivalent config (measured: it ran the marker). It was the same sink by an
// unscreened route, it belonged to the git RULE rather than to this list, and closing it was
// a MORE-restrictive change owing its own replay — so pg2-a5r9r recorded it rather than
// acting on it. pg2-a12rl then closed it in `internal/rules/git`'s
// hasGitConfigEnvInjection, which withdraws the Approve for any `GIT_CONFIG*` assignment on
// a git leaf. It makes the ruling above stronger, not weaker, either way: the control was
// never one this list held or could reach. NOTHING HERE CHANGED, and nothing here should —
// soleSimpleCommandLeaf's `len(call.Assigns) > 0` refusal already floors the env spelling
// inside a substitution body.
var gitReadSubcommands = map[string]bool{
	"rev-parse": true, "rev-list": true, "symbolic-ref": true,
	// `describe` and `status` reach `core.fsmonitor` (`describe` only in its `--dirty`
	// spelling, which `tokens[1]` cannot separate) and they STAY. That is a ruling, not
	// an oversight — see THE pg2-a5r9r RULING above, and
	// substitution_test.go's TestIsSafeSubstitutionBody_IndexConsultingIncumbentsStay,
	// which pins both verdicts so a later reader finds a decision rather than an
	// accident.
	"merge-base": true, "describe": true, "status": true,
	// ls-tree PASSES ALL FIVE. It reads a TREE object and prints only
	// mode/type/oid/size/path; its tree-ish operand is mandatory, so it never stats
	// the worktree (no index refresh, no fsmonitor); no flag emits blob content —
	// `-l` adds a size and `--format`'s atoms are `%(objectmode)`, `%(objecttype)`,
	// `%(objectname)`, `%(objectsize)`, `%(objectsize:disk)`, `%(path)`, none of
	// which routes through a filter; and it is local-only and read-only.
	//
	// It is here because this repo's own CLAUDE.md "Premise Freshness"
	// `next-free-id?` probe uses it, and pg2-qcw5w's census found that probe to be 6
	// of the 8 prompting rows it collected. Adding it is NOT sufficient to clear that
	// probe — see IsSafeSubstitutionBody's DECLINED PIPELINE RELAXATION note, which
	// is the reason, and engine_integration_test.go's
	// TestIntegration_F3NextFreeIdProbeStillPrompts, which pins the outcome.
	"ls-tree": true,
}

// stripGitDashC (pg2-jq8tn) consumes zero or more leading `-C <path>` pairs from
// tokens — the slice AFTER "git" — so the git branch below can look up the REAL
// subcommand instead of missing it because `-C` is occupying tokens[1].
//
// SCOPED TO `-C` AND ONLY `-C`, DELIBERATELY. THE pg2-a5r9r RULING (see
// gitReadSubcommands' criterion 3 discussion above) keeps this whole seam's git
// admission keyed on `tokens[1]` EXACTLY, because that exactness is what makes
// `git -c core.fsmonitor=… status` fail the `gitReadSubcommands[tokens[1]]` lookup
// (`tokens[1]` is `-c`, not `status`) — a config-injection route the RULING relies
// on staying closed. A general "skip any leading global option" loop would
// resolve PAST `-c` (and `--git-dir=`, `--work-tree=`, `--namespace=`,
// `--exec-path[=]`, `-p`/`--paginate`/`-P`/`--no-pager`, …) to the same `status`,
// re-opening exactly that route. So this helper recognizes the ONE spelling
// `-C <path>` and nothing else; every other leading option must keep landing in
// tokens[1] and keep missing the lookup, unchanged.
//
// `-C`'S SYNTAX, MEASURED on git 2.54.0, 2026-08-17 (a plain `git -C … rev-parse`
// probe from a scratch repo; re-derivable by anyone with that git, this is not
// assumed from the man page):
//
//   - the operand is a MANDATORY SEPARATE argv token. `git -Crepo1 …` fails with
//     `unknown option: -Crepo1` (glued form invalid) and `git -C=repo1 …` fails
//     with `unknown option: -C=repo1` (`=` form invalid too) — only the two-token
//     spelling `-C`, then the path as the NEXT token, is valid.
//   - `-C` MAY REPEAT, each resolved relative to the previous: `git -C repo1 -C
//     ../repo2 rev-parse --show-toplevel` reports repo2's path. This helper does
//     NOT resolve that chain — it collects every operand and screens each
//     independently through readerArgsClearance, which is a conservative
//     simplification (screening more paths than the single one git would
//     actually chdir through can only make the verdict MORE restrictive, never
//     less) sufficient for this bead's scope.
//   - a trailing bare `-C` with no following token at all is invalid git syntax
//     (`no directory given for '-C' option`) — ok=false, fail closed.
//   - `git -C <path>` with NOTHING after the path (no subcommand token survives)
//     is a distinct, valid outcome: git itself falls back to top-level help.
//     stripGitDashC reports it as rest == nil, ok == true — NOT a failure to
//     detect — and classifySubstitutionCommand's existing "no subcommand token"
//     handling (len(rest) == 0) takes it from there.
//
// ok is false ONLY for the malformed trailing-bare-`-C` case above; the caller
// MUST NOT proceed to a subcommand lookup when ok is false.
func stripGitDashC(tokens []string) (rest []string, paths []string, ok bool) {
	i := 0
	for i < len(tokens) && tokens[i] == "-C" {
		if i+1 >= len(tokens) {
			return nil, nil, false // trailing bare -C, no operand: fail closed
		}
		paths = append(paths, tokens[i+1])
		i += 2
	}
	return tokens[i:], paths, true
}

func classifySubstitutionCommand(tokens []string) SubstitutionClearance {
	if len(tokens) == 0 {
		return SubstitutionRefused
	}
	cmd := tokens[0]
	// A WRITE SPELLING DISQUALIFIES BEFORE ANY LIST IS CONSULTED (pg2-xl79d). Every
	// entry on both lists is there because it READS, so a flag that turns one into a
	// writer voids the ground for its membership — and the check sits ahead of the
	// branches so it cannot be forgotten by a later addition. See hasWriteFlag.
	if hasWriteFlag(cmd, tokens[1:]) {
		return SubstitutionRefused
	}
	if safeCmdSubstitutions[cmd] {
		// No delegation: a member of this list cannot emit another file's bytes in ANY
		// spelling, so it names no path whose READABILITY decides anything. The bare
		// spellings agree — `readlink /etc/shadow`, `realpath /etc/shadow` and
		// `basename /etc/shadow` all measured `allow` on a064a73e, because safecmds
		// classifies them as name-resolution rather than content reads.
		return SubstitutionCleared
	}
	if cmd == "hostname" && len(tokens) == 1 { // bare hostname reads; `hostname X` sets it
		return SubstitutionCleared
	}
	if cmd == "go" && len(tokens) >= 2 && tokens[1] == "env" {
		for _, t := range tokens[2:] { // go env -w/-u mutate persistent config
			if isGoEnvMutatingFlag(t) {
				return SubstitutionRefused
			}
		}
		return SubstitutionCleared
	}
	if cmd == "git" && len(tokens) >= 2 {
		// pg2-jq8tn: strip any leading `-C <path>` pairs so the real subcommand — not
		// `-C` itself — is what gets looked up. When there is no leading `-C` at all,
		// stripGitDashC returns tokens[1:] unchanged with an empty paths slice, so this
		// reduces to the original tokens[1]-exact lookup below (readerArgsClearance(nil)
		// is SubstitutionCleared, so minClearance is a no-op in that case).
		if rest, paths, ok := stripGitDashC(tokens[1:]); ok && len(rest) > 0 && gitReadSubcommands[rest[0]] {
			// UNION, folded MOST-RESTRICTIVE-WINS (minClearance, same combinator
			// SubstitutionClearance already defines): the subcommand admission is
			// SubstitutionCleared on its own, but each `-C` operand is a path this seam
			// did not screen before, so it goes through readerArgsClearance exactly like
			// a fileReaderSubstitutions argv path (pg2-zpct4) — a deny-listed secret
			// refuses, a path-shaped operand delegates to patheval, and a refusal
			// anywhere wins.
			return minClearance(SubstitutionCleared, readerArgsClearance(paths))
		}
	}
	if fileReaderSubstitutions[cmd] {
		return readerArgsClearance(tokens[1:])
	}
	return SubstitutionRefused
}

// readerArgsClearance classifies the ARGUMENTS of a fileReaderSubstitutions member —
// the only branch whose members can emit another file's bytes, and therefore the only
// branch where path readability decides anything (pg2-zpct4).
//
// TWO SCREENS, and they are a UNION with different owners. IsSecret is a static
// classification this seam OWNS, so it REFUSES; a path-shaped token is a question
// `patheval` owns, so it DELEGATES. A refusal anywhere in the argv wins over a
// delegation, which is why the loop keeps scanning after the first delegation instead
// of returning early.
func readerArgsClearance(args []string) SubstitutionClearance {
	clearance := SubstitutionCleared
	for _, t := range args {
		operand := t
		if strings.HasPrefix(t, "-") {
			// A glued value (--flag=value) still names a real path to read (e.g.
			// `grep --file=.env`); recheck the value half. Bare short flags
			// (-c, -v, ...) carry no value — skip them.
			//
			// A SEPARATE-TOKEN flag operand (`jq -f PATH`) is NOT attributed to its
			// flag here: it arrives as the next loop iteration and is screened as an
			// ordinary token, which is stricter, not looser. What this cannot do is
			// make safecmds attribute it — see the pg2-wrxg6 note in THE pg2-zpct4
			// RECONCILIATION.
			eq := strings.IndexByte(t, '=')
			if eq < 0 {
				continue
			}
			operand = t[eq+1:]
		}
		if secretpath.IsSecret(operand) {
			return SubstitutionRefused // reading a deny-listed secret → force a prompt
		}
		if LooksLikePath(operand) {
			clearance = SubstitutionDelegated
		}
	}
	return clearance
}

// isGoEnvMutatingFlag reports whether t is any spelling of go env's -w/-u
// mutating flag: dash count (-w, --w) and a glued value (-w=true) must not
// let it slip past a naive exact-token match.
func isGoEnvMutatingFlag(t string) bool {
	if !strings.HasPrefix(t, "-") {
		return false
	}
	name := strings.TrimLeft(t, "-")
	if eq := strings.IndexByte(name, '='); eq >= 0 {
		name = name[:eq]
	}
	return name == "w" || name == "u"
}

// IsSafeSubstitutionBody reports whether cmdStr — the inner body of a
// $(...) or `...` command substitution — is safe under the STATIC allowlist. A
// body is safe only when it contains no nested substitution AND it parses to
// EXACTLY ONE SIMPLE COMMAND with no heredoc and no redirection other than a
// screened pure read (redirectClearance), and that command's command+args
// pass classifySubstitutionCommand.
//
// Quote-awareness is now a PARSER FACT rather than a property inherited from a
// leaf count (ADR 0039 step 2a): the body goes through the seam, so the '|' in
// `grep -E 'a|b' file` is a literal inside one argument word because the bash
// grammar says so, not because a hand-rolled splitter happened to track quote
// state. `soleSimpleCommandLeaf` also TIGHTENS the shape test — the sole
// statement must BE a simple command, not merely reduce to one leaf — which is
// what keeps a real grammar from newly clearing `(cat VERSION)` or
// `{ cat VERSION; }`; see its own comment.
//
// An UNPARSEABLE body is refused for the same reason a nested substitution is,
// and then some: if the body does not parse, "no substitution found" is not
// evidence there is none, so clearing it would clear text nobody enumerated. That
// was the reachable half of the pg2-wguam P0 — the outgoing backtick extent scan
// (`indexUnescapedBacktick`, deleted in this step) was not quote-aware at all, so
// “ `echo don't` “ yielded a quote-unbalanced body that still reduced to one
// safe-looking `echo` leaf. A body that does not parse now cannot reach the
// allowlist at all, because the seam returns no leaf for it.
//
// This is the STATIC FLOOR consulted by the engine's substitution-body
// recursion (pg2-1q5i3): a body the allowlist rejects can never be made LESS
// restrictive by full-engine recursion (e.g. `git show HEAD` is approved by the
// git rule but deliberately excluded here for the textconv/external-diff RCE
// surface). Recursion may only ADD demotions, never unlock what this blocks.
//
// # DECLINED: admitting a pipeline whose every stage is allowlisted
//
// The SOLE-SIMPLE-COMMAND shape is deliberate, and the proposal to widen it to "a
// PIPELINE all of whose stages individually pass classifySubstitutionCommand" is
// DECLINED. pg2-mgs91 asked the question and accepted this answer; do not re-open it
// without new evidence, and note that "the probe still prompts" is not new evidence —
// it is the accepted consequence, recorded below.
//
// The motivating case was this repo's own CLAUDE.md "Premise Freshness"
// `next-free-id?` probe, whose body is
// `git ls-tree -r --name-only main -- docs/adr | rg … | sort -n | tail -1` — four
// stages, each of which the allowlist would clear on its own. Three grounds:
//
//  1. THE ALLOWLIST'S SAFETY CLAIM IS ARGV-ONLY; A PIPELINE ADDS STDIN.
//     classifySubstitutionCommand decides on the command name plus the ARGUMENT tokens
//     — that IS the whole of the fileReaderSubstitutions branch, which clears `grep`
//     only after screening each argv path through secretpath. Its claim is therefore
//     "every byte this command reads is a path I inspected", and in a pipeline that
//     claim is FALSE for every stage after the first: its input is the pipe. So the
//     relaxation does not merely admit a new shape, it changes the AUDIT UNIT from
//     command+argv to command+argv+stdin, and silently re-opens the safety argument
//     for every entry ALREADY on the lists above — none of which was audited as a
//     SINK fed attacker-influenced bytes — and for every entry added later, which
//     nobody would audit that way either. ADR 0040 settles the analogous question
//     for the consumer allowlist: the unit of trust is the COMMAND. A pipeline is
//     not one command.
//  2. IT IS SHAPE-GATED APPROVAL, WHICH ADR 0039 PRICED AND DEFERRED. See that
//     ADR's Alternatives Considered, "Shape-gated approval": making Approve
//     conditional on a shape carries the geometry the pre-parse fast path was
//     rejected for, moved one layer down — a predicate that wrongly reports a shape
//     as admissible WIDENS approval — and adopting it requires its own fuzz
//     invariant and its own replay, i.e. its own bead. The pipeline predicate is
//     exactly that: a recursive walk over a *syntax.BinaryCmd tree whose accept set
//     must be Pipe/PipeAll and nothing else, where one missed operator admits `&&`
//     — arbitrary command sequencing — into the static allowlist.
//  3. THE MEASURED BENEFIT IS ONE PROMPT. pg2-qcw5w's census found 8 prompting rows
//     of this class; 6 are the one probe above. A prompt on a probe whose output the
//     agent then reads is cheap. pg2-wguam's floor is not.
//
// What this does NOT rest on, so the argument is not overstated: it is NOT a claim
// that a pipeline of allowlisted stages is exploitable today, and NOT a claim that
// the relaxation could not be written fail-closed (soleSimpleCommandLeaf's `ok=false`
// covers unparseable input, and a pipeline variant could do the same). It rests on
// (1) the audit unit changing for entries already trusted, and (2) ADR 0039 having
// already decided that this class of widening buys its own bead.
//
// CONSEQUENCE, stated so the `ls-tree` entry above is not misread as a fix: THE
// `next-free-id?` PROBE STILL PROMPTS. It is a 4-stage pipeline, so this shape test
// floors it whatever gitReadSubcommands contains — the two reasons pg2-mgs91 names
// are independent and only one of them was removed. That verdict is PINNED, not
// emergent, by engine_integration_test.go's
// TestIntegration_F3NextFreeIdProbeStillPrompts and by
// TestIsSafeSubstitutionBody_GitReadSubcommandAudit here in cmdparse.
func IsSafeSubstitutionBody(cmdStr string) bool {
	return ClassifySubstitutionBody(cmdStr) == SubstitutionCleared
}

// ClassifySubstitutionBody is IsSafeSubstitutionBody's three-valued form, and the entry
// point a CONSUMER of the floor should use (pg2-zpct4). The bool form answers "may this
// body skip the authoritative path model entirely?", which is the right question for
// ExpansionSafeCmd and the wrong one for a floor: it cannot distinguish a body this seam
// REFUSES from one whose only open question belongs to another model. See
// SubstitutionClearance and THE pg2-zpct4 RECONCILIATION.
func ClassifySubstitutionBody(cmdStr string) SubstitutionClearance {
	leaf, ok := soleSimpleCommandLeaf(cmdStr)
	if !ok {
		return SubstitutionRefused
	}
	if leaf.Executable == "" || leaf.HasHeredoc {
		return SubstitutionRefused
	}
	tokens := append([]string{leaf.Executable}, leaf.Args...)
	return minClearance(classifySubstitutionCommand(tokens), redirectClearance(leaf.Redirections))
}

// redirectClearance classifies the redirections a substitution body carries: a PURE READ
// from a path this seam can dispose of. It replaces the blanket
// `len(leaf.Redirections) > 0` refusal (pg2-xl79d), whose only measured cost was the
// idiom `X=$(wc -l < "$f")` — one of the 37 asking rows, and refused for a reason that
// does not survive being stated: `wc -l < f` and `wc -l f` read the same bytes of the
// same file, and the argv spelling was already cleared.
//
// TWO CONDITIONS, and the FIRST is the security one:
//
//  1. NO WRITE DIRECTION. The test is hookio.RedirectionKind.IsWrite, which is `!=
//     RedirectStdin` — so `>`, `>>`, `>|`, `2>`, `9>`, `&>`, `>& FILE` and bash's `<>`
//     read-write open are ALL refused, and a kind added to that enum later is refused
//     until someone deliberately classifies it. This is what keeps
//     `X=$(jq -r .x "$f" > /etc/passwd)` off the list: a body that WRITES can never be
//     cleared here, whatever its executable. (`2>&1` and `>&-` are unaffected in the
//     other direction — attachRedir records nothing for a descriptor duplication or
//     close, so they never reach this function; that is why the blanket refusal did not
//     already cost the `git rev-parse HEAD 2>&1` idiom, and it still does not.)
//  2. THE SOURCE PATH GETS THE SAME DISPOSITION THE fileReaderSubstitutions BRANCH
//     GIVES AN ARGV PATH — a deny-listed secret REFUSES, a path-shaped token DELEGATES
//     (pg2-zpct4, readerArgsClearance). Without it a `<` would be a route around that
//     branch: `$(wc -l < .env)` reading what `$(wc -l .env)` refuses, and
//     `$(wc -l < /etc/shadow)` clearing what `wc -l /etc/shadow` now delegates.
//
// WHY THIS IS NOT THE DECLINED PIPELINE RELAXATION, since both are arguments about
// stdin: the audit unit does not change. That decline rests on a pipeline stage's stdin
// being ANOTHER COMMAND'S OUTPUT — bytes no screen inspected — so the list's claim
// ("every byte this command reads is a path I inspected") becomes false. A `<` redirect's
// source IS a path, and it IS dispositioned, right here in condition 2. The claim holds
// unchanged; only the syntax by which the path arrives is new.
//
// Heredocs and herestrings are NOT covered by this and stay refused by the caller's
// HasHeredoc test: their bytes are inline text, not a path, so condition 2 has nothing
// to screen and the I2 heredoc floor owns them.
//
// # THE MEASURED LIMIT pg2-xl79d RECORDED HERE IS NOW CLOSED (pg2-zpct4)
//
// pg2-xl79d recorded that condition 2's screen was `secretpath.IsSecret`, NARROWER than
// the model the engine's path evaluation applies through recursion — `secretDirs` is
// `secrets` and `.ssh`, `secretBasenames` is the `.credentials`/`auth.json` family, so
// `~/.aws/credentials` is not a secretpath at all — and that because ExpansionSafeCmd
// skips the recursion entirely, an entry cleared here never met the richer model. It
// recorded the one row that moved, and the four argv spellings of the same read that were
// ALREADY `allow` beside it:
//
//	X=$(wc -l < /Users/phillipg/.aws/credentials) echo hi    ask -> ALLOW   (pg2-xl79d)
//	X=$(cat /Users/phillipg/.aws/credentials) echo hi         allow         (already)
//	X=$(wc -l /Users/phillipg/.aws/credentials) echo hi       allow         (already)
//	X=$(head -1 /Users/phillipg/.aws/credentials) echo hi     allow         (already)
//	X=$(grep -c x /Users/phillipg/.aws/credentials) echo hi   allow         (already)
//
// Its own prescription was that strengthening the screen "belongs to BOTH spellings at
// once", and that it MUST NOT be done by widening `internal/secretpath` in passing —
// that map is consumed by every rule, so a new entry there is a repo-wide policy change.
// pg2-zpct4 does exactly that and by neither of the wrong routes: BOTH spellings now
// DELEGATE a path-shaped operand to the model that owns readability, so all five rows
// above are decided by `patheval` rather than by this screen, and `internal/secretpath` is
// untouched. `wc -l < F` and `wc -l F` remain EQUAL, which was the whole thesis of the
// widening; what changed is that the shared answer is now the authoritative one.
//
// The RELATION is what is asserted in the tests rather than these verdicts, so retuning
// either model cannot silently invert it: the `<` spelling is never LESS restrictive than
// the argv spelling of the same read, and neither is ever less restrictive than the BARE
// command spelling.
func redirectClearance(redirs []hookio.Redirection) SubstitutionClearance {
	clearance := SubstitutionCleared
	for _, rd := range redirs {
		if rd.Kind.IsWrite() || secretpath.IsSecret(rd.Path) {
			return SubstitutionRefused
		}
		if LooksLikePath(rd.Path) {
			clearance = SubstitutionDelegated
		}
	}
	return clearance
}

// SubstitutionKind classifies an extracted shell substitution.
type SubstitutionKind int

const (
	SubstCommand    SubstitutionKind = iota // $(...)
	SubstBacktick                           // `...`
	SubstProcessIn                          // <(...)
	SubstProcessOut                         // >(...)
)

// Substitution is one top-level shell substitution extracted from raw command
// text by EnumerateSubstitutions.
type Substitution struct {
	Kind SubstitutionKind
	Body string // inner command text, verbatim (NOT unquoted)
}

// IsCommandSubstitution reports whether the substitution captures a command's
// OUTPUT — $(...) or `...`. Only these are governed by the static allowlist
// floor (IsSafeSubstitutionBody); process substitutions (<(...) / >(...)) have
// no static allowlist and are governed by full-engine recursion alone.
func (s Substitution) IsCommandSubstitution() bool {
	return s.Kind == SubstCommand || s.Kind == SubstBacktick
}

// SubstitutionScan is the result of scanning text for substitutions: the
// TOP-LEVEL bodies found, plus whether the scan actually managed to model the
// text it was given.
//
// Unparseable is the security-load-bearing half (pg2-wguam). The bodies alone
// cannot distinguish "this text contains no substitution" from "the scan lost
// track and stopped looking", and the engine's fold is Approve iff no leaf
// objects — so an empty result read as the former is an AUTO-APPROVE of whatever
// the scan skipped. A caller making a security decision MUST floor an
// Unparseable scan at Abstain and MUST NOT treat its Substitutions as complete.
type SubstitutionScan struct {
	// Substitutions are the top-level bodies found. When Unparseable is set this
	// list is a PREFIX, not an inventory: it holds the substitutions whose extents
	// the seam could still delimit exactly, and omits any whose closing delimiter
	// was missing — for those the extent is unknown, so nothing inside them has
	// been enumerated.
	Substitutions []Substitution
	// Unparseable reports that the text is not valid bash, as judged by the STRICT
	// parser behind the seam: an unterminated `$(`/backtick, an unbalanced quote, an
	// unclosed here-document, a zsh-only construct. It is deliberately NOT weakened
	// by the error-recovering pass the seam consults to salvage the prefix above —
	// recovery adds bodies to recurse and never clears this flag (I8 forbids a
	// fallback parser).
	Unparseable bool
	// Reason names the specific failure, for the deferring caller's reason string.
	Reason string
}

// DELETED, and the deletion is a coverage claim: `matchParen`.
//
// It was the last raw-text paren matcher outside the seam — structure derived from
// bytes, which ADR 0039's I9 forbids there. ADR 0039 step 2a (pg2-zeqa5) removed
// three of its four callers and kept it as a SHIM for the fourth,
// `classifyCmdSubstitution`, on the env-assignment classification path. pg2-hed0a
// migrated that path to the seam (see shellparse.go's env-assignment VALUE
// classifier), which left `classifyCmdSubstitution`,
// `classifyBacktickSubstitution` and this matcher with no callers at all, so all
// three are gone rather than kept as dead code the fuzz harness could enshrine.
//
// pg2-x9452 (step 5) asserts this removal; the RULE-side scanners its acceptance
// criteria also name (docker's `splitOnShellOperators`, gitdir's `scopeLeaves` and
// `containsVarRef`, envvars' value scan) are untouched and still its.

// DELETED, and the deletion is a coverage claim: `commandStartOffset`, the
// quote/paren-aware byte scan that found where a command's executable begins.
// `StripLeadingEnvAssignments` now lives on the seam (shellparse.go) and reads the
// boundary off the AST — the assignments are `CallExpr.Assigns` and the command
// starts at `Args[0]` — so the one thing this scan hand-rolled a paren counter for,
// the `FOO=(a b) cmd` array form, is a parse error under Variant(LangBash) and
// lands on the fail-closed branch instead of on a second structure model.

// wrapperPrefixes lists (executable, subcommand) pairs that act as transparent
// wrappers. A command matching one of these is unwrapped so downstream rules
// evaluate the inner command instead.
var wrapperPrefixes = []struct {
	executable string
	subcommand string
}{
	{"cloudflared", "access"},
}

// execPrefixes run an inner command after their own flags / NAME=VALUE
// assignments. They must be unwrapped so downstream rules evaluate the inner
// command — otherwise `env rm -rf /etc` / `command rm -rf /etc` hide a
// dangerous command behind a name that looks read-only.
var execPrefixes = map[string]bool{"env": true, "command": true}

// unwrapExecPrefix strips an env/command prefix's flags and NAME=VALUE
// assignments and returns the inner executable + its args. Any `env NAME=VALUE`
// assignments encountered are CAPTURED into envAssigns (not merely stripped) so
// the env-var guard sees them regardless of the prefix form (pg2-gkd5e). ok is
// false when no inner command follows (bare `env`, or only flags/assignments) —
// the caller then leaves the command as-is (a read-only env query) but still
// records the captured assignments.
func unwrapExecPrefix(base string, args []string) (inner string, innerArgs []string, envAssigns []EnvAssignment, ok bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" { // explicit end of options
			i++
			break
		}
		// `command -v/-V NAME` describes/locates NAME (like `which`) — it does NOT
		// execute NAME. Do not unwrap: leave the bare `command` for the safe-commands
		// rule to approve as a read-only lookup. (`command -p` DOES execute → unwrap.)
		if base == "command" && (a == "-v" || a == "-V") {
			return "", nil, nil, false
		}
		// env NAME=VALUE assignment (command has none) — capture it so the env-var
		// guard can inspect it, then continue past it to the inner command.
		if base == "env" && !strings.HasPrefix(a, "-") && strings.Contains(a, "=") {
			envAssigns = append(envAssigns, newEnvAssignment(a))
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			// env -u NAME and -C DIR take a following argument; consume it too.
			if base == "env" && (a == "-u" || a == "-C") {
				i += 2
				continue
			}
			i++
			continue
		}
		break // first bare, non-assignment token is the inner executable
	}
	if i >= len(args) {
		return "", nil, envAssigns, false
	}
	return args[i], args[i+1:], envAssigns, true
}

// commandRunner describes a command-runner wrapper's flag grammar: which options
// take a SEPARATE following value (consume the next token too), and whether the
// wrapper has a mandatory DURATION operand before the inner command.
type commandRunner struct {
	// valueOpts are the short/long options that take a value in their SEPARATE
	// form and therefore consume the following token. Glued forms (`-n10`,
	// `--adjustment=10`, `-oL`, `--output=L`) are a single `-`-prefixed token and
	// are skipped as ordinary options without a lookahead, so they need no entry.
	valueOpts map[string]bool
	// hasDuration marks a wrapper (only `timeout`) whose first BARE operand is a
	// DURATION that precedes the command, not the command itself.
	hasDuration bool
}

// commandRunnerPrefixes are command-runner wrappers that execute an INNER command
// after their own options. Like execPrefixes (env/command) they must be unwrapped
// so downstream argv[0]-keyed rules (dangerouscmds, buildtools, …) evaluate the
// real command — otherwise `nice dd if=/dev/zero of=x` parses to Executable
// `nice` and no rule matches → abstain, when dangerouscmds SHOULD see `dd` and
// Reject (tc-otuid). Unlike env, these wrappers set NO NAME=VALUE assignments, so
// no EnvAssignment capture is needed. Flag grammars follow GNU coreutils.
//
// `xargs` is DELIBERATELY EXCLUDED: internal/rules/safecmds handles it with
// richer semantics (extractXargsCommand, `sh -c` recursion, stdin-append) that a
// parser-level unwrap would conflict with and drop. Do NOT add it here.
var commandRunnerPrefixes = map[string]commandRunner{
	// nohup: only --help/--version, neither takes a value → no valueOpts.
	"nohup": {},
	// nice: -n/--adjustment take a value (legacy `-10` is a single option token).
	"nice": {valueOpts: map[string]bool{"-n": true, "--adjustment": true}},
	// stdbuf: -i/-o/-e and their long forms take a value.
	"stdbuf": {valueOpts: map[string]bool{
		"-i": true, "-o": true, "-e": true,
		"--input": true, "--output": true, "--error": true,
	}},
	// timeout: -s/--signal and -k/--kill-after take a value; -p/-f/-v (and long
	// forms) do not. The first bare operand is the DURATION, then the command.
	"timeout": {
		valueOpts:   map[string]bool{"-s": true, "--signal": true, "-k": true, "--kill-after": true},
		hasDuration: true,
	},
}

// timeoutDurationPattern matches a GNU timeout DURATION operand: an integer or
// decimal optionally suffixed with a unit (s/m/h/d). It is a SAFETY gate — see
// unwrapCommandRunner — so an unknown future option that swallows the command
// cannot make us skip the real command.
var timeoutDurationPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[smhd]?$`)

// unwrapCommandRunner strips a command-runner wrapper's options (and, for
// timeout, its mandatory duration operand) and returns the inner executable + its
// args. It walks args skipping `-`-prefixed option tokens (consuming a following
// value for cr.valueOpts entries), stops at `--` or the first bare token, then:
//   - for a plain wrapper, that first bare token is the inner command;
//   - for timeout (hasDuration), the first bare token is the DURATION — it is
//     skipped, and the SECOND bare token is the command; but ONLY when the first
//     bare token actually looks like a duration (timeoutDurationPattern). If it
//     does not, the command is left un-unwrapped (ok=false).
//
// CONSERVATISM (mirrors unwrapExecPrefix's ok=false path): whenever an inner
// command cannot be confidently identified — no bare command token, the required
// duration+command pair is absent, or a value-taking option consumed what would
// have been the command — ok is false and the caller leaves the ParsedCommand
// as-is. That yields the current behavior (abstain / defer to Claude), which is
// SAFE; mis-identifying a benign token as the command is the dangerous direction
// and never happens here.
func unwrapCommandRunner(cr commandRunner, args []string) (inner string, innerArgs []string, ok bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" { // explicit end of options
			i++
			break
		}
		if strings.HasPrefix(a, "-") {
			// A value-taking option in its SEPARATE form consumes the following
			// token too; glued forms are a single token skipped by this same branch.
			if cr.valueOpts[a] {
				i += 2
				continue
			}
			i++
			continue
		}
		break // first bare (non-'-') token
	}
	if cr.hasDuration {
		// timeout: the first bare token is the DURATION. Only treat it as a
		// duration+command pair if it matches the duration shape; otherwise be
		// conservative and do not unwrap.
		if i >= len(args) || !timeoutDurationPattern.MatchString(args[i]) {
			return "", nil, false
		}
		i++ // skip the duration operand; the command is the next bare token
	}
	if i >= len(args) {
		return "", nil, false // no inner command (bare wrapper / options only)
	}
	return args[i], args[i+1:], true
}

// newEnvAssignment builds an EnvAssignment from a raw NAME=VALUE token,
// classifying its value's expansion the same way extractExecAndArgs does. The
// bash append form NAME+=VALUE is normalized to NAME so name-based guards (e.g.
// the env-var rule's injector set) are not bypassed by `export LD_PRELOAD+=…` —
// a '+' can only be the append operator here, since env-var names cannot contain
// one.
func newEnvAssignment(raw string) EnvAssignment {
	eq := strings.Index(raw, "=")
	name := strings.TrimSuffix(raw[:eq], "+")
	return EnvAssignment{
		Name:      name,
		Value:     raw[eq+1:],
		Raw:       raw,
		Expansion: classifyExpansion(raw[eq+1:]),
	}
}

func unwrapCommand(pc ParsedCommand) ParsedCommand {
	base := filepath.Base(pc.Executable)
	// `export VAR=VALUE ...` is an assignment builtin: lift each NAME=VALUE arg
	// into EnvVars so the env-var guard sees the assignment regardless of position
	// (leading / export / env-prefix / its own compound segment), while keeping the
	// leaf rule-visible with Executable=="export" so a bare `export`/`export NAME`
	// stays a read-only query the safe-commands rule can approve. Non-assignment
	// args (a bare name to export, `-f`, ...) therefore stay as args. The env-var
	// rule is DECISIVE for flagged vars and runs first, so it prevents auto-approval
	// of a dangerous `export VAR=VALUE` before safe-commands is consulted.
	//
	// A leaf with an EMPTY Executable is now equally rule-visible: the engine's
	// command-less-leaf branch runs the rule chain on its EnvVars (pg2-mtnmb). It did
	// not before, which is why an assignment-only compound segment auto-approved.
	if base == "export" {
		return liftAssignmentArgs(pc)
	}
	if execPrefixes[base] {
		if inner, innerArgs, envAssigns, ok := unwrapExecPrefix(base, pc.Args); ok {
			// COPY-then-override rather than a fresh literal: every field an unwrap does
			// not deliberately change (Raw, Heredocs, the pipeline coordinates, …) must
			// survive it, and a literal silently drops any field added later.
			next := pc
			next.Executable = inner
			next.Args = innerArgs
			next.EnvVars = appendEnvAssignments(pc.EnvVars, envAssigns)
			return unwrapCommand(next)
		} else if len(envAssigns) > 0 {
			// No inner command (bare `env`/`command`, or flags + NAME=VALUE only) —
			// leave the leaf as a read-only env query, but keep the captured
			// assignments visible to the env-var guard so a standalone
			// `env DANGEROUS=…` does not slip past it (pg2-gkd5e).
			pc.EnvVars = appendEnvAssignments(pc.EnvVars, envAssigns)
			return pc
		}
		// No inner command and no assignments (bare `env`/`command` or flags only) —
		// leave as-is; it is a read-only environment query handled by safe-commands.
		return pc
	}
	// Command-runner wrappers (nice/timeout/nohup/stdbuf) run an inner command
	// after their own options. Recurse like execPrefixes so nested cases —
	// `nice env dd`, `timeout 5 nice dd` — unwrap all the way to the real command
	// (tc-otuid). On failure to identify an inner command, leave the leaf as-is
	// (the safe abstain/defer default).
	if cr, isRunner := commandRunnerPrefixes[base]; isRunner {
		if inner, innerArgs, ok := unwrapCommandRunner(cr, pc.Args); ok {
			next := pc
			next.Executable = inner
			next.Args = innerArgs
			return unwrapCommand(next)
		}
		return pc
	}
	for _, wp := range wrapperPrefixes {
		if base != wp.executable {
			continue
		}
		// Args must be: [subcommand, innerExec, ...]
		if len(pc.Args) < 2 || pc.Args[0] != wp.subcommand {
			continue
		}
		next := pc
		next.Executable = pc.Args[1]
		next.Args = pc.Args[2:]
		return next
	}
	return pc
}

// liftAssignmentArgs moves every leading-style NAME=VALUE token out of pc.Args
// and into pc.EnvVars (classifying each value), returning the leaf with the
// remaining non-assignment args. Used for the `export` assignment builtin so the
// env-var guard inspects `export VAR=VALUE` exactly like the leading `VAR=VALUE`
// form (pg2-gkd5e).
func liftAssignmentArgs(pc ParsedCommand) ParsedCommand {
	var remaining []string
	envVars := pc.EnvVars
	for _, a := range pc.Args {
		if isEnvAssign(a) {
			envVars = append(envVars, newEnvAssignment(a))
			continue
		}
		remaining = append(remaining, a)
	}
	pc.EnvVars = envVars
	pc.Args = remaining
	return pc
}

// appendEnvAssignments concatenates captured assignments onto an existing slice,
// returning base unchanged when there is nothing to add.
func appendEnvAssignments(base, extra []EnvAssignment) []EnvAssignment {
	if len(extra) == 0 {
		return base
	}
	return append(base, extra...)
}

type ExpansionKind int

const (
	ExpansionNone       ExpansionKind = iota // static value: "/foo/bar"
	ExpansionVarRef                          // $VAR, ${VAR:-default}
	ExpansionSafeCmd                         // $(mktemp), $(date +%F)
	ExpansionArithmetic                      // $((1+2))
	ExpansionUnknown                         // can't classify
)

type EnvAssignment struct {
	Name      string
	Value     string
	Raw       string
	Expansion ExpansionKind
}

type ParsedCommand struct {
	Executable           string
	Args                 []string
	EnvVars              []EnvAssignment
	Redirections         []hookio.Redirection
	ProcessSubstitutions []string // inner commands from <(cmd) and >(cmd)
	HasHeredoc           bool
	// Heredocs are this leaf's heredoc EXTENTS: delimiter, quoting, and the body
	// text that stripHeredocBodies lifted out of the command before splitCompound
	// could shred it into pseudo-leaves (pg2-r2rf3). Empty for a `<<<` herestring,
	// which carries its word inline and has no body.
	Heredocs []Heredoc
	Raw      string
	Comment  string
	// PipelineID and PipelineIndex expose the PIPELINE RELATION between leaves —
	// the one compound operator whose meaning splitCompound used to discard
	// (tc-vul7). `|` was consumed exactly like `;`, `&&` and `&`, so a rule holding
	// one leaf could not tell where that leaf's OUTPUT went; `cat .git/config | tee
	// /tmp/x` and `cat .git/config | grep url` were indistinguishable at leaf scope,
	// and RootExpression carries only text, not the relation.
	//
	// Leaves of the SAME expression that share a PipelineID are stages of one
	// pipeline, ordered by PipelineIndex; stage N's stdout is stage N+1's stdin. IDs
	// are per-Parse-call, so leaves from DIFFERENT Parse calls (e.g. an outer
	// expression and a substitution body) MUST NOT be compared — see
	// DownstreamStages, which is the supported accessor.
	//
	// The zero value is a lone stage of the first pipeline, which is what a
	// hand-built ParsedCommand should mean. Synthesized leaves that belong to no
	// pipeline (a `for` word list, a leftover heredoc extent) carry -1.
	PipelineID    int
	PipelineIndex int
	// SubshellScope is the CHAIN OF SUBSHELL IDS enclosing this leaf, OUTERMOST to
	// INNERMOST — empty for a leaf lowered at the top level, inside no `( … )` at
	// all. Like PipelineID, IDs are per-Parse-call: a scope path from one leaf set
	// MUST NOT be compared against another Parse call's.
	//
	// This is what lets InCommandVars (pg2-4ak2k) tell "the same subshell" from "a
	// sibling subshell at the same nesting depth", which a bare depth counter
	// cannot: two leaves at depth 1 can sit in DIFFERENT, mutually invisible
	// subshells. One leaf's scope path is a PREFIX of (or equal to) another leaf's
	// exactly when the first leaf's subshell is STILL OPEN at the second leaf's
	// position — i.e. the first is an enclosing scope of the second, or they are
	// the very same scope. A path that is longer than, or diverges from, the
	// other's is a CLOSED or SIBLING subshell and carries no visibility either way.
	//
	// The zero value (nil) is top-level, matching PipelineID's zero-value
	// convention of meaning "no special scoping applies".
	SubshellScope []int
}

// DownstreamStages returns the leaves of `leaves` that receive the STDOUT of a
// stage whose Raw text equals leafRaw — i.e. everything leafRaw pipes into.
//
// `leaves` MUST be a single Parse call's output (pipeline IDs are per-call), and
// is normally Parse of the whole expression a rule was handed as
// hookio.HookInput.RootExpression. Matching on Raw is exact: the engine builds a
// leaf's synthetic HookInput from that same Raw and hands the rule the same
// RootExpression it parsed the leaf out of, so re-parsing either yields the same
// text. A leafRaw that matches nothing returns no stages — the same answer as "no
// pipeline", which is the pre-tc-vul7 behavior and therefore never a regression.
//
// Every match is unioned, so a text appearing twice in an expression contributes
// both pipelines' downstreams rather than silently picking one.
func DownstreamStages(leaves []ParsedCommand, leafRaw string) []ParsedCommand {
	var out []ParsedCommand
	for _, stage := range leaves {
		if stage.PipelineID < 0 || stage.Raw != leafRaw {
			continue
		}
		for _, candidate := range leaves {
			if candidate.PipelineID == stage.PipelineID && candidate.PipelineIndex > stage.PipelineIndex {
				out = append(out, candidate)
			}
		}
	}
	return out
}

// DELETED, and the deletion is a coverage claim: the shared byte scanner
// `shellScanner` (with `scanFrame`, `newShellScanner`, `advance` and `nested`),
// plus `hasLiveRedirChar`, `ExtractComment` and `StripComment`.
//
// The scanner was ADR 0039's root cause 1 in person: it reported what ONE BYTE
// does and had no extent API, so every caller needing a REGION hand-rolled a
// quote-blind depth counter while holding it. Commit 1c749bbd consolidated four
// drifted copies into it and nine more instances surfaced afterwards — which is why
// this step deletes the scanner rather than consolidating onto it again.
//
// Where each capability went:
//
//   - word start, quoting and nesting are now parser facts inside the seam;
//   - `UnquotedMask` survives as an EXPORTED seam function (shellparse.go) computed
//     from the AST's quoted/substitution/arithmetic spans. Its sole caller is
//     rules/ssh's `hasWriteRedirection`, a rule-side raw-text scanner ADR 0039's
//     step 5 (pg2-x9452) still owns, so the capability cannot be removed here — but
//     its IMPLEMENTATION no longer is a second structure model;
//   - `hasLiveRedirChar` is gone with its only caller, `extractRedirections`: a
//     quoted `>` is a *syntax.SglQuoted part of an argument word, so `grep '>' f`
//     yields no redirection structurally rather than by a subtractive guard;
//   - `ExtractComment` is gone, replaced by the seam's `CommandComment`, and
//     `StripComment` is gone with the per-line comment pass. Under
//     KeepComments(true) a comment never appears in a CallExpr at all, so the pass
//     is retired BY CONSTRUCTION rather than reimplemented.

// Parse lowers a command string to CETA's leaf commands.
//
// It is a FACADE over the seam (`ParseShell`) and carries no structure logic of its
// own. Before ADR 0039 step 2 it WAS the outgoing front end —
// `stripHeredocBodies`, then `splitCompound`, then `resolveLoops`, then per-segment
// `tokenize` / `extractRedirections` / `extractExecAndArgs` — five independent text
// passes each deciding command boundaries for itself. All five are deleted; see the
// obituaries in this file and in heredoc.go.
//
// It DISCARDS the seam's `Unparseable` flag, so every caller whose "no leaves"
// branch could reach an APPROVAL must call `ParseShell` and honour I1b instead. The
// engine does exactly that (`EvaluateExpression` folds the unparseable floor
// through MostRestrictive); a RULE reaching zero leaves reports
// hookio.ErrNotApplicable and gates nothing, which is why the flag is safe to drop
// at this boundary and nowhere else.
//
// Comments are NOT pre-stripped: under KeepComments(true) they are parser facts and
// never appear in a command's words, and `ParsedCommand.Comment` is filled from
// `Stmt.Comments`.
func Parse(command string) []ParsedCommand {
	return ParseShell(command).Leaves
}

// DELETED, and the deletion is a coverage claim: `segment`, `assignPipelineIDs`,
// `splitCompound`, `tokenize`, and the ENTIRE `resolveLoops` family —
// `resolveLoops`, `extractLoopBody`, `isLoopKeyword`, `isDoneKeyword`,
// `parseDoKeyword`, `doneResidue` and `forWordList`.
//
// `splitCompound` and `tokenize` are ADR 0039's ROOT CAUSE 2: every pass returned
// TEXT (`[]string`, a rewritten string), so structure discovered by one pass was
// discarded and the next re-derived it. The seam returns leaves from one AST.
//
// The `resolveLoops` family is ADR 0039's ROOT CAUSE 4 and inventory sites 12 and
// 13 — the class where structure is not mis-derived but DISCARDED. It replaced a
// loop with its body and advanced past the terminator segment; pg2-qkecz patched
// the two known drops back in by adding `doneResidue` (hole A, the terminator's
// redirection) and `forWordList` (hole B, the `for` word list), which is why those
// two are members of the family here even though the earlier inventory predates
// them. Patching a text pass cannot close the class: it can only close the
// instances somebody found. Over the seam both holes are STRUCTURAL and more
// general — a compound's redirections sit on the compound's own *syntax.Stmt, so
// `emitCompoundRedirs` covers `done > f`, `(cmd) > f`, `{ …; } > f`, `if … fi > f`
// and `case … esac > f` with one rule and no residue text-prefix match — and the
// class itself is now guarded by the I14 coverage check
// (`TestLeafSpansCoverEveryCallExpr`), which is the only mechanism that can see
// root cause 4 at all.
//
// While the text version survived, so did root cause 4. It does not survive.

// unquote is RETAINED, deliberately, as a POST-LOWERING helper — the one piece of
// the outgoing tokenizer this migration must NOT replace.
//
// Reason: it defines CETA's token spelling, and the parser's own literal expansion
// is STRICTER. `unquote` strips quoting only when the WHOLE token is wrapped in one
// quote character, so `a"b"c` KEEPS its quotes, and both `envvars.literalValue` and
// `isStaticAbsolutePath` reject any value with a surviving quote. A true literal
// expansion would yield `abc` and make mixed-quoted values newly CLEAR the very
// predicate I4 exists to fence — ADR 0039's Consequences names this as one of the
// constructs that changes verdict in the LESS-restrictive direction if lowered
// naively. The seam therefore applies THIS function to each word's exact source
// slice (see wordToken), which is what makes the parity provable rather than
// argued, and TestShellParse_UnquoteParity_MixedQuoting pins the mixed case.
//
// It is a pure string -> string function over an ALREADY-DELIMITED token: it
// derives no structure, so it is not a scanner and I9 does not reach it.
func unquote(s string) string {
	if len(s) < 2 {
		return s
	}
	if s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	if s[0] == '"' && s[len(s)-1] == '"' {
		inner := s[1 : len(s)-1]
		var buf strings.Builder
		buf.Grow(len(inner))
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) {
				next := inner[i+1]
				switch next {
				case '"', '\\':
					buf.WriteByte(next)
					i++
				default:
					buf.WriteByte(inner[i])
				}
			} else {
				buf.WriteByte(inner[i])
			}
		}
		return buf.String()
	}
	return s
}

// UnwrapGluedQuotes removes ONE matched pair of surrounding shell quotes from a
// VALUE that arrived GLUED to an unquoted prefix in the same word — the extremely
// common `key='value'` shape (`-f query='{ viewer { login } }'`,
// `git -c core.fsmonitor='false'`, `--method='PUT'`). It is a SHARED, EXPLICITLY
// CALLED helper (pg2-9zgso), generalised from the identical private
// `unwrapGluedQuotes` that `internal/rules/gh/api.go` carried alone since
// pg2-44dsd — that instance is now DELETED in favour of this one.
//
// # THE DECISION: A HELPER EACH READER CALLS, NOT A LOWERING CHANGE
//
// pg2-9zgso's acceptance criteria required recording, at this decision point,
// why the fix is a shared helper rather than a change to `unquote`/`wordToken`
// above. `unquote` strips quoting only when the WHOLE token is wrapped in ONE
// quote character, and that restriction is DELIBERATE, not an oversight it would
// be tidy to lift: its own comment records that a true PER-PART literal expansion
// would turn a mixed-quoting token like `a"b"c` into `abc`, and that
// `envvars.literalValue` and `isStaticAbsolutePath` both rely on a quote
// character SURVIVING in a mixed token as their signal that the value is not
// fully static and must be fenced conservatively (I4). `key=value` glued to an
// unquoted key is exactly this mixed-quoting shape, so teaching the LOWERING
// itself to resolve it generically would silently widen every other consumer of
// that signal too — a change whose blast radius is the WHOLE token population and
// which would owe its own full-corpus replay (ADR 0039's Enforcement), not the
// scoped one this bead ran. A shared helper that each `key=value` READER calls
// EXPLICITLY on the specific substring it already knows is a value (the text
// after `=`) confines the change to an enumerable, individually-reviewed set of
// call sites — see pg2-9zgso's audit of internal/rules for that set — instead of
// every token cmdparse ever produces.
//
// # BEHAVIOUR
//
// THE BOUNDARY THIS REPAIRS, MEASURED (originally pg2-44dsd, 2026-08-14, `gh api`;
// the same lowering behaviour applies to every caller). cmdparse strips quotes
// when the WHOLE token is quoted, and leaves them in place when a quoted segment
// is GLUED to an unquoted prefix:
//
//	-f 'query={ viewer { login } }'   -> arg `query={ viewer { login } }`   (stripped)
//	-f "query={ viewer { login } }"   -> arg `query={ viewer { login } }`   (stripped)
//	-f query='{ viewer { login } }'   -> arg `query='{ viewer { login } }'` (KEPT)
//	-f query="{ viewer { login } }"   -> arg `query="{ viewer { login } }"` (KEPT)
//	-f title='my title'              -> arg `title='my title'`             (KEPT)
//
// THE ERROR DIRECTION IS SAFE OR INERT IN EVERY CASE, which is what makes this
// helper acceptable rather than a layering violation — it can only ever make a
// value MORE readable to whatever ALLOWLIST or STRICT PARSE the caller applies
// next, never less:
//
//   - A value that GENUINELY begins and ends with a quote character must be
//     written with the OTHER quote outside (`draft="'true'"`, `query="'X'"`), so
//     the pair this strips is the outer one and what remains still carries the
//     inner quotes — `'true'` fails strconv.ParseBool and `'X'` does not scan as
//     GraphQL. Neither reaches an allowlisted clearance.
//   - A MULTI-SEGMENT concatenation (`title='a'x'b'`) is NOT reconstructed: the
//     interior holds the wrapper character, so the value is left EXACTLY as
//     cmdparse produced it and every caller falls back to its restrictive branch.
//   - AN UNTERMINATED quote (`'true`, one quote only) is left EXACTLY as produced:
//     the last byte does not match the first, so nothing is stripped.
//
// THIS IS NOT UNIVERSAL SAFETY. A caller comparing the unwrapped value against a
// DENYLIST or doing a SUBSTRING/PREFIX test can still be evaded by the quoted
// spelling if it does NOT route the value through this helper — the direction
// this fixes is "allowlist/strict-parse readers currently over-refuse the quoted
// spelling", not "every reader of a key=value argument is safe". pg2-9zgso's
// audit found call sites of exactly that failing-open shape (e.g.
// internal/rules/ssh's password-auth substring check) that this helper does NOT
// reach, because they were not routed through it — see that bead's report.
func UnwrapGluedQuotes(value string) string {
	if len(value) < 2 {
		return value
	}
	q := value[0]
	if q != '\'' && q != '"' {
		return value
	}
	if value[len(value)-1] != q {
		return value
	}
	inner := value[1 : len(value)-1]
	if strings.IndexByte(inner, q) >= 0 {
		// More than one quoted segment, or an escaped wrapper. Reconstructing that
		// is out of scope: declining leaves every caller on its restrictive branch.
		return value
	}
	return inner
}

// DELETED, and the deletion is a coverage claim: `splitFDPrefix`, `isVarName` and
// `redirectionCore` — the token-level `[FD]OP[TARGET]` grammar tc-xs8x built to
// replace a fixed six-spelling table.
//
// They existed only because a redirection arrived as a TOKEN, so its descriptor
// prefix had to be re-derived from bytes. The parser hands the seam a
// *syntax.Redirect with `N` (the descriptor), `Op` and `Word` already separated, so
// `redirCore` (shellparse.go) maps the operator ENUM onto the same operator text
// and `redirectionKind` below is reached unchanged. bash's `{varname}>` open-and-
// assign form, which `isVarName` gated, is `Redirect.N` with a non-numeric value.

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// redirectionKind classifies a (descriptor, operator) pair. Kinds exist so that
// consumers can ask what STREAM is affected without re-parsing operator text:
// only stdout-bearing kinds capture a command's payload (cmdparse.CapturesStdout),
// while every non-stdin kind is a write for the engine's path check.
func redirectionKind(fd, core string) hookio.RedirectionKind {
	switch core {
	case "<":
		return hookio.RedirectStdin
	case "<>":
		return hookio.RedirectReadWrite
	case "&>", "&>>":
		return hookio.RedirectAll
	case ">&":
		// `>& FILE` with NO descriptor is bash's both-streams form. With an
		// explicit descriptor the construct is ambiguous in bash; fall through to
		// the descriptor's own classification, which is the conservative reading.
		if fd == "" {
			return hookio.RedirectAll
		}
	}
	switch fd {
	case "", "1":
		return hookio.RedirectStdout
	case "2":
		return hookio.RedirectStderr
	}
	return hookio.RedirectOtherFD
}

// DELETED, and the deletion is a coverage claim: `extractRedirections` and
// `extractExecAndArgs`.
//
// `extractRedirections` walked an already-tokenised segment looking for operator
// text, which is why it needed `raws` (the pre-unquote token text) to stay
// quote-aware and why the fd-prefixed heredoc `2<<EOF` LEAKED into the argument
// list as a phantom operand — a token-prefix match cannot see a descriptor. Over
// the seam `attachRedirs` reads `Stmt.Redirs`, so the operator, its descriptor and
// its target are never re-derived from text and the phantom operand cannot exist
// (TestShellParse_FdPrefixedHeredocHasNoPhantomOperand).
//
// `extractExecAndArgs` split a token list into the leading NAME=VALUE assignments
// and the command. The parser separates them itself: leading assignments are
// `CallExpr.Assigns`, the command is `Args[0]`. That distinction is pg2-gkd5e's
// position-independence invariant, and conflating them is exactly what a positional
// text scan cannot avoid — `cmd FOO=1` has an assignment-SHAPED operand that
// `Args` correctly keeps as an argument.
//
// `isEnvAssign` / `isValidEnvName` are RETAINED below: they are predicates over an
// ALREADY-DELIMITED assignment token, not scanners, and the seam consults them to
// decide whether an `*syntax.Assign` can become an `EnvAssignment` at all (the
// indexed form `BEAD_IDS[1]=x` cannot, and must reach a data leaf rather than
// vanish).

// HasUnsafeCommandSubstitution reports whether s embeds a substitution whose body is
// not on the static safe list (safeCmdSubstitutions), or text whose substitutions
// cannot be enumerated at all. Bare $VAR / ${VAR} references and arithmetic
// $((...)) are NOT substitutions and return false; $(date)/$(mktemp) return false.
//
// It lets a caller demote ANY leaf whose executable or args embed an arbitrary inner
// command (e.g. `echo $(rm -rf ~)`), even when the outer command is otherwise "safe"
// — the outer rule never sees the inner command.
//
// NO PRODUCTION CALLER, stated so nobody mistakes it for a live gate: every
// reference is a test or the fuzz harness. It is kept, and kept CORRECT, because the
// fuzz harness asserts properties over it and a wrong invariant enshrined there is
// worse than none. The live env-assignment path is classifyExpansion; the live leaf
// path is the engine's foldSubstitutionScan.
//
// Over the seam (ADR 0039 step 5's residue, pg2-hed0a) it no longer walks the text
// itself. That matters for two inputs its byte loop got wrong:
//
//   - `$(( $(curl|sh) + 1 ))` — the loop SKIPPED the whole `$((` extent on a
//     two-character lookahead, so a command substitution nested in arithmetic was
//     never examined. The scan enumerates it (step 2a's replay Cause B).
//   - text the scan cannot model — `$(oops` — now answers true through the
//     Unparseable branch rather than by a failed paren match, so absence of
//     enumerated bodies is never read as evidence of safety (the pg2-wguam rule).
//
// A PROCESS substitution body is held to the same allowlist. Its own kind carries no
// static allowlist on the engine's path, and this predicate has no production caller
// to widen, so the uniform (stricter) treatment is the safe default. A value that is
// ONLY a process substitution still returns false: it contains neither `$` nor a
// backtick, so the shortcut below answers first — the same pre-existing gap
// classifyExpansion records for pg2-x9452.
func HasUnsafeCommandSubstitution(s string) bool {
	if !strings.ContainsAny(s, "$`") {
		return false
	}
	scan := ScanSubstitutions(s)
	if scan.Unparseable {
		return true
	}
	for _, sub := range scan.Substitutions {
		if !IsSafeSubstitutionBody(sub.Body) {
			return true
		}
	}
	return false
}

// isEnvAssign reports whether s is a NAME=VALUE environment assignment. It is the
// single gate in front of newEnvAssignment (which indexes raw[:eq] with no bounds
// check), so all four call sites — commandStartOffset, liftAssignmentArgs and
// extractExecAndArgs — share one definition of "assignment".
//
// The NAME must be a valid shell identifier. Without that check ANY token
// containing an '=' was accepted, so a command FRAGMENT produced by a scanner
// desync (pg2-3ggxm) was lifted into EnvVars as a phantom assignment whose "name"
// was raw shell text — escalated to Ask by the env-var rule and echoed into the
// user-facing prompt. This is defense-in-depth behind the quote-aware scanners:
// a real assignment always has a valid identifier, so the guard can never mask a
// legitimate one. It also subsumes the former leading-'-' check, since '-' is not
// a valid identifier start.
func isEnvAssign(s string) bool {
	eq := strings.Index(s, "=")
	if eq <= 0 {
		return false
	}
	return isValidEnvName(s[:eq])
}

// isValidEnvName reports whether name matches ^[A-Za-z_][A-Za-z0-9_]*$, tolerating
// the single trailing '+' of the bash append form NAME+=VALUE (newEnvAssignment
// normalizes that suffix away, so the guard must accept it or `export
// LD_PRELOAD+=…` would stop being seen as an assignment).
func isValidEnvName(name string) bool {
	name = strings.TrimSuffix(name, "+")
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z'):
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

func NormalizeExecutable(executable, projectRoot, cwd string) string {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return executable
	}
	projectRoot = filepath.Clean(projectRoot)
	cwd = filepath.Clean(cwd)
	if !filepath.IsAbs(executable) {
		if strings.HasPrefix(executable, "./") {
			executable = filepath.Join(cwd, executable)
		} else if !strings.Contains(executable, "/") {
			return executable
		} else {
			executable = filepath.Join(cwd, executable)
		}
	}
	executable = filepath.Clean(executable)
	if projectRoot != "" && (executable == projectRoot || strings.HasPrefix(executable+"/", projectRoot+"/")) {
		rel, err := filepath.Rel(projectRoot, executable)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return executable
}

// NormalizeCommand returns a canonical, NON-truncated representation of a bash
// command, suitable as a stable grouping key for "same command" analysis (e.g.
// bucketing decision rows in the identify-hook-misses taxonomy).
//
// It parses the command into leaf commands — splitting on &&, ||, ;, |,
// newlines, loops and subshells (see Parse / splitCompound) — and re-joins each
// leaf's normalized "<executable> <args...>" form with " && ". Because Parse
// splits newline- and &&-separated compounds into the SAME leaf sequence,
// "cd foo && work" and "cd foo\nwork" yield the SAME key. Env-assignment-only
// and redirection-only leaves (no executable) are dropped from the key.
//
// Unlike asklog.bashSummary, NormalizeCommand NEVER truncates at the first
// newline or at a fixed length, so two distinct commands that share a long
// (>120-char) common prefix remain distinct keys instead of collapsing into a
// phantom bucket (bead pg2-okd13.3). projectRoot/cwd thread through to
// NormalizeExecutable so path executables normalize consistently; pass "" when
// unknown (non-path executables are unaffected by either).
func NormalizeCommand(command, projectRoot, cwd string) string {
	leaves := Parse(command)
	parts := make([]string, 0, len(leaves))
	for _, lc := range leaves {
		if lc.Executable == "" {
			continue
		}
		exec := NormalizeExecutable(lc.Executable, projectRoot, cwd)
		if len(lc.Args) > 0 {
			parts = append(parts, exec+" "+strings.Join(lc.Args, " "))
		} else {
			parts = append(parts, exec)
		}
	}
	if len(parts) == 0 {
		return strings.TrimSpace(command)
	}
	return strings.Join(parts, " && ")
}
